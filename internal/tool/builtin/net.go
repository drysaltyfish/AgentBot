package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/drysaltyfish/agentbot/internal/httpx"
	"github.com/drysaltyfish/agentbot/internal/tool"
)

type httpFetch struct{ deps Deps }

func (httpFetch) Name() string { return "http_fetch" }
func (httpFetch) Description() string {
	return "抓取一个 http/https 链接的文本内容；私网地址会被拒绝"
}
func (httpFetch) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"url": {Type: "string", Description: "要抓取的 http/https 链接"},
		},
		Required: []string{"url"},
	}
}
func (httpFetch) ReadOnly() bool        { return true }
func (httpFetch) ConcurrencySafe() bool { return true }

type httpFetchArgs struct {
	URL string `arg:"url,required"`
}

// Execute 走 F-59 的 httpx：私网拒绝、DNS 解析结果校验、每一跳复检、体积上限、超时。
//
// 刻意不自己写 http.Client：SSRF 防护的细节（尤其 DNS rebinding）很容易漏，
// 复用一份经过测试的实现比"再写一遍"安全得多。
func (t httpFetch) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	in, err := tool.ParseArgs[httpFetchArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}

	cfg := t.deps.HTTP
	if cfg.MaxBytes == 0 {
		cfg = httpx.Defaults()
	}

	callCtx, cancel := context.WithTimeout(ctx, DefaultFetchTimeout)
	defer cancel()

	resp, err := cfg.Get(callCtx, in.URL)
	if err != nil {
		return tool.Failure(fmt.Sprintf("抓取失败: %v", err)), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return tool.Failure(fmt.Sprintf("目标返回 %d", resp.StatusCode)), nil
	}

	body := string(resp.Body)
	if resp.Truncated {
		body += "\n[truncated]"
	}
	return tool.Success(truncateOutput(body)), nil
}
