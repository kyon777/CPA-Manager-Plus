# CPAM 全局凭证健康摘要实施计划

> 执行方式：在隔离 worktree `lo/custom` 中按 TDD 执行；不操作 CPA Core。

## 1. 固化接口和纯分类器（RED → GREEN）

**文件**：`apps/manager-server/internal/service/credentiallist/global_metrics.go`、对应测试。

- 定义 `GlobalMetrics`、摘要状态和 `GlobalMetricsReader` 接口。
- 先写测试：全量库存中 100 个无证据的新账号计为 `unconfirmed`；禁用、认证失败、额度风险、成功证据按优先级互斥计数。
- 运行 `go test ./internal/service/credentiallist -run GlobalMetrics -count=1` 确认测试先失败。
- 实现最小纯函数和身份索引，运行同一命令至通过。

## 2. 接入 Manager 只读证据和快照缓存

**文件**：`global_metrics.go`、`service.go`、`apps/manager-server/internal/app/context.go`。

- 用 Store 的只读 repositories 读取最近请求、Header 额度快照、活动冷却、待处理操作、最近巡检/恢复投影。
- 不写任何 usage/billing/quota 数据；对每种证据设置上限和超时。
- 为完整库存指纹缓存摘要，处理 `warming`、`ready`、`stale`、`unavailable`。
- 保留旧构造函数，应用层使用带摘要读取器的新构造方式。
- 写服务测试覆盖摘要缓存、后台 warming、不完整库存不返回 ready、证据读取失败不影响列表。

## 3. 前端使用全局摘要

**文件**：`usageService.ts`、`AccountsPage.tsx`、`AccountMetricsGrid.tsx`、相关测试。

- 扩展 `CredentialListResponse` 类型。
- `ready` 且有摘要时直接使用全局卡片；`warming/unavailable` 时不把当前页标为全局，显示同步状态并保留可用回退。
- 删除服务端分页模式下的“当前页健康卡片”误导文案；保留分页、搜索、最近请求排序和代理筛选行为。
- 增加组件/页面测试，验证 50 条当前页 + 100 条全局待确认时卡片显示 100。

## 4. 验证和交付

```powershell
go test ./internal/service/credentiallist -count=1
go test ./internal/app -count=1
npm run test -- --run src/features/accounts/model/accountRows.test.ts src/features/accounts/components/AccountMetricsGrid.test.tsx src/features/accounts/hooks/useCredentialListPage.test.tsx src/features/accounts/accountsWorkspaceWiring.test.ts
npm run build
git diff --check
```

完成前核对：只改 CPAM；没有 CPA Core 容器/代码变更；不推送上游、不部署生产，除非用户另行要求。
