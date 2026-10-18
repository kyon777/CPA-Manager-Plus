# 代理故障替换与批量 Proxy URL 更新 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不修改 CPA Core、Token、计费、额度或调度逻辑的前提下，为 CPAM 增加可持久化的代理异常识别、人工确认替换与复检，以及凭证列表的统一/代理池批量 `proxy_url` 更新。

**Architecture:** 新增 `proxyreplacement` 领域模块：SQLite 持久化代理状态、加密保存任务映射、受控并发执行任务；巡检和凭证列表只调用同一组 CPAM 管理端 API。代理池仍以既有 `ProxyFilterSettings.URLs` 为唯一 URL 来源，新增状态表只保存规范化 URL 的 SHA-256 指纹。成功写入后的巡检来源任务调用一个不触发自动动作、不触发用量采集的定向 Codex probe；手工凭证列表任务绝不 probe。

**Tech Stack:** Go 1.x、SQLite、现有 `security.Protector`、既有 `cpaauthfiles.MutationCoordinator`、React 19、TypeScript、Vitest、Go `testing`、i18next。

---

## 0. 不可变边界与交付检查

- 只修改 `CPA Manager Plus` 工作树；不修改、构建、重启或部署 CPA Core / `cli-proxy-api`。
- 写 CPA 的请求只能命中既有 `PATCH /v0/management/auth-files/fields` 或既有认证文件上传回退路径；字段白名单严格为 `proxy_url`。
- 不能调用用量采集、额度刷新、`account-window-usage`、价格/历史统计刷新来完成此功能。
- 代理 URL 只能出现在已经通过面板认证的 API 响应或浏览器 UI；应用日志、任务摘要日志、状态表不能写完整 URL（尤其不能写代理认证信息）。
- 上游只允许 fetch/merge；此功能完成后的提交只推 `origin/lo/custom`。

## 1. 文件结构

### 新建文件

| 文件 | 职责 |
| --- | --- |
| `apps/manager-server/internal/model/proxy_replacement.go` | 领域常量、任务/项/状态/请求 DTO、URL 规范化、SHA-256 指纹、代理链路失败分类。 |
| `apps/manager-server/internal/repository/proxyreplacement/repository.go` | SQLite 的代理状态、任务、任务项 CRUD；任务项 URL 通过 `security.Protector` 加密。 |
| `apps/manager-server/internal/repository/proxyreplacement/repository_test.go` | 状态持久化、加密字段、草稿过期、claim/recover 的仓储测试。 |
| `apps/manager-server/internal/service/proxypoolstate/service.go` | 状态登记器；向巡检暴露无 URL 持久化的 suspect/restore 接口，向替换任务暴露状态读取接口。 |
| `apps/manager-server/internal/service/proxypoolstate/service_test.go` | 原因码脱敏、手动恢复和稳定指纹映射测试。 |
| `apps/manager-server/internal/service/proxyreplacement/service.go` | 预览分配、确认、后台执行、锁、受控并发、冲突检测、批量结果和失效代理状态写入。 |
| `apps/manager-server/internal/service/proxyreplacement/service_test.go` | 分配、字段白名单、并发冲突、部分成功、重启恢复、仅成功项复检测试。 |
| `apps/manager-server/internal/service/cpaauthfiles/proxy_patch.go` | 仅代理字段的 CPA field-patch 与插件虚拟凭证 JSON 回退写入。 |
| `apps/manager-server/internal/service/cpaauthfiles/proxy_patch_test.go` | `proxy_url` 载荷、数组记录定位、旧代理快照、插件回退测试。 |
| `apps/manager-server/internal/http/controller/proxyreplacement/handler.go` | 代理状态、预览、确认、任务读取、巡检 signal 的严格认证 HTTP API。 |
| `apps/manager-server/internal/http/controller/proxyreplacement/handler_test.go` | 授权、未知字段拒绝、状态码、任务分页与 URL 不写日志的 HTTP 合约测试。 |
| `apps/web/src/features/accounts/model/proxyReplacement.ts` | 前端代理规范化、任务状态、预览呈现、轮询终态和本地巡检候选判定。 |
| `apps/web/src/features/accounts/model/proxyReplacement.test.ts` | 前端末尾 `/`、重复、状态、signal 排除规则测试。 |
| `apps/web/src/features/accounts/components/ProxyReplacementModal.tsx` | 统一代理/代理池两个模式、预览映射、确认、进度轮询、复制结果的可复用弹窗。 |
| `apps/web/src/features/accounts/components/ProxyReplacementModal.module.scss` | 弹窗映射表、状态标签、错误文本、分页布局。 |
| `apps/web/src/features/accounts/components/ProxyReplacementModal.test.tsx` | 两种模式、无候选、确认按钮、部分失败、轮询结果测试。 |

### 修改文件

| 文件 | 改动 |
| --- | --- |
| `apps/manager-server/internal/repository/sqlite/migrate.go` | 创建三张代理替换相关表，并提供旧数据库安全补列/索引。 |
| `apps/manager-server/internal/repository/sqlite/migrate_test.go` | 验证全新库和旧库重开后的三张表、索引、URL 明文不落任务表。 |
| `apps/manager-server/internal/store/store.go` | 暴露 `ProxyReplacements` 仓储及窄包装方法。 |
| `apps/manager-server/internal/app/context.go` | 构造共享代理状态注册器、Codex inspection 服务和替换任务服务，复用同一个 mutation coordinator。 |
| `apps/manager-server/internal/service/codexinspection/service.go` | 将可证明的代理链路错误写为 `ProxySuspect`，并暴露仅用于代理替换的定向 probe。 |
| `apps/manager-server/internal/service/codexinspection/service_test.go` | 覆盖 502 request-failed、超时、401/429/额度/业务 5xx 排除、定向 probe 不触发自动动作。 |
| `apps/manager-server/internal/model/codex_inspection.go` | 增加 `ProxySuspect` 持久化结果字段。 |
| `apps/manager-server/internal/repository/codexinspection/repository.go` | 读写 `proxy_suspect`。 |
| `apps/manager-server/internal/http/router/router.go` | 在泛化 `/v0/management/` proxy 之前挂载代理替换 handler。 |
| `apps/manager-server/cmd/cpa-manager-plus/main.go` | 启动并在退出时有界等待代理替换任务 worker。 |
| `apps/web/src/services/api/usageService.ts` | 定义并调用代理替换 API；不复用重型 credential runtime/usage 请求。 |
| `apps/web/src/features/accounts/components/ProxyFilterModal.tsx` | 在已有已保存代理 URL 弹窗中显示 Normal/Suspect 并提供手动恢复。 |
| `apps/web/src/features/accounts/AccountsPage.tsx` | 多选工具栏新增“批量修改代理 URL”；任务完成后只失效凭证清单缓存。 |
| `apps/web/src/features/monitoring/codexInspection.ts` | `CodexInspectionResultItem` 增加 `proxySuspect`，筛选类型增加 `proxy_abnormal`。 |
| `apps/web/src/features/monitoring/model/codexInspectionPresentation.ts` | 新增“代理异常”计数/过滤，但不改变已有 action。 |
| `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx` | 新 tab 和“更换代理并复检 (N)”入口。 |
| `apps/web/src/features/monitoring/CodexInspectionPage.tsx` | 本机巡检完成后上报合格 signal；打开共享替换弹窗。 |
| `apps/web/src/features/monitoring/ServerCodexInspectionPage.tsx` | 服务端巡检读取持久化 `proxySuspect` 并打开共享替换弹窗。 |
| `apps/web/src/i18n/locales/en.json`、`ru.json`、`zh-CN.json`、`zh-TW.json` | 新按钮、状态、错误、映射、确认、分页文案。 |

## 2. API 合约（实现前冻结）

所有 `/v0/management/proxy-replacements/**` 路由采用既有面板认证。读取使用 `AuthorizePanel`，创建 preview、确认、恢复状态、上报 signal 使用 `AuthorizeAdmin`。

```text
GET  /v0/management/proxy-replacements/proxy-pool
POST /v0/management/proxy-replacements/proxy-pool/{fingerprint}/restore
POST /v0/management/proxy-replacements/inspection-signals
POST /v0/management/proxy-replacements/previews
POST /v0/management/proxy-replacements/tasks/{taskID}/confirm
GET  /v0/management/proxy-replacements/tasks/{taskID}?page=1&page_size=50
```

`POST /previews` 的唯一允许 JSON 形状：

```json
{
  "source": "inspection",
  "mode": "pool",
  "targets": [
    {
      "clientKey": "run-17:codex:42",
      "fileName": "alice.json",
      "authIndex": "42",
      "accountEmail": "alice@example.com",
      "provider": "codex",
      "runtimeId": "runtime-42",
      "accountId": "acct-snapshot",
      "accountSnapshot": "member-snapshot"
    }
  ]
}
```

合法 `source`：`inspection`、`batch_uniform`、`batch_pool`；合法 `mode`：`uniform`、`pool`。 `batch_uniform` 必须含非空 `uniformProxyURL`；`inspection` 必须使用 `pool` mode 且自动设置 `recheckAfterWrite=true`；`batch_pool` 使用 `pool` mode 且 `recheckAfterWrite=false`。任何额外顶层或 target 字段一律 `400`。

任务响应只在已经认证的面板会话中返回完整映射 URL；持久化任务表和日志只保存加密 URL 或 SHA-256 指纹。

---

### Task 1: 建立领域模型、规范化和失败分类

