# Pricing Catch-up and Truthful Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 加快独立 pricing 派生追赶；在派生未完成时仍让当前页通过有界、只读的精准回退显示金额；并让状态接口报告主库真实 target 与 lag。

**Architecture:** `derivedstore.Engine` 在 pricing backlog 大于阈值时使用有界 burst，而主库仍是 query-only source、派生库仍是唯一 writer。snapshot 将完整 source target 传入 sink；Store 在 pricing 不完整时仅针对当前请求账号键走现有 source pricing 的 query-only 精准路径，并以 core/history 覆盖校验决定是否接受金额；无法证明的个别账号才走已有无价格 history totals 回退。

**Tech Stack:** Go 1.24、SQLite、CPAM Manager Server、React/TypeScript（仅现有响应渲染验证）。

---

### Task 1: 为 pricing 快车道定义配置并测试解析

**Files:**
- Modify: `apps/manager-server/internal/config/config.go`
- Modify: `apps/manager-server/internal/config/config_test.go`
- Modify: `apps/manager-server/internal/config/derived_storage_test.go`

- [ ] **Step 1: 写失败配置测试**

```go
func TestLoadSeparateDerivedPricingCatchupControls(t *testing.T) {
    clearConfigEnv(t)
    t.Setenv(configEnvKey, filepath.Join(t.TempDir(), "missing.json"))
    t.Setenv("USAGE_DERIVED_STORAGE_MODE", "separate")
    t.Setenv("USAGE_DERIVED_PRICING_CATCHUP_BATCH_SIZE", "2000")
    t.Setenv("USAGE_DERIVED_PRICING_CATCHUP_MAX_BATCHES", "8")
    t.Setenv("USAGE_DERIVED_PRICING_CATCHUP_LAG_EVENTS", "10000")
    cfg, err := LoadWithoutCreatingDefault()
    if err != nil { t.Fatal(err) }
    if cfg.DerivedPricingCatchupBatchSize != 2000 || cfg.DerivedPricingCatchupMaxBatches != 8 || cfg.DerivedPricingCatchupLagEvents != 10000 { t.Fatalf("...%#v", cfg) }
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/config -run TestLoadSeparateDerivedPricingCatchupControls -count=1`

Expected: FAIL，因为 Config 尚无对应字段。

- [ ] **Step 3: 最小实现配置字段与 clamp**

在 `Config`、JSON config、defaults 和 `LoadWithoutCreatingDefault` 增加三个字段；batch size 限制为 `50..5000`，burst 限制为 `1..8`，lag 阈值限制为 `1..10_000_000`，默认 `1000/4/10000`。

- [ ] **Step 4: 验证配置包**

Run: `go test ./internal/config -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add apps/manager-server/internal/config
git commit -m "feat: configure pricing catch-up burst"
```

### Task 2: 用完整 source target 持久化派生 stream 状态

**Files:**
- Modify: `apps/manager-server/internal/derivedstore/contracts.go`
- Modify: `apps/manager-server/internal/derivedstore/engine.go`
- Modify: `apps/manager-server/internal/derivedstore/sink.go`
- Modify: `apps/manager-server/internal/derivedstore/sink_test.go`

- [ ] **Step 1: 写失败 sink 测试**

```go
func TestSinkRetainsFullSourceTargetBeyondCommittedBatch(t *testing.T) {
    state, err := sink.Apply(ctx, ApplyBatch{Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "g", ToEventID: 500, SourceTargetEventID: 5_000})
    if err != nil { t.Fatal(err) }
    if state.CoverageEventID != 500 || state.TargetEventID != 5_000 { t.Fatalf("state=%#v", state) }
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/derivedstore -run TestSinkRetainsFullSourceTargetBeyondCommittedBatch -count=1`

Expected: FAIL，因为 ApplyBatch 尚无完整 source target 字段，且 sink 写的是 ToEventID。

- [ ] **Step 3: 最小实现**

给 `ApplyBatch` 增加 `SourceTargetEventID`；engine 从 snapshot 传入；sink 在插入、generation reset、普通 update 中更新 `target_event_id=max(existing, SourceTargetEventID)`，coverage 仍仅使用 `ToEventID`。空 source target 时为兼容旧调用回退 `ToEventID`。

- [ ] **Step 4: 验证 derivedstore**

Run: `go test ./internal/derivedstore -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add apps/manager-server/internal/derivedstore
git commit -m "fix: persist full derived source target"
```

