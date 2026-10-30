# 独立派生 SQLite 存储 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 CPAM 的可重建用量派生数据写入独立 `usage-derived.sqlite`，从根源隔离主 `usage.sqlite` 的派生写锁，同时保持当前页历史/价格、仪表盘和归档的正确性。

**Architecture:** 主库继续是唯一事实源并由现有 collector、计费、额度、恢复和控制面使用。独立派生 runtime 用只读源连接短批读取主库/归档数据，在独立 SQLite 中写入物化结果与 checkpoint；读路径合并已验证派生快照和主库水位线后的短尾部，绝不在 HTTP 请求中同步回填。

**Tech Stack:** Go 1.24、`database/sql`、`modernc.org/sqlite`、现有 CPAM `store`/repository/worker 架构、React/TypeScript 管理端、Docker 本地 CPAM、服务器原生 `cpam.service`。

---

## 不可变边界

- 只修改 CPAM。绝不修改、重启、构建、部署 CPA Core、`cli-proxy-api`、`/home/cpa` 或 `cpa.service`。
- 主 `usage.sqlite` 保留原始 `usage_events`、identity ledger、价格目录、Credits、额度、归档、恢复、代理、巡检和优先级状态；不得复制/迁移/删除这些事实数据。
- `usage-derived.sqlite` 只容纳可重建 materialization、状态、水位线与派生索引；不含 Token、认证 JSON、管理 Key、代理 URL 或原始错误正文。
- 禁止 SQLite `ATTACH` 跨库事务。一次源读取结束后，才开始一次派生库写事务。
- 现有当前页历史策略、240 秒 Manager 请求超时、最近请求/额度独立刷新节奏、Token Recovery、批量导入、代理功能与服务器错误降优先级二改必须保留。

## 文件结构

| 路径 | 职责 |
| --- | --- |
| `apps/manager-server/internal/config/config.go` | 解析独立派生模式、路径和节流参数。 |
| `apps/manager-server/internal/repository/sqlite/derived_db.go` | 打开只读主库连接与独立派生 DB；不对只读源执行迁移。 |
| `apps/manager-server/internal/repository/sqlite/derived_migrate.go` | 仅创建派生库 schema、状态表和索引。 |
| `apps/manager-server/internal/derivedstore/contracts.go` | source、sink、coverage、stream、generation 的稳定接口和数据类型。 |
| `apps/manager-server/internal/derivedstore/source.go` | 从主库/归档做短只读批次读取，并返回 source epoch、target watermark、价格 revision。 |
| `apps/manager-server/internal/derivedstore/sink.go` | 对独立库写入物化行、本地 applied-event ledger 与 coverage。 |
| `apps/manager-server/internal/derivedstore/engine.go` | 受限调度、退避、yield、generation 切换和 status 快照。 |
| `apps/manager-server/internal/store/derived_runtime.go` | 将双库读取接入现有 Store 公共 API，保留 legacy 单库 fallback。 |
| `apps/manager-server/internal/worker/separate_derived.go` | 独立派生 Worker；不复用会写主库的旧 worker。 |
| `apps/manager-server/internal/repository/usagearchive/derived_guard.go` | 归档删除前校验独立派生 coverage。 |
| `apps/manager-server/internal/service/monitoring/service.go` | 独立模式下把 `catch_up=true` 改为非阻塞 wake，不在 HTTP 内写派生。 |
| `apps/manager-server/internal/service/dashboard/service.go` | 使用 Store 的双库快照 API，不更改 Dashboard 业务指标。 |
| `apps/manager-server/cmd/cpa-manager-plus/main.go` | 按 storage mode 创建 runtime、启动正确的 Worker、关闭正确资源。 |
| `apps/manager-server/internal/httpapi/*` | 输出脱敏派生状态，不泄露路径或敏感数据。 |
| `apps/web/src/features/accounts/*` | 将已有的金额与 `sync_status` 分离渲染：有已验证金额时不得因后台同步标签而隐藏金额。 |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | 固化本二改不可回退约束。 |

---

### Task 1: 配置模型与启动前冲突校验

**Files:**
- Modify: `apps/manager-server/internal/config/config.go`
- Modify: `apps/manager-server/internal/config/config_test.go`
- Create: `apps/manager-server/internal/config/derived_storage_test.go`

- [ ] **Step 1: 写出失败配置测试。**

