package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ResponseFormat.Type 的取值（F-31）。
const (
	TypeText       = "text"
	TypeJSONObject = "json_object"
	TypeJSONSchema = "json_schema"
)

const schemaInstructionPrefix = "输出必须是满足以下 JSON Schema 的单个 JSON 对象，不要输出解释或 Markdown 代码块：\n"

// 结构化输出相关哨兵错误。
var (
	// ErrNonJSON 表示模型回复根本不是合法 JSON。
	ErrNonJSON = errors.New("structured output is not valid JSON")
	// ErrMissingField 表示严格模式下缺少 schema 声明为 required 的字段。
	ErrMissingField = errors.New("structured output missing required field")
	// ErrStructuredOutput 表示重试一次后仍无法解析。
	ErrStructuredOutput = errors.New("structured output failed after retry")
	// ErrNoResponseFormat 表示请求没有声明结构化输出格式。
	ErrNoResponseFormat = errors.New("chat request has no response format")
)

// JSONSchemaOf 用反射为 T 生成基础 JSON Schema（F-31）。
//
// 支持 object/array/string/integer/number/boolean、required（无 omitempty 的字段）
// 与 enum（字段标签 enum:"a,b,c"）。生成后会自校验；非法 schema 在此返回错误，
// 使调用方能在启动期发现，而不是等模型返回后才炸。
//
// 注意：规格里写作 JSONSchemaOf[T]() ResponseFormat，但"非法则启动期报错"这条边界
// 要求能返回错误，因此签名带 error。这是有意的偏离。
func JSONSchemaOf[T any]() (ResponseFormat, error) {
	root := reflect.TypeOf((*T)(nil)).Elem()
	schema, err := schemaForType(root, map[reflect.Type]bool{})
	if err != nil {
		return ResponseFormat{}, fmt.Errorf("build json schema for %s: %w", root, err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return ResponseFormat{}, fmt.Errorf("encode json schema for %s: %w", root, err)
	}
	if err := ValidateSchema(raw); err != nil {
		return ResponseFormat{}, fmt.Errorf("generated json schema for %s is invalid: %w", root, err)
	}
	return ResponseFormat{Type: TypeJSONSchema, Schema: raw, Strict: true}, nil
}

// ValidateSchema 校验一段 JSON Schema 是否属于受支持的子集。
func ValidateSchema(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("json schema is empty")
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return fmt.Errorf("parse json schema: %w", err)
	}
	return validateSchemaNode(node, "$")
}

var (
	rawMessageType = reflect.TypeOf(json.RawMessage{})
	timeType       = reflect.TypeOf(time.Time{})
)

// schemaForType 递归生成类型对应的 JSON Schema 片段。
func schemaForType(t reflect.Type, seen map[reflect.Type]bool) (any, error) {
	if t == nil {
		return map[string]any{}, nil
	}
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	if t == rawMessageType {
		// RawMessage 已经是 JSON，无需再约束。
		return map[string]any{}, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaForType(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			// 自引用类型：用空 schema 截断递归，避免无限展开。
			return map[string]any{}, nil
		}
		seen[t] = true
		defer delete(seen, t)
		return schemaForStruct(t, seen)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			// []byte 在 encoding/json 里是 base64 字符串。
			return map[string]any{"type": "string"}, nil
		}
		return arraySchema(t.Elem(), seen)
	case reflect.Array:
		return arraySchema(t.Elem(), seen)
	case reflect.Map:
		key := t.Key()
		if key.Kind() != reflect.String && key.Kind() != reflect.Int && key.Kind() != reflect.Int64 {
			return nil, fmt.Errorf("map key type %s is not supported", key)
		}
		val, err := schemaForType(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": val}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("unsupported type %s (%s)", t, t.Kind())
	}
}

func arraySchema(elem reflect.Type, seen map[reflect.Type]bool) (any, error) {
	item, err := schemaForType(elem, seen)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "array", "items": item}, nil
}

