// Package scoped 实现作用域配置的分层取值与人格路由键（FEATURES.md F-82）。
//
// 配置从最不具体到最具体分三层：全局 → 群 → 用户；人格是另一条维度，
// 它回答"当前处于哪套设定"。取值时按作用域路径逐级回退，直到全局默认。
//
// 显式零值与"未设置"必须可区分：Get 用 ok 布尔表达存在性，绝不把零值当缺省。
// 人格名本身就是一个作用域（Session.Persona 即作用域键），对本包而言没有魔法。
package scoped

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DefaultPersona 是未配置任何人格时使用的内置人格名。
//
// 空人格必须仍然得到稳定的键，因此空串在归一化时一律替换为它。
const DefaultPersona = "default"

// KeyPersona 是配置里"当前人格"的键名。
const KeyPersona = "persona"

// maxPersonaNameLen 是人格名长度上限。
const maxPersonaNameLen = 64

// NormalizePersona 把人格名归一化：两端空白去掉，空串替换为 DefaultPersona。
func NormalizePersona(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultPersona
	}
	return name
}

// ValidPersonaName 报告 name 是否是合法人格名。
//
// 合法名以字母或数字开头，只含小写字母、数字、'.'、'_'、'-'，长度不超过 64。
// 大写与路径分隔符被拒绝：人格名同时是文件名与路由键的一部分，允许它们只会带来歧义。
func ValidPersonaName(name string) bool {
	if len(name) == 0 || len(name) > maxPersonaNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ScopePath 把若干作用域段拼成一条层级路径，忽略空段。
//
// 路径中越靠后的段越具体、优先级越高；Get 会从整条路径逐级回退到全局。
func ScopePath(segments ...string) string {
	kept := make([]string, 0, len(segments))
	for _, s := range segments {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, "/")
}

// CleanScope 归一化作用域：去掉空白与空段，统一分隔符。
func CleanScope(scope string) string {
	return ScopePath(strings.Split(scope, "/")...)
}

// Parent 返回作用域路径的上一级；全局（空串）的上一级仍是空串。
func Parent(scope string) string {
	scope = CleanScope(scope)
	if i := strings.LastIndexByte(scope, '/'); i >= 0 {
		return scope[:i]
	}
	return ""
}

// PersonaScope 返回某个人格定义所在的作用域，即人格名本身。
func PersonaScope(persona string) string { return NormalizePersona(persona) }

// GroupScope 返回群作用域；群为空时返回空串（即全局）。
func GroupScope(group string) string {
	if strings.TrimSpace(group) == "" {
		return ""
	}
	return ScopePath("group", group)
}

// UserScope 返回"群 → 用户"作用域；没有群时退化为纯用户作用域，
// 用户为空时退化为群作用域。
func UserScope(group, user string) string {
	if strings.TrimSpace(user) == "" {
		return GroupScope(group)
	}
	return ScopePath(GroupScope(group), "user", user)
}

// SessionScopes 返回一个会话解析配置时的作用域优先级链（高 → 低）：
// 用户 → 群 → 人格 → 全局。配合 Config.GetScoped 使用。
//
// 人格放在场景之后：群/用户是"在哪套设定下按场景微调"，人格是"哪套设定"。
// 空人格归一到 DefaultPersona，因此链尾始终有一个可命中的默认层。
func SessionScopes(persona, group, user string) []string {
	scopes := make([]string, 0, 3)
	add := func(s string) {
		if s == "" {
			return
		}
		for _, existing := range scopes {
			if existing == s {
				return
			}
		}
		scopes = append(scopes, s)
	}
	add(UserScope(group, user))
	add(GroupScope(group))
	add(PersonaScope(persona))
	return scopes
}

// RouteKey 由 (scope, persona) 计算稳定、无歧义的路由/缓存键。
//
// 键里同时含作用域与人格：同作用域同人格必得同键；换人格必得不同键——
// 这是 F-65 半静态段稳定、以及前缀缓存按人格隔离的前提。空人格先归一化为
// DefaultPersona，因此空人格也有稳定键。用长度前缀分隔字段，避免 "a/b" 与
// 人格名互相混淆。
func RouteKey(scope, persona string) string {
	scope = CleanScope(scope)
	persona = NormalizePersona(persona)
	return fmt.Sprintf("v1:%d:%s:%d:%s", len(scope), scope, len(persona), persona)
}

// RouteFingerprint 返回路由键的短摘要（十六进制），适合放进日志与指标标签。
func RouteFingerprint(scope, persona string) string {
	return shortHash(RouteKey(scope, persona))
}

// shortHash 返回字符串的 8 字节 SHA-256 摘要（十六进制）。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