```go
func TestLoadSeparateDerivedDefaultsToDataDir(t *testing.T) {
    t.Setenv("USAGE_DERIVED_STORAGE_MODE", "separate")
    t.Setenv("USAGE_DATA_DIR", t.TempDir())
    cfg, err := config.Load()
    require.NoError(t, err)
    require.Equal(t, config.DerivedStorageSeparate, cfg.DerivedStorageMode)
    require.Equal(t, filepath.Join(cfg.DataDir, "usage-derived.sqlite"), cfg.DerivedDBPath)
}

func TestLoadRejectsSeparatePathEqualToPrimary(t *testing.T) {
    dir := t.TempDir()
    t.Setenv("USAGE_DERIVED_STORAGE_MODE", "separate")
    t.Setenv("USAGE_DB_PATH", filepath.Join(dir, "usage.sqlite"))
    t.Setenv("USAGE_DERIVED_DB_PATH", filepath.Join(dir, "usage.sqlite"))
    _, err := config.Load()
    require.ErrorContains(t, err, "USAGE_DERIVED_DB_PATH must differ from USAGE_DB_PATH")
}
```

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/config -run 'TestLoad(SeparateDerivedDefaultsToDataDir|RejectsSeparatePathEqualToPrimary)' -count=1
```
Expected: FAIL，因为 mode、path 与校验尚不存在。

- [ ] **Step 3: 实现明确配置契约。**

```go
type DerivedStorageMode string

const (
    DerivedStoragePrimary  DerivedStorageMode = "primary"
    DerivedStorageSeparate DerivedStorageMode = "separate"
)

// Config additions. BackgroundDerivationsEnabled stays the worker on/off gate.
DerivedStorageMode      DerivedStorageMode
DerivedDBPath           string
DerivedSourceBatchSize  int
DerivedCycleMaxBatches  int
DerivedYield            time.Duration
```

实现 `parseDerivedStorageMode`，只接受 `primary`、`separate`；默认 `primary`。`separate` 的默认
路径是 `filepath.Join(dataDir, "usage-derived.sqlite")`。对 batch size 做 `50..5000` clamp、
cycle batches 做 `1..8` clamp、yield 做 `25..2000ms` clamp。路径等于主库路径、路径为空、
或 `separate` 模式下路径无法规范化时返回启动错误。不要删除或改变
`USAGE_BACKGROUND_DERIVATIONS_ENABLED` 的现有解释。

- [ ] **Step 4: 运行配置回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/config -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/internal/config
git commit -m "feat: configure separate derived SQLite storage"
```

### Task 2: 建立主库 source identity、只读打开器与派生专属 schema

**Files:**
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/db.go`
- Create: `apps/manager-server/internal/repository/sqlite/derived_db.go`
- Create: `apps/manager-server/internal/repository/sqlite/derived_migrate.go`
- Create: `apps/manager-server/internal/repository/sqlite/derived_db_test.go`
- Create: `apps/manager-server/internal/repository/sqlite/derived_migrate_test.go`

- [ ] **Step 1: 写失败测试，证明源库永远只读且 schema 被隔离。**

```go
func TestOpenReadOnlySourceRejectsWrite(t *testing.T) {
    primary := newMigratedPrimaryDB(t)
    ro, err := sqlite.OpenReadOnly(primary.Path())
    require.NoError(t, err)
    _, err = ro.Exec(`insert into settings(key, value) values ('x', 'y')`)
    require.Error(t, err)
}

func TestOpenDerivedDoesNotCreatePrimaryControlTables(t *testing.T) {
    derived, err := sqlite.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
    require.NoError(t, err)
    requireTableExists(t, derived, "derived_stream_state")
    requireTableMissing(t, derived, "token_recovery_tasks")
    requireTableMissing(t, derived, "settings")
}
```

- [ ] **Step 2: 运行测试确认失败。**

Run:
```powershell
cd apps/manager-server
go test ./internal/repository/sqlite -run 'Test(OpenReadOnlySourceRejectsWrite|OpenDerivedDoesNotCreatePrimaryControlTables)' -count=1
```
Expected: FAIL。

- [ ] **Step 3: 实现三个独立数据库边界。**

在主库迁移中只添加一次性 source identity：

```sql
create table if not exists usage_source_identity (
  singleton integer primary key check (singleton = 1),
  source_epoch text not null,
  created_at_ms integer not null
);
```

启动迁移使用随机 128-bit hex 写入唯一 `(singleton=1)` 行；已有行绝不修改。

`OpenReadOnly(path)` 使用 `file:<path>?mode=ro`、`query_only=ON`、短 `busy_timeout`，不调用
`Migrate`，也不使用 `_txlock=immediate`。`OpenDerived(path)` 调用全新的 `MigrateDerived`，只创建：

```text
derived_store_meta
derived_stream_state
derived_applied_events
usage_account_model_rollups
usage_pricing_account_rollups_v1
usage_pricing_account_price_cache_v1
usage_pricing_hourly_rollups_v1
usage_dashboard_hourly_rollups
usage_hourly_aggregate_v1
usage_monitoring_* pure projection / stats / metadata tables
```

`usage_event_identity_ledger`、`usage_archive_*`、`model_prices`、`settings` 和所有任务/凭证表明确
不在 `MigrateDerived` 内。派生 schema 的 state 表记录 `source_epoch`、stream、generation、
coverage/target event ID、pricing revision、archive generation、状态与无敏感错误码。

- [ ] **Step 4: 运行 SQLite 包回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/repository/sqlite -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/internal/repository/sqlite
git commit -m "feat: add isolated derived SQLite schema"
```

