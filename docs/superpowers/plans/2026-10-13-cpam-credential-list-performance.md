# CPAM 凭证列表与批量导入性能优化实施计划

> **For implementation:** 本计划只修改 CPAM（Manager Server 与 Web），绝不修改、部署或重启 CPA Core。

**Goal:** 在保留“最近请求”全局排序/展示语义的前提下，降低凭证列表首次加载、刷新、批量粘贴 JSON 和 502/503/限流降优先级链路对 CPA Core 与 CPAM SQLite 的放大压力，并消除常见的 30 秒前端超时。

**Architecture:**
1. Manager Server 为只读 auth-files 清单提供按上游配置隔离的内存 stale-while-revalidate 缓存和并发合并；写操作成功后失效缓存。
2. 502/503/精确 429 降优先级 worker 缓存短期 auth-files 快照、失败冷却并使用与 CPAM 相同的已解析上游配置，避免每条错误都重新拉完整清单。
3. 新增轻量“最近请求索引”接口，前端用它维持全局按最近请求排序；明细历史请求改为只加载当前页。
4. 多 JSON 粘贴改为一次提交至 CPAM 的异步批量作业，服务端串行上传、前端轮询状态、完成后只刷新一次清单。

**Tech Stack:** Go net/http / 现有 Manager Server service-repository 分层、React/TypeScript、Vitest、Go testing。

---

## 约束与验收准则

- 只改 apps/manager-server 和 apps/web；不触碰 cli-proxy-api、CPA Core 数据卷、Core 容器或其配置。
- 不把认证 JSON/token 写入 SQLite、日志、任务状态响应或浏览器 localStorage。
- 清单缓存不能跨不同 CPA upstream/management key 复用，缓存 key 只保存不可逆摘要。
- GET /v0/management/auth-files 的读缓存命中必须保持原始响应体；auth-files 的成功写操作必须失效缓存。
- 最近请求全局排序仍基于全部当前筛选范围的轻量索引，而不是仅当前页。
- 批量导入 100 个 JSON 时浏览器仅发起一次提交请求，完成后仅做一次清单刷新。
- 不做在线 VACUUM、清理 usage 数据或修改既有统计口径；数据库维护单独在备份/维护窗口实施。

---

## Task 1: 降优先级 worker 去重、缓存与内部上游解析

**Files:**
- Modify: apps/manager-server/internal/worker/server_error_priority_demotion.go
- Modify: apps/manager-server/internal/worker/server_error_priority_demotion_test.go
- Modify: apps/manager-server/cmd/manager-server/main.go（或现有装配位置）

**Steps:**
1. 先新增失败测试：连续同一 provider 的候选事件在快照 TTL 内只拉一次 auth-files，且失败后冷却期内不再次拉取。
2. 新增可注入 setup resolver；存在 resolver 时优先使用其已解析配置，保留旧构造函数/回退以避免现有调用被破坏。
3. 在 worker 内保存短 TTL 的完整 auth-files 快照和按账号的近期处理标记；优先从缓存计算同 provider 排除自身后的最高优先级。
4. 上传 priority 成功后同步更新内存快照或失效，保证后续候选不会拿到旧 peer max。
5. 使用相同 resolver 装配 worker，禁止输出 management key。
6. 运行针对 worker 的 Go 测试并确认先失败、后通过。

## Task 2: auth-files 读缓存与写后失效

**Files:**
- Modify: apps/manager-server/internal/service/proxy/service.go
- Modify: apps/manager-server/internal/service/proxy/service_test.go（或新增专用测试）
- Modify: apps/manager-server/internal/http/controller/proxy/handler.go（仅在需要暴露缓存状态时）

**Steps:**
1. 先新增测试：同一 setup 的并发/连续 GET 只调用一次上游，缓存过期后可合并刷新；不同 setup 不共享；写成功会失效。
2. 用不可逆的 setup 摘要作为内存缓存 scope，缓存原始 2xx JSON body/content type。
3. 新鲜缓存直接返回；过期但仍可用时立即返回 stale 并后台单飞刷新；冷启动仍按既有反向代理路径请求上游。
4. 对 auth-files 的 2xx POST/PUT/PATCH/DELETE 失效相应 scope，避免导入/修改后旧清单持续展示。
5. 加响应头 X-CPAM-Inventory-Cache: fresh|stale|miss 与安全的年龄毫秒，便于排障但不泄密。
6. 运行 proxy 相关 Go 测试。

## Task 3: 轻量最近请求索引与当前页明细

**Files:**
- Modify: apps/manager-server/internal/service/monitoring/service.go
- Modify: apps/manager-server/internal/http/controller/monitoring/handler.go
- Modify: apps/manager-server/internal/http/router/router.go
- Modify/Add: 相应 Go 测试
- Modify: apps/web/src/services/api/monitoring.ts
- Modify: apps/web/src/pages/accounts/AccountsPage.tsx
- Modify/Add: 前端测试

**Steps:**
1. 写 Go 测试：给定多个 target，轻量 endpoint 仅返回 row_key/latest_request/recent_requests/sync_status，不触发 usage/cost 聚合。
2. 提取现有 recent-request 查询的可复用部分，新增受限（最多 1000 target）的批量索引 endpoint。
3. 前端为所有当前筛选行请求轻量索引以维持全局“最近请求”排序；详细 account history 请求只针对 pageRows。
4. 保留抽屉/详情请求的按账号补齐行为，避免点击非当前缓存行时出现空白。
5. 写前端测试确认 global request evidence 仍可排序且明细 target 数量不超过一页。

## Task 4: 服务端异步批量 JSON 导入

**Files:**
- Add: apps/manager-server/internal/service/authfilesbatch/...
- Add: apps/manager-server/internal/http/controller/authfilesbatch/...
- Modify: apps/manager-server/internal/http/router/router.go
- Modify: apps/web/src/services/api/authFiles.ts
- Modify: apps/web/src/hooks/useAuthFilesData.ts
- Modify: 粘贴 JSON 对话框组件与测试

**Steps:**
1. 先写服务测试：一次 multipart 提交的多个文件形成一个短生命周期作业，服务端逐个上传；状态响应不回显文件内容/token。
2. 限制批次（100 文件、合理总大小），用内存短生命周期作业存储原始字节，串行上传以避免击穿 Core。
3. 状态响应只含计数、文件名和安全错误摘要；作业结束/过期后清除字节。
4. 前端多文件/数组粘贴走一个 batch POST，轮询状态；作业完成后只调用一次 loadFiles()。
5. 单文件保持原有直传路径，减少回归面。
6. 写前端测试确认多项只触发一次 batch 提交与一次完成刷新。

## Task 5: 回归、构建与部署前检查

**Files:**
- 可选 Modify: docs/...（记录已知运行数据维护项）

**Steps:**
1. 格式化 Go/TS；运行新增和受影响的 unit tests。
2. 运行 npm run build、Manager Server Go 测试。
3. 检查 git diff --check、git status，确认没有 CPA Core 路径或敏感数据。
4. 根据运行时配置构建 CPAM 专用镜像并滚动重建 cpa-manager-plus；不重启 cli-proxy-api。
5. 部署后验证缓存响应头、批量作业状态和页面加载日志；再提交并只推送 origin 的个人仓库，绝不推送上游。
