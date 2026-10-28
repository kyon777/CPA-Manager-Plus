# Account History Price Cache + Background Exact Repricing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the current-page `account-history` response return the last known exact price immediately, while recalculating stale prices in a bounded background worker without changing billing source data.

**Architecture:** Add a small derived SQLite price-cache table keyed by stable account and pricing revision. The monitoring service reads completed pricing rows first, then the cache for incomplete accounts, and enqueues missing/stale keys without scanning raw events in the HTTP request. A CPAM-only worker performs the exact raw-event read in bounded batches, writes cache rows atomically, and the existing page refresh patches the visible cards.

**Tech Stack:** Go, `database/sql` with SQLite, existing CPAM rollup/repository packages, React/TypeScript/Vitest, existing Docker/native CPAM deployment runbook.

---

## Task 1: Add the derived price-cache schema

**Files:**
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Test: `apps/manager-server/internal/repository/sqlite/migrate_test.go`

- [ ] **Step 1: Write the migration regression test**

Add `TestMigrateCreatesAccountHistoryPriceCacheSchema` next to the existing derived-schema tests. Open a fresh test DB, call `Migrate(db)`, query `pragma_table_info('usage_pricing_account_price_cache_v1')`, and assert these columns exist with the stated types:

```go
want := map[string]string{
    "account_key": "TEXT",
    "pricing_revision": "TEXT",
    "covered_event_id": "INTEGER",
    "total_cost": "REAL",
    "updated_at_ms": "INTEGER",
    "status": "TEXT",
    "last_error": "TEXT",
}
```

Also query `sqlite_master` and assert indexes named `idx_usage_pricing_account_price_cache_account` and `idx_usage_pricing_account_price_cache_revision` exist. Call `Migrate(db)` a second time and assert it remains successful and the row count in `sqlite_master` does not increase.

- [ ] **Step 2: Run the new test and verify it fails**

Run:

```powershell
go test ./apps/manager-server/internal/repository/sqlite -run '^TestMigrateCreatesAccountHistoryPriceCacheSchema$' -count=1
```

Expected: FAIL because the cache table and indexes do not yet exist.

- [ ] **Step 3: Add the idempotent table and indexes**

In `migrate.go`, add the constant `usagePricingAccountPriceCacheTable` and run these statements inside the existing migration transaction:

```sql
create table if not exists usage_pricing_account_price_cache_v1 (
    account_key text not null,
    pricing_revision text not null,
    covered_event_id integer not null default 0,
    total_cost real not null,
    updated_at_ms integer not null,
    status text not null default 'fresh',
    last_error text not null default '',
    primary key (account_key, pricing_revision),
    check (status in ('fresh', 'stale', 'failed'))
);
create index if not exists idx_usage_pricing_account_price_cache_account
    on usage_pricing_account_price_cache_v1(account_key, updated_at_ms desc);
create index if not exists idx_usage_pricing_account_price_cache_revision
    on usage_pricing_account_price_cache_v1(pricing_revision, covered_event_id);
```

Keep this migration DDL-only; it must not scan `usage_events` or block startup on a rebuild.

- [ ] **Step 4: Run the migration test and the existing SQLite tests**

Run:

```powershell
go test ./apps/manager-server/internal/repository/sqlite -run 'TestMigrateCreatesAccountHistoryPriceCacheSchema|TestMigrate' -count=1
```

Expected: PASS, including the repeat-migration assertion.

- [ ] **Step 5: Commit the schema change**

```powershell
git add apps/manager-server/internal/repository/sqlite/migrate.go apps/manager-server/internal/repository/sqlite/migrate_test.go
git commit -m "feat: add derived account price cache schema"
```

## Task 2: Implement cache repository reads and atomic writes

**Files:**
- Modify: `apps/manager-server/internal/repository/usagepricing/repository.go`
- Create: `apps/manager-server/internal/repository/usagepricing/price_cache.go`
- Test: `apps/manager-server/internal/repository/usagepricing/price_cache_test.go`

- [ ] **Step 1: Define the cache row and repository contract**

Add:

```go
type PriceCacheRow struct {
    AccountKey       string
    PricingRevision  string
    CoveredEventID   int64
    TotalCost        float64
    UpdatedAtMS      int64
    Status           string
    LastError        string
}
```

