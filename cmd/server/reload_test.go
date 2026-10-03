package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drysaltyfish/agentbot/internal/moderation"
	"github.com/drysaltyfish/agentbot/internal/textguard"
)

// Test_F24_SensitiveWordsHotReload 覆盖 F-24 点名的"敏感词表"热加载：
// 改文件后无需重启即生效，且旧词表在新词表生效前一直可用。
func Test_F24_SensitiveWordsHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.txt")
	if err := os.WriteFile(path, []byte("旧词\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m0, err := compileWordsFile(path, "***")
	if err != nil {
		t.Fatalf("compileWordsFile: %v", err)
	}
	eng := moderation.New(moderation.Options{
		Matcher:   textguard.NewEngine(m0),
		Sensitive: moderation.SensitiveBlock,
	})
	if d, _ := eng.Review(context.Background(), moderation.Message{Text: "旧词"}, moderation.Meta{}); !d.Blocked() {
		t.Fatalf("初始词表应拦截")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := watchSensitiveWords(ctx, path, nil, eng, testLogger(t))
	if w == nil {
		t.Fatalf("配置了词表文件时 watcher 不应为 nil")
	}
	defer w.Stop()

	if err := os.WriteFile(path, []byte("新词\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d, _ := eng.Review(context.Background(), moderation.Message{Text: "新词"}, moderation.Meta{}); d.Blocked() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("词表更新后未生效：热加载没有把新词表换上去")
}

// Test_F24_NoWordsFileMeansNoWatcher 覆盖边界：未配置词表文件时不启动监听。
func Test_F24_NoWordsFileMeansNoWatcher(t *testing.T) {
	eng := moderation.New(moderation.Options{})
	if w := watchSensitiveWords(context.Background(), "", nil, eng, testLogger(t)); w != nil {
		t.Fatalf("未配置词表文件时不应启动 watcher")
	}
	if w := watchSensitiveWords(context.Background(), "/tmp/x", nil, nil, testLogger(t)); w != nil {
		t.Fatalf("没有审查引擎时不应启动 watcher")
	}
}