**Files:**
- Create: `apps/manager-server/internal/model/proxy_replacement.go`
- Create: `apps/manager-server/internal/model/proxy_replacement_test.go`
- Create: `apps/web/src/features/accounts/model/proxyReplacement.ts`
- Create: `apps/web/src/features/accounts/model/proxyReplacement.test.ts`

- [ ] **Step 1: 写 Go 的失败测试，固定 URL 规范化、指纹与分类边界。**

```go
func TestNormalizeProxyURLAndFingerprint(t *testing.T) {
    if got := model.NormalizeProxyURL("  http://u:p@host:8080/// "); got != "http://u:p@host:8080" {
        t.Fatalf("normalized = %q", got)
    }
    if model.ProxyFingerprint("http://u:p@host:8080/") != model.ProxyFingerprint("http://u:p@host:8080") {
        t.Fatal("trailing slash must not change fingerprint")
    }
}

func TestIsSuspectedProxyTransportFailure(t *testing.T) {
    yes := model.ProxyFailureEvidence{ProxyURL: "http://p:1", ErrorKind: "request_error", ErrorDetail: `api-call failed: 502 Bad Gateway {"error":"request failed"}`}
    if !model.IsSuspectedProxyTransportFailure(yes) { t.Fatal("expected suspect") }
    for _, no := range []model.ProxyFailureEvidence{
        {ProxyURL: "http://p:1", StatusCode: 401, ErrorKind: "http_status"},
        {ProxyURL: "http://p:1", StatusCode: 429, ErrorKind: "http_status", ErrorDetail: `{"detail":"Rate limit exceeded"}`},
        {ProxyURL: "http://p:1", ErrorKind: "http_status", ErrorDetail: "HTTP 503 upstream business response"},
        {ProxyURL: "", ErrorKind: "request_error", ErrorDetail: "context deadline exceeded"},
    } {
        if model.IsSuspectedProxyTransportFailure(no) { t.Fatalf("unexpected suspect: %#v", no) }
    }
}
```

- [ ] **Step 2: 运行测试，确认当前失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/model -run 'TestNormalizeProxyURLAndFingerprint|TestIsSuspectedProxyTransportFailure' -count=1
```
Expected: `FAIL`，因为 `proxy_replacement.go` 尚不存在。

- [ ] **Step 3: 实现最小稳定领域契约。**

在 `proxy_replacement.go` 定义以下不可变名称和值，所有后续任务只使用这些名称：

```go
package model

type ProxyPoolStatus string
const (
    ProxyPoolStatusNormal  ProxyPoolStatus = "normal"
    ProxyPoolStatusSuspect ProxyPoolStatus = "suspect"
)
type ProxyReplacementSource string
const (
    ProxyReplacementSourceInspection  ProxyReplacementSource = "inspection"
    ProxyReplacementSourceBatchUniform ProxyReplacementSource = "batch_uniform"
    ProxyReplacementSourceBatchPool    ProxyReplacementSource = "batch_pool"
)
type ProxyReplacementMode string
const (
    ProxyReplacementModeUniform ProxyReplacementMode = "uniform"
    ProxyReplacementModePool    ProxyReplacementMode = "pool"
)
type ProxyReplacementTaskStatus string
const (
    ProxyReplacementTaskDraft       ProxyReplacementTaskStatus = "draft"
    ProxyReplacementTaskQueued      ProxyReplacementTaskStatus = "queued"
    ProxyReplacementTaskRunning     ProxyReplacementTaskStatus = "running"
    ProxyReplacementTaskCompleted   ProxyReplacementTaskStatus = "completed"
    ProxyReplacementTaskPartial     ProxyReplacementTaskStatus = "partial"
    ProxyReplacementTaskFailed      ProxyReplacementTaskStatus = "failed"
    ProxyReplacementTaskExpired     ProxyReplacementTaskStatus = "expired"
    ProxyReplacementTaskUnavailable ProxyReplacementTaskStatus = "unavailable"
)
type ProxyReplacementItemStatus string
const (
    ProxyReplacementItemPlanned          ProxyReplacementItemStatus = "planned"
    ProxyReplacementItemWriting          ProxyReplacementItemStatus = "writing"
    ProxyReplacementItemUpdated          ProxyReplacementItemStatus = "updated"
    ProxyReplacementItemSkippedStale     ProxyReplacementItemStatus = "skipped_stale"
    ProxyReplacementItemWriteFailed      ProxyReplacementItemStatus = "write_failed"
    ProxyReplacementItemRecheckRunning   ProxyReplacementItemStatus = "recheck_running"
    ProxyReplacementItemRecheckSucceeded ProxyReplacementItemStatus = "recheck_succeeded"
    ProxyReplacementItemRecheckFailed    ProxyReplacementItemStatus = "recheck_failed"
)

type ProxyReplacementTarget struct {
    ClientKey       string `json:"clientKey"`
    FileName        string `json:"fileName"`
    AuthIndex       string `json:"authIndex,omitempty"`
    AccountEmail    string `json:"accountEmail,omitempty"`
    Provider        string `json:"provider"`
    RuntimeID       string `json:"runtimeId,omitempty"`
    AccountID       string `json:"accountId,omitempty"`
    AccountSnapshot string `json:"accountSnapshot,omitempty"`
}

type ProxyPoolState struct {
    Fingerprint       string          `json:"fingerprint"`
    Status            ProxyPoolStatus `json:"status"`
    LastFailureAtMS   int64           `json:"lastFailureAtMs,omitempty"`
    LastFailureReason string          `json:"lastFailureReason,omitempty"`
    UpdatedAtMS       int64           `json:"updatedAtMs"`
}

type ProxyReplacementTask struct {
    ID                  int64                      `json:"id"`
    Source              ProxyReplacementSource     `json:"source"`
    Mode                ProxyReplacementMode       `json:"mode"`
    Status              ProxyReplacementTaskStatus `json:"status"`
    RecheckAfterWrite   bool                       `json:"recheckAfterWrite"`
    CanConfirm          bool                       `json:"canConfirm"`
    TaskOffset          int                        `json:"taskOffset"`
    TotalCount          int                        `json:"totalCount"`
    UpdatedCount        int                        `json:"updatedCount"`
    FailedCount         int                        `json:"failedCount"`
    SkippedCount        int                        `json:"skippedCount"`
    RecheckSuccessCount int                        `json:"recheckSuccessCount"`
    RecheckFailedCount  int                        `json:"recheckFailedCount"`
    UnusedNormalCount   int                        `json:"unusedNormalCount"`
    FallbackNormalCount int                        `json:"fallbackNormalCount"`
    SuspectExcludedCount int                       `json:"suspectExcludedCount"`
    ExpiresAtMS         int64                      `json:"expiresAtMs,omitempty"`
    CreatedAtMS         int64                      `json:"createdAtMs"`
    UpdatedAtMS         int64                      `json:"updatedAtMs"`
}

type ProxyReplacementTaskItem struct {
    ID                   int64                      `json:"id"`
    TaskID               int64                      `json:"taskId"`
    ItemOrder            int                        `json:"itemOrder"`
    Target               ProxyReplacementTarget     `json:"target"`
    OldProxyURL          string                     `json:"oldProxyUrl,omitempty"`
    OldProxyFingerprint  string                     `json:"oldProxyFingerprint,omitempty"`
    NewProxyURL          string                     `json:"newProxyUrl,omitempty"`
    NewProxyFingerprint  string                     `json:"newProxyFingerprint,omitempty"`
    AllocationSource     string                     `json:"allocationSource,omitempty"`
    Status               ProxyReplacementItemStatus `json:"status"`
    WriteErrorCode       string                     `json:"writeErrorCode,omitempty"`
    WriteErrorMessage    string                     `json:"writeErrorMessage,omitempty"`
    RecheckErrorCode     string                     `json:"recheckErrorCode,omitempty"`
    RecheckErrorMessage  string                     `json:"recheckErrorMessage,omitempty"`
}

type ProxyReplacementTaskPage struct {
    Task     ProxyReplacementTask       `json:"task"`
    Items    []ProxyReplacementTaskItem `json:"items"`
    Page     int                        `json:"page"`
    PageSize int                        `json:"pageSize"`
    Total    int                        `json:"total"`
}

type ProxyReplacementItemOutcome struct {
    WriteErrorCode       string
    WriteErrorMessage    string
    RecheckErrorCode     string
    RecheckErrorMessage  string
}

type ProxyReplacementProbeResult struct {
    Target       ProxyReplacementTarget
    Succeeded    bool
    ErrorCode    string
    ErrorMessage string
    ProxySuspect bool
}

