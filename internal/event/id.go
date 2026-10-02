package event

import (
	"encoding/json"
	"hash/crc64"
	"strconv"
)

var isoTable = crc64.MakeTable(crc64.ISO)

// ID 是通用消息 ID：数字平台与字符串平台共用同一套下游接口（FEATURES.md F-02）。
//
// 零值构造请用 IDFromInt64 / IDFromString，不要直接构造字面量。
type ID struct {
	num int64
	raw string
}

// IDFromInt64 用数字构造。
func IDFromInt64(v int64) ID { return ID{num: v} }

// IDFromString 用字符串构造。
//
// 先尝试解析为整数；失败则用 crc64(ISO) 做映射，并在结果落入真实数字 ID 的
// 取值区间时置位（否则伪造 ID 可能与真实 QQ 号相撞）。
func IDFromString(s string) ID {
	if s == "" {
		return ID{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ID{num: n, raw: s}
	}
	u := crc64.Checksum([]byte(s), isoTable)
	if u <= 0xffff_ffff {
		u |= 1 << 32
	}
	u &^= 1 << 63 // 保持为正数，便于日志阅读
	//nolint:gosec // u 的最高位刚被清除，转换到 int64 不会溢出
	return ID{num: int64(u), raw: s}
}

// Int64 返回数字表示。
func (id ID) Int64() int64 { return id.num }

// String 返回原始串（若有），否则返回数字的十进制表示。
func (id ID) String() string {
	if id.raw != "" {
		return id.raw
	}
	return strconv.FormatInt(id.num, 10)
}

// IsZero 判断是否为零值。
func (id ID) IsZero() bool { return id.num == 0 && id.raw == "" }

// Equal 比较两个 ID；raw 非空时比 raw，否则比 num。
func (id ID) Equal(other ID) bool {
	if id.raw != "" || other.raw != "" {
		return id.raw == other.raw
	}
	return id.num == other.num
}

// MarshalJSON 自适应输出：raw 可解析为整数或为空时输出 number，否则输出 string。
func (id ID) MarshalJSON() ([]byte, error) {
	if id.raw == "" {
		return []byte(strconv.FormatInt(id.num, 10)), nil
	}
	if n, err := strconv.ParseInt(id.raw, 10, 64); err == nil {
		return []byte(strconv.FormatInt(n, 10)), nil
	}
	return json.Marshal(id.raw)
}

// UnmarshalJSON 同时接受 number 与 string。
func (id *ID) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*id = ID{}
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*id = IDFromString(s)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = IDFromInt64(n)
	return nil
}
