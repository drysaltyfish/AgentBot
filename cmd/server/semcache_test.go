package main

import (
	"strings"
	"testing"

	"github.com/drysaltyfish/agentbot/internal/config"
)

// Test_F63_BuildSemcacheFollowsConfig 覆盖 F-63 的接线本身：
// 默认关闭、开启后真的构造出缓存、非法跳过正则让启动失败（fail-fast）。
func Test_F63_BuildSemcacheFollowsConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	lg := testLogger(t)

	if c, err := buildSemcache(cfg, nil, lg); err != nil || c != nil {
		t.Fatalf("默认关闭时应返回 (nil,nil): (%v,%v)", c, err)
	}

	cfg.Semcache.Enabled = ptr(true)
	cfg.Semcache.Threshold = ptr(0.9)
	cfg.Semcache.MaxEntries = ptr(8)
	c, err := buildSemcache(cfg, nil, lg)
	if err != nil || c == nil {
		t.Fatalf("启用后应构造出缓存: (%v,%v)", c, err)
	}

	// 非法正则必须让构造失败：静默忽略会让"跳过实时话题"变成一句空话。
	cfg.Semcache.SkipPatterns = []string{"("}
	if _, err := buildSemcache(cfg, nil, lg); err == nil {
		t.Fatal("非法跳过正则必须让启动失败")
	}
}

// Test_F63_SemcacheConfigValidation 钉住配置校验：阈值越界要在启动期报错。
func Test_F63_SemcacheConfigValidation(t *testing.T) {
	t.Parallel()
	// config.Default() 本身并不完整（没有 model/url），这里只断言 semcache 相关的问题：
	// 逐个字段验证比"整体合法"更能指出到底是谁错了。
	cfg := config.Default()
	cfg.Semcache.Threshold = ptr(1.5)
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "semcache.threshold") {
		t.Fatalf("阈值 >1 必须被拒绝: %v", err)
	}
	cfg.Semcache.Threshold = ptr(0.5)
	cfg.Semcache.TTLSeconds = ptr(0)
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "semcache.ttl_seconds") {
		t.Fatalf("ttl_seconds=0 必须被拒绝: %v", err)
	}
	cfg.Semcache.TTLSeconds = ptr(60)
	err = cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "semcache.") {
		t.Fatalf("合法的 semcache 配置不该出现在错误里: %v", err)
	}
}
