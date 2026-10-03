package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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