func schemaForStruct(t reflect.Type, seen map[reflect.Type]bool) (any, error) {
	props := make(map[string]any, t.NumField())
	required := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // 未导出字段不参与编解码。
		}
		name, opts := jsonFieldName(f)
		if name == "-" {
			continue
		}
		fieldSchema, err := schemaForType(f.Type, seen)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		if enum := f.Tag.Get("enum"); enum != "" {
			fieldSchema = withEnum(fieldSchema, enum)
		}
		props[name] = fieldSchema
		if !hasOption(opts, "omitempty") {
			required = append(required, name)
		}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

func jsonFieldName(f reflect.StructField) (string, string) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, ""
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = f.Name
	}
	return name, strings.Join(parts[1:], ",")
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func withEnum(base any, enum string) any {
	src, _ := base.(map[string]any)
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	values := strings.Split(enum, ",")
	enumValues := make([]any, 0, len(values))
	for _, v := range values {
		enumValues = append(enumValues, strings.TrimSpace(v))
	}
	out["enum"] = enumValues
	return out
}

// validateSchemaNode 自校验生成结果，确保它落在受支持的 JSON Schema 子集内。
func validateSchemaNode(node any, path string) error {
	if _, ok := node.(bool); ok {
		return nil // JSON Schema 允许布尔 schema，直接放行。
	}
	m, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node must be an object, got %T", path, node)
	}
	if raw, ok := m["type"]; ok {
		if err := validateTypeValue(raw, path); err != nil {
			return err
		}
	}
	if raw, ok := m["properties"]; ok {
		props, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: properties must be an object", path)
		}
		for name, sub := range props {
			if err := validateSchemaNode(sub, path+"."+name); err != nil {
				return err
			}
		}
		if reqRaw, ok := m["required"]; ok {
			if err := validateRequiredValue(reqRaw, props, path); err != nil {
				return err
			}
		}
	}
	if raw, ok := m["items"]; ok {
		if err := validateSchemaNode(raw, path+"[]"); err != nil {
			return err
		}
	}
	if raw, ok := m["additionalProperties"]; ok {
		isBool := false
		if _, isBool = raw.(bool); !isBool {
			if err := validateSchemaNode(raw, path+".*"); err != nil {
				return err
			}
		}
	}
	if raw, ok := m["enum"]; ok {
		if _, isArray := raw.([]any); !isArray {
			return fmt.Errorf("%s: enum must be an array", path)
		}
	}
	return nil
}

func validateTypeValue(raw any, path string) error {
	switch v := raw.(type) {
	case string:
		if !validJSONType(v) {
			return fmt.Errorf("%s: invalid type %q", path, v)
		}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok || !validJSONType(s) {
				return fmt.Errorf("%s: invalid type entry %v", path, item)
			}
		}
	default:
		return fmt.Errorf("%s: type must be a string or array", path)
	}
	return nil
}

