package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Test_F79_ExampleConfigHasNoUnknownKeysAnywhere 是上面那条测试的**加强版**。
//
// 上面只遍历顶层键，所以嵌套拼错（例如把 ratelimit.user_burst 写成 user_bust）
// 不会被发现：顶层键 ratelimit 仍然存在，那个测试照样通过。
// 而运行时用的是 KnownFields(true)（load.go），同一个文件到了线上才会启动失败。
//
// 这里用与装载完全相同的解码方式解析示例配置：任何层级出现 schema 里没有的键，
// 都会在 CI 里失败，而不是等到部署那一刻。
func Test_F79_ExampleConfigHasNoUnknownKeysAnywhere(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("config.example.yaml 存在 schema 之外的键（嵌套也算）: %v", err)
	}
}

// Test_F79_ExampleConfigKeysExistInSchema 保证文档里写的配置项真的存在（F-79）。
//
// 反过来的方向（schema 有了但示例没写）不报错：可选字段不写进示例是合理的。
func Test_F79_ExampleConfigKeysExistInSchema(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse config.example.yaml: %v", err)
	}

	known := map[string]bool{}
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		known[strings.Split(tag, ",")[0]] = true
	}

	for key := range doc {
		if !known[key] {
			t.Errorf("config.example.yaml 里的 %q 在 config.Config 里不存在——文档描述了不存在的配置项", key)
		}
	}
}
