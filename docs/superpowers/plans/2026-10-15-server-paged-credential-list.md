# 服务端分页凭证列表实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让 CPAM 普通凭证列表通过 CPA 的 `page/page_size/include_proxy_url` 契约读取当前页摘要，首屏同时显示 `note` 与 `proxy_url`，并保持最近请求、代理筛选、批量动作和计费链路不变。

**Architecture:** Manager Server 为 CPA auth-files 分页请求提供 setup+查询变体的 stale-while-revalidate 缓存，并提供一个只读的凭证列表聚合接口：默认请求当前页，复杂筛选/最近请求排序由服务端快照处理，响应只包含当前页摘要。Web 的普通列表使用聚合接口；OAuth、批量变更、详情校验继续使用显式的定向/全量兼容接口。列表路径关闭页面级 runtime metadata 下载，代理以 CPA 投影为权威值。

**Tech Stack:** Go `net/http`、现有 `ProxyService`/SQLite store、React/TypeScript、Vitest、Go `testing`。

---

### Task 1: CPA 分页请求与缓存变体

**Files:**
- Modify: `apps/manager-server/internal/service/proxy/service.go`
- Test: `apps/manager-server/internal/service/proxy/service_test.go`
- Modify: `apps/web/src/types/authFile.ts`
- Modify: `apps/web/src/services/api/authFiles.ts`
- Test: `apps/web/src/services/api/authFiles.test.ts`

- [ ] 为带 `page/page_size/include_proxy_url` 的 GET 写 Go 回归测试：上游收到精确 snake_case 查询；同一 setup 不同页不共享缓存；写操作使全部变体失效。
- [ ] 为 `authFilesApi.listPage` 写 Vitest：发送 `page`、`page_size`、`include_proxy_url=1`，去重只作用于当前页且保留服务端 `total/page/page_size/has_more/proxy_url_included`。
- [ ] 实现查询规范化、变体缓存键、并发合并刷新和带查询的 CPA 请求；保留旧的无查询定向请求作为 mutation/lookup 兼容路径。
- [ ] 实现类型与 API 方法，禁止普通分页请求携带完整 token 字段到 CPAM 新接口。

### Task 2: Manager Server 凭证列表聚合接口

**Files:**
- Create: `apps/manager-server/internal/service/credentiallist/service.go`
- Test: `apps/manager-server/internal/service/credentiallist/service_test.go`
- Create: `apps/manager-server/internal/http/controller/credentiallist/handler.go`
- Test: `apps/manager-server/internal/http/controller/credentiallist/handler_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/http/router/router.go`

- [ ] 先写服务测试：默认页只返回 `page_size` 条；快照 key 按 setup 隔离；search/provider/enabled 过滤与最近请求排序在服务端执行；响应不含 token/完整 JSON。
- [ ] 实现受限内存快照：首请求同步页 1 并后台预热剩余页；复杂查询在快照未完成时返回 `sync_status=warming`，不伪造全局结果；TTL 与写操作失效。
- [ ] 使用 `ProxyService` 的分页读取能力，不访问 collector、pricing 或 usage writer；只读加入现有 recent index（失败时保留 `-`）。
- [ ] 暴露 `GET /usage-service/credential-list`，要求面板鉴权，支持 `page/page_size/search/provider/status/sort/include_proxy_url`，返回当前页、total、sync 状态和 facets。

### Task 3: Web 普通列表切换与 runtime metadata 降载

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Test: `apps/web/src/services/api/usageService.credentialList.test.ts`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/hooks/useProxyFilterInventory.ts`
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts`
- Test: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Test: `apps/web/src/features/accounts/hooks/useProxyFilterInventory.test.tsx`

- [ ] 先写 API/组件失败测试：普通页只请求 credential-list；CPA 已投影代理时不请求页面级 runtime metadata；代理筛选使用已启用摘要中的 proxy URL。
- [ ] 实现 debounce 250–400ms、页码/筛选/排序参数和 stale 页面保留；当前页才加载详细 history/window/header 数据。
- [ ] 保留 mutation/lookup/OAuth 旧 API；变更后只失效当前列表查询与代理摘要，不触碰 usage 表。
- [ ] 增加 `sync_status`/同步年龄提示，未知代理不得判为未使用。

### Task 4: 回归、文档与发布前检查

**Files:**
- Modify: `docs/superpowers/specs/2026-10-15-server-paged-credential-list-design.md`
- Modify: `docs/CPAM_CUSTOMIZATIONS.md`（若存在；否则创建）

- [ ] 运行 Go/TS 定向测试、type-check、build、`git diff --check`。
- [ ] 检查列表请求未调用模型转发、collector、pricing 或 usage writer；确认 `usage_events` 相关代码无 diff。
- [ ] 从干净 worktree 构建 CPAM 候选镜像，记录 Core 容器身份前后未变；只重建 `cpa-manager-plus`。
- [ ] 更新二改记忆，提交并只推送 `origin/lo/custom`；不向 `upstream` push。