type ProxyFailureEvidence struct {
    ProxyURL    string
    StatusCode  int
    ErrorKind   string
    ErrorDetail string
}
```

实现 `NormalizeProxyURL(string) string`（`TrimSpace` 后去掉全部末尾 `/`）、`ProxyFingerprint(string) string`（规范化值的 SHA-256 hex；空值返回空串）和 `IsSuspectedProxyTransportFailure(ProxyFailureEvidence) bool`：只接受非空代理的 `request_error` 且包含 timeout/context deadline，或包含 `api-call failed: 502` 与 JSON `"error":"request failed"`；先排除 401、429、`quota`、`credit`、成功业务响应的 `http_status`。

- [ ] **Step 4: 为前端镜像逻辑写测试并实现。**

```ts
it('only marks a proxied local inspection transport failure as suspected', () => {
  expect(isLocalProxySuspect({ proxyUrl: 'http://p:1/', errorKind: 'request_error', errorDetail: 'api-call failed: 502 {"error":"request failed"}' })).toBe(true);
  expect(isLocalProxySuspect({ proxyUrl: 'http://p:1', statusCode: 429, errorKind: 'http_status', errorDetail: 'Rate limit exceeded' })).toBe(false);
});
```

`proxyReplacement.ts` 导出 `normalizeProxyReplacementURL`、`proxyReplacementFingerprint`、`isLocalProxySuspect`、任务终态判断和后续 API DTO 类型；其 URL 语义必须与 Go 完全一致。

- [ ] **Step 5: 运行两侧单元测试。**

Run:
```powershell
cd apps/manager-server; go test ./internal/model -run 'TestNormalizeProxyURLAndFingerprint|TestIsSuspectedProxyTransportFailure' -count=1
cd ../..; npm --workspace apps/web run test -- src/features/accounts/model/proxyReplacement.test.ts
```
Expected: 两条命令均 `PASS`。

- [ ] **Step 6: 提交领域基础。**

```powershell
git add apps/manager-server/internal/model/proxy_replacement.go apps/manager-server/internal/model/proxy_replacement_test.go apps/web/src/features/accounts/model/proxyReplacement.ts apps/web/src/features/accounts/model/proxyReplacement.test.ts
git commit -m "feat: define proxy replacement domain model"
```

### Task 2: 增加 SQLite 持久化、任务 URL 加密和 Store 暴露

**Files:**
- Create: `apps/manager-server/internal/repository/proxyreplacement/repository.go`
- Create: `apps/manager-server/internal/repository/proxyreplacement/repository_test.go`
- Create: `apps/manager-server/internal/service/proxypoolstate/service.go`
- Create: `apps/manager-server/internal/service/proxypoolstate/service_test.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate_test.go`
- Modify: `apps/manager-server/internal/store/store.go`

- [ ] **Step 1: 写仓储和 migration 失败测试。**

```go
func TestTaskURLsAreEncryptedAtRestAndReadableToAuthorizedService(t *testing.T) {
    db := openTestDB(t)
    protector := testProtector(t)
    repo := proxyreplacement.New(db, protector)
    task, err := repo.CreateDraft(context.Background(), model.ProxyReplacementTask{Source: model.ProxyReplacementSourceInspection}, []model.ProxyReplacementTaskItem{{OldProxyURL: "http://old:secret@p:1", NewProxyURL: "http://new:secret@p:2"}})
    if err != nil { t.Fatal(err) }
    if task.ID == 0 { t.Fatal("missing task id") }
    var raw string
    if err := db.QueryRow(`select new_proxy_ciphertext from proxy_replacement_task_items where task_id = ?`, task.ID).Scan(&raw); err != nil { t.Fatal(err) }
    if strings.Contains(raw, "secret") { t.Fatal("raw proxy credential persisted") }
    page, err := repo.GetTask(context.Background(), task.ID, 1, 50)
    if err != nil || page.Items[0].NewProxyURL != "http://new:secret@p:2" { t.Fatalf("page = %#v err=%v", page, err) }
}

func TestProxyReplacementMigrationAddsTablesToExistingDatabase(t *testing.T) {
    db := openLegacyFixtureThenReopen(t)
    for _, table := range []string{"proxy_pool_state", "proxy_replacement_tasks", "proxy_replacement_task_items"} {
        if !tableExists(t, db, table) { t.Fatalf("missing %s", table) }
    }
}

func TestPoolStateCanBeRestoredWithoutPersistingURL(t *testing.T) {
    state := mustMarkSuspect(t, proxypoolstate.New(repo), "http://user:secret@proxy:8080/", "transport_timeout")
    if state.Status != model.ProxyPoolStatusSuspect || state.Fingerprint == "" { t.Fatalf("state=%#v", state) }
    restored := mustRestore(t, proxypoolstate.New(repo), state.Fingerprint)
    if restored.Status != model.ProxyPoolStatusNormal { t.Fatalf("restored=%#v", restored) }
}
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/repository/proxyreplacement ./internal/repository/sqlite ./internal/service/proxypoolstate -run 'TestTaskURLsAreEncryptedAtRestAndReadableToAuthorizedService|TestProxyReplacementMigrationAddsTablesToExistingDatabase' -count=1
```
Expected: `FAIL`，因为 package、tables 和 `Store.ProxyReplacements` 尚不存在。

- [ ] **Step 3: 创建三个 SQLite 表和索引。**

在 `migrate.go` 的现有 `statements` 中增加以下 schema；名称不可变：

```sql
create table if not exists proxy_pool_state (
    proxy_fingerprint text primary key,
    status text not null,
    last_failure_at_ms integer,
    last_failure_reason text,
    updated_at_ms integer not null
);
create table if not exists proxy_replacement_tasks (
    id integer primary key autoincrement,
    source text not null,
    mode text not null,
    status text not null,
    recheck_after_write integer not null default 0,
    task_offset integer not null default 0,
    total_count integer not null default 0,
    updated_count integer not null default 0,
    failed_count integer not null default 0,
    skipped_count integer not null default 0,
    recheck_success_count integer not null default 0,
    recheck_failed_count integer not null default 0,
    unused_normal_count integer not null default 0,
    fallback_normal_count integer not null default 0,
    suspect_excluded_count integer not null default 0,
    last_error_code text,
    last_error_message text,
    expires_at_ms integer,
    confirmed_at_ms integer,
    started_at_ms integer,
    completed_at_ms integer,
    created_at_ms integer not null,
    updated_at_ms integer not null
);
create index if not exists idx_proxy_replacement_tasks_claim
    on proxy_replacement_tasks(status, created_at_ms, id);
create table if not exists proxy_replacement_task_items (
    id integer primary key autoincrement,
    task_id integer not null,
    item_order integer not null,
    client_key text not null,
    file_name text not null,
    auth_index text not null default '',
    account_email text not null default '',
    provider text not null,
    runtime_id text not null default '',
    account_id text not null default '',
    account_snapshot text not null default '',
    old_proxy_fingerprint text not null default '',
    old_proxy_ciphertext text,
    new_proxy_fingerprint text not null default '',
    new_proxy_ciphertext text,
    allocation_source text not null default '',
    status text not null,
    write_error_code text,
    write_error_message text,
    recheck_error_code text,
    recheck_error_message text,
    created_at_ms integer not null,
    updated_at_ms integer not null,
    foreign key(task_id) references proxy_replacement_tasks(id) on delete cascade,
    unique(task_id, item_order)
);
create index if not exists idx_proxy_replacement_task_items_task_order
    on proxy_replacement_task_items(task_id, item_order);
```

在 `ensureProxyReplacementTaskColumns(db)` 中通过 `pragma table_info` 对既有早期开发库补齐未来缺失列；在 `Migrate` 主流程中调用它。不要在 migration 中删除或重建任何现有 usage / token / inspection 表。

- [ ] **Step 4: 实现 Repository 和 Store。**

`repository.go` 的接口必须至少包含：

```go
type Repository interface {
    CreateDraft(context.Context, model.ProxyReplacementTask, []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error)
    GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error)
    ConfirmDraft(context.Context, int64, int64) (model.ProxyReplacementTask, error)
    ClaimNextQueued(context.Context) (model.ProxyReplacementTask, bool, error)
    RequeueRunningOnStartup(context.Context) (int64, error)
    MarkItem(context.Context, int64, model.ProxyReplacementItemStatus, model.ProxyReplacementItemOutcome) error
    CompleteTask(context.Context, int64) (model.ProxyReplacementTask, error)
    ExpireDrafts(context.Context, int64) (int64, error)
    UpsertProxyPoolState(context.Context, model.ProxyPoolState) (model.ProxyPoolState, error)
    ListProxyPoolStates(context.Context) ([]model.ProxyPoolState, error)
    RestoreProxyPoolState(context.Context, string) (model.ProxyPoolState, error)
}
```

`New(db, protector...)` 复用 `security.Protector.ProtectString` / `UnprotectString` 写入 `old_proxy_ciphertext` 与 `new_proxy_ciphertext`。没有 protector 的测试/旧部署保持既有仓储风格可读，但生产 `store.New` 必须传入现有 protector。`GetTask` 解密后才填充 API DTO；任何仓储错误字符串不得拼入完整 URL。

在 `store.Store` 添加：

```go
ProxyReplacements proxyreplacement.Repository
```

并在 `New` 中通过 `proxyreplacement.New(db, protector...)` 初始化；添加窄包装 `GetProxyReplacementTask`、`LoadProxyPoolStates` 等供 service 使用，避免 controller 直接碰 SQLite。

新增 `proxypoolstate.Service`，使巡检和任务执行不存在 package cycle：

```go
type Store interface {
    UpsertProxyPoolState(context.Context, model.ProxyPoolState) (model.ProxyPoolState, error)
    ListProxyPoolStates(context.Context) ([]model.ProxyPoolState, error)
    RestoreProxyPoolState(context.Context, string) (model.ProxyPoolState, error)
}