### Task 3: 主库/归档短批 source reader 与派生写入 sink

**Files:**
- Create: `apps/manager-server/internal/derivedstore/contracts.go`
- Create: `apps/manager-server/internal/derivedstore/source.go`
- Create: `apps/manager-server/internal/derivedstore/archive_source.go`
- Create: `apps/manager-server/internal/derivedstore/sink.go`
- Create: `apps/manager-server/internal/derivedstore/source_test.go`
- Create: `apps/manager-server/internal/derivedstore/sink_test.go`

- [ ] **Step 1: 定义编译期阻断主库写入的接口并写失败测试。**

```go
type Source interface {
    Identity(context.Context) (SourceIdentity, error)
    Snapshot(context.Context) (SourceSnapshot, error)
    EventsAfter(context.Context, int64, int64, int) ([]EventRecord, error)
    ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error)
    PricingRevision(context.Context) (string, error)
}

type Sink interface {
    State(context.Context, Stream) (StreamState, error)
    Apply(context.Context, ApplyBatch) (StreamState, error)
    Status(context.Context) (Status, error)
}
```

测试应向主库插入 3 条事件，执行 `EventsAfter`，并断言主库的 `usage_events`、
`usage_event_identity_ledger`、旧 rollup state 和 settings 的行数/值都没有变化。

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/derivedstore -run 'Test(SourceReadsShortBoundedBatch|SinkAdvancesCoverageOnlyAfterCommit)' -count=1
```
Expected: FAIL。

- [ ] **Step 3: 实现 snapshot、archive 与 sink 原子性。**

`Snapshot` 先从只读主库取得 `source_epoch`、`target_event_id`、archive generation 和
canonical model-price revision。它只在 source read transaction 中读取，transaction 在返回前结束。

`EventsAfter` 必须按 `id asc`、`id > after AND id <= target`、有界 `LIMIT` 读取；包含现有
pricing、monitoring 和 account-history 转换所需字段，但不把 Token、请求 body 或代理数据写入
日志。`ArchivedEventsAfter` 使用现有 archive reader 与 `usage_archive_event_refs` 的覆盖证明，
保留 raw event ID 顺序。

`Sink.Apply` 在派生库单一 transaction 内：写聚合 upsert、写 `derived_applied_events`、更新
`derived_stream_state.coverage_event_id`。任何 SQL 错误回滚整个批次，coverage 保持原值。

- [ ] **Step 4: 运行 reader/sink 测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/derivedstore -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/internal/derivedstore
git commit -m "feat: add bounded source and sink for derived storage"
```

### Task 4: 将现有纯派生算法接入双库 engine，并保留逐 stream generation

**Files:**
- Create: `apps/manager-server/internal/derivedstore/engine.go`
- Create: `apps/manager-server/internal/derivedstore/pricing.go`
- Create: `apps/manager-server/internal/derivedstore/history.go`
- Create: `apps/manager-server/internal/derivedstore/monitoring.go`
- Create: `apps/manager-server/internal/derivedstore/hourly.go`
- Create: `apps/manager-server/internal/derivedstore/engine_test.go`
- Modify: `apps/manager-server/internal/repository/usagepricing/*.go`
- Modify: `apps/manager-server/internal/repository/usagerollup/*.go`
- Modify: `apps/manager-server/internal/repository/usagemonitoring/*.go`
- Modify: `apps/manager-server/internal/repository/usageaggregate/*.go`

- [ ] **Step 1: 建立 legacy 与独立 sink 的结果对比测试。**

为同一固定事件集（成功/失败、晚到事件、多个账户、多个模型、已归档事件、价格 revision 变化）运行：

```go
legacy := runLegacySingleDBRollups(t, primaryPath)
separate := runSeparateDerivedRollups(t, primaryPath, derivedPath)
require.Equal(t, legacy.AccountHistory, separate.AccountHistory)
require.Equal(t, legacy.Pricing, separate.Pricing)
require.Equal(t, legacy.Hourly, separate.Hourly)
require.Equal(t, legacy.Monitoring, separate.Monitoring)
```

- [ ] **Step 2: 运行对比测试确认失败。**

Run:
```powershell
cd apps/manager-server
go test ./internal/derivedstore -run TestSeparateDerivedParityWithLegacyRollups -count=1
```
Expected: FAIL。

- [ ] **Step 3: 抽取纯 event-to-rollup 转换，不搬运主库控制表。**