Extend the `usagepricing.Repository` interface with:

```go
LoadAccountPriceCacheTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]PriceCacheRow, error)
UpsertAccountPriceCache(ctx context.Context, rows []PriceCacheRow) error
MarkAccountPriceCacheFailure(ctx context.Context, accountKeys []string, revision string, message string, nowMS int64) error
```

- [ ] **Step 2: Write failing repository tests**

Create fixtures with two revisions for one account and one row for another. Assert `LoadAccountPriceCacheTx` returns the newest `updated_at_ms` row per account, `UpsertAccountPriceCache` replaces the same `(account_key, pricing_revision)` atomically, and failure marking changes only `status` and `last_error` while preserving `total_cost`.

- [ ] **Step 3: Implement set-based reads and writes**

Use one `IN (...)` query for reads; never issue one SQL query per account. The read query must select the newest row per account with a window function:

```sql
select account_key, pricing_revision, covered_event_id, total_cost,
       updated_at_ms, status, last_error
from (
  select c.*, row_number() over (
    partition by account_key order by updated_at_ms desc
  ) as rn
  from usage_pricing_account_price_cache_v1 c
  where account_key in (...)
)
where rn = 1
```

Writes use one transaction and `insert ... on conflict(account_key, pricing_revision) do update`, preserving the last known cost on failure.

- [ ] **Step 4: Run repository tests**