type Service struct { store Store }
func (s *Service) MarkSuspect(ctx context.Context, proxyURL string, reason string) error
func (s *Service) List(ctx context.Context) ([]model.ProxyPoolState, error)
func (s *Service) Restore(ctx context.Context, fingerprint string) (model.ProxyPoolState, error)
```

`MarkSuspect` 只接收完整 URL 于内存，立即计算 fingerprint 后丢弃 URL；`reason` 必须收敛为 `transport_502_request_failed`、`transport_timeout` 或 `transport_unknown`，不可落入原始错误正文。`Service` 是 Task 5 的 `ProxyFailureReporter` 及 Task 4 的 pool-state reader/writer；两侧都仅依赖上述接口。

- [ ] **Step 5: 运行迁移和仓储测试。**

Run:
```powershell
cd apps/manager-server; go test ./internal/repository/proxyreplacement ./internal/repository/sqlite ./internal/service/proxypoolstate ./internal/store -count=1
```
Expected: `PASS`，且测试 DB 内的 `new_proxy_ciphertext` 不出现 URL 中的用户名/密码。

- [ ] **Step 6: 提交持久化层。**

```powershell
git add apps/manager-server/internal/repository/proxyreplacement apps/manager-server/internal/service/proxypoolstate apps/manager-server/internal/repository/sqlite/migrate.go apps/manager-server/internal/repository/sqlite/migrate_test.go apps/manager-server/internal/store/store.go
git commit -m "feat: persist proxy pool state and replacement tasks"
```

### Task 3: 建立仅 `proxy_url` 的 CPA 写入原语

**Files:**
- Create: `apps/manager-server/internal/service/cpaauthfiles/proxy_patch.go`
- Create: `apps/manager-server/internal/service/cpaauthfiles/proxy_patch_test.go`
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client.go`

- [ ] **Step 1: 写 field-patch 与 JSON 回退的失败测试。**

```go
func TestPatchProxyURLTargetSendsOnlyAllowedFields(t *testing.T) {
    received := map[string]any{}
    server := newCPAFieldsServer(t, func(body []byte) { _ = json.Unmarshal(body, &received) })
    client := cpaauthfiles.New(server.Client())
    err := client.PatchProxyURLTarget(context.Background(), server.URL, "key", credentialTarget("runtime-7"), "http://user:pass@proxy:8080")
    if err != nil { t.Fatal(err) }
    if diff := cmp.Diff(map[string]any{"name":"runtime-7", "proxy_url":"http://user:pass@proxy:8080"}, received); diff != "" { t.Fatal(diff) }
}

func TestRewriteProxyURLPreservesAllNonProxyFields(t *testing.T) {
    raw := []byte(`[{"auth_index":"7","email":"a@example.com","note":"keep","priority":5,"refresh_token":"rt","proxy_url":"http://old"}]`)
    next, err := cpaauthfiles.RewriteProxyURL(raw, cpaauthfiles.ProxyRecordLocator{AuthIndex:"7", AccountEmail:"a@example.com"}, "http://old", "http://new")
    if err != nil { t.Fatal(err) }
    assertJSONPath(t, next, "0.note", "keep"); assertJSONPath(t, next, "0.priority", 5); assertJSONPath(t, next, "0.proxy_url", "http://new")
}
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/cpaauthfiles -run 'TestPatchProxyURLTargetSendsOnlyAllowedFields|TestRewriteProxyURLPreservesAllNonProxyFields' -count=1
```
Expected: `FAIL`，因为 API 和 JSON helper 尚不存在。

- [ ] **Step 3: 实现精确 field patch。**

添加：

```go
func (c *Client) PatchProxyURLTarget(ctx context.Context, baseURL, managementKey string, target StatusMutationTarget, proxyURL string) error
```

实现规则：

1. `target.Scope` 必须为 `StatusMutationScopeCredential`，否则返回 `ErrStatusMutationScopeAmbiguous`，不允许用 source-file selector 广播写入。
2. `proxyURL` 必须经 `model.NormalizeProxyURL` 变成非空值。
3. 唯一 JSON 载荷为 `{"name": target.File.ID, "proxy_url": normalized}`，只有非空 `auth_index` 才额外增加 `auth_index`。
4. 向 `PATCH /v0/management/auth-files/fields` 写入；沿用 `actionHTTPError` 读取至多 4096 字节错误正文。
5. 不接受 priority、token、note、disabled、headers 或任意 map 透传参数。

- [ ] **Step 4: 实现插件虚拟记录回退 JSON helper。**

新增 `ProxyRecordLocator` 和：

```go
func ReadProxyURLFromJSON(raw []byte, locator ProxyRecordLocator) (string, error)
func RewriteProxyURL(raw []byte, locator ProxyRecordLocator, expectedOldProxy, nextProxy string) ([]byte, error)
```

`RewriteProxyURL` 要求：单对象按邮箱/可选 index 定位；数组必须由 auth index 命中唯一记录；当前 URL 规范化后必须等于 `expectedOldProxy`；只替换/写入 selected record 的 `proxy_url`；完整 JSON 根、其他数组成员、Token、note、priority、headers 不改变；当前 URL 不一致时返回 `ErrIdentityMismatch`。不得使用 Token Recovery 包中的未导出 helper，避免代理写入和 Token 获取产生耦合。

- [ ] **Step 5: 运行包含现有 priority 回归的 package 测试。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/cpaauthfiles -count=1
```
Expected: `PASS`，既有 priority/disabled 载荷测试仍通过。

- [ ] **Step 6: 提交 CPA 写入原语。**

```powershell
git add apps/manager-server/internal/service/cpaauthfiles/proxy_patch.go apps/manager-server/internal/service/cpaauthfiles/proxy_patch_test.go apps/manager-server/internal/service/cpaauthfiles/client.go
git commit -m "feat: add guarded proxy url auth-file patch"
```

### Task 4: 实现代理池分配器和可恢复替换任务服务

**Files:**
- Create: `apps/manager-server/internal/service/proxyreplacement/service.go`
- Create: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/manager-server/internal/model/proxy_replacement.go`

- [ ] **Step 1: 写候选分配和任务恢复失败测试。**

```go
func TestPlanPoolPrefersUnusedThenRotatesAllNormalCandidates(t *testing.T) {
    // Task ID 1 makes the all-candidate round-robin order p2,p1; p1 must still win
    // the first assignment because it is the only currently unoccupied normal proxy.
    plan := mustPlan(t, service, model.ProxyReplacementTask{ID: 1}, pool("http://p1/", "http://p2", "http://suspect"), states(suspect("http://suspect")), enabled("http://p2"), inspectionTargets("http://old", 5))
    if got := urls(plan.Items); !reflect.DeepEqual(got, []string{"http://p1", "http://p2", "http://p1", "http://p2", "http://p1"}) {
        t.Fatalf("urls = %#v", got)
    }
    if contains(urls(plan.Items), "http://old") || contains(urls(plan.Items), "http://suspect") { t.Fatalf("excluded URL used: %#v", urls(plan.Items)) }
}

func TestWorkerResumesAfterRestartWithoutOverwritingChangedProxy(t *testing.T) {
    task := createConfirmedTask(t, service, target("alice.json", "42", "http://old"), "http://new")
    mutateCPAProxy(t, "alice.json", "42", "http://someone-else")
    service.Recover(context.Background())
    waitTerminal(t, service, task.ID)
    item := getOnlyItem(t, service, task.ID)
    if item.Status != model.ProxyReplacementItemSkippedStale { t.Fatalf("status = %s", item.Status) }
}
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement -run 'TestPlanPoolPrefersUnusedThenRotatesAllNormalCandidates|TestWorkerResumesAfterRestartWithoutOverwritingChangedProxy' -count=1
```
Expected: `FAIL`，因为 service 尚不存在。

- [ ] **Step 3: 实现 service API 与分配器。**

在 service 中定义以下窄依赖，便于 test fake：

```go
type TaskStore interface {
    CreateDraft(context.Context, model.ProxyReplacementTask, []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error)
    GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error)
    ConfirmDraft(context.Context, int64, int64) (model.ProxyReplacementTask, error)
    ClaimNextQueued(context.Context) (model.ProxyReplacementTask, bool, error)
    RequeueRunningOnStartup(context.Context) (int64, error)
    MarkItem(context.Context, int64, model.ProxyReplacementItemStatus, model.ProxyReplacementItemOutcome) error
    CompleteTask(context.Context, int64) (model.ProxyReplacementTask, error)
    ExpireDrafts(context.Context, int64) (int64, error)
}
type SettingsStore interface { LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error) }
type PoolStateStore interface {
    List(context.Context) ([]model.ProxyPoolState, error)
    MarkSuspect(context.Context, string, string) error
}
type SetupResolver interface { ResolveSetup(context.Context) (store.Setup, bool, error) }
type AuthFilesClient interface {
    Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
    ResolveStatusMutationTarget(context.Context, string, string, string, string) (cpaauthfiles.StatusMutationTarget, error)
    PatchProxyURLTarget(context.Context, string, string, cpaauthfiles.StatusMutationTarget, string) error
    Download(context.Context, string, string, string) ([]byte, error)
    Upload(context.Context, string, string, string, []byte) error
}
type Rechecker interface { RecheckProxyReplacement(context.Context, []model.ProxyReplacementTarget, int) []model.ProxyReplacementProbeResult }
```

`CreatePreview` 的固定算法（`unused` 和 `all` 均按保存代理列表的规范化首次出现顺序构造）：

```go
type proxyCandidate struct { URL, Fingerprint string; InUse bool }
type proxyAssignment struct { Candidate proxyCandidate; Source string }
func allocatePool(taskID int64, unused, all []proxyCandidate, targetCount int) []proxyAssignment {
    if targetCount <= 0 || len(all) == 0 {
        return []proxyAssignment{}
    }
    result := make([]proxyAssignment, 0, targetCount)
    if len(unused) > 0 {
        unusedStart := int(taskID % int64(len(unused)))
        for step := 0; step < len(unused) && len(result) < targetCount; step++ {
            candidate := unused[(unusedStart+step)%len(unused)]
            result = append(result, proxyAssignment{Candidate: candidate, Source: "unused"})
        }
    }
    allStart := int(taskID % int64(len(all)))
    for cycle := 0; len(result) < targetCount; cycle++ {
        candidate := all[(allStart+cycle)%len(all)]
        source := "fallback"
        if !candidate.InUse { source = "unused" }
        result = append(result, proxyAssignment{Candidate: candidate, Source: source})
    }
    return result
}
```

