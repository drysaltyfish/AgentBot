// Package unsafeutil 是仓库内唯一允许使用 unsafe 的目录（FEATURES.md F-73）。
//
// .golangci.yml 对 internal/unsafeutil/ 关闭了 forbidigo，其余目录一律禁止
// unsafe。任何放进本包的文件都必须在文件头注释里写明：
//
//  1. 为什么标准库无法达成同样的效果；
//  2. 该用法在什么条件下是安全的；
//  3. 对应的测试在哪里。
//
// 目前本包为空——这是有意为之。请不要为了"以后可能用到"而添加内容。
package unsafeutil
