package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint 返回若干字段的稳定指纹，用于幂等导入与去重。
//
// 各字段之间插入 0 字节分隔：否则 ("ab","c") 与 ("a","bc") 会拼成同一串、
// 得到同一指纹，把两条不同的记录误判成重复。
//
// 取 SHA-256 的前 16 字节（128 位）：这个量级下碰撞可以忽略，
// 又比存全量哈希省一半空间。
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}
