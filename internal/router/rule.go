package router

// Rule 只做判断与解析：它把解析结果写进 Ctx.State，不产生副作用。
//
// Rule 内不得起 goroutine（会被判定为快速返回而使超时失效）。
type Rule func(*Ctx) bool

// Handler 只做业务：它从 Ctx.State 取参数，不重复解析。
type Handler func(*Ctx)

// And 返回"全部通过"的组合规则；空入参恒为真。
func And(rules ...Rule) Rule {
	snapshot := append([]Rule(nil), rules...)
	return func(c *Ctx) bool {
		for _, r := range snapshot {
			if !r(c) {
				return false
			}
		}
		return true
	}
}

// All 是 And 的别名，保持 F-10 的 R.All(rules...) 写法。
func All(rules ...Rule) Rule { return And(rules...) }

// Or 返回"任一通过"的组合规则；空入参恒为假。
func Or(rules ...Rule) Rule {
	snapshot := append([]Rule(nil), rules...)
	return func(c *Ctx) bool {
		for _, r := range snapshot {
			if r(c) {
				return true
			}
		}
		return false
	}
}

// Not 取反。
func Not(r Rule) Rule {
	return func(c *Ctx) bool { return !r(c) }
}
