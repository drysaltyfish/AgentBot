package tool

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type calcArgs struct {
	Expr    string  `arg:"expr,required"`
	Digits  int     `arg:",default=2"`
	Mode    string  `arg:"mode,enum=fast|slow"`
	Verbose bool    `arg:"verbose"`
	Ratio   float64 `arg:"ratio"`
	Ignored string  `arg:"-"`
	Renamed string  `arg:"alias"`
}

func Test_F43_DecodesAndAppliesDefaults(t *testing.T) {
	t.Parallel()
	got, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1+1"}`))
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if got.Expr != "1+1" {
		t.Fatalf("expr: %q", got.Expr)
	}
	if got.Digits != 2 {
		t.Fatalf("default 未生效: %d", got.Digits)
	}
}

func Test_F43_ReportsAllMissingRequiredAtOnce(t *testing.T) {
	t.Parallel()
	type twoRequired struct {
		A string `arg:"a,required"`
		B string `arg:"b,required"`
		C string `arg:"c"`
	}
	_, err := ParseArgs[twoRequired](json.RawMessage(`{"c":"x"}`))
	if err == nil {
		t.Fatalf("缺必填应报错")
	}
	var errs ArgErrors
	if !errors.As(err, &errs) {
		t.Fatalf("错误类型应为 ArgErrors: %T", err)
	}
	// 一次报全部，而不是只报第一个。
	if len(errs) != 2 {
		t.Fatalf("应同时报告两个缺失字段: %+v", errs)
	}
	joined := err.Error()
	if !strings.Contains(joined, "a") || !strings.Contains(joined, "b") {
		t.Fatalf("错误信息应点名全部缺失字段: %s", joined)
	}
}

func Test_F43_EnumRejected(t *testing.T) {
	t.Parallel()
	_, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1","mode":"warp"}`))
	if err == nil {
		t.Fatalf("非法枚举应报错")
	}
	var errs ArgErrors
	if !errors.As(err, &errs) || len(errs) != 1 || errs[0].Field != "mode" {
		t.Fatalf("应精确指出 mode: %+v", err)
	}
}

// Test_F43_LenientConversion 覆盖"模型把数字/布尔写成字符串"。
func Test_F43_LenientConversion(t *testing.T) {
	t.Parallel()
	got, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1","digits":"5","verbose":"true","ratio":"1.5"}`))
	if err != nil {
		t.Fatalf("宽松转换应成功: %v", err)
	}
	if got.Digits != 5 || !got.Verbose || got.Ratio != 1.5 {
		t.Fatalf("宽松转换结果错误: %+v", got)
	}
}

func Test_F43_TypeMismatchReportsField(t *testing.T) {
	t.Parallel()
	_, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1","digits":"不是数字"}`))
	if err == nil {
		t.Fatalf("无法转换应报错")
	}
	var errs ArgErrors
	if !errors.As(err, &errs) || errs[0].Field != "digits" {
		t.Fatalf("应指出具体字段: %+v", err)
	}
	if !strings.Contains(errs[0].Rule, "integer") {
		t.Fatalf("应说明期望类型: %+v", errs[0])
	}
}

func Test_F43_UnknownFieldsIgnoredByDefault(t *testing.T) {
	t.Parallel()
	got, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1","bogus":"x"}`))
	if err != nil {
		t.Fatalf("未知字段默认应被忽略: %v", err)
	}
	if got.Expr != "1" {
		t.Fatalf("expr: %q", got.Expr)
	}

	_, err = ParseArgsStrict[calcArgs](json.RawMessage(`{"expr":"1","bogus":"x"}`))
	if err == nil {
		t.Fatalf("严格模式应拒绝未知字段")
	}
	var errs ArgErrors
	if !errors.As(err, &errs) || errs[0].Field != "bogus" {
		t.Fatalf("应指出未知字段名: %+v", err)
	}
}

func Test_F43_IgnoredAndRenamedFields(t *testing.T) {
	t.Parallel()
	got, err := ParseArgs[calcArgs](json.RawMessage(`{"expr":"1","alias":"别名","ignored":"不该进来"}`))
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if got.Renamed != "别名" {
		t.Fatalf("重命名字段未生效: %q", got.Renamed)
	}
	if got.Ignored != "" {
		t.Fatalf("arg:\"-\" 字段不应被赋值: %q", got.Ignored)
	}
}

func Test_F43_NonObjectAndNonStructInputs(t *testing.T) {
	t.Parallel()
	if _, err := ParseArgs[calcArgs](json.RawMessage(`[1,2]`)); err == nil {
		t.Fatalf("数组入参应报错")
	}
	// 空入参等价于"什么都没传"，此时只应触发必填校验。
	if _, err := ParseArgs[calcArgs](json.RawMessage(`{}`)); err == nil {
		t.Fatalf("空对象应触发必填校验")
	}
	if _, err := ParseArgs[int](json.RawMessage(`1`)); err == nil {
		t.Fatalf("非结构体类型应报错")
	}
}

func Test_F43_ArgErrorMessages(t *testing.T) {
	t.Parallel()
	e := &ArgError{Field: "city", Rule: "required", Got: "缺失"}
	for _, want := range []string{"city", "required", "缺失"} {
		if !strings.Contains(e.Error(), want) {
			t.Fatalf("错误信息缺少 %q: %s", want, e.Error())
		}
	}
	multi := ArgErrors{e, {Field: "n", Rule: "integer", Got: `"x"`}}
	if !strings.Contains(multi.Error(), "city") || !strings.Contains(multi.Error(), "n") {
		t.Fatalf("汇总错误应包含全部问题: %s", multi.Error())
	}
}