具体执行顺序：

1. 加载加密保存的 `ProxyFilterSettings.URLs`，用 `model.NormalizeProxyURL` 去重。
2. 从 CPA 拉取完整 inventory 一次，使用全部已启用账号的 `proxy_url` 构建 `InUse` 集合。
3. 将 `proxy_pool_state.status=suspect`、空值和 inspection target 的 `expectedOldProxy` 排除；普通 batch-pool 不排除自身当前代理。
4. `batch_uniform` 不从池中分配，所有目标使用规范化 `uniformProxyURL`；不自动写回保存池。
5. Preview 保存 snapshot target、旧 URL、分配的新 URL、allocation source (`unused` / `fallback`) 与 15 分钟 `expires_at_ms`。没有候选时创建 `unavailable` task，`canConfirm=false`。
6. Preview 从不写 CPA。

- [ ] **Step 4: 实现确认、worker、锁与冲突处理。**

`Confirm` 仅把未过期 `draft` 原子转换为 `queued` 并通知 worker。worker 每次 claim 一个 task，每 task 最多 `4` 个 physical-file jobs 并行；同一 physical file 的多条 credential item 在同一个 job 内串行。每个 item 必须按以下顺序执行：

```go
release, err := mutationCoordinator.Acquire(ctx, item.FileName)
if err != nil { markWriteFailed(item, "lock_failed", "凭证写入锁不可用"); return }
defer release()

current, target, err := resolveCurrentCredentialAndProxy(ctx, setup, item)
switch {
case err != nil:
    markWriteFailed(item, "credential_lookup_failed", redact(err))
case NormalizeProxyURL(current.ProxyURL) == NormalizeProxyURL(item.NewProxyURL):
    markUpdated(item) // 重启时 PATCH 已生效但响应丢失的幂等恢复
case NormalizeProxyURL(current.ProxyURL) != NormalizeProxyURL(item.OldProxyURL):
    markSkippedStale(item, "stale_proxy", "账号代理已被其他操作修改，未覆盖")
default:
    patchExactlyProxyURL(ctx, setup, target, item)
}
```

`patchExactlyProxyURL` 先调用 `PatchProxyURLTarget`；仅当 CPA 返回 `IsPluginVirtualMutationConflict` 时下载受锁 physical JSON，调用 `RewriteProxyURL`，上传并下载回读验证。所有成功/失败状态都通过 repository 持久化。完成一个 task 后只调用一次凭证 inventory invalidator；绝不调用 UsageService、Collector、quota snapshot 或 priority policy。

启动恢复规则：把 `running` task 重新排队，把 `writing`/`recheck_running` item 复位为 `planned`；前述“当前值等于目标值”分支防止重复覆盖或误判。

- [ ] **Step 5: 扩展测试为字段白名单、部分失败、过期和无候选。**

```go
func TestManualBatchDoesNotProbeOrTouchUsage(t *testing.T) {
    task := confirmManualUniformTask(t, service)
    waitTerminal(t, service, task.ID)
    if probes.Calls != 0 || usage.Calls != 0 { t.Fatalf("probes=%d usage=%d", probes.Calls, usage.Calls) }
    if got := cpa.LastFieldsPayload(); !reflect.DeepEqual(got, map[string]any{"name":"runtime-1", "proxy_url":"http://new"}) { t.Fatal(got) }
}

func TestExpiredDraftCannotBeConfirmed(t *testing.T) {
    task := createExpiredDraft(t, service)
    _, err := service.Confirm(context.Background(), task.ID)
    if !errors.Is(err, proxyreplacement.ErrTaskExpired) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 6: 运行 service 与 Go 静态检查。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/proxyreplacement ./internal/model ./internal/service/cpaauthfiles -count=1
gofmt -w internal/service/proxyreplacement internal/model/proxy_replacement.go
git diff --check
```
Expected: 所有测试 `PASS`，`git diff --check` 无输出。

- [ ] **Step 7: 提交任务执行服务。**

```powershell
git add apps/manager-server/internal/service/proxyreplacement apps/manager-server/internal/model/proxy_replacement.go
git commit -m "feat: execute durable proxy replacement tasks"
```

### Task 5: 将巡检的疑似代理异常持久化，并提供定向复检

**Files:**
- Modify: `apps/manager-server/internal/model/codex_inspection.go`
- Modify: `apps/manager-server/internal/repository/codexinspection/repository.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service_test.go`

- [ ] **Step 1: 写巡检分类与无副作用定向 probe 的失败测试。**

```go
func TestInspectSingleAccountMarksOnlyTransportProxyFailure(t *testing.T) {
    result := inspectWithAPIError(t, `api-call failed: 502 Bad Gateway {"error":"request failed"}`, "http://proxy:1")
    if !result.ProxySuspect { t.Fatal("proxy failure must be marked") }
    for _, tc := range []probeFixture{unauthorizedFixture(), rateLimitedFixture(), quotaFixture(), business503Fixture(), noProxyTimeoutFixture()} {
        if got := inspectFixture(t, tc); got.ProxySuspect { t.Fatalf("unexpected suspect for %s", tc.Name) }
    }
}

func TestReplacementRecheckDoesNotTriggerAutomaticActionsOrUsageCollection(t *testing.T) {
    service := newInspectionService(t)
    out := service.RecheckProxyReplacement(context.Background(), []model.ProxyReplacementTarget{target}, 1)
    if len(out) != 1 || autoActionCalls(t) != 0 || usageCollectorCalls(t) != 0 { t.Fatal("recheck leaked side effects") }
}
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/codexinspection -run 'TestInspectSingleAccountMarksOnlyTransportProxyFailure|TestReplacementRecheckDoesNotTriggerAutomaticActionsOrUsageCollection' -count=1
```
Expected: `FAIL`，因为 `ProxySuspect` 与 `RecheckProxyReplacement` 尚不存在。

- [ ] **Step 3: 修改 inspection persistence。**

为 `model.CodexInspectionResult` 加：

```go
ProxySuspect bool `json:"proxySuspect"`
```

在 `codex_inspection_results` 创建 schema 中增加 `proxy_suspect integer not null default 0`，并在 `ensureCodexInspectionResultColumns` 把此列加入可独立补列集。更新 repository 的 insert、select、scan 和 bool 映射；旧 runs 读出默认 `false`。

- [ ] **Step 4: 在 `inspectSingleAccount` 中调用 Task 1 分类器。**

在已有 `request_error` / status 分支生成完整 `CodexInspectionResult` 后读取当前 `cpaauthfiles.File.Raw` 中的 `proxy_url`，构造：

```go
evidence := model.ProxyFailureEvidence{
    ProxyURL: proxyURLFromRaw(item.Raw),
    StatusCode: dereferenceStatusCode(base.StatusCode),
    ErrorKind: base.ErrorKind,
    ErrorDetail: base.ErrorDetail,
}
base.ProxySuspect = model.IsSuspectedProxyTransportFailure(evidence)
```

`ProxySuspect=true` 时仅调用注入的 `ProxyFailureReporter.MarkSuspect(ctx, model.ProxyPoolState{...})`，保存指纹、时间与不含 URL 的有限原因码，例如 `transport_502_request_failed` 或 `transport_timeout`。不变更 `Action`、`ActionReason`、`disabled`、`priority` 或自动 reauth 流程。

- [ ] **Step 5: 实现 `RecheckProxyReplacement`。**

它接收 Task 4 的成功项身份列表，使用已有 CPA `api-call` probe 的 request construction；固定 `autoActionMode=none`、不写 `codex_inspection_runs`、不调用 `ReauthRecoveryNotifier`、不调用 usage collectors。返回每项：

```go
type ProxyReplacementProbeResult struct {
    Target model.ProxyReplacementTarget
    Succeeded bool
    ErrorCode string
    ErrorMessage string
    ProxySuspect bool
}
```

若 recheck 再次命中 `ProxySuspect`，通过同一 `ProxyFailureReporter` 将新 proxy 指纹状态设为 `suspect`；Task 4 将结果存为 `recheck_failed`，不回滚、不再次分配。

- [ ] **Step 6: 运行 inspection 全包测试。**

Run:
```powershell
cd apps/manager-server; go test ./internal/service/codexinspection ./internal/repository/codexinspection ./internal/repository/sqlite -count=1
```
Expected: `PASS`；401、429、额度和成功返回的业务 5xx 都不进入代理池状态。

- [ ] **Step 7: 提交巡检桥接。**

```powershell
git add apps/manager-server/internal/model/codex_inspection.go apps/manager-server/internal/repository/codexinspection apps/manager-server/internal/repository/sqlite/migrate.go apps/manager-server/internal/service/codexinspection
git commit -m "feat: classify proxy transport failures in inspection"
```

### Task 6: 连接 App Context、HTTP 路由、任务生命周期

**Files:**
- Create: `apps/manager-server/internal/http/controller/proxyreplacement/handler.go`
- Create: `apps/manager-server/internal/http/controller/proxyreplacement/handler_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/http/router/router.go`
- Modify: `apps/manager-server/internal/http/router/router_test.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`

- [ ] **Step 1: 写 handler 合约失败测试。**