### Task 3: 实现有界 pricing 优先 catch-up 调度

**Files:**
- Modify: `apps/manager-server/internal/derivedstore/engine.go`
- Modify: `apps/manager-server/internal/derivedstore/engine_test.go`
- Modify: `apps/manager-server/internal/store/derived_runtime.go`

- [ ] **Step 1: 写失败 engine 调度测试**

```go
func TestEnginePrioritizesPricingBurstWhileHistoryStaysServiced(t *testing.T) {
    engine := NewEngine(EngineConfig{Source: source, Sink: sink, BatchSize: 1, MaxBatches: 2, PricingCatchupBatchSize: 2, PricingCatchupMaxBatches: 3, PricingCatchupLagEvents: 2})
    _, err := engine.RunCycle(context.Background())
    if err != nil { t.Fatal(err) }
    if got := streamSequence(sink.applied); !slices.Equal(got, []Stream{StreamAccountHistory, StreamPricing, StreamPricing, StreamPricing}) { t.Fatalf("sequence=%v", got) }
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/derivedstore -run TestEnginePrioritizesPricingBurstWhileHistoryStaysServiced -count=1`

Expected: FAIL，当前 engine 严格 round-robin。

- [ ] **Step 3: 最小实现快车道**

扩展 `EngineConfig` 并将 Store 配置传入 engine。snapshot 后读取 pricing/history state；pricing epoch、generation、revision 不一致或真实 lag 超过阈值时：仅在 history 尚未覆盖 target 时先执行一个 normal history batch，然后以 pricing 专用 batch size 与 burst budget 调用 `processStream`。正常低 lag 情况保留原有 `nextStream` round-robin。

- [ ] **Step 4: 验证 engine 与 Store runtime**

Run: `go test ./internal/derivedstore ./internal/store -run 'TestEngine|TestSeparateDerived' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add apps/manager-server/internal/derivedstore apps/manager-server/internal/store/derived_runtime.go
git commit -m "perf: prioritize bounded pricing catch-up"
```

### Task 4: 价格不完整时保留当前页精准金额回退

**Files:**
- Modify: `apps/manager-server/internal/store/derived_runtime.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`
- Modify: `apps/manager-server/internal/service/monitoring/service_test.go`

- [ ] **Step 1: 写失败 Store/monitoring 测试**

```go
func TestSeparateDerivedSnapshotRecoversExactCurrentPagePricingWhenDerivedPricingLags(t *testing.T) {
    snapshot, err := st.LoadUsagePricingAccountSnapshot(ctx, []string{accountKey})
    if err != nil { t.Fatal(err) }
    if len(snapshot.Rows) != 1 || snapshot.Rows[0].TotalTokens != 30 { t.Fatalf("rows=%#v", snapshot.Rows) }
    if _, incomplete := snapshot.PricingIncompleteAccountKeys[accountKey]; incomplete { t.Fatal("complete current-page price was marked incomplete") }
}

func TestAccountHistoryKeepsExactCostWhileSeparatePricingIsPending(t *testing.T) {
    response, err := service.AccountHistory(ctx, request)
    if err != nil { t.Fatal(err) }
    item := response.Items[0]
    if !item.Matched || item.TotalRequests != 2 || item.TotalTokens != 30 || item.TotalCost == nil { t.Fatalf("item=%#v", item) }
}
```

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/store ./internal/service/monitoring -run 'TestSeparateDerivedSnapshotRecoversExactCurrentPagePricingWhenDerivedPricingLags|TestAccountHistoryKeepsExactCostWhileSeparatePricingIsPending' -count=1`

Expected: FAIL，因为只要 derived core 行已经存在，当前实现不会触发 source query-only pricing 回退，导致 price rows 缺失。

- [ ] **Step 3: 最小实现按页精准 source pricing 回退**

在 `separateDerivedRuntime` 提取一个只读事务 helper：先调用
`sourcePricing.LoadAccountRowsTx`，再用 `accountPricingCoverageIncompleteKeys` 对照本次 `CoreRows`；
只对仍不完整的传入账号键调用 `LoadAccountRowsFromRetainedEventsTx`，只保留通过覆盖验证的行。pricing
stream 未 ready 或派生行不完整时调用它，用完整 source rows 覆盖 current page 的 partial rows；其余
未验证账号才进入 `PricingIncompleteAccountKeys`。该 helper 只能使用 `runtime.sourceDB` 的
`ReadOnly + query_only=ON` 事务，禁止在 HTTP 路径扫描全账号或写主库。

- [ ] **Step 4: 验证 Store/monitoring**

Run: `go test ./internal/store ./internal/service/monitoring -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add apps/manager-server/internal/store/derived_runtime.go apps/manager-server/internal/store/derived_runtime_test.go apps/manager-server/internal/service/monitoring/service_test.go
git commit -m "fix: recover current-page pricing while catchup runs"
```

### Task 5: 状态接口显示 live primary target 与真实 lag

**Files:**
- Modify: `apps/manager-server/internal/store/derived_runtime.go`
- Modify: `apps/manager-server/internal/http/controller/system/handler.go`
- Modify: `apps/manager-server/internal/httpapi/server_compat_test.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`

- [ ] **Step 1: 写失败状态测试**

```go
func TestSeparateDerivedStatusUsesLivePrimaryTarget(t *testing.T) {
    status, err := st.SeparateDerivedStatus(ctx)
    if err != nil { t.Fatal(err) }
    pricing := findStream(status, derivedstore.StreamPricing)
    if pricing.TargetEventID != 10 || pricing.CoverageEventID != 2 { t.Fatalf("pricing=%#v", pricing) }
}
```

并在 HTTP compatibility test 中断言 `targetEventId=10`、`lagEventId=8`、`status="catching_up"`。

- [ ] **Step 2: 运行失败测试**

Run: `go test ./internal/store ./internal/httpapi -run 'TestSeparateDerivedStatusUsesLivePrimaryTarget|TestServerCompatStatusExposesSanitizedSeparateDerivedStatus' -count=1`

Expected: FAIL，因为当前 status 只回读 sink 的小批 target。

- [ ] **Step 3: 最小实现**

`SeparateDerivedStatus` 读取 sink 后再读取 runtime source snapshot，把每条 stream 的 target 覆盖为 live primary target；若 coverage 小于 target 并且原状态为 ready，在 handler 的公开副本中显示 `catching_up`。不泄露 DB path、event 内容、认证或代理数据。

- [ ] **Step 4: 验证 HTTP 状态接口**

Run: `go test ./internal/store ./internal/http/controller/system ./internal/httpapi -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add apps/manager-server/internal/store/derived_runtime.go apps/manager-server/internal/http/controller/system/handler.go apps/manager-server/internal/httpapi/server_compat_test.go apps/manager-server/internal/store/derived_runtime_test.go
git commit -m "fix: report live derived lag"
```

### Task 6: 记忆、验证与双端部署

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Modify: `docs/superpowers/deployments/2026-10-30-pricing-catchup-truthful-status.md`

- [ ] **Step 1: 更新二改记忆**

记录 pricing burst 环境变量、current-page query-only exact price fallback、live target status 和不变量，明确未来更新不得恢复伪 lag=0、不得让 HTTP 回退为全量 raw pricing scan、不得把可验证 current-page 金额降级成 `-`。

- [ ] **Step 2: 跑验证门槛**

Run:

```powershell
cd apps/manager-server
go test -count=1 ./internal/config ./internal/derivedstore ./internal/store ./internal/service/monitoring ./internal/http/controller/system ./internal/httpapi
cd ../..
npm --workspace apps/web run type-check
npm run build
```

Expected: PASS。

- [ ] **Step 3: 提交、推送与部署**

遵循 `C:\kyon\docker\cpa-manager\CPAM_UPDATE_RUNBOOK.md`：验证 remote push URL；只推送 `origin lo/custom`；从本 worktree 构建候选镜像；本地仅定向部署 `cpa-manager-plus`；将同一 amd64 二进制部署至服务器；仅 `systemctl restart cpam.service`。

- [ ] **Step 4: 部署后有界验收**

查询 `/status?scope=database-maintenance` 验证 pricing `targetEventId` 等于 live primary snapshot、lag 非零时不再为 0；对 50 个当前页账号执行一次 `account-history`，验证 pricing 未完成时可核验账号仍有精确金额，只有不可核验账号为 null；核对 `/health`、candidate SHA、服务器 `cpa.service` PID、本地 CPA Core 容器身份均未变化。

- [ ] **Step 5: 最终提交**

```bash
git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/deployments
git commit -m "docs: record pricing catch-up safeguards"
```
