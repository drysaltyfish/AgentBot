package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/tool"
)

type currentTime struct{ deps Deps }

func (currentTime) Name() string { return "current_time" }
func (currentTime) Description() string {
	return "获取当前时间，可指定时区"
}
func (currentTime) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"timezone": {Type: "string", Description: "IANA 时区名，例如 Asia/Shanghai；默认为 UTC"},
			"format":   {Type: "string", Description: "rfc3339（默认）或 unix", Enum: []string{"rfc3339", "unix"}},
		},
	}
}
func (currentTime) ReadOnly() bool        { return true }
func (currentTime) ConcurrencySafe() bool { return true }

type currentTimeArgs struct {
	Timezone string `arg:"timezone"`
	Format   string `arg:"format,enum=rfc3339|unix,default=rfc3339"`
}

func (t currentTime) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Failure(err.Error()), nil
	}
	in, err := tool.ParseArgs[currentTimeArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	loc := time.UTC
	if in.Timezone != "" {
		parsed, err := time.LoadLocation(in.Timezone)
		if err != nil {
			return tool.Failure(fmt.Sprintf("无效时区 %q: %v", in.Timezone, err)), nil
		}
		loc = parsed
	}
	now := t.deps.Now().In(loc)
	if in.Format == "unix" {
		return tool.Success(strconv.FormatInt(now.Unix(), 10)), nil
	}
	return tool.Success(now.Format(time.RFC3339)), nil
}

type jsonQuery struct{}

func (jsonQuery) Name() string { return "json_query" }
func (jsonQuery) Description() string {
	return "按路径从 JSON 里取值，只读，不执行任何代码"
}
func (jsonQuery) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"json": {Type: "string", Description: "JSON 文本"},
			"path": {Type: "string", Description: "路径，例如 a.b[0].c"},
		},
		Required: []string{"json", "path"},
	}
}
func (jsonQuery) ReadOnly() bool        { return true }
func (jsonQuery) ConcurrencySafe() bool { return true }

type jsonQueryArgs struct {
	JSON string `arg:"json,required"`
	Path string `arg:"path,required"`
}

func (jsonQuery) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Failure(err.Error()), nil
	}
	in, err := tool.ParseArgs[jsonQueryArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	v, err := QueryJSON([]byte(in.JSON), in.Path)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return tool.Failure(fmt.Sprintf("结果无法序列化: %v", err)), nil
	}
	return tool.Success(truncateOutput(string(out))), nil
}

// QueryJSON 按 path 从 JSON 文本取值。
//
// 用 UseNumber 解码：消息 ID、QQ 号是 int64，float64 会静默丢精度（F-39 的同类教训）。
func QueryJSON(data []byte, path string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		return root, nil
	}

	cur := root
	for _, seg := range splitPath(path) {
		switch {
		case seg.key != "":
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("路径 %q 处不是对象", seg.key)
			}
			v, ok := obj[seg.key]
			if !ok {
				return nil, fmt.Errorf("路径 %q 不存在", seg.key)
			}
			cur = v
		default:
			arr, ok := cur.([]any)
			if !ok {
				return nil, fmt.Errorf("路径下标 %d 处不是数组", seg.index)
			}
			if seg.index < 0 || seg.index >= len(arr) {
				return nil, fmt.Errorf("数组下标 %d 越界（长度 %d）", seg.index, len(arr))
			}
			cur = arr[seg.index]
		}
	}
	return cur, nil
}

type pathSeg struct {
	key   string
	index int
}

// splitPath 把 a.b[0].c 切成 a / b / [0] / c。
func splitPath(path string) []pathSeg {
	var out []pathSeg
	var buf strings.Builder
	flushKey := func() {
		if buf.Len() > 0 {
			out = append(out, pathSeg{key: buf.String()})
			buf.Reset()
		}
	}
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			flushKey()
		case '[':
			flushKey()
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				return append(out, pathSeg{key: path[i:]})
			}
			idx, err := strconv.Atoi(strings.TrimSpace(path[i+1 : i+j]))
			if err != nil {
				idx = -1
			}
			out = append(out, pathSeg{index: idx})
			i += j
		default:
			buf.WriteByte(path[i])
		}
	}
	flushKey()
	return out
}