```go
func TestPreviewRejectsUnexpectedFieldsAndDoesNotWriteCPA(t *testing.T) {
    rr := postJSON(t, handler, "/v0/management/proxy-replacements/previews", `{"source":"inspection","mode":"pool","targets":[],"priority":1}`)
    if rr.Code != http.StatusBadRequest { t.Fatalf("status=%d", rr.Code) }
    if cpa.PatchCalls != 0 { t.Fatal("preview wrote CPA") }
}

func TestTaskRouteWinsBeforeGenericManagementProxy(t *testing.T) {
    rr := authorizedRequest(t, router.New(appCtx), http.MethodGet, "/v0/management/proxy-replacements/proxy-pool", nil)
    if rr.Code != http.StatusOK { t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String()) }
    if genericProxyCalls(t) != 0 { t.Fatal("route leaked to generic CPA proxy") }
}
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
cd apps/manager-server; go test ./internal/http/controller/proxyreplacement ./internal/http/router -run 'TestPreviewRejectsUnexpectedFieldsAndDoesNotWriteCPA|TestTaskRouteWinsBeforeGenericManagementProxy' -count=1
```
Expected: `FAIL`，因为 handler 与路由尚不存在。

- [ ] **Step 3: 实现严格 HTTP decoder 和路由。**

`handler.go` 必须：

- 使用 `http.MaxBytesReader`（preview 最大 1 MiB、signal 最大 256 KiB）；
- 使用 `json.Decoder` 加第二次 decode 检查 EOF；
- 对 root 与每个 target 建立允许字段 map；
- `POST /inspection-signals` 仅接收 `{ "signals": [...] }`，每项只带 `target`、`statusCode`、`errorKind`、`errorDetail`、`observedAtMs`；service 必须重新从 CPA inventory 读取当前 `proxy_url`，不得信任浏览器输入的 proxy URL；
- `GET task` 校验 `page >= 1`、`1 <= page_size <= 100`；
- 任务 preview/confirm/restore/signal 使用 `AuthorizeAdmin`，pool/task read 使用 `AuthorizePanel`；
- API 错误返回无完整代理 URL 的 error code/message。

在 `router.go` 创建 `proxyReplacementHandler`，传入 `rootHandler`，并在 generic management proxy 前增加：

```go
if strings.HasPrefix(r.URL.Path, "/v0/management/proxy-replacements") {
    middleware.WithCORS(appCtx.Config, proxyReplacementHandler.Handle)(w, r)
    return
}
```

- [ ] **Step 4: 连接依赖并启动 service。**

在 `app.Context` 增加：

```go
ProxyReplacementService *proxyreplacementsvc.Service
```

构造顺序必须为：创建 `authFileMutationCoordinator` → 创建 `proxypoolstatesvc.New(st)` 的独立 `proxyPoolStateRegistry` → 以 registry 为 `ProxyFailureReporter` 创建 `CodexInspectionService` → 以 inspection service 为 `Rechecker`、registry 为 `PoolStateStore`、相同 coordinator、`proxyService` invalidator 创建 `ProxyReplacementService`。不得让两个 service 各自创建 coordinator，也不得让 `codexinspection` import `proxyreplacement`。

在 `main.go` 的现有 worker start 区增加：

```go
serverApp.AppContext().ProxyReplacementService.Start(ctx)
```

在退出路径、`server.Shutdown` 前使用 10 秒 `shutdownCtx` 调用：

```go
if err := serverApp.AppContext().ProxyReplacementService.StopAndWait(shutdownCtx); err != nil {
    log.Printf("shutdown proxy replacement worker: %v", err)
}
```

`Start` 调用 `RequeueRunningOnStartup` 后循环 claim；如果 process 在 CPA response 前退出，Task 4 的旧/新代理比较实现幂等恢复。

- [ ] **Step 5: 运行 controller、router 和启动回归。**

Run:
```powershell
cd apps/manager-server; go test ./internal/http/controller/proxyreplacement ./internal/http/router ./internal/app ./cmd/cpa-manager-plus -count=1
gofmt -w internal/http/controller/proxyreplacement internal/app/context.go internal/http/router/router.go cmd/cpa-manager-plus/main.go
git diff --check
```
Expected: `PASS`；路由不落到 CPA generic proxy。

- [ ] **Step 6: 提交 HTTP/生命周期接线。**

```powershell
git add apps/manager-server/internal/http/controller/proxyreplacement apps/manager-server/internal/http/router apps/manager-server/internal/app/context.go apps/manager-server/cmd/cpa-manager-plus/main.go
git commit -m "feat: expose proxy replacement management API"
```

### Task 7: 扩展保存代理弹窗，显示疑似状态和恢复按钮

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.module.scss`
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: 写前端 API 和弹窗失败测试。**

```tsx
it('shows suspect state and restores only the selected saved proxy', async () => {
  mockGetProxyPool([{ fingerprint: 'fp-a', status: 'suspect', lastFailureReason: 'transport_timeout' }]);
  render(<ProxyFilterModal open urls={['http://a/', 'http://b']} onClose={vi.fn()} onSave={vi.fn()} />);
  expect(await screen.findByText('疑似失效')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: '恢复为正常' }));
  expect(mockRestoreProxyPool).toHaveBeenCalledWith('fp-a');
});
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyFilterModal.test.tsx
```
Expected: `FAIL`，因为 API 和状态 UI 尚不存在。

- [ ] **Step 3: 在 `usageService.ts` 定义 API。**

增加明确类型和调用：

```ts
export interface ProxyPoolStateResponse { fingerprint: string; status: 'normal' | 'suspect'; lastFailureAtMs?: number; lastFailureReason?: string; }
getProxyReplacementPool: (base: string, signal?: AbortSignal) => axios.get(...'/v0/management/proxy-replacements/proxy-pool'...)
restoreProxyReplacementPoolEntry: (base: string, fingerprint: string) => axios.post(...`/proxy-pool/${encodeURIComponent(fingerprint)}/restore`...)
```

使用现有 `buildUrl`、auth header 和 axios cancellation 风格；不能加载 credential runtime metadata 来计算状态。

- [ ] **Step 4: 修改 `ProxyFilterModal`。**

进入弹窗时一次性读取 pool state，把 `normalizeProxyFilterURL(url)` 的 fingerprint 映射到 state；每个已有 URL 显示 `正常` 或 `疑似失效`。仅 `suspect` 行显示 `恢复为正常`。恢复成功后只在本地将该 fingerprint 变为 `normal`，不改变保存的 URL 文本，不触发凭证列表刷新，不触发巡检。

保留既有“保存列表”和“筛选未使用 URL”行为，末尾 `/` 的状态匹配必须命中同一个 fingerprint。

- [ ] **Step 5: 完善 i18n 并跑测试。**

添加键：`accounts.proxy_pool_normal`、`accounts.proxy_pool_suspect`、`accounts.proxy_pool_restore`、`accounts.proxy_pool_restored`、`accounts.proxy_pool_failure_timeout`、`accounts.proxy_pool_failure_request_failed`。四种语言文件都必须有相同 key 集。

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyFilterModal.test.tsx
npm --workspace apps/web run type-check
```
Expected: `PASS`。

- [ ] **Step 6: 提交代理池状态 UI。**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/components/ProxyFilterModal.tsx apps/web/src/features/accounts/components/ProxyFilterModal.module.scss apps/web/src/features/accounts/components/ProxyFilterModal.test.tsx apps/web/src/i18n/locales
git commit -m "feat: show and restore proxy pool states"
```

### Task 8: 实现共享的代理替换预览、确认和轮询弹窗

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Create: `apps/web/src/features/accounts/components/ProxyReplacementModal.tsx`
- Create: `apps/web/src/features/accounts/components/ProxyReplacementModal.module.scss`
- Create: `apps/web/src/features/accounts/components/ProxyReplacementModal.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: 写 modal 行为失败测试。**

```tsx
it('requires preview then confirmation and polls a persisted task', async () => {
  mockCreatePreview({ task: draftTask({ canConfirm: true, items: [assignment('alice', 'http://old', 'http://new', 'unused')] }) });
  mockConfirmTask({ task: queuedTask() });
  mockGetTask.once({ task: runningTask(1, 2) }).once({ task: completedTask(2, 2) });
  render(<ProxyReplacementModal open source="batch_pool" targets={[target('alice')]} onClose={vi.fn()} onCompleted={vi.fn()} />);
  await userEvent.click(screen.getByRole('button', { name: '生成映射预览' }));
  expect(await screen.findByText('未占用代理')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: '确认更新代理' }));
  expect(await screen.findByText('2 / 2')).toBeInTheDocument();
});

it('disables confirmation when no normal candidate exists', async () => {
  mockCreatePreview({ task: unavailableTask('没有可用候选代理') });
  render(<ProxyReplacementModal open source="batch_pool" targets={[target("alice")]} onClose={vi.fn()} onCompleted={vi.fn()} />);
  await userEvent.click(screen.getByRole('button', { name: '生成映射预览' }));
  expect(screen.getByRole('button', { name: '确认更新代理' })).toBeDisabled();
});
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyReplacementModal.test.tsx
```
Expected: `FAIL`，因为 component/API 尚不存在。

- [ ] **Step 3: 扩展 `usageService.ts` 任务 API。**

定义并实现：

```ts
createProxyReplacementPreview(base, request, signal)
confirmProxyReplacementTask(base, taskId, signal)
getProxyReplacementTask(base, taskId, { page, pageSize, signal })
signalProxyInspectionFailures(base, signals, signal)
```

响应 DTO 必须包含 `task.status`、`canConfirm`、`summary`、分页 `items`、每项 `oldProxyUrl` / `newProxyUrl` / `allocationSource` / `writeErrorMessage` / `recheckErrorMessage`。API wrapper 不得调用 `authFilesApi.list`、`credential-runtime-metadata`、history 或 usage endpoint。

- [ ] **Step 4: 实现 modal 状态机。**

`ProxyReplacementModal` props：

