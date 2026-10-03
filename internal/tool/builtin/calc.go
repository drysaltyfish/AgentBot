package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"

	"github.com/drysaltyfish/agentbot/internal/tool"
)

// MaxNumber 是计算器允许的数值上限（防止构造超大数把内存/时间拖垮）。
const MaxNumber = 1e15

type calculator struct{}

func (calculator) Name() string { return "calculator" }
func (calculator) Description() string {
	return "计算一个算术表达式，支持加 减 乘 除 取余 与括号"
}
func (calculator) Parameters() tool.Schema {
	return tool.Schema{
		Properties: map[string]tool.Property{
			"expr": {Type: "string", Description: "算术表达式，例如 (1+2)*3"},
		},
		Required: []string{"expr"},
	}
}
func (calculator) ReadOnly() bool        { return true }
func (calculator) ConcurrencySafe() bool { return true }

type calcArgs struct {
	Expr string `arg:"expr,required"`
}

func (calculator) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Failure(err.Error()), nil
	}
	in, err := tool.ParseArgs[calcArgs](args)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	v, err := EvalExpr(in.Expr)
	if err != nil {
		return tool.Failure(err.Error()), nil
	}
	return tool.Success(strconv.FormatFloat(v, 'f', -1, 64)), nil
}

// EvalExpr 用 go/parser 解析并白名单求值。
//
// 这里是"零注入面"的关键：**只允许**字面量、二元运算、一元运算与括号。
// 函数调用、标识符、下标、类型断言等一律拒绝，因此 __import__("os") 这类输入
// 会在节点类型检查处被挡下，而不是靠字符串黑名单。
func EvalExpr(src string) (float64, error) {
	expr, err := parser.ParseExpr(src)
	if err != nil {
		return 0, fmt.Errorf("表达式无法解析: %w", err)
	}
	return evalNode(expr)
}

func evalNode(n ast.Node) (float64, error) {
	switch e := n.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT && e.Kind != token.FLOAT {
			return 0, fmt.Errorf("不支持的常量类型: %s", e.Kind)
		}
		v, err := strconv.ParseFloat(e.Value, 64)
		if err != nil {
			return 0, fmt.Errorf("常量无法解析: %w", err)
		}
		return checkRange(v)

	case *ast.ParenExpr:
		return evalNode(e.X)

	case *ast.UnaryExpr:
		v, err := evalNode(e.X)
		if err != nil {
			return 0, err
		}
		// token.Token 有近 70 个取值，这里只允许四则；其余落到 default 拒绝。
		//nolint:exhaustive // 白名单式 switch，default 即拒绝
		// token.Token 有近 70 个取值，这里只允许四则；其余落到 default 拒绝。
		//nolint:exhaustive // 白名单式 switch，default 即拒绝
		switch e.Op {
		case token.ADD:
			return checkRange(v)
		case token.SUB:
			return checkRange(-v)
		default:
			return 0, fmt.Errorf("不支持的一元运算符: %s", e.Op)
		}

	case *ast.BinaryExpr:
		l, err := evalNode(e.X)
		if err != nil {
			return 0, err
		}
		r, err := evalNode(e.Y)
		if err != nil {
			return 0, err
		}
		// token.Token 有近 70 个取值，这里只允许四则；其余落到 default 拒绝。
		//nolint:exhaustive // 白名单式 switch，default 即拒绝
		switch e.Op {
		case token.ADD:
			return checkRange(l + r)
		case token.SUB:
			return checkRange(l - r)
		case token.MUL:
			return checkRange(l * r)
		case token.QUO:
			if r == 0 {
				return 0, fmt.Errorf("除数不能为 0")
			}
			return checkRange(l / r)
		case token.REM:
			// 取余只对整数有意义：浮点取余几乎总是模型的误用。
			if l != math.Trunc(l) || r != math.Trunc(r) {
				return 0, fmt.Errorf("取余只支持整数")
			}
			if r == 0 {
				return 0, fmt.Errorf("取余的除数不能为 0")
			}
			return checkRange(math.Mod(l, r))
		default:
			return 0, fmt.Errorf("不支持的运算符: %s", e.Op)
		}

	default:
		// 函数调用、标识符、下标、选择器等全部落到这里被拒绝。
		return 0, fmt.Errorf("表达式含不被允许的语法: %T", n)
	}
}

func checkRange(v float64) (float64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("计算结果不是有限数")
	}
	if math.Abs(v) > MaxNumber {
		return 0, fmt.Errorf("数值超出上限 %g", float64(MaxNumber))
	}
	return v, nil
}