将现有 repository 中依赖 `usage_events` 的聚合计算拆成输入为 `[]EventRecord`、输出为
`ApplyBatch` 的纯转换函数。保留当前字段语义、模型归一化、identity 规则、late event 的 bucket
修正和价格计算；不要在新路径调用主库的 `CatchUpUsagePricing`、`CatchUpAccountHistoryRollups` 或
`CatchUpUsageHourlyAggregate`，因为它们会在主库写入。

价格 revision 改变时，engine 创建新 pricing generation，并保留旧完成 generation 供
`stale_repricing` 读取。新 generation 覆盖 `target_event_id` 后，在派生库单 transaction 中更新
active generation。任何不完整 generation 不得标记 `fresh`。

- [ ] **Step 4: 实现受控调度。**

```go
func (e *Engine) RunCycle(ctx context.Context) (CycleResult, error) {
    // one stream at a time; at most cfg.CycleMaxBatches batches total
    // source transaction ends before sink.Apply begins
    // sleep cfg.Yield after a completed batch sequence
}
```

`RunCycle` 轮转 stream，单进程只有一个 engine writer。源读取超过预算、出现 SQLite
`BUSY`/`LOCKED` 或 context 取消时，记录无敏感 code、保持 checkpoint、退避并 yield；不把错误
升级为主库写入或 HTTP 请求工作。

- [ ] **Step 5: 运行相关 package 回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/derivedstore ./internal/repository/usagepricing ./internal/repository/usagerollup ./internal/repository/usagemonitoring ./internal/repository/usageaggregate -count=1
```
Expected: PASS。

- [ ] **Step 6: 提交。**

```powershell
git add apps/manager-server/internal/derivedstore apps/manager-server/internal/repository
git commit -m "feat: materialize usage rollups in separate database"
```

### Task 5: 在 Store 和服务读路径中合并独立快照与主库尾部

**Files:**
- Modify: `apps/manager-server/internal/store/store.go`
- Create: `apps/manager-server/internal/store/derived_runtime.go`
- Create: `apps/manager-server/internal/store/derived_runtime_test.go`
- Modify: `apps/manager-server/internal/service/monitoring/service.go`
- Modify: `apps/manager-server/internal/service/monitoring/service_test.go`
- Modify: `apps/manager-server/internal/service/dashboard/service.go`
- Modify: `apps/manager-server/internal/service/dashboard/service_test.go`

- [ ] **Step 1: 写 50 账户当前页的双库读取测试。**

```go
func TestSeparateDerivedAccountHistoryMergesOnlyTail(t *testing.T) {
    rt := newSeparateRuntime(t, withCoverage(100))
    insertEvents(t, rt.Primary(), eventsWithIDs(1, 120))
    got, err := rt.LoadUsagePricingAccountSnapshot(ctx, visible50Keys)
    require.NoError(t, err)
    require.Equal(t, int64(100), got.State.CoverageEventID)
    require.Equal(t, expectedTotalsFromEvents(visible50Keys, 1, 120), totals(got))
    require.Equal(t, 20, rt.SourceReadMetrics().LastTailEventCount)
}
```

另写测试：价格 revision 不同返回可显示的旧金额加 `stale_repricing`；coverage/archive proof 失败返回
`pending` 或已验证 legacy fallback，绝不返回伪造的 `fresh`。

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/store ./internal/service/monitoring ./internal/service/dashboard -run 'TestSeparateDerived|TestAccountHistory' -count=1
```
Expected: FAIL。

- [ ] **Step 3: 为 Store 增加可选 DerivedRuntime。**

```go
type DerivedRuntime interface {
    LoadAccountSnapshot(context.Context, []string) (UsagePricingAccountSnapshot, bool, error)
    LoadHourlySnapshot(context.Context, UsageHourlyAggregateFilter, UsagePricingHourlyFilter) (UsageHourlyPricingSnapshot, bool, error)
    Wake()
    Status(context.Context) (derivedstore.Status, error)
    Close() error
}
```

`bool` 表示独立结果是否已被 coverage/epoch/revision 证明可用；为 `false` 时走当前只读 legacy
路径或受限 raw-tail 路径。不得因为独立库空白而启动全量扫描。

`LoadUsagePricingAccountSnapshot` 与 `LoadUsageHourlyPricingSnapshot` 保持公开返回类型兼容；
账户页已有 `total_cost` 时必须显示金额，把 `sync_status` 仅作为状态提示。独立模式中的
`AccountHistoryRequest.CatchUp=true` 改为 `runtime.Wake()`，响应不执行同步派生写入。