```ts
type ProxyReplacementModalProps = {
  open: boolean;
  source: 'inspection' | 'batch_uniform' | 'batch_pool';
  targets: ProxyReplacementTarget[];
  onClose: () => void;
  onCompleted: (task: ProxyReplacementTask) => void;
};
```

状态流：`editing → previewing → preview_ready → confirming → polling → terminal`。手工 `batch_uniform` 显示 URL 文本框和保存池下拉候选；`batch_pool` 与 `inspection` 显示 pool 说明。展示任务的 `unusedNormalCount`、`fallbackNormalCount`、`suspectExcludedCount`、分页映射和“复制完整映射”。复制格式为每行 `账号<TAB>原代理<TAB>新代理<TAB>分配来源<TAB>状态`。

轮询每 800 ms，在 `completed|partial|failed|expired|unavailable` 停止，并在 component unmount / task ID 改变时取消 `AbortController`。任务完成只调用 `onCompleted` 一次。

- [ ] **Step 5: 增加逐项状态文案和测试。**

映射：

```ts
updated + recheck_succeeded => '代理已更新，复检成功'
updated + recheck_failed => `代理已更新，复检失败：${message}`
write_failed => `代理更新失败：${message}`
skipped_stale => '账号代理已被其他操作修改，未覆盖'
```

测试必须断言原始错误引用不会被 task `recheckErrorMessage` 替换；inspection 页面会保留原始 result error，modal 只显示恢复任务结果。

- [ ] **Step 6: 运行 modal 与 TypeScript 测试。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyReplacementModal.test.tsx src/features/accounts/model/proxyReplacement.test.ts
npm --workspace apps/web run type-check
```
Expected: `PASS`。

- [ ] **Step 7: 提交共享 modal。**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/components/ProxyReplacementModal.tsx apps/web/src/features/accounts/components/ProxyReplacementModal.module.scss apps/web/src/features/accounts/components/ProxyReplacementModal.test.tsx apps/web/src/i18n/locales
git commit -m "feat: add proxy replacement preview modal"
```

### Task 9: 凭证列表多选增加批量代理 URL 操作

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.module.scss`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts`

- [ ] **Step 1: 写多选工具栏失败测试。**

```tsx
it('opens batch proxy replacement for exactly selected credential identities', async () => {
  renderAccountsWithSelection([row('a.json', '1'), row('b.json', '2')]);
  await userEvent.click(screen.getByRole('button', { name: '批量修改代理 URL' }));
  expect(screen.getByRole('dialog', { name: '批量修改代理 URL' })).toBeInTheDocument();
  expect(modalTargets()).toEqual([target('a.json', '1'), target('b.json', '2')]);
});

it('does not invoke quota, history, usage or runtime metadata after a manual proxy task finishes', async () => {
  finishProxyTask();
  expect(mockAccountWindowUsage).not.toHaveBeenCalled();
  expect(mockHistory).not.toHaveBeenCalled();
  expect(mockRuntimeMetadata).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/features/accounts/accountsWorkspaceWiring.test.ts
```
Expected: `FAIL`，因为工具栏入口不存在。

- [ ] **Step 3: 添加入口且保留全部原有批量操作。**

在 `renderFloatingBatchActions` 中，在既有“批量调整优先级”旁边添加 `批量修改代理 URL`。它使用现有 `selectedAuthFileRows` 和 `getAuthFilePatchTarget(row.raw)` 构造 Task 1 target；不能删除或替换优先级、websocket、恢复、删除按钮。

默认 source 为 `batch_pool`，modal 内可切到 `batch_uniform`。仅在至少一个可解析稳定 target 已选中时启用；不可解析 row 在打开前排除并显示选中/可处理数量。

- [ ] **Step 4: 连接完成回调。**

`onCompleted` 只执行现有凭证清单 cache invalidation/当前 page reload：

```ts
invalidateCredentialList();
void reloadCurrentCredentialPage({ preserveSelection: false });
```

禁止调用任何 `refreshQuota`、`refreshWindowUsage`、`loadHistory`、`useCredentialRuntimeMetadata` 全量加载或 health inspection。弹窗关闭后清空已完成 selection，避免用户误把旧 selection 再次提交。

- [ ] **Step 5: 运行账户页面回归。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/features/accounts/accountsWorkspaceWiring.test.ts
npm --workspace apps/web run type-check
```
Expected: `PASS`；既有批量 priority 测试继续通过。

- [ ] **Step 6: 提交凭证列表接入。**

```powershell
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.module.scss apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts
git commit -m "feat: batch update credential proxy urls"
```

### Task 10: 健康巡检新增“代理异常”筛选、signal 与替换入口

**Files:**
- Modify: `apps/web/src/features/monitoring/codexInspection.ts`
- Modify: `apps/web/src/features/monitoring/model/codexInspectionPresentation.ts`
- Modify: `apps/web/src/features/monitoring/model/codexInspectionPresentation.test.ts`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`
- Modify: `apps/web/src/features/monitoring/ServerCodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/ServerCodexInspectionPage.lifecycle.test.tsx`

- [ ] **Step 1: 写 presentation 筛选失败测试。**

```ts
it('filters proxy abnormalities without changing the original action bucket', () => {
  const reauthAndProxy = result({ action: 'reauth', proxySuspect: true });
  expect(filterInspectionResults([reauthAndProxy], 'all', 'proxy_abnormal')).toEqual([reauthAndProxy]);
  expect(filterInspectionResults([reauthAndProxy], 'all', 'reauth')).toEqual([reauthAndProxy]);
  expect(getActionFilterCounts([reauthAndProxy]).proxy_abnormal).toBe(1);
});
```

- [ ] **Step 2: 运行测试，确认失败。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/model/codexInspectionPresentation.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx
```
Expected: `FAIL`，因为 `proxy_abnormal` 尚未定义。

- [ ] **Step 3: 扩展过滤模型与结果 panel。**

将 `CodexInspectionStoredActionFilter` 加入 `'proxy_abnormal'`；`filterInspectionResults` 对该值仅筛选 `item.proxySuspect === true`，绝不重写 `item.action`。`CodexInspectionResultsPanel` tab 顺序固定为：

```text
全部 | 重新登录 | 删除 | 禁用 | 启用 | 保留 | 代理异常
```

新增 props：

```ts
pageProxyReplacementCount?: number;
pageProxyReplacementSubmitting?: boolean;
onReplaceProxyAndReinspect?: () => void;
```

只有当前 filter 为 `proxy_abnormal` 时显示 `更换代理并复检 (N)`；`N` 是当前分页且可构造稳定 target 的数量。按钮加载态不可重复点。

- [ ] **Step 4: 实现本机巡检 signal 上报。**

在 `CodexInspectionPage` 将本机 result 转为 `ProxyFailureSignal`，调用 `signalProxyInspectionFailures`。信号只在 `isLocalProxySuspect(result)` 为 true 时发送；每个 `runGeneration + account key + normalized error detail` 只允许一次，使用 `useRef<Set<string>>` 去重。上报失败只显示 warning，不阻断原有巡检结果、自动 reauth、建议操作或手工操作。

点击按钮时把当前 `resultPagination.pageItems` 中 `proxySuspect` 的 stable target 交给 `ProxyReplacementModal`，source 固定为 `inspection`。成功替换后只重新渲染这些结果的恢复标签/打开 task detail；不重跑全部本机巡检。

- [ ] **Step 5: 实现服务端巡检入口。**

`ServerCodexInspectionPage` 从 API result 的 `proxySuspect` 直接过滤。使用同一共享 modal、相同的当前页 stable target builder 和 inspection source；不依赖 credential runtime metadata。

任务完成后不改写原 server inspection result 的 `action` 或原始 `errorDetail`；页面只在该行附加最近代理任务状态链接/提示，完整结果从 task API 读取。

- [ ] **Step 6: 添加 lifecycle 回归测试。**

```tsx
it('reports a local proxy suspect once across rerenders', async () => {
  render(<CodexInspectionPage />); finishLocalRun(proxySuspectResult());
  await waitFor(() => expect(mockSignalProxyFailures).toHaveBeenCalledTimes(1));
  rerender(<CodexInspectionPage />);
  expect(mockSignalProxyFailures).toHaveBeenCalledTimes(1);
});

it('server inspection exposes current proxy abnormal page targets only', async () => {
  renderServerRun([result({ proxySuspect: true }), result({ proxySuspect: false })]);
  await userEvent.click(screen.getByRole('button', { name: /代理异常/ }));
  expect(screen.getByRole('button', { name: '更换代理并复检 (1)' })).toBeEnabled();
});
```

- [ ] **Step 7: 运行 monitoring 回归。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/monitoring/model/codexInspectionPresentation.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/ServerCodexInspectionPage.lifecycle.test.tsx
npm --workspace apps/web run type-check
```
Expected: `PASS`；401/429/配额项目不会出现在“代理异常”。

- [ ] **Step 8: 提交巡检页面接入。**

```powershell
git add apps/web/src/features/monitoring/codexInspection.ts apps/web/src/features/monitoring/model/codexInspectionPresentation.ts apps/web/src/features/monitoring/model/codexInspectionPresentation.test.ts apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx apps/web/src/features/monitoring/CodexInspectionPage.tsx apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx apps/web/src/features/monitoring/ServerCodexInspectionPage.tsx apps/web/src/features/monitoring/ServerCodexInspectionPage.lifecycle.test.tsx
git commit -m "feat: replace suspected proxy failures from inspection"
```

### Task 11: 端到端回归、构建验证与安全审计

**Files:**
- Modify only when a failing test demonstrates a defect in the files above.

