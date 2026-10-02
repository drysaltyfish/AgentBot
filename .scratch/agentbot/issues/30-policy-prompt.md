# 30 · F-53 权限即提示词

- **Feature**: F-53（P0）
- **里程碑**: M1
- **Status**: resolved
- **前置 ticket**: 26
- **写域（建议）**: internal/policy/, actions.yaml

## 目标

只做执行前拦截时模型仍会不断尝试越权；把"你只能调这些"写进提示词可大幅降低越权尝试。

## 交付物

- `actions.yaml` 声明 action 的 `desc`/`params`/`data` 与各角色允许列表；`//go:embed` 默认版本 + 外部文件覆盖
- `Policy.Render(role) string` 渲染 Markdown 表格，复用 F-33 的 `mdTable`
- 提示词显式声明"列表中没有的 action 不允许调用"
- 角色由**库内** `Resolver.Resolve(ctx, event)` 推导（基于 F-14 的规则 + 带超时的群成员查询）

## 验收

- 渲染结果与 `testdata/policy-<role>.golden` 逐字节相等
- 引用未定义 action 的 YAML 在启动期被拒绝
- 提示词中确实包含"列表中没有的 action 不允许调用"
- role 查询失败或无 sender → 落到最低权限角色（fail-closed）

## 备注（已定决策）

- Q7 已定：不接受宿主传入的裸 role 字符串
- 缓存失效见 F-54（M3）；热加载会主动使 F-65 静态段失效

## Comments

### 2026-10-02 · 完成记录

实测：`internal/policy/policy.go` + 内置 `actions.yaml` + 单测（9 项）。默认表启动期校验通过、5 个角色有序；**引用未定义 action 的 YAML 在启动期被拒并指出该 action 名**、无 action 时报 `ErrNoActions`；`Render` 两次逐字节相同、含强制句「列表中没有的 action 不允许调用。」与表头、且**不出现该角色无权调用的 action**；未知角色渲染返回 `ErrUnknownRole`；`Allow` 首次未命中才折叠 set（100 次命中后填充次数仍为 1）；未知角色/未定义 action 一律 fail-closed 返回 false；`Invalidate` 后缓存重建；16 goroutine 并发 `Allow`/`Render` 无异常。角色**在库内推导**：超管/群主/管理员/成员正确，查询失败、nil lookup、私聊、未知角色、nil resolver 全部落到 `everyone`；推导出的角色直接喂给 `Allow` 得到正确判定。
