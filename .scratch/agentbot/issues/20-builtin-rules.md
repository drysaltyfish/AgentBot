# 20 · F-14 内置规则库

- **Feature**: F-14（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 17
- **写域（建议）**: internal/router/

## 目标

80% 的插件都在写"命令匹配/关键词/@我"，内置可减少重复。

## 交付物

- `Kind`/`Command`/`Prefix`/`Suffix`/`Keyword`/`FullMatch`/`Regex`
- `AtMe`/`OnlyGroup`/`OnlyPrivate`/`OnlyToMe`
- 权限类：`SuperUser`/`GroupAdmin`/`GroupOwner`/`HigherThan`
- 媒体类：`HasImage`/`HasReply`；`CheckUser`/`CheckGroup`

## 验收

- 每条规则至少 3 组正例 + 3 组反例（表驱动）
- nil Sender 的事件跑全部规则不 panic
- `Regex` 预编译，不得每条事件重新编译

## 备注（已定决策）

- `HigherThan` 查询群成员必须用带超时 ctx，失败返回 false（fail-closed）
- 权限规则不得直接解引用 `Sender.Role`

## Comments

### 2026-10-02 · 完成记录

实测：`internal/router/rules.go` + `rules_test.go`。文本类（Prefix 多备选/后缀/关键词/全等/Regex）9 组、范围类（OnlyGroup/OnlyPrivate/OnlyToMe/AtMe）11 组、媒体类（HasImage 写 `StateKeyImageURLs`、HasReply 写 `StateKeyReplyID`）、ID 类（SuperUser/CheckUser/CheckGroup）表驱动全过；权限类**全部 fail-closed**：nil lookup、查询返回 error、角色不足均返回 false；`HigherThan` 拒绝同用户比较；**无 sender 的 notice 事件跑全部 20 条规则不 panic**；`Regexp` 对非法模式返回 error、`Regex` 对非法模式恒不匹配，且子匹配写入 `StateKeyRegexMatch`。
