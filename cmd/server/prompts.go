package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/observe"
	"github.com/drysaltyfish/agentbot/internal/prompt"
)

// prefixData 是静态前缀模板的数据。
//
// 字段与顺序都固定：F-65 要求静态段在进程启动期内逐字节稳定，
// 因此这里**不放时间、随机数、会话相关内容**——那些属于动态段。
// （时间已经在轮次渲染里出现，重复放在静态段只会让缓存每次失效。）
type prefixData struct {
	SystemPrompt    string
	ProactiveMemory string
	Identity        string
	ToolHint        string
	// Now/Timezone 只为外部模板保留：内置模板**不使用**它们，
	// 因为静态段里放时间会让前缀每次渲染都变，缓存直接失效。
	Now      time.Time
	Timezone string
}

// buildPromptEngine 构造模板引擎并在启动期校验（F-33）。
//
// 校验必须发生在启动期：模板写错变量名、括号不配对，都要在服务起来之前暴露，
// 而不是等某个用户聊到某条分支才发现。外部目录（prompt.dir）里的同名模板
// 覆盖内置版本；缺失则回退内置版本，不中断服务。
func buildPromptEngine(cfg *config.Config, lg *observe.Logger) (*prompt.Engine, error) {
	eng := prompt.New(prompt.Options{
		Dir: strings.TrimSpace(cfg.Prompt.Dir),
		// 样例数据只需"形状正确"：校验看的是模板引用的字段在不在。
		SampleData: map[string]any{
			"SystemPrompt":    "样例系统提示词",
			"ProactiveMemory": "样例记忆指令",
			"Identity":        "样例身份说明",
			"ToolHint":        "样例工具提示",
			"Now":             time.Now(),
			"Timezone":        "Asia/Shanghai",
		},
		Now: time.Now,
		OnSlowRender: func(name string, took time.Duration) {
			if lg != nil {
				lg.Component("prompt").Warn("prompt template render is slow", "template", name, "took", took)
			}
		},
	})
	if err := eng.ValidateStartup(); err != nil {
		return nil, fmt.Errorf("validate prompt templates in %q: %w", cfg.Prompt.Dir, err)
	}
	return eng, nil
}

// renderSystemPrefix 用模板渲染不可变前缀（F-33 + F-65）。
func renderSystemPrefix(eng *prompt.Engine, data prefixData) (string, error) {
	if eng == nil {
		return "", fmt.Errorf("prompt engine is nil")
	}
	text, err := eng.Render("system", data)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}
