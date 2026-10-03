package textsim

import "testing"

// Test_MinLengthGuard 守住实测过的那对误合并。
func Test_MinLengthGuard(t *testing.T) {
	t.Parallel()
	// 4 个字，相似度正好 0.50，但它们是两件不同的事。
	if IsDuplicate("旧的一条", "新的一条") {
		t.Fatalf("短文本不该靠相似度合并（实测相似度 0.50）")
	}
	// 6 个字以上才启用相似度：实测 0.57，确实是同一件事。
	if !IsDuplicate("我喜欢喝橙汁", "用户喜欢喝橙汁") {
		t.Fatalf("改写应判为同一条（相似度 %.2f）", Similarity("我喜欢喝橙汁", "用户喜欢喝橙汁"))
	}
	// 不同的偏好不该合并。
	if IsDuplicate("我喜欢喝橙汁", "我喜欢喝冰美式") {
		t.Fatalf("不同偏好不该合并（相似度 %.2f）", Similarity("我喜欢喝橙汁", "我喜欢喝冰美式"))
	}
	// 完全相同永远算同一条，与长度无关。
	if !IsDuplicate("短", "短") {
		t.Fatalf("完全相同必须判为同一条")
	}
	if IsDuplicate("短", "长") {
		t.Fatalf("不同的短文本不得合并")
	}
}

func Test_SimilaritySymmetry(t *testing.T) {
	t.Parallel()
	a, b := "我喜欢喝橙汁", "用户喜欢喝橙汁"
	if Similarity(a, b) != Similarity(b, a) {
		t.Fatalf("相似度应对称")
	}
	if Similarity("", "x") != 0 {
		t.Fatalf("空串相似度应为 0")
	}
}