- [ ] **Step 4: 运行 Store/服务回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/store ./internal/service/monitoring ./internal/service/dashboard -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/internal/store apps/manager-server/internal/service/monitoring apps/manager-server/internal/service/dashboard
git commit -m "feat: read usage snapshots from isolated derived storage"
```

### Task 6: 为归档删除加入独立派生 coverage guard

**Files:**
- Create: `apps/manager-server/internal/repository/usagearchive/derived_guard.go`
- Modify: `apps/manager-server/internal/repository/usagearchive/repository.go`
- Modify: `apps/manager-server/internal/service/usage/archive.go`
- Create: `apps/manager-server/internal/repository/usagearchive/derived_guard_test.go`
- Modify: `apps/manager-server/internal/service/usage/archive_test.go`

- [ ] **Step 1: 写 archive delete fail-closed 测试。**

```go
func TestDeleteArchiveRejectsWhenSeparateCoverageBehindSegment(t *testing.T) {
    guard := staticDerivedGuard{CoverageEventID: 99, SourceEpoch: "epoch-a", Ready: true}
    run := archiveRunWithMaxRawEventID(100)
    err := guard.ValidateDelete(context.Background(), run)
    require.ErrorIs(t, err, usagearchive.ErrDerivedCoverageIncomplete)
}

func TestDeleteArchiveAllowsMatchingSeparateCoverage(t *testing.T) {
    guard := staticDerivedGuard{CoverageEventID: 100, SourceEpoch: "epoch-a", Ready: true}
    require.NoError(t, guard.ValidateDelete(context.Background(), archiveRunWithMaxRawEventID(100)))
}
```

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/repository/usagearchive ./internal/service/usage -run 'TestDeleteArchive.*Separate' -count=1
```
Expected: FAIL。

- [ ] **Step 3: 注入 guard 并保留原有 archive 证明。**

定义：

```go
type DerivedCoverageGuard interface {
    ValidateDelete(context.Context, usagearchive.Run) error
}
```

进入 `StatusDeleting` 前，guard 必须确认 source epoch、archive generation 与所有需服务 archive
历史的 stream coverage 覆盖该 run 的最大 raw event ID。guard 检查在主库 delete transaction
开始前完成；其结果只允许删除更早的已持久化 event，不允许放宽现有 `validateDeleteCoverage`。
独立派生不可用或不完整时返回 `ErrDerivedCoverageIncomplete` 并在 UI maintenance status 中显示
可操作原因；绝不删除原始数据。

- [ ] **Step 4: 运行 archive 回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/repository/usagearchive ./internal/service/usage -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/internal/repository/usagearchive apps/manager-server/internal/service/usage
git commit -m "feat: guard archive deletion with derived coverage"
```

### Task 7: 接线独立 Worker、状态端点与主库写入隔离

**Files:**
- Create: `apps/manager-server/internal/worker/separate_derived.go`
- Create: `apps/manager-server/internal/worker/separate_derived_test.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/httpapi/server.go`
- Modify: `apps/manager-server/internal/httpapi/server_compat_test.go`
- Modify: `apps/manager-server/internal/service/usage/maintenance.go`
- Modify: `apps/manager-server/internal/service/usage/maintenance_test.go`

- [ ] **Step 1: 写启动矩阵测试。**

```go
func TestSeparateModeStartsOnlySeparateWorker(t *testing.T) {
    app := newRuntimeForTest(t, config.DerivedStorageSeparate, true)
    require.True(t, app.SeparateDerivedWorkerStarted())
    require.False(t, app.LegacyPrimaryDerivedWorkersStarted())
}

func TestSeparateModePausedKeepsDerivedReadsAvailable(t *testing.T) {
    app := newRuntimeForTest(t, config.DerivedStorageSeparate, false)
    require.False(t, app.SeparateDerivedWorkerStarted())
    require.True(t, app.DerivedReaderAvailable())
}
```

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
cd apps/manager-server
go test ./internal/worker ./internal/httpapi -run 'TestSeparateMode|TestServerCompatStatusIncludes.*Derived' -count=1
```
Expected: FAIL。

- [ ] **Step 3: 实现 mode-exclusive 启动顺序。**

在 `main.go` 中：

```go
switch cfg.DerivedStorageMode {
case config.DerivedStoragePrimary:
    // Preserve current workers only when BackgroundDerivationsEnabled is true.
case config.DerivedStorageSeparate:
    // Open separate runtime; never call legacy RunDerivedStartupMaintenance,
    // StartDerivedMaintenance, or legacy rollup workers against primary DB.
    // Start SeparateDerivedWorker only when BackgroundDerivationsEnabled is true.
}
```

collector 仍先把事件写入主库；其 event fanout 只向独立 Worker 发送 `Wake()`，不传递会写主库的
旧 rollup handler。关闭时先停止 Worker，再关闭派生 runtime，最后关闭主 Store。所有 status 读取
只读派生 metadata，不扫描 event 历史。

`/status?scope=database-maintenance` 和 `/v0/management/usage/maintenance` 增加已脱敏的
`derived_storage` 字段：mode、worker 状态、每 stream coverage/target/lag/generation/last success、
派生库大小、最近 batch 耗时、退避计数。禁止返回绝对路径、secret、credential、proxy 或原始错误文本。

- [ ] **Step 4: 运行启动/API 回归。**