func validJSONType(s string) bool {
	switch s {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func validateRequiredValue(raw any, props map[string]any, path string) error {
	switch list := raw.(type) {
	case []string:
		for _, name := range list {
			if _, ok := props[name]; !ok {
				return fmt.Errorf("%s: required %q has no matching property", path, name)
			}
		}
	case []any:
		for _, item := range list {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s: required entries must be strings", path)
			}
			if _, ok := props[name]; !ok {
				return fmt.Errorf("%s: required %q has no matching property", path, name)
			}
		}
	default:
		return fmt.Errorf("%s: required must be an array", path)
	}
	return nil
}

// AdaptResponseFormat 按供应商能力降级结构化输出（F-31）。
//
// 供应商不支持 json_schema 时降级为 json_object，并返回需要附加到提示词的
// schema 文本（支持时返回空串）。降级只影响格式声明，不改变调用方 schema。
func AdaptResponseFormat(format ResponseFormat, supportsJSONSchema bool) (ResponseFormat, string) {
	if format.Type != TypeJSONSchema || supportsJSONSchema {
		return format, ""
	}
	downgraded := ResponseFormat{Type: TypeJSONObject, Strict: format.Strict}
	if len(format.Schema) == 0 {
		return downgraded, ""
	}
	return downgraded, schemaInstructionPrefix + string(format.Schema)
}

// ApplyFormatFallback 返回应用降级后的请求副本：格式替换为 json_object，
// 并把 schema 文本作为**末尾**的 system 消息附加。
//
// 附加在末尾而不是改写开头 system：ADR-0002 的稳定前缀不被打断，前缀缓存仍可命中。
func ApplyFormatFallback(req *ChatRequest, supportsJSONSchema bool) *ChatRequest {
	if req == nil || req.ResponseFormat == nil {
		return req
	}
	adapted, text := AdaptResponseFormat(*req.ResponseFormat, supportsJSONSchema)
	out := *req
	out.ResponseFormat = &adapted
	if text == "" {
		return &out
	}
	msgs := make([]Message, 0, len(req.Messages)+1)
	msgs = append(msgs, req.Messages...)
	msgs = append(msgs, Message{Role: RoleSystem, Content: text})
	out.Messages = msgs
	return &out
}

// DecodeStructured 把模型回复解析进 target，并按需做严格校验（F-31）。
//
// 非 JSON 返回 ErrNonJSON；严格模式下缺 required 字段返回 ErrMissingField。
func DecodeStructured(data []byte, target any, format ResponseFormat) error {
	if target == nil {
		return errors.New("structured output target is nil")
	}
	if !json.Valid(data) {
		return fmt.Errorf("%w: payload is not a JSON value", ErrNonJSON)
	}
	if format.Strict && len(format.Schema) > 0 {
		if err := checkRequired(data, format.Schema, "$"); err != nil {
			return err
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	return nil
}

// DecodeStructuredString 与 DecodeStructured 相同，输入为字符串。
func DecodeStructuredString(content string, target any, format ResponseFormat) error {
	return DecodeStructured([]byte(strings.TrimSpace(content)), target, format)
}

func checkRequired(data []byte, schema json.RawMessage, path string) error {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("parse response schema: %w", err)
	}
	return checkRequiredNode(data, s, path)
}

func checkRequiredNode(data []byte, schema map[string]any, path string) error {
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("%w: %s is not a JSON object", ErrNonJSON, path)
		}
		required, _ := schema["required"].([]any)
		for _, item := range required {
			name, ok := item.(string)
			if !ok || name == "" {
				continue
			}
			if _, exists := obj[name]; !exists {
				return fmt.Errorf("%w: %s.%s", ErrMissingField, path, name)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for name, raw := range obj {
			sub, ok := props[name].(map[string]any)
			if !ok {
				continue
			}
			if err := checkRequiredNode(raw, sub, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("%w: %s is not a JSON array", ErrNonJSON, path)
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return nil
		}
		for i, item := range arr {
			if err := checkRequiredNode(item, items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ChatStructured 调用 LLM 并把回复解析进 target（F-31）。
//
// 解析失败时**最多重试 1 次**，并把失败原因附在重试提示里。重试使用请求副本，
// 不修改调用方的 ChatRequest。
func ChatStructured(ctx context.Context, l LLM, req *ChatRequest, target any) (*ChatResponse, error) {
	if req == nil || req.ResponseFormat == nil {
		return nil, ErrNoResponseFormat
	}
	format := *req.ResponseFormat
	first, err := l.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if firstErr := DecodeStructuredString(first.Content, target, format); firstErr == nil {
		return first, nil
	} else {
		retryReq := cloneForStructuredRetry(req, first.Content, firstErr)
		second, retryErr := l.Chat(ctx, retryReq)
		if retryErr != nil {
			return nil, retryErr
		}
		if decodeErr := DecodeStructuredString(second.Content, target, format); decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrStructuredOutput, decodeErr)
		}
		return second, nil
	}
}

func cloneForStructuredRetry(req *ChatRequest, previous string, cause error) *ChatRequest {
	out := *req
	msgs := make([]Message, 0, len(req.Messages)+2)
	msgs = append(msgs, req.Messages...)
	msgs = append(msgs,
		Message{Role: RoleAssistant, Content: previous},
		Message{
			Role:    RoleUser,
			Content: fmt.Sprintf("上一次输出无法解析为目标 JSON：%v。请只输出一个合法 JSON 对象，不要输出解释或 Markdown 代码块。", cause),
		},
	)
	out.Messages = msgs
	return &out
}
