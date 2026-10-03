package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type f31Address struct {
	City string `json:"city"`
	Zip  int    `json:"zip,omitempty"`
}

type f31Profile struct {
	Name    string            `json:"name"`
	Age     int               `json:"age"`
	Admin   bool              `json:"admin"`
	Score   float64           `json:"score"`
	Tags    []string          `json:"tags"`
	Address f31Address        `json:"address"`
	Level   string            `json:"level" enum:"low,medium,high"`
	Extra   map[string]string `json:"extra,omitempty"`
	Raw     json.RawMessage   `json:"raw,omitempty"`
	Secret  string            `json:"-"`
	hidden  string
}

func schemaNode(t *testing.T, schema map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := schema
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("schema path %v missing at %q: %+v", path, p, cur)
		}
		cur = next
	}
	return cur
}

func Test_F31_JSONSchemaOfGeneratesValidSchema(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Profile]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	if format.Type != TypeJSONSchema || !format.Strict {
		t.Fatalf("format: %+v", format)
	}
	if err := ValidateSchema(format.Schema); err != nil {
		t.Fatalf("generated schema is not valid: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(format.Schema, &schema); err != nil {
		t.Fatalf("decode generated schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("root type: %v", schema["type"])
	}
	if got := schemaNode(t, schema, "properties", "name")["type"]; got != "string" {
		t.Fatalf("name type: %v", got)
	}
	if got := schemaNode(t, schema, "properties", "age")["type"]; got != "integer" {
		t.Fatalf("age type: %v", got)
	}
	if got := schemaNode(t, schema, "properties", "score")["type"]; got != "number" {
		t.Fatalf("score type: %v", got)
	}
	if got := schemaNode(t, schema, "properties", "admin")["type"]; got != "boolean" {
		t.Fatalf("admin type: %v", got)
	}
	if got := schemaNode(t, schema, "properties", "tags", "items")["type"]; got != "string" {
		t.Fatalf("tags items type: %v", got)
	}
	if got := schemaNode(t, schema, "properties", "address")["type"]; got != "object" {
		t.Fatalf("address type: %v", got)
	}
	enum, ok := schemaNode(t, schema, "properties", "level")["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Fatalf("level enum: %+v", schemaNode(t, schema, "properties", "level")["enum"])
	}
	required, _ := schema["required"].([]any)
	got := map[string]bool{}
	for _, r := range required {
		if s, ok := r.(string); ok {
			got[s] = true
		}
	}
	for _, want := range []string{"name", "age", "admin", "score", "tags", "address", "level"} {
		if !got[want] {
			t.Fatalf("required must contain %q: %v", want, required)
		}
	}
	for _, notWant := range []string{"extra", "raw", "secret"} {
		if got[notWant] {
			t.Fatalf("required must not contain %q: %v", notWant, required)
		}
	}
	// 引用未导出字段，证明它被反射生成器跳过而不是参与 schema。
	_ = f31Profile{hidden: "secret"}
}

func Test_F31_JSONSchemaOfSupportsRootArray(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[[]f31Address]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	if err := ValidateSchema(format.Schema); err != nil {
		t.Fatalf("array schema invalid: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(format.Schema, &schema); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if schema["type"] != "array" {
		t.Fatalf("root type: %v", schema["type"])
	}
}

func Test_F31_UnsupportedTypeReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := JSONSchemaOf[chan int](); err == nil {
		t.Fatalf("channel field must not be representable as JSON schema")
	}
}

func Test_F31_ValidateSchemaRejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema string
	}{
		{"empty", ""},
		{"bad type", `{"type":"wat"}`},
		{"required without property", `{"type":"object","properties":{"a":{"type":"string"}},"required":["b"]}`},
		{"not json", "{"},
	}
	for _, tc := range cases {
		if err := ValidateSchema([]byte(tc.schema)); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
}

const f31ValidProfile = `{"name":"n","age":1,"admin":true,"score":1.5,"tags":[],"address":{"city":"SH"},"level":"low"}`

func Test_F31_StrictRejectsMissingField(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Profile]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	var out f31Profile
	if err := DecodeStructuredString(f31ValidProfile, &out, format); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if out.Name != "n" || out.Level != "low" || out.Address.City != "SH" {
		t.Fatalf("decoded: %+v", out)
	}
	missing := `{"name":"n","age":1,"admin":true,"score":1.5,"tags":[],"address":{"city":"SH"}}`
	if err := DecodeStructuredString(missing, &out, format); !errors.Is(err, ErrMissingField) {
		t.Fatalf("strict decode: actual=%v expected ErrMissingField", err)
	}
	// 非严格模式允许静默零值，用于确认 Strict 开关真的生效。
	loose := format
	loose.Strict = false
	if err := DecodeStructuredString(missing, &out, loose); err != nil {
		t.Fatalf("loose decode must succeed: %v", err)
	}
}

