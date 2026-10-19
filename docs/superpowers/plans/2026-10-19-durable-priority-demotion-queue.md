# Durable Priority Demotion Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace CPAM's lossy server-error priority-demotion channel with a durable SQLite queue that preserves one eligible event as one priority mutation, uses eight controlled workers, and leaves CPA Core billing, usage, quota, and history behavior untouched.

**Architecture:** A pure event-classification package is shared by live collector fanout and a cursor reconciler. A SQLite repository owns bootstrap floors, enabled intervals, admissions, leases, FIFO claims, retries, and retention. The worker only resolves CPA setup, acquires the existing credential mutation lock, calculates the existing peer-priority target, patches CPA, and settles its lease. The in-memory wake buffer is notification-only: a full buffer may coalesce wake signals but must never discard a durable job.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite`), existing CPAM Store/repository layer, existing CPA management `cpaauthfiles.Client`, Go `testing` and `httptest`.

---

## Scope and invariants

- Only CPAM changes. CPA Core, `cli-proxy-api`, `/home/cpa`, `cpa.service`, and CPA Core deployment are out of scope.
- The queue never writes `usage_events`, pricing, quota snapshots, historical/dashboard rollups, or imports. It only reads minimal `usage_events` columns to create redacted jobs.
- One qualifying `event_hash` maps to one durable job. Ten different events for one credential remain ten ordered jobs, never a merged `-10` mutation.
- Existing eligibility remains exact: HTTP 502; HTTP 503; HTTP 429 only when JSON `detail == "Rate limit exceeded"`; HTTP 500 only for the two approved Codex Responses transport strings (`unexpected EOF`, and exact `utls ... 407 Proxy Authentication Required`).
- Existing priority calculation remains exact: exclude target; if highest enabled same-provider peer is lower than current, use `peer_max - 1`; otherwise `current - 1`; clamp to zero.
- Queue records contain no base URL, management key, token, whole credential JSON, proxy credential, or raw failure body.
- Bootstrap records the current maximum `usage_events.id` before HTTP listener and collector startup. IDs at/below that floor are never replayed.
- Enabled intervals preserve the panel-switch contract: events created while disabled never become jobs after re-enable. Jobs admitted before disabling stay durable and pause.

## File map

| Path | Responsibility |
| --- | --- |
| `internal/servererrorpriority/event.go` | Shared pure eligibility, redacted input, credential-key normalization. |
| `internal/servererrorpriority/event_test.go` | Exact 502/503/429/500 and identity tests. |
| `internal/model/server_error_priority_demotion.go` | Job, state, cursor, interval, and stats model values. |
| `internal/repository/servererrorpriority/repository.go` | Bootstrap, interval, admission, reconciliation, lease, claim, settlement, cleanup, stats. |
| `internal/repository/servererrorpriority/repository_test.go` | SQLite lifecycle and migration tests. |
| `internal/repository/sqlite/migrate.go` | Additive queue tables/indexes only. |
| `internal/store/store.go` | New queue repository construction and exposure. |
| `internal/config/config.go`, `_test.go` | Worker/wake-buffer environment configuration. |
| `internal/worker/server_error_priority_demotion.go`, `_test.go` | Durable executor, CPA idempotence, inventory single-flight cache. |
| `internal/worker/automation_runtime.go`, `_test.go` | Persistent enable-switch synchronization. |
| `cmd/cpa-manager-plus/main.go` | Queue bootstrap before listener/collector. |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | Future-update preservation contract. |

### Task 1: Extract one shared error-event classifier

**Files:**
- Create: `apps/manager-server/internal/servererrorpriority/event.go`
- Create: `apps/manager-server/internal/servererrorpriority/event_test.go`
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion.go`
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion_test.go`

- [ ] **Step 1: Write failing classifier tests.**

```go
func TestClassifyAcceptsOnlyApprovedErrorEvents(t *testing.T) {
    base := usage.Event{Failed: true, EventHash: "evt", AuthFileSnapshot: "a.json", AuthIndex: "idx", Provider: "codex", AccountSnapshot: "a@example.com"}
    cases := []struct{ name string; code int; body, want string }{
        {"502", http.StatusBadGateway, "", QualificationHTTP502},
        {"503", http.StatusServiceUnavailable, "", QualificationHTTP503},
        {"429", http.StatusTooManyRequests, `{"detail":"Rate limit exceeded"}`, QualificationRateLimit429},
        {"500 eof", http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/codex/responses": unexpected EOF`, QualificationCodexTransport500},
        {"500 proxy", http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/codex/responses": utls: dial upstream: proxy CONNECT returned status 407 Proxy Authentication Required`, QualificationCodexTransport500},
    }
    for _, tc := range cases { t.Run(tc.name, func(t *testing.T) { event := base; event.FailStatusCode, event.FailBody = tc.code, tc.body; got, ok := Classify(event); if !ok || got.QualificationCode != tc.want { t.Fatalf("Classify = %#v, %t", got, ok) } }) }
}

func TestClassifyRejectsGeneric429And500(t *testing.T) {
    base := usage.Event{Failed: true, AuthFileSnapshot: "a.json", AuthIndex: "idx", Provider: "codex"}
    cases := []usage.Event{
        withFailure(base, http.StatusTooManyRequests, ""),
        withFailure(base, http.StatusTooManyRequests, `{"detail":"different"}`),
        withFailure(base, http.StatusTooManyRequests, "Rate limit exceeded"),
        withFailure(base, http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/conversations": unexpected EOF`),
        withFailure(base, http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/codex/responses": connection reset by peer`),
    }
    for _, event := range cases { if _, ok := Classify(event); ok { t.Fatalf("unexpected qualified event: %#v", event) } }
}

func TestClassifyMarksEligibleEventWithoutStableCredentialIdentityIgnored(t *testing.T) {
    input, ok := Classify(usage.Event{Failed: true, FailStatusCode: http.StatusBadGateway, EventHash: "evt"})
    if !ok || input.IgnoredReason != "credential_identity_missing" { t.Fatalf("input=%#v ok=%t", input, ok) }
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/servererrorpriority -run TestClassify -count=1
```

Expected: compile failure because the package/API does not exist.

- [ ] **Step 3: Implement the minimal pure API.**

```go
type Input struct {
    EventHash, FileName, AuthIndex, Provider string
    AccountSnapshot, AccountIDSnapshot string
    CredentialKey, QualificationCode, IgnoredReason string
    StatusCode int
    EventAtMS int64
}

func Classify(event usage.Event) (Input, bool) {
    // Strict status/body qualification and normalized snapshots.
    // Missing stable identity returns an eligible input with IgnoredReason.
    // Nonmatching failures return false.
}
```

Move old worker predicates to forwarding wrappers only where an old unit test needs the unexported name. Do not leave duplicated qualification logic.

- [ ] **Step 4: Verify GREEN.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/servererrorpriority ./internal/worker -run 'Test(Classify|ServerErrorPriorityDemotionCandidateAccepts)' -count=1
```

Expected: PASS; approved old matching behavior is unchanged.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/servererrorpriority apps/manager-server/internal/worker/server_error_priority_demotion.go apps/manager-server/internal/worker/server_error_priority_demotion_test.go
git commit -m "refactor: share priority demotion qualification"
```

### Task 2: Add only additive SQLite schema and redacted model types

**Files:**
- Create: `apps/manager-server/internal/model/server_error_priority_demotion.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Create: `apps/manager-server/internal/repository/servererrorpriority/repository_test.go`

- [ ] **Step 1: Write a migration regression test before schema changes.**

```go
func TestMigrationCreatesPriorityQueueWithoutChangingUsageEvents(t *testing.T) {
    db := openTestDB(t)
    mustExec(t, db, `insert into usage_events (event_hash,timestamp_ms,timestamp,model,created_at_ms) values ('existing',1,'1970-01-01T00:00:00Z','m',1)`)
    must(t, sqlite.Migrate(db))
    assertCount(t, db, "usage_events", 1)
    for _, table := range []string{"server_error_priority_demotion_jobs", "server_error_priority_demotion_cursor", "server_error_priority_demotion_enabled_intervals"} { assertTableExists(t, db, table) }
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority -run TestMigrationCreatesPriorityQueueWithoutChangingUsageEvents -count=1
```

Expected: FAIL because queue tables do not exist.

- [ ] **Step 3: Add model states and additive migration DDL.**

```go
const (
    ServerErrorPriorityDemotionPending   = "pending"
    ServerErrorPriorityDemotionLeased    = "leased"
    ServerErrorPriorityDemotionRetryWait = "retry_wait"
    ServerErrorPriorityDemotionCompleted = "completed"
    ServerErrorPriorityDemotionFailed    = "failed"
    ServerErrorPriorityDemotionIgnored   = "ignored"
)
```

Add only the three following schema objects, then indexes on `(state,available_at_ms,id)`, `(credential_key,id)`, and `(completed_at_ms,state)`:

```sql
create table if not exists server_error_priority_demotion_jobs (
  id integer primary key autoincrement,
  event_hash text not null unique, source_event_id integer not null,
  credential_key text not null, file_name text not null,
  auth_index text not null, provider text not null,
  account_snapshot text not null default '', account_id_snapshot text not null default '',
  status_code integer not null, qualification_code text not null, event_at_ms integer not null,
  state text not null, available_at_ms integer not null,
  lease_token text, lease_until_ms integer, attempt_count integer not null default 0,
  priority_before integer, priority_target integer,
  last_error_code text, last_error text, completed_reason text,
  created_at_ms integer not null, updated_at_ms integer not null, completed_at_ms integer
);
create table if not exists server_error_priority_demotion_cursor (
  id integer primary key check(id = 1), source_floor_event_id integer not null,
  scan_cursor_event_id integer not null, updated_at_ms integer not null
);
create table if not exists server_error_priority_demotion_enabled_intervals (
  id integer primary key autoincrement, start_event_id integer not null,
  end_event_id integer, created_at_ms integer not null, closed_at_ms integer
);
create unique index if not exists idx_server_error_priority_demotion_active_interval
  on server_error_priority_demotion_enabled_intervals(end_event_id) where end_event_id is null;
```

Do not alter, rebuild, delete, or reindex `usage_events`; no queue foreign key should block source-event archiving.

- [ ] **Step 4: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority -run TestMigrationCreatesPriorityQueueWithoutChangingUsageEvents -count=1
```

```powershell
git add apps/manager-server/internal/model/server_error_priority_demotion.go apps/manager-server/internal/repository/sqlite/migrate.go apps/manager-server/internal/repository/servererrorpriority/repository_test.go
git commit -m "feat: add priority demotion queue schema"
```

### Task 3: Implement bootstrap, enabled intervals, durable admission, and reconciliation

**Files:**
- Create: `apps/manager-server/internal/repository/servererrorpriority/repository.go`
- Modify: `apps/manager-server/internal/repository/servererrorpriority/repository_test.go`
- Modify: `apps/manager-server/internal/store/store.go`

- [ ] **Step 1: Write failing bootstrap/interval/admission tests.**

```go
func TestBootstrapDoesNotReplayEventsBeforeFloor(t *testing.T) {
    repo, usageRepo := newQueueRepository(t)
    insertUsageEvent(t, usageRepo, eligible502("old"))
    must(t, repo.Bootstrap(ctx, true, 100))
    _, err := repo.Reconcile(ctx, 100, 101); must(t, err)
    assertJobs(t, repo)
}

func TestDisabledIntervalIsNeverBackfilledAfterReenable(t *testing.T) {
    repo, usageRepo := newQueueRepository(t)
    must(t, repo.Bootstrap(ctx, true, 1))
    insertUsageEvent(t, usageRepo, eligible502("enabled-one"))
    must(t, repo.SetEnabled(ctx, false, 2))
    insertUsageEvent(t, usageRepo, eligible502("disabled"))
    must(t, repo.SetEnabled(ctx, true, 3))
    insertUsageEvent(t, usageRepo, eligible502("enabled-two"))
    _, err := repo.Reconcile(ctx, 100, 4); must(t, err)
    assertJobHashes(t, repo, "enabled-one", "enabled-two")
}

func TestEnqueueKeepsTenUniqueEventsAsTenJobs(t *testing.T) {
    repo, usageRepo := newQueueRepository(t)
    must(t, repo.Bootstrap(ctx, true, 1))
    events := make([]usage.Event, 0, 10)
    for n := 0; n < 10; n++ { event := eligible502(fmt.Sprintf("event-%d", n)); events = append(events, event); insertUsageEvent(t, usageRepo, event) }
    _, err := repo.EnqueueEvents(ctx, events, 2); must(t, err)
    _, err = repo.EnqueueEvents(ctx, events, 3); must(t, err)
    assertJobCount(t, repo, 10)
    assertDistinctJobIDs(t, repo, 10)
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority -run 'Test(Bootstrap|DisabledInterval|Enqueue)' -count=1
```

Expected: compile failure for `New`, `Bootstrap`, `SetEnabled`, `Reconcile`, and `EnqueueEvents`.

- [ ] **Step 3: Implement the transactional repository contract.**

```go
type Repository interface {
    Bootstrap(context.Context, bool, int64) error
    SetEnabled(context.Context, bool, int64) error
    EnqueueEvents(context.Context, []usage.Event, int64) (model.ServerErrorPriorityDemotionEnqueueResult, error)
    Reconcile(context.Context, int, int64) (model.ServerErrorPriorityDemotionReconcileResult, error)
    RecoverExpiredLeases(context.Context, int64) (int64, error)
    ClaimNext(context.Context, int64, int64, string) (model.ServerErrorPriorityDemotionJob, bool, error)
    RecordPriorityPlan(context.Context, int64, string, int, int, int64) error
    Complete(context.Context, int64, string, string, int64) error
    Retry(context.Context, int64, string, string, string, int64, int64) error
    Terminal(context.Context, int64, string, string, string, string, string, int64) error
    CleanupTerminal(context.Context, int64) (int64, error)
    Stats(context.Context, int64) (model.ServerErrorPriorityDemotionStats, error)
}
```

`Bootstrap` inserts the cursor once with both values equal to `max(usage_events.id)`, then opens/closes the active interval from the supplied effective setting. `SetEnabled(true)` opens at `max(id)+1`; `SetEnabled(false)` closes at current max ID. The scan cursor is deliberately not advanced on disable: reconciliation still records the exact closed enabled interval and skips disabled rows transactionally, while claiming remains paused. Both transitions operate in one SQLite transaction.

`EnqueueEvents` resolves each source ID by hash, verifies source floor and active interval, uses Task 1's pure classifier, and `INSERT OR IGNORE`s by hash. A matching error with no stable identity is a terminal `ignored` job with bounded `credential_identity_missing`; a nonmatching failure creates no job. `Reconcile` scans minimal source columns in ascending fixed batches, uses the same classifier/interval check, inserts idempotently, and moves the scan cursor in the same transaction.

Expose it without changing existing semantics:

```go
type Store struct {
    // existing fields unchanged
    ServerErrorPriorityDemotions servererrorpriorityrepo.Repository
}
```

- [ ] **Step 4: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority ./internal/store -count=1
```

```powershell
git add apps/manager-server/internal/repository/servererrorpriority apps/manager-server/internal/store/store.go
git commit -m "feat: persist priority demotion admissions"
```

### Task 4: Implement lease recovery, FIFO claims, settlement, and retention

**Files:**
- Modify: `apps/manager-server/internal/repository/servererrorpriority/repository.go`
- Modify: `apps/manager-server/internal/repository/servererrorpriority/repository_test.go`

- [ ] **Step 1: Write failing queue-lifecycle tests.**

```go
func TestClaimSerializesSameCredentialAndAllowsDifferentCredential(t *testing.T) {
    repo := seededQueue(t, "a-1", "a", "a-2", "a", "b-1", "b")
    first, ok, err := repo.ClaimNext(ctx, 100, 200, "lease-a")
    if err != nil || !ok || first.EventHash != "a-1" { t.Fatal(first, ok, err) }
    second, ok, err := repo.ClaimNext(ctx, 100, 200, "lease-b")
    if err != nil || !ok || second.EventHash != "b-1" { t.Fatal(second, ok, err) }
}

func TestExpiredLeaseCanBeClaimedAgain(t *testing.T) {
    repo := seededQueue(t, "event", "credential")
    first, ok, err := repo.ClaimNext(ctx, 100, 200, "first"); if err != nil || !ok { t.Fatal(first, ok, err) }
    recovered, err := repo.RecoverExpiredLeases(ctx, 201); if err != nil || recovered != 1 { t.Fatal(recovered, err) }
    second, ok, err := repo.ClaimNext(ctx, 201, 301, "second")
    if err != nil || !ok || second.ID != first.ID || second.LeaseToken != "second" { t.Fatal(second, ok, err) }
}

func TestCleanupDeletesOnlyOldCompletedAndIgnored(t *testing.T) {
    repo := seedTerminalAndActiveStates(t)
    deleted, err := repo.CleanupTerminal(ctx, fourteenDaysAfterTerminal); if err != nil || deleted != 2 { t.Fatal(deleted, err) }
    assertRemainingStates(t, repo, "pending", "retry_wait", "leased", "failed")
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority -run 'Test(Claim|ExpiredLease|Cleanup)' -count=1
```

Expected: FAIL until claim/settlement methods exist.

- [ ] **Step 3: Implement exact FIFO and lease ownership.**

Claim in one SQLite write transaction with this selection:

```sql
select j.id from server_error_priority_demotion_jobs j
where j.state in ('pending','retry_wait') and j.available_at_ms <= ?
  and not exists (
    select 1 from server_error_priority_demotion_jobs earlier
    where earlier.credential_key = j.credential_key and earlier.id < j.id
      and earlier.state in ('pending','retry_wait','leased')
  )
order by j.id limit 1;
```

Update the selected row to `leased`, assign a random 32-byte hex lease token, set lease expiration, and increment attempts. Every `RecordPriorityPlan`, `Complete`, `Retry`, and `Terminal` update must include `(id, state='leased', lease_token)` so a stale worker cannot settle a newer lease. Expired leases only become `retry_wait`; they do not decrement attempts or delete tasks. Cleanup deletes only `completed`/`ignored` rows older than fourteen days.

- [ ] **Step 4: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/servererrorpriority -count=1
go test -race ./internal/repository/servererrorpriority -count=1
```

```powershell
git add apps/manager-server/internal/repository/servererrorpriority
git commit -m "feat: lease priority demotion jobs safely"
```

### Task 5: Configure eight workers and persist enable-switch transitions before collection

**Files:**
- Modify: `apps/manager-server/internal/config/config.go`
- Modify: `apps/manager-server/internal/config/config_test.go`
- Modify: `apps/manager-server/internal/worker/automation_runtime.go`
- Modify: `apps/manager-server/internal/worker/automation_runtime_test.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`

- [ ] **Step 1: Write failing configuration and runtime reload tests.**

```go
func TestLoadPriorityQueueSettingsAreBounded(t *testing.T) {
    t.Setenv("USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WORKERS", "99")
    t.Setenv("USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WAKE_BUFFER", "1")
    cfg, err := LoadWithoutCreatingDefault(); if err != nil { t.Fatal(err) }
    if cfg.ServerErrorPriorityDemotionWorkers != 32 || cfg.ServerErrorPriorityDemotionWakeBuffer != 64 { t.Fatalf("cfg=%#v", cfg) }
}

func TestAutomationRuntimeReloadSynchronizesPriorityQueue(t *testing.T) {
    priority := &recordingServerErrorPriorityAutomationWorker{}
    runtime := NewAutomationRuntime(settings, nil, nil, nil, priority)
    must(t, runtime.Reload(ctx))
    if priority.setEnabledCalls != 1 || !priority.enabled { t.Fatalf("state=%#v", priority) }
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/config ./internal/worker -run 'Test(LoadPriorityQueueSettingsAreBounded|AutomationRuntimeReloadSynchronizesPriorityQueue)' -count=1
```

Expected: FAIL because config fields and `SetEnabled` do not exist.

- [ ] **Step 3: Add bounded config and transition API.**

Parse `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WORKERS` with default `8`, clamped `[1,32]`; parse `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WAKE_BUFFER` with default `8192`, clamped `[64,32768]`.

Extend the narrow runtime worker contract:

```go
type serverErrorPriorityAutomationWorker interface {
    Bootstrap(context.Context, bool) error
    Start(context.Context)
    SetEnabled(context.Context, bool) error
    HandleUsageEvents(context.Context, collectorpkg.RuntimeConfig, []usage.Event)
}
```

`AutomationRuntime.Reload` calls `SetEnabled(ctx, settings.ServerErrorPriorityDemotionEnabled)` and returns an error if the persistence transition fails. `main.go` calls `Bootstrap(ctx, runtimeSettings.ServerErrorPriorityDemotionEnabled)` directly after construction but before `net.Listen` and before `collectorWorker.Start`; startup fails rather than accepting events without a durable floor.

- [ ] **Step 4: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/config ./internal/worker -run 'Test(Load|Automation)' -count=1
```

```powershell
git add apps/manager-server/internal/config apps/manager-server/internal/worker/automation_runtime.go apps/manager-server/internal/worker/automation_runtime_test.go apps/manager-server/cmd/cpa-manager-plus/main.go
git commit -m "feat: configure priority demotion queue runtime"
```

### Task 6: Replace the lossy channel with an eight-worker durable executor

**Files:**
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion.go`
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion_test.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`

- [ ] **Step 1: Write failing executor tests.**

```go
func TestWorkerExecutesTenEventsAsTenPatches(t *testing.T) {
    // Queue ten unique 502 events for one credential starting at priority 30.
    // Use one worker for deterministic order; wait for ten completed jobs.
    // Assert exactly ten PATCH calls and targets 29,28,...,20.
}

func TestWorkerHonorsConfiguredParallelismAndCredentialFIFO(t *testing.T) {
    // Fake CPA blocks PATCH calls. Configure two workers; max concurrent PATCH=2.
    // A second job for credential A starts only after A's first job settles.
}

func TestWorkerDoesNotPatchAgainWhenRecoveredTargetAlreadyExists(t *testing.T) {
    // Seed expired lease with before=20,target=19; fake CPA reports 19.
    // Recover and process: completed_reason=already_at_target, PATCH calls=0.
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/worker -run 'TestWorker(ExecutesTenEvents|HonorsConfiguredParallelism|DoesNotPatchAgain)' -count=1
```

Expected: FAIL because the old worker owns only a lossy local channel.

- [ ] **Step 3: Build the durable worker and notification behavior.**

```go
type ServerErrorPriorityDemotionOptions struct {
    Workers           int
    WakeBuffer        int
    LeaseDuration     time.Duration
    ReconcileInterval time.Duration
    StatsInterval     time.Duration
    CleanupInterval   time.Duration
}
```

Construct it from `Store.ServerErrorPriorityDemotions`, existing `MutationCoordinator`, existing setup resolver, and the bounded config settings. `HandleUsageEvents` calls `EnqueueEvents`, logs explicit SQLite admission errors, then performs nonblocking `wake()`. Delete `job queue full, dropped auth file` entirely. If wake buffer is full, return normally because admission already committed the job.

`Start` launches exactly `Workers` claim loops plus one scheduler. Claim loops pause while disabled and otherwise claim/process repeatedly until none are ready. Scheduler always reconciles source rows through the persisted enabled intervals (so a closed enabled interval cannot be lost), but only enabled claim loops execute mutations; it also recovers expired leases, cleans old terminal records, and logs bounded aggregate queue stats every sixty seconds.

- [ ] **Step 4: Implement the CPA idempotence protocol.**

For every leased job: resolve current setup; build/validate identity; acquire existing `MutationCoordinator`; resolve verified CPA target; inspect current priority and peer inventory. Persist `priority_before` and `priority_target` before PATCH.

- Current priority equal to stored target: complete `already_at_target` with no patch.
- Current priority equal to stored before: retry the same patch.
- Any other current priority: recompute the pre-approved peer formula and store a replacement before/target pair.
- Current priority zero: complete `floor_reached` and never write negative priority.
- Context/CPA transient failures: bounded exponential backoff into `retry_wait`.
- Missing/deleted/unsupported identity: settle `failed` or `ignored` with bounded redacted text.

Keep the existing 20-second inventory TTL and 45-second failure cooldown, but add a per-setup in-flight object (`done chan struct{}`) so eight workers cannot simultaneously fetch the same full inventory. Update cached target priority after successful PATCH.

- [ ] **Step 5: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/worker -run 'ServerErrorPriority|TestWorker' -count=1
go test -race ./internal/worker -run 'ServerErrorPriority|TestWorker' -count=1
```

```powershell
git add apps/manager-server/internal/worker/server_error_priority_demotion.go apps/manager-server/internal/worker/server_error_priority_demotion_test.go apps/manager-server/cmd/cpa-manager-plus/main.go
git commit -m "feat: execute priority demotions from durable queue"
```

### Task 7: Prove fanout/toggle behavior and accounting isolation end to end

**Files:**
- Modify: `apps/manager-server/internal/worker/automation_runtime_test.go`
- Create: `apps/manager-server/internal/worker/server_error_priority_integration_test.go`
- Modify: `apps/manager-server/internal/repository/servererrorpriority/repository_test.go`

- [ ] **Step 1: Write failing integration tests.**

```go
func TestPriorityFanoutNeverBackfillsDisabledPeriod(t *testing.T) {
    // Bootstrap disabled; insert eligible source event and invoke fanout: no job.
    // Enable through Runtime.Reload; next event creates one job.
    // Disable/re-enable; reconciler never creates a job for the disabled event.
}

func TestPriorityQueueNeverMutatesUsagePricingOrQuota(t *testing.T) {
    // Snapshot source usage count plus representative pricing/rollup/quota values.
    // Enqueue, claim, plan, and settle a priority job.
    // Every accounting value is unchanged.
}
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/worker ./internal/repository/servererrorpriority -run 'TestPriority(Fanout|QueueNeverMutates)' -count=1
```

Expected: FAIL until real fanout/runtime/repository wiring is connected.

- [ ] **Step 3: Implement only production-path wiring required by those failures.**

Use the actual `AutomationRuntime`, collector handler contract, Store, and temporary SQLite repositories. Do not add test-only production bypasses. Token recovery, usage rollup, hourly aggregate, and existing automation fanout behavior must remain unchanged.

- [ ] **Step 4: Verify GREEN and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/servererrorpriority ./internal/repository/servererrorpriority ./internal/worker ./internal/config ./internal/store -count=1
go test -race ./internal/servererrorpriority ./internal/repository/servererrorpriority ./internal/worker -count=1
```

```powershell
git add apps/manager-server/internal/worker/automation_runtime_test.go apps/manager-server/internal/worker/server_error_priority_integration_test.go apps/manager-server/internal/repository/servererrorpriority/repository_test.go
git commit -m "test: cover durable priority queue lifecycle"
```

### Task 8: Record custom behavior and execute final CPAM-only verification

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [ ] **Step 1: Update custom-change memory.**

Add explicit preservation requirements: CPAM-local `server_error_priority_demotion_*` tables; never restore a lossy memory channel during an upstream merge; exact eligible errors; current peer formula and zero floor; default eight workers; enabled intervals; no historical replay; no secret persistence; additive-only `usage.sqlite` migration.

- [ ] **Step 2: Format, test, and build.**

Run:

```powershell
Set-Location apps/manager-server
gofmt -w internal/servererrorpriority internal/model/server_error_priority_demotion.go internal/repository/servererrorpriority internal/repository/sqlite/migrate.go internal/store/store.go internal/config/config.go internal/config/config_test.go internal/worker/server_error_priority_demotion.go internal/worker/server_error_priority_demotion_test.go internal/worker/automation_runtime.go internal/worker/automation_runtime_test.go cmd/cpa-manager-plus/main.go
go test ./... -count=1
go build ./cmd/cpa-manager-plus
```

Expected: all commands exit zero. On failure, return to the smallest failing regression test and root-cause analysis before changing another subsystem.

- [ ] **Step 3: Check forbidden scope and commit.**

```powershell
git diff --check lo/custom...HEAD
git diff --name-only lo/custom...HEAD
git status --short
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: record durable priority queue customization"
```

Expected: only CPAM source/test/doc paths changed; no CPA Core path, key, token, SQLite binary, or deployment artifact is committed.

## Deployment gate

Deployment is outside this implementation plan and occurs only after fresh green verification and an explicit LO deployment request. Build from this clean isolated worktree; rebuild/restart only local `cpa-manager-plus`; compare local CPA Core container ID before/after. On the remote host update only `/home/cpam` and restart only `cpam.service`; compare `cpa.service` PID before/after and do not touch `/home/cpa` or `cli-proxy-api`. Never push upstream; if requested, push only the reviewed result to private `origin` branch `lo/custom`.