- [ ] **Step 1: 写一个跨 service 的测试 fixture，验证只有成功写入项复检。**

在 `apps/manager-server/internal/service/proxyreplacement/service_test.go` 增加：

```go
func TestInspectionTaskRechecksOnlySuccessfulWrites(t *testing.T) {
    task := createInspectionTask(t, service, []targetFixture{
        {File: "ok.json", OldProxy: "http://old-a", NewProxy: "http://new-a"},
        {File: "changed.json", OldProxy: "http://old-b", NewProxy: "http://already-changed"},
        {File: "reject.json", OldProxy: "http://old-c", NewProxy: "http://new-c", PatchErr: errors.New("CPA rejected")},
    })
    confirmAndWait(t, service, task.ID)
    if got := probes.TargetFiles(); !reflect.DeepEqual(got, []string{"ok.json"}) { t.Fatalf("probed = %#v", got) }
    page := getTaskPage(t, service, task.ID)
    assertItemStatus(t, page, "changed.json", model.ProxyReplacementItemSkippedStale)
    assertItemStatus(t, page, "reject.json", model.ProxyReplacementItemWriteFailed)
}
```

- [ ] **Step 2: 运行所有定向回归。**

Run:
```powershell
cd apps/manager-server; go test ./internal/model ./internal/repository/proxyreplacement ./internal/repository/codexinspection ./internal/repository/sqlite ./internal/service/cpaauthfiles ./internal/service/codexinspection ./internal/service/proxyreplacement ./internal/http/controller/proxyreplacement ./internal/http/router ./internal/app -count=1
cd ../..; npm --workspace apps/web run test -- src/features/accounts/model/proxyReplacement.test.ts src/features/accounts/components/ProxyFilterModal.test.tsx src/features/accounts/components/ProxyReplacementModal.test.tsx src/features/accounts/AccountsPage.test.tsx src/features/accounts/accountsWorkspaceWiring.test.ts src/features/monitoring/model/codexInspectionPresentation.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/ServerCodexInspectionPage.lifecycle.test.tsx
```
Expected: 全部 `PASS`。

- [ ] **Step 3: 运行完整构建检查。**

Run:
```powershell
npm --workspace apps/web run type-check
npm --workspace apps/web run build
cd apps/manager-server; go test ./... -count=1
cd ../..; git diff --check
```
Expected: web build 成功、Go 全包测试通过、`git diff --check` 无输出。

- [ ] **Step 4: 手工审核写入和日志边界。**

Run:
```powershell
rg -n 'proxy_url|proxyUrl|oldProxyURL|newProxyURL' apps/manager-server/internal/service/proxyreplacement apps/manager-server/internal/http/controller/proxyreplacement apps/manager-server/internal/repository/proxyreplacement
rg -n 'log\.(Print|Printf|Println)|slog\.' apps/manager-server/internal/service/proxyreplacement apps/manager-server/internal/http/controller/proxyreplacement
```

Expected: CPA patch payload构造只能出现 `name`、可选 `auth_index`、`proxy_url`；日志调用不插入 `OldProxyURL`、`NewProxyURL` 或未脱敏 error body。若命中不符合项，先添加失败测试，再删除/脱敏该日志字段。

- [ ] **Step 5: 提交验证修复。**

```powershell
git add apps/manager-server/internal/service/proxyreplacement/service_test.go
git add -u
git commit -m "test: cover proxy replacement task boundaries"
```

### Task 12: 部署前验收与私有仓库交付

**Files:**
- No source change required if Task 11 is clean.

- [ ] **Step 1: 核验变更范围。**

Run:
```powershell
git status --short
git log --oneline --decorate -8
git diff upstream/main...HEAD -- apps/manager-server apps/web | rg -n 'cli-proxy-api|CPA Core|docker compose.*cpa(\s|$)'
```
Expected: 工作树干净；没有 CPA Core / `cli-proxy-api` 修改。若 `upstream/main` 分支名称不同，用现有 remote 默认分支替换，但绝不推 upstream。

- [ ] **Step 2: 从已验证的隔离 worktree 构建候选镜像。**

只在本计划已提交且 Task 11 全绿的隔离 worktree 执行；使用仓库根 `Dockerfile.manager-server`，不从脏的 `source` 工作树构建。

Run:
```powershell
$wt = 'C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-batch-actions-20261017'
$sha = (git -C $wt rev-parse --short HEAD).Trim()
$tag = "cpa-manager-plus:lo-custom-v1.14.7-20261018-$sha"
Push-Location $wt
docker build --pull --platform linux/amd64 `
  --build-arg VERSION="1.14.7-lo-custom-20261018" `
  --build-arg SOURCE_COMMIT=$sha `
  -t $tag -f Dockerfile.manager-server .
docker image inspect -f '{{.Id}}' $tag
Pop-Location
```
Expected: 输出一个候选镜像 ID；构建上下文是 `$wt`，不是 `C:\kyon\docker\cpa-manager\source`。

- [ ] **Step 3: 本地只定向部署 `cpa-manager-plus`，并冻结 CPA Core 身份。**

Run:
```powershell
$project = 'C:\kyon\docker\cpa-manager'
$beforeManager = docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cpa-manager-plus
$beforeCore = docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cli-proxy-api
Push-Location $project
function global:Read-Host {
  param([string]$Prompt)
  Write-Host "$Prompt [auto-confirmed]"
  'PROMOTE_CPAM'
}
& .\deploy-cpa-manager-key-policy.ps1 -Promote -CandidateImage $tag
Pop-Location
$afterManager = docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cpa-manager-plus
$afterCore = docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cli-proxy-api
if ($beforeCore -ne $afterCore) { throw "CPA Core changed: before=$beforeCore after=$afterCore" }
if ($beforeManager -eq $afterManager) { throw 'CPAM was not replaced by the candidate image' }
```
Expected: 只有 `cpa-manager-plus` 的身份变化；`cli-proxy-api` 的 ID、StartedAt、Image 完全相同。禁止运行未带服务名的 `docker compose up -d`。

- [ ] **Step 4: 从同一个候选镜像部署服务器原生 CPAM。**

使用已配置的 SSH MCP `LACPA-VPS`，先记录：

```bash
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
systemctl is-active cpam.service
```

从 `$tag` 导出 Linux amd64 二进制到临时目录，计算 SHA-256，传到 `/home/cpam/_incoming/cpa-manager-plus.<sha>`。在服务器执行以下原子替换序列（保留 `/home/cpam/config/cpam.env`、`/home/cpam/secrets/*`、`/home/cpam/data/*`、`data.key` 与 OpenResty 配置）：

```bash
set -euo pipefail
install -d -m 0750 /home/cpam/_incoming
install -m 0755 -o root -g root /home/cpam/_incoming/cpa-manager-plus.<sha> /home/cpam/app/cpa-manager-plus.next
sha256sum /home/cpam/app/cpa-manager-plus.next
mv /home/cpam/app/cpa-manager-plus /home/cpam/app/cpa-manager-plus.previous
mv /home/cpam/app/cpa-manager-plus.next /home/cpam/app/cpa-manager-plus
printf 'version=1.14.7-lo-custom-20261018\ncommit=%s\nsha256=%s\ndeployed_at=%s\n' '<sha>' '<sha256>' "$(date -u +%FT%TZ)" > /home/cpam/DEPLOYMENT-MANIFEST
systemctl restart cpam.service
systemctl is-active cpam.service
curl -fsS http://127.0.0.1:18317/health
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
```

Expected: 仅 `cpam.service` 重启；远端 `cpa.service` 部署前后 PID 完全相同；禁止使用服务器 Docker、禁止进入 `/home/cpa`、禁止执行 `systemctl restart cpa.service`。
- [ ] **Step 5: 运行部署后最小 smoke。**

检查：

```text
1. 保存代理弹窗能显示 Normal/Suspect，末尾 / 同一状态。
2. 凭证列表选两个账号可打开“批量修改代理 URL”预览，预览不产生 CPA PATCH。
3. 手工统一模式完成后只更新 proxy_url，不出现额度/用量刷新请求。
4. 已知巡检 502 request-failed 行进入“代理异常”，401/429 不进入。
5. 确认 inspection 任务后，成功项显示定向复检结果；失败/冲突项不复检。
```

- [ ] **Step 6: 只推私有分支。**

Run:
```powershell
git remote -v
git push origin HEAD:lo/custom
git ls-remote --heads origin lo/custom
```
Expected: 只发生 `origin` 的 `lo/custom` 更新；没有 `git push upstream`。

---

## 3. 验收映射

| 需求 | 实现任务 |
| --- | --- |
| 单次代理链路失败标记疑似异常，不自动改账号 | Task 1、Task 5、Task 10 |
| 502 request failed/timeout 命中；401/429/额度/业务 5xx 排除 | Task 1、Task 5 |
| 保存代理状态、状态持久化、手动恢复 | Task 2、Task 6、Task 7 |
| 未占用优先、正常已占用回退、无限复用、稳定偏移 | Task 4 |
| preview 冻结、确认执行、刷新/关闭浏览器不丢任务 | Task 2、Task 4、Task 6、Task 8 |
| 新代理复检失败保留新值并标记 suspect | Task 4、Task 5 |
| 凭证列表统一/代理池两个模式 | Task 8、Task 9 |
| 巡检“代理异常”tab 和更换后复检 | Task 5、Task 8、Task 10 |
| 只修改 proxy_url，不动 Token/priority/status/计费 | Task 3、Task 4、Task 11 |
| 不碰 CPA Core / cli-proxy-api，私有推送 | Task 12 |