Run:
```powershell
cd apps/manager-server
go test ./internal/worker ./internal/httpapi ./internal/service/usage -count=1
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/manager-server/cmd/cpa-manager-plus apps/manager-server/internal/worker apps/manager-server/internal/app apps/manager-server/internal/httpapi apps/manager-server/internal/service/usage
git commit -m "feat: run derived workers outside primary SQLite"
```

### Task 8: 保持前端金额可见并呈现非阻塞状态

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/services/api/usageService.test.ts`
- Modify: `apps/web/src/features/usage/*`（仅实际维护状态组件）

- [ ] **Step 1: 写 UI 回归测试。**

```tsx
it('shows a verified cached amount while repricing is pending', async () => {
  renderAccountsWithHistory({ total_cost: 1.81, sync_status: 'stale_repricing' })
  expect(await screen.findByText('$1.81')).toBeVisible()
  expect(screen.getByText(/同步中|价格后台重算中/)).toBeVisible()
})

it('does not issue account-history catch_up on a normal visible-page refresh', async () => {
  await refreshVisiblePage()
  expect(lastAccountHistoryBody()).not.toMatchObject({ catch_up: true })
})
```

- [ ] **Step 2: 运行失败测试。**

Run:
```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/services/api/usageService.test.ts --testTimeout=20000
```
Expected: FAIL。

- [ ] **Step 3: 只改展示与请求契约。**

当后端返回非空 `total_cost` 时，金额始终显示；`fresh`、`stale_repricing`、`pending` 只影响小状态文案，
不能覆盖金额。普通首次加载、翻页、筛选、自动刷新、右上角刷新都继续遵守已确认的当前页目标范围，
不传 `catch_up=true`。不新增浏览器 localStorage、全量账户历史请求或价格轮询风暴。

- [ ] **Step 4: 运行前端验证。**

Run:
```powershell
npm --workspace apps/web run type-check
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx src/services/api/usageService.test.ts --testTimeout=20000
npm run build
```
Expected: PASS。

- [ ] **Step 5: 提交。**

```powershell
git add apps/web/src/features/accounts apps/web/src/services/api
git commit -m "fix: keep verified account prices visible during derivation"
```

### Task 9: 数据完整性、性能和压力验证

**Files:**
- Create: `apps/manager-server/internal/derivedstore/integration_test.go`
- Create: `apps/manager-server/internal/derivedstore/benchmark_test.go`
- Modify: `apps/manager-server/internal/service/monitoring/account_history_benchmark_test.go`
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [ ] **Step 1: 写强制完整性测试。**

测试必须覆盖：

```text
1. separate worker 运行时主库旧 rollup state 和旧 rollup 行不前进；
2. 原始 usage_events、identity ledger、Credits、quota、token recovery 和优先级队列完全不变；
3. 同一固定 event 集的 account-history、dashboard hourly、pricing totals 与 legacy 精确路径一致；
4. 价格 revision 切换不把已知金额变为 0；
5. archive segment 未覆盖时删除失败，完整覆盖后才允许；
6. source epoch 不同的 derived DB 永远不被读取；
7. Worker 崩溃/重启后从 committed coverage 继续，不重复累计；
8. 普通 HTTP account-history 请求不产生主库派生写入。
```

- [ ] **Step 2: 加入可重复性能基准。**

```go
func BenchmarkSeparateDerivedVisiblePageUnderBackfill(b *testing.B) {
    // seed 5,000 accounts and a fixed history fixture
    // run background separate engine concurrently
    // issue a 50-account visible-page snapshot read
    // report p50/p95 and assert no full-history target expansion
}
```

基准输出至少记录：主库 source-tail 行数、派生库批次耗时、当前页 `account-history` p50/p95、
主库 `BUSY/LOCKED` 数和派生退避数。基准不得访问 CPA Core。

- [ ] **Step 3: 运行针对性验证。**

Run:
```powershell
cd apps/manager-server
go test ./internal/derivedstore ./internal/store ./internal/service/monitoring ./internal/service/dashboard ./internal/service/usage ./internal/worker ./internal/httpapi -count=1
go test ./internal/derivedstore -run '^$' -bench BenchmarkSeparateDerivedVisiblePageUnderBackfill -benchtime=3x -count=1
```
Expected: PASS；基准显示可见页只读取 bounded tail。

- [ ] **Step 4: 提交。**

```powershell
git add apps/manager-server/internal/derivedstore apps/manager-server/internal/service/monitoring docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "test: verify isolated derivation integrity and page latency"
```

### Task 10: 完整构建、二改审计与本地定向部署

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Create: `docs/superpowers/deployments/2026-10-30-separate-derived-sqlite-rollout.md`

- [ ] **Step 1: 在干净隔离 worktree 审计。**

Run:
```powershell
git status --short
git diff --check
git log --oneline lo/custom..HEAD
rg -n 'USAGE_DERIVED_STORAGE_MODE|USAGE_DERIVED_DB_PATH|DerivedStorageSeparate' apps/manager-server docs/LO-CPAM-CUSTOM-MEMORY.md
```
Expected: 只有本功能提交；无 whitespace 错误；二改记忆包含独立派生库约束。

- [ ] **Step 2: 运行发布验证。**

Run:
```powershell
cd apps/manager-server
go test -count=1 ./internal/config ./internal/repository/sqlite ./internal/derivedstore ./internal/store ./internal/httpapi ./internal/worker ./internal/service/monitoring ./internal/service/dashboard ./internal/service/usage

docker run --rm --platform linux/amd64 `
  --mount type=bind,source=$PWD\..\..,target=/src `
  -w /src/apps/manager-server golang:1.24-alpine `
  sh -lc 'export PATH=/usr/local/go/bin:$PATH; go test ./... -count=1'

cd ..\..
npm ci
npm --workspace apps/web run type-check
npm run build
$env:TZ='Asia/Shanghai'; npm --workspace apps/web run test -- src --testTimeout=20000
```
Expected: PASS；若 Windows test glob 产生既有测试发现问题，记录其环境限制，但 Go/Linux、type-check、build 和目标前端测试必须成功。

- [ ] **Step 3: 合并到干净 `lo/custom` 并只推送 origin。**

```powershell
git -C <clean-lo-custom-worktree> status --short
git -C <clean-lo-custom-worktree> merge --ff-only <verified-branch>
git -C <clean-lo-custom-worktree> remote get-url --push origin
git -C <clean-lo-custom-worktree> remote get-url --push upstream
git -C <clean-lo-custom-worktree> push origin lo/custom
git -C <clean-lo-custom-worktree> ls-remote origin refs/heads/lo/custom
```
Expected: `origin` 是 LO 私有仓库；`upstream` push URL 为 `DISABLED`；绝不 push upstream。

- [ ] **Step 4: 构建候选 CPAM 镜像并记录 CPA Core 身份。**

Run from verified worktree:
```powershell
$tag = 'cpa-manager-plus:lo-custom-separate-derived-20261030-' + (git rev-parse --short HEAD)
docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cpa-manager-plus
docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cli-proxy-api
docker build --pull --platform linux/amd64 `
  --build-arg VERSION=('lo-custom-separate-derived-20261030-' + (git rev-parse --short HEAD)) `
  --build-arg SOURCE_COMMIT=(git rev-parse --short HEAD) `
  -t $tag -f Dockerfile.manager-server .
docker image inspect -f '{{.Id}}' $tag
```
Expected: 仅构建 CPAM 候选镜像，未重建 CPA Core。

- [ ] **Step 5: 本地 staged 配置与定向部署。**

先备份 `compose.yaml`，只为 `cpa-manager-plus` 写入：

```text
USAGE_DERIVED_STORAGE_MODE=separate
USAGE_DERIVED_DB_PATH=/data/usage-derived.sqlite
USAGE_DERIVED_SOURCE_BATCH_SIZE=500
USAGE_DERIVED_CYCLE_MAX_BATCHES=2
USAGE_DERIVED_YIELD_MS=100
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

第一次启动保持 Worker 暂停，只验证独立 DB 打开、前端读 fallback、主库不被重建。然后仅在验证后将
`USAGE_BACKGROUND_DERIVATIONS_ENABLED=true`，再次只重建 `cpa-manager-plus`，开始短批回填。

Run:
```powershell
function global:Read-Host { param([string]$Prompt); Write-Host "$Prompt [auto-confirmed]"; 'PROMOTE_CPAM' }
& .\deploy-cpa-manager-key-policy.ps1 -Promote -CandidateImage $tag
Invoke-WebRequest -UseBasicParsing -Uri http://127.0.0.1:18317/health -TimeoutSec 10
docker inspect -f '{{.Id}}|{{.State.StartedAt}}|{{.Image}}' cli-proxy-api
docker logs --tail 100 cpa-manager-plus
```
Expected: CPAM healthy；CPA Core 容器 ID/StartedAt 不变；原始 data volume 未被复制或迁移。

- [ ] **Step 6: 本地验收。**

使用 `/status?scope=database-maintenance` 确认 `storage_mode=separate`、Worker 状态、stream
coverage、target、lag 和 source epoch 匹配。对同一当前页 50 账户记录切换前/后的请求数、Token、
成功率、总金额；在独立 Worker 回填时反复刷新当前页，确认金额可见、页面 TTFB 不出现四分钟等待、
主库旧 rollup checkpoints 不前进。

- [ ] **Step 7: 提交部署文档。**

```powershell
git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/deployments/2026-10-30-separate-derived-sqlite-rollout.md
git commit -m "docs: record separate derived store rollout"
```

### Task 11: 服务器原生 CPAM staged 部署与可逆回滚

**Files:**
- Modify on server only: `/home/cpam/config/cpam.env`
- Create on server only: `/home/cpam/data/usage-derived.sqlite`（由 CPAM 首次启动创建）
- Modify on server only: `/home/cpam/DEPLOYMENT-MANIFEST`
- Preserve: `/home/cpam/data/usage.sqlite`, `/home/cpam/data/data.key`, `/home/cpam/secrets/*`, OpenResty、证书配置。

- [ ] **Step 1: 部署前冻结证据。**

通过 `LACPA-VPS` 记录：

```bash
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
systemctl is-active cpam.service
sha256sum /home/cpam/data/data.key
ls -l /home/cpam/data/usage.sqlite
cp -a /home/cpam/config/cpam.env /home/cpam/backups/config/$(date -u +%Y%m%dT%H%M%SZ)-before-separate-derived/cpam.env
```

Expected: `cpa.service` PID 被保存；不读取、不输出 secrets。

- [ ] **Step 2: 从本地已验证候选镜像提取同一 Linux amd64 二进制。**

```powershell
$cid = docker create $tag
docker cp "$cid`:/app/cpa-manager-plus" .\_deploy-artifacts\cpa-manager-plus
docker rm $cid
(Get-FileHash .\_deploy-artifacts\cpa-manager-plus -Algorithm SHA256).Hash
```

通过受控传输放入服务器 `/home/cpam/_incoming/`；不上传任何 `usage.sqlite`、`data.key`、volume、
secret 或认证文件。

- [ ] **Step 3: 首次服务器 staged 启动。**

原子替换仅 `/home/cpam/app/cpa-manager-plus`，随后在 `cpam.env` 添加以下行且保留其他现有二改变量：

```text
USAGE_DERIVED_STORAGE_MODE=separate
USAGE_DERIVED_DB_PATH=/home/cpam/data/usage-derived.sqlite
USAGE_DERIVED_SOURCE_BATCH_SIZE=500
USAGE_DERIVED_CYCLE_MAX_BATCHES=2
USAGE_DERIVED_YIELD_MS=100
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

只执行：

```bash
systemctl restart cpam.service
systemctl is-active cpam.service
curl -fsS http://127.0.0.1:18317/health
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
```

Expected: `cpam.service=active`、health 200、CPA PID 和部署前完全一致。不得执行
`systemctl restart cpa.service`，不得操作 `/home/cpa`。

- [ ] **Step 4: 服务器 baseline 验收后启用独立 Worker。**

确认 `/status?scope=database-maintenance` 显示 separate reader 已打开、源 epoch 匹配、没有 legacy
主库派生 Worker 后，将 **仅该一行** 改为：

```text
USAGE_BACKGROUND_DERIVATIONS_ENABLED=true
```

再次只重启 `cpam.service`。以 50 个当前页账户反复查看 `account-history`、价格、最近请求、额度窗口和
仪表盘；同时观察 maintenance status 的 coverage/lag，确认主库 `usage_events` 仍在增长但主库旧
pricing/history checkpoints 不因新 Worker 前进。

- [ ] **Step 5: 服务器回滚路径。**

若任何 stream 出现 source epoch 不匹配、金额不一致、archive coverage 被拒绝异常，先暂停 Worker：

```text
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

然后只重启 `cpam.service`。若需要恢复 legacy 读路径，再设置：

```text
USAGE_DERIVED_STORAGE_MODE=primary
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

并只重启 `cpam.service`。不恢复、不覆盖、不移动主 `usage.sqlite`；独立 DB 保留供诊断或以后重建。

- [ ] **Step 6: 最终服务器验收。**

```bash
systemctl is-active cpam.service
curl -fsS http://127.0.0.1:18317/health
curl -fsS https://rcpam.kyon666.top/health
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
systemctl is-active openresty-cpa.service
```

Expected: CPAM 内外 health 均 200；OpenResty/证书配置未改；CPA PID 不变；部署 manifest 记录候选
commit、binary SHA-256、部署时间和 `separate` 模式，不包含 secrets。

---

## Plan self-review

- **Spec coverage:** 主/派生数据分层、读写隔离、archive safety、价格 generation、当前页读取、状态可见性、
  回滚和双端部署均有对应 Task 1–11。
- **No placeholders:** 所有新配置、关键接口、测试条件、命令和部署环境值均已明确；没有留待补写的执行步骤。
- **Type consistency:** `DerivedStorageMode`、`Source`、`Sink`、`DerivedRuntime`、`DerivedCoverageGuard` 与
  Task 1–7 的职责一致；公共 Store 返回类型保持原有 `UsagePricingAccountSnapshot`/
  `UsageHourlyPricingSnapshot`。
- **Scope check:** 该工作仅处理 CPAM 的用量派生/读取架构；不触及 CPA Core、凭证操作或业务计费事实。