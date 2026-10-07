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

// SandboxRequest 声明 http_fetch 需要联网（F-46）。
//
// 沙箱的联网闸门完全依赖这个声明：Policy.Check 只在 req.Network 为真时才看
// network_tools / allow_network。没有它，这两个配置项永远是空转的——
// "默认不放开联网"也就只是文档里的一句话。
//
// 声明之后语义才真正成立：启用沙箱且没有把 http_fetch 列进 network_tools
// （也没开 allow_network）时，调用会被结构化拒绝而不是静默放行。
// 非联网参数（url）不需要声明，它由 httpx 自己的 SSRF 防护负责。
func (httpFetch) SandboxRequest(json.RawMessage) tool.SandboxRequest {
	return tool.SandboxRequest{Network: true}
}

// 编译期断言：联网声明必须始终满足沙箱的接口，否则上面那段契约会静默失效。
var _ tool.SandboxDeclarer = httpFetch{}

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