```powershell
go test ./apps/manager-server/internal/repository/usagepricing -run 'TestAccountPriceCache' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the repository layer**

```powershell
git add apps/manager-server/internal/repository/usagepricing
git commit -m "feat: add account price cache repository"
```

## Task 3: Make the store snapshot path fast and cache-aware

**Files:**
- Modify: `apps/manager-server/internal/store/store.go`
- Modify: `apps/manager-server/internal/store/usage_pricing_coverage.go`
- Test: `apps/manager-server/internal/store/store_test.go`

- [ ] **Step 1: Add snapshot metadata types and a failing fallback test**

Extend `UsagePricingAccountSnapshot` with:

```go
PriceCache map[string]usagepricing.PriceCacheRow
PriceRevision string
PriceTargetEventID int64
```

Create a test that places the pricing repository in `rebuilding` state, inserts an old cache row, and makes the current rollup incomplete. Assert `LoadUsagePricingAccountSnapshot` returns under the test context deadline, returns the old cache row, and does not call the raw-event recovery path.

- [ ] **Step 2: Remove synchronous raw-tail recovery from the HTTP snapshot path**

In `LoadUsagePricingAccountSnapshot`, retain the set-based reads of core rows, current pricing rows, model prices, and cache rows. Delete the call to `LoadAccountRowsFromEventsTx` from this request path. Compute incomplete account keys with the existing coverage helper and return them as metadata for the service/worker. Do not change `CatchUpUsagePricing` or the normal background rollup worker.

- [ ] **Step 3: Preserve existing exact rows and cache fallback semantics**

Use current-revision pricing rows for accounts whose coverage matches. For an incomplete account, leave the core totals available and let the monitoring service substitute the newest cache cost. If no cache row exists, return a nil cost state; never synthesize zero.

- [ ] **Step 4: Run store and monitoring regression tests**

```powershell
go test ./apps/manager-server/internal/store ./apps/manager-server/internal/service/monitoring -run 'TestAccountHistory|Test.*Pricing.*Recovery|Test.*Price.*Cache' -count=1 -timeout=10m
```

Expected: PASS, with no raw-event scan in the rebuilding fallback test.

- [ ] **Step 5: Commit the store change**

```powershell
git add apps/manager-server/internal/store
git commit -m "perf: serve account prices from derived cache during rebuild"
```

## Task 4: Add the bounded background exact-repricing worker

**Files:**
- Create: `apps/manager-server/internal/worker/account_price_cache.go`
- Test: `apps/manager-server/internal/worker/account_price_cache_test.go`
- Modify: `apps/manager-server/internal/store/store.go`

- [ ] **Step 1: Define the queue contract and write failing worker tests**

Define the queue contract in `apps/manager-server/internal/service/monitoring/service.go` (not in `worker`, to avoid a Go import cycle):

```go
type AccountPriceCacheEnqueuer interface {
    EnqueueAccountPriceRecalculation(accountKeys []string)
}
```

The worker implements this interface structurally; the monitoring package does not import `worker`. Tests must prove duplicate keys coalesce, queue capacity is bounded, a batch is processed with one exact read, and a failed batch leaves the previous cached cost intact while recording `status='failed'`.

- [ ] **Step 2: Implement the deduplicated queue**

Use a mutex-protected map keyed by `account_key + "\\x00" + pricing_revision + "\\x00" + target_event_id`, a buffered wake channel, and a fixed worker concurrency of 1. Batch no more than 25 account keys per read and cap pending keys at 5,000; duplicate enqueue attempts must not grow the queue.

- [ ] **Step 3: Implement exact background recalculation**

The worker obtains the current pricing structure revision and latest event ID, opens a read transaction, calls the existing exact `LoadAccountRowsFromEventsTx` only from the worker, computes each account's cost with `pricing.CostForModelCandidatesWithServiceTier`, and writes cache rows in one short write transaction. Retry SQLite busy/locked errors twice with 250ms/1s backoff. The worker must never update `usage_events`, Credits, or CPA Core state.

- [ ] **Step 4: Add worker lifecycle integration tests**

Start the worker with a cancellable context, enqueue the same 25 keys three times, wait for one completion signal, and assert exactly one cache revision was written per key. Cancel the context and assert the goroutine exits.

- [ ] **Step 5: Commit the worker**

```powershell
git add apps/manager-server/internal/worker apps/manager-server/internal/store/store.go
git commit -m "feat: asynchronously recalculate account price cache"
```

## Task 5: Wire monitoring responses to the cache and queue

**Files:**
- Modify: `apps/manager-server/internal/service/monitoring/service.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`
- Test: `apps/manager-server/internal/service/monitoring/service_test.go`

- [ ] **Step 1: Add price-state fields and an enqueue setter**

Extend `AccountHistoryItem` with:

```go
PriceState          string `json:"price_state"`
PriceRevision       string `json:"price_revision,omitempty"`
PriceCoveredEventID int64  `json:"price_covered_event_id,omitempty"`
PriceUpdatedAtMS    *int64 `json:"price_updated_at_ms,omitempty"`
```

Add the `AccountPriceCacheEnqueuer` interface and a small interface field to `monitoring.Service`, plus `SetAccountPriceCacheEnqueuer(enqueuer AccountPriceCacheEnqueuer)`; keep the constructor signature backward-compatible.

- [ ] **Step 2: Write service tests for fresh, stale, and unavailable prices**

Use a fake enqueuer and fixtures for three accounts:

1. current pricing coverage → `price_state=fresh` and current cost;
2. incomplete coverage with old cache → old cost and `price_state=stale_repricing`, with one enqueue;
3. incomplete coverage without cache → `total_cost=null` and `price_state=unavailable`, with one enqueue.

Assert repeated requests do not synchronously scan raw events and duplicate keys are passed only once per request.

- [ ] **Step 3: Implement response assembly**

Build request/token totals from the existing core/pricing rows. For each account, choose the current exact cost when complete; otherwise choose the newest cache row and expose its state/metadata. Enqueue only incomplete or missing accounts after the read snapshot has been assembled. Keep `sync_status` semantics unchanged: it still reports the usage checkpoint, not price freshness.

- [ ] **Step 4: Wire the worker without touching CPA Core**

Create `accountPriceCacheWorker := worker.NewAccountPriceCacheWorker(db)` in `apps/manager-server/cmd/cpa-manager-plus/main.go` only when `BackgroundDerivationsEnabled` is true, call `serverApp.AppContext().MonitoringService.SetAccountPriceCacheEnqueuer(...)`, and start it beside the existing CPAM derived workers. Do not modify the CPA collector, CPA service, or upstream URL.

- [ ] **Step 5: Run service tests**

```powershell
go test ./apps/manager-server/internal/service/monitoring ./apps/manager-server/internal/worker -run 'Test.*Price|TestAccountHistory' -count=1 -timeout=10m
```

Expected: PASS.

- [ ] **Step 6: Commit service integration**

```powershell
git add apps/manager-server/internal/service/monitoring apps/manager-server/internal/app/context.go apps/manager-server/cmd/cpa-manager-plus/main.go
git commit -m "feat: expose cached account price states"
```

## Task 6: Update the web API model and visible-card rendering

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/model/accountHistoryRows.ts`
- Test: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Extend the TypeScript response type**

Add the four optional/required price fields to `MonitoringAccountHistoryItem`, matching the JSON names and the Go types.

- [ ] **Step 2: Add a focused rendering test**

Mock an account-history item with `total_cost=2.50`, `price_state='stale_repricing'`, and `sync_status='pending'`. Assert the card renders `$2.50`, a non-blocking `price recalculation pending` marker, and does not replace the price with the existing generic history spinner. Add a second fixture with `price_state='unavailable'` and assert the cost placeholder is shown.

- [ ] **Step 3: Render price state without increasing request fan-out**

Keep `buildAccountHistoryTargetBatches` and the current-page target set. Add a small state label beside the cost metric; do not add per-account requests or change the existing recent-index polling. The existing account-history refresh will patch the visible rows when the worker completes.

- [ ] **Step 4: Run web tests and type checks**

```powershell
pnpm --dir apps/web test --runInBand src/features/accounts/AccountsPage.test.tsx
pnpm --dir apps/web typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit the web change**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/model/accountHistoryRows.ts apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "feat: render cached account price state"
```

## Task 7: End-to-end verification and deployment candidate

**Files:**
- Modify: `CPAM_UPDATE_RUNBOOK.md` only if the new derived table or health check needs documentation
- Test artifacts: `_diagnostics/price-cache-*`

- [ ] **Step 1: Run focused Go tests**

```powershell
go test ./apps/manager-server/internal/repository/sqlite ./apps/manager-server/internal/repository/usagepricing ./apps/manager-server/internal/store ./apps/manager-server/internal/service/monitoring ./apps/manager-server/internal/worker -count=1 -timeout=15m
```

- [ ] **Step 2: Run the web regression suite**

```powershell
pnpm --dir apps/web test --runInBand src/features/accounts/AccountsPage.test.tsx
pnpm --dir apps/web typecheck
```

- [ ] **Step 3: Run a 50-account performance probe**

Measure TTFB and total time for `POST /v0/management/monitoring/account-history` while pricing state is rebuilding. Record that the response returns cached costs without raw-tail recovery and that the worker later changes matching cache rows to `fresh`. Compare against the pre-change ~39 second probe.

- [ ] **Step 4: Build the CPAM-only candidate**

Use the existing isolated worktree/build scripts to build only `cpa-manager-plus`. Before deployment record the local CPA Core container ID and the server `cpa.service` PID; after deployment assert both are unchanged.

- [ ] **Step 5: Deploy local Docker CPAM and native `/home/cpam` CPAM**

Follow `CPAM_UPDATE_RUNBOOK.md`: preserve `/home/cpam/config/cpam.env`, `/home/cpam/secrets/*`, `/home/cpam/data/*`, `usage.sqlite`, `data.key`, and OpenResty; restart only `cpam.service` on the server. Do not touch `/home/cpa`, `cpa.service`, or `cli-proxy-api`.

- [ ] **Step 6: Verify runtime and billing invariants**

Check health, cache table existence, account-history latency, non-null cached prices, unchanged CPA Core identity, unchanged raw `usage_events` count, and unchanged billing/usage totals for a fixed sample of accounts. Save command outputs under `_diagnostics/price-cache-<timestamp>/`.

- [ ] **Step 7: Commit deployment documentation and push only `origin/lo/custom`**

Do not push upstream. Update the二改 memory/runbook with the new price-cache invariant, commit it, push `lo/custom`, and verify the remote SHA.

## Self-review checklist

- The plan covers schema, read path, worker, service/API contract, UI, tests, performance validation, and the required dual CPAM deployment.
- No task changes CPA Core or `cli-proxy-api`.
- No task rewrites raw usage events or billing source data.
- Every stale/failure path preserves the last known cost and never substitutes zero.
- The current-page history target remains bounded; global recent-request sorting stays on its separate index.

