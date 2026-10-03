// AgentBot 的组合根：读配置 → 装配 → 启动 → 优雅关闭（FEATURES.md F-25 / F-70）。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/drysaltyfish/agentbot/internal/config"
)

// replyWorkers 是回复工作池大小；LLM 调用不应阻塞事件读循环。
const replyWorkers = 4

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agentbot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	checkOnly := fs.Bool("check-config", false, "只校验配置并打印生效配置（脱敏）后退出，不启动服务")
	selfTest := fs.Int64("selftest", 0, "连接平台后向该 QQ 号发送一条自检消息，然后退出")
	showStats := fs.Bool("stats", false, "打印用量台账后退出（F-85）")
	exportMem := fs.String("export-memories", "", "把全部记忆导出到该 JSONL 文件后退出（F-88）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	if *checkOnly {
		out, err := cfg.RedactedYAML()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprint(stdout, out)
		return 0
	}
	if *selfTest != 0 {
		return runSelfTest(cfg, *selfTest, stderr)
	}
	if *showStats {
		return runStats(cfg, stdout, stderr)
	}
	if *exportMem != "" {
		return runExportMemories(cfg, *exportMem, stdout, stderr)
	}
	return serve(cfg, stderr)
}