func Test_F31_NonJSONReturnsClearError(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Address]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	var out f31Address
	err = DecodeStructuredString("I cannot comply", &out, format)
	if !errors.Is(err, ErrNonJSON) {
		t.Fatalf("actual=%v expected ErrNonJSON", err)
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error must say non-JSON clearly: %v", err)
	}
}

func Test_F31_FallbackDowngradesToJSONObject(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Address]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	downgraded, text := AdaptResponseFormat(format, false)
	if downgraded.Type != TypeJSONObject {
		t.Fatalf("downgraded type: %q", downgraded.Type)
	}
	if len(downgraded.Schema) != 0 {
		t.Fatalf("json_object must not carry a schema: %s", downgraded.Schema)
	}
	if !strings.Contains(text, "city") {
		t.Fatalf("schema text must be attached to the prompt: %q", text)
	}
	kept, none := AdaptResponseFormat(format, true)
	if kept.Type != TypeJSONSchema || none != "" {
		t.Fatalf("supported provider must keep json_schema: %+v %q", kept, none)
	}
	empty, emptyText := AdaptResponseFormat(ResponseFormat{Type: TypeJSONSchema}, false)
	if empty.Type != TypeJSONObject || emptyText != "" {
		t.Fatalf("schema without body: %+v %q", empty, emptyText)
	}
}

func Test_F31_ApplyFormatFallbackAppendsSchemaText(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Address]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	req := &ChatRequest{
		Messages:       []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}},
		ResponseFormat: &format,
	}
	out := ApplyFormatFallback(req, false)
	if out.ResponseFormat.Type != TypeJSONObject {
		t.Fatalf("adapted format: %+v", out.ResponseFormat)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("schema text must be appended as one extra message: %d", len(out.Messages))
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != RoleSystem || !strings.Contains(last.Content, "city") {
		t.Fatalf("appended message: %+v", last)
	}
	// 原请求不得被修改（前缀稳定性要求原地不动）。
	if len(req.Messages) != 2 || req.ResponseFormat.Type != TypeJSONSchema {
		t.Fatalf("ApplyFormatFallback must not mutate the input: %+v", req)
	}
}

func Test_F31_ChatStructuredRetriesOnce(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Profile]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	f := NewFakeLLM(
		ScriptedResponse{Response: &ChatResponse{Content: "not json", FinishReason: "stop"}},
		ScriptedResponse{Response: &ChatResponse{Content: f31ValidProfile, FinishReason: "stop"}},
	)
	var out f31Profile
	req := &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, ResponseFormat: &format}
	resp, err := ChatStructured(context.Background(), f, req, &out)
	if err != nil {
		t.Fatalf("ChatStructured: %v", err)
	}
	if resp.Content != f31ValidProfile || out.Name != "n" {
		t.Fatalf("retry result: resp=%q out=%+v", resp.Content, out)
	}
	if f.Calls() != 2 {
		t.Fatalf("attempts: actual=%d expected=2", f.Calls())
	}
	last := f.LastRequest()
	if len(last.Messages) != 3 {
		t.Fatalf("retry request must carry the failure reason: %d messages", len(last.Messages))
	}
	if !strings.Contains(last.Messages[2].Content, "无法解析") {
		t.Fatalf("retry prompt must include the failure reason: %q", last.Messages[2].Content)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("ChatStructured must not mutate the caller request: %d messages", len(req.Messages))
	}
}

func Test_F31_ChatStructuredGivesUpAfterOneRetry(t *testing.T) {
	t.Parallel()
	format, err := JSONSchemaOf[f31Address]()
	if err != nil {
		t.Fatalf("JSONSchemaOf: %v", err)
	}
	f := NewFakeLLM(
		ScriptedResponse{Response: &ChatResponse{Content: "nope"}},
		ScriptedResponse{Response: &ChatResponse{Content: "still nope"}},
	)
	var out f31Address
	req := &ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, ResponseFormat: &format}
	if _, err := ChatStructured(context.Background(), f, req, &out); !errors.Is(err, ErrStructuredOutput) {
		t.Fatalf("actual=%v expected ErrStructuredOutput", err)
	}
	if f.Calls() != 2 {
		t.Fatalf("attempts: actual=%d expected=2 (no more than one retry)", f.Calls())
	}
}

func Test_F31_ChatStructuredRequiresResponseFormat(t *testing.T) {
	t.Parallel()
	f := NewFakeLLM()
	var out f31Address
	if _, err := ChatStructured(context.Background(), f, userReq(), &out); !errors.Is(err, ErrNoResponseFormat) {
		t.Fatalf("actual=%v expected ErrNoResponseFormat", err)
	}
	if f.Calls() != 0 {
		t.Fatalf("must reject before calling the model, calls=%d", f.Calls())
	}
}
