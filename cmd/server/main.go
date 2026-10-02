// AgentBot 的组合根：读配置 → 装配 → 启动 → 优雅关闭（FEATURES.md F-25 / F-70）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drysaltyfish/agentbot/internal/bot"
	"github.com/drysaltyfish/agentbot/internal/config"
	"github.com/drysaltyfish/agentbot/internal/observe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agentbot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	checkOnly := fs.Bool("check-config", false, "只校验配置并打印生效配置（脱敏）后退出，不启动服务")
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

	return serve(cfg, stderr)
}

func shutdownTimeout(cfg *config.Config) time.Duration {
	if cfg.Shutdown.Timeout != nil && cfg.Shutdown.Timeout.D > 0 {
		return cfg.Shutdown.Timeout.D
	}
	return 10 * time.Second
}

func serve(cfg *config.Config, stderr io.Writer) int {
	timeout := shutdownTimeout(cfg)

	lg := observe.New(observe.Options{
		Level:        cfg.Log.Level,
		Format:       cfg.Log.Format,
		Components:   cfg.Log.Components,
		DebugContent: cfg.Log.DebugContent,
		QueueSize:    queueSize(cfg),
		Writer:       os.Stdout,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lg.Close(ctx)
	}()

	app := bot.New(bot.WithShutdownTimeout(timeout))
	app.MarkRunning()

	lifecycle := lg.Component("lifecycle")
	lifecycle.Info("agentbot started",
		"transport", cfg.Transport.Mode,
		"llm_provider", cfg.LLM.Provider,
		"capabilities", []string{"config", "logging", "graceful-shutdown"},
	)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	<-sig
	lifecycle.Info("shutdown signal received", "timeout", timeout.String())

	go func() {
		<-sig
		_, _ = fmt.Fprintln(stderr, "second signal received: forcing exit")
		os.Exit(1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		lifecycle.Error("shutdown incomplete", "error", err)
		return 1
	}
	lifecycle.Info("shutdown complete")
	return 0
}

func queueSize(cfg *config.Config) int {
	if cfg.Log.QueueSize != nil {
		return *cfg.Log.QueueSize
	}
	return 1024
}
