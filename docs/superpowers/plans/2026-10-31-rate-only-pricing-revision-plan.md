# Rate-Only Pricing Revision Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make any monetary edit to an existing CPAM model price update current displayed costs from existing token aggregates without replaying online or archived usage events; retain a replay only when model membership or a context-tier threshold changes.

**Architecture:** The separate-derived pricing stream carries two explicit fingerprints. Its v2 generation uses only the structural fingerprint, while the existing `pricing_revision` database column continues to store the value fingerprint. Pricing rows remain token-counter materializations and are evaluated with the current `model_prices` map on the read path. A one-time derived-only transaction promotes a current legacy v1 generation to v2 without reading source events; incompatible legacy generations use the normal safe structural replay.

**Tech Stack:** Go, SQLite/WAL, CPAM manager server, `derivedstore`, `usagepricing`, focused `go test` package tests.

---

## File map

| File | Responsibility after this change |
| --- | --- |
| `apps/manager-server/internal/derivedstore/contracts.go` | Explicit structure/value revisions and transition metadata in source, state, and apply contracts. |
| `apps/manager-server/internal/derivedstore/source.go` | Read a consistent price map and expose both fingerprints in `SourceSnapshot`. |
| `apps/manager-server/internal/derivedstore/engine.go` | Generate pricing v2 from structure only; rate edits become zero-event metadata applies. |
| `apps/manager-server/internal/derivedstore/sink.go` | Persist revisions, preserve coverage for rate-only updates, record transitions. |
| `apps/manager-server/internal/derivedstore/pricing_generation_migration.go` | Promote a current canonical legacy v1 pricing generation without source replay. |
| `apps/manager-server/internal/derivedstore/codex_identity_migration.go` | Fold historic Codex identity rows into the v2 generation. |
| `apps/manager-server/internal/repository/sqlite/derived_migrate.go` | Additive migration of only derived-DB state columns. |
| `apps/manager-server/internal/store/derived_runtime.go` | Startup promotion and structural-only read readiness. |
| `apps/manager-server/internal/store/derived_bootstrap.go` | Seed fresh separate-derived state with both revisions. |
| `apps/manager-server/internal/http/controller/system/handler.go` | Expose pricing revisions/transition in maintenance status. |

## Invariants

1. `usage.sqlite` is query-only to the derived worker. No primary facts, credentials, Credits, quotas, CPA Core, or `cli-proxy-api` data changes.
2. A rate-only change preserves pricing generation, coverage, and processed event count and never starts at event ID `0`.
3. A model-set or context-threshold change creates a new structure generation and safely replays archive-plus-online events.
4. Existing SQLite `pricing_revision` remains the value hash; the only new revision column is `pricing_structure_revision`.
5. Promotion rewrites only current readable materialized-row generation and stream state. It intentionally does **not** rewrite the historical applied-event ledger: that ledger is not used by the reader/coverage proof and mass-updating millions of ledger keys would recreate contention.

### Task 1: Make source/state revisions explicit and migrate schema additively

**Files:**
- Modify: `apps/manager-server/internal/derivedstore/contracts.go`
- Modify: `apps/manager-server/internal/derivedstore/source.go`
- Modify: `apps/manager-server/internal/repository/sqlite/derived_migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/derived_migrate_test.go`
- Modify: `apps/manager-server/internal/derivedstore/engine_test.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`

- [x] **Step 1: Write the failing legacy-schema migration test.**

Append this test to `internal/repository/sqlite/derived_migrate_test.go`:

```go
func TestMigrateDerivedAddsPricingRevisionColumnsToExistingState(t *testing.T) {
    db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy-derived.sqlite"))
    if err != nil { t.Fatalf("open legacy db: %v", err) }
    t.Cleanup(func() { _ = db.Close() })
    _, err = db.Exec(`create table derived_stream_state (
        stream_name text primary key, source_epoch text not null default '',
        generation text not null default '', status text not null default 'pending',
        coverage_event_id integer not null default 0, target_event_id integer not null default 0,
        pricing_revision text not null default '', archive_generation text not null default '',
        processed_events integer not null default 0, last_success_at_ms integer,
        last_run_started_at_ms integer, last_batch_duration_ms integer not null default 0,
        backoff_count integer not null default 0, last_error_code text not null default '',
        updated_at_ms integer not null default 0
    )`)
    if err != nil { t.Fatalf("seed legacy schema: %v", err) }
    if err := MigrateDerived(db); err != nil { t.Fatalf("migrate: %v", err) }
    columns := map[string]bool{}
    rows, err := db.Query(`pragma table_info(derived_stream_state)`)
    if err != nil { t.Fatal(err) }
    defer rows.Close()
    for rows.Next() {
        var cid, notNull, pk int; var name, kind string; var defaultValue any
        if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil { t.Fatal(err) }
        columns[name] = true
    }
    for _, name := range []string{"pricing_revision", "pricing_structure_revision", "last_transition"} {
        if !columns[name] { t.Fatalf("missing state column %q: %#v", name, columns) }
    }
}
```

- [x] **Step 2: Verify the test fails before implementation.**

Run from `apps/manager-server`:

```powershell
go test ./internal/repository/sqlite -run '^TestMigrateDerivedAddsPricingRevisionColumnsToExistingState$' -count=1
```

Expected: FAIL because `pricing_structure_revision` and `last_transition` are absent.

- [x] **Step 3: Replace ambiguous contracts with explicit revisions.**

In `contracts.go`, use these exact fields and remove `PricingRevision(context.Context) (string, error)` from `Source`:

```go
type SourceSnapshot struct {
    Identity                 SourceIdentity
    TargetEventID            int64
    ArchiveGeneration        string
    PricingStructureRevision string
    PricingValueRevision     string
}

type StreamState struct {
    Stream                   Stream
    SourceEpoch              string
    Generation               string
    Status                   string
    CoverageEventID          int64
    TargetEventID            int64
    PricingStructureRevision string
    PricingValueRevision     string
    ArchiveGeneration        string
    ProcessedEvents          int64
    LastSuccessAtMS          int64
    LastErrorCode            string
    LastTransition           string
}

type ApplyBatch struct {
    Stream                   Stream
    SourceEpoch              string
    Generation               string
    PricingStructureRevision string
    PricingValueRevision     string
    ArchiveGeneration        string
    // retain FromEventID, ToEventID, SourceTargetEventID, Events, Rows, AppliedAtMS
}
```

In `source.go`, read prices once inside `Snapshot` and return both revisions from that map:

```go
prices, err := s.ModelPrices(ctx)
if err != nil { return SourceSnapshot{}, fmt.Errorf("read model price revisions: %w", err) }
return SourceSnapshot{
    Identity: identity, TargetEventID: target, ArchiveGeneration: archiveGeneration,
    PricingStructureRevision: model.ModelPriceStructureRevision(prices),
    PricingValueRevision: model.ModelPriceValueRevision(prices),
}, nil
```

Delete `sqlSource.PricingRevision`. Update all scripted-source snapshots/mocks to provide both hashes from the same price map.

- [x] **Step 4: Add additive derived-only SQLite migration.**

Add these columns after `pricing_revision` in the fresh `derived_stream_state` DDL:

```sql
pricing_structure_revision text not null default '',
last_transition text not null default '',
```

After `ensureDerivedMaterializedSubjectKey(db)`, call `ensureDerivedStreamStateColumns(db)`. Its required implementation is:

```go
func ensureDerivedStreamStateColumns(db *sql.DB) error {
    rows, err := db.Query(`pragma table_info(derived_stream_state)`)
    if err != nil { return fmt.Errorf("inspect derived stream state schema: %w", err) }
    defer rows.Close()
    seen := map[string]bool{}
    for rows.Next() {
        var cid, notNull, primaryKey int; var name, columnType string; var defaultValue any
        if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil { return err }
        seen[name] = true
    }
    if err := rows.Err(); err != nil { return err }
    missing := map[string]string{
        "pricing_structure_revision": "pricing_structure_revision text not null default ''",
        "last_transition": "last_transition text not null default ''",
    }
    for _, name := range []string{"pricing_structure_revision", "last_transition"} {
        if !seen[name] {
            if _, err := db.Exec("alter table derived_stream_state add column " + missing[name]); err != nil {
                return fmt.Errorf("add derived stream state %s: %w", name, err)
            }
        }
    }
    return nil
}
```

- [x] **Step 5: Format and run focused contract/schema tests.**

```powershell
gofmt -w internal/derivedstore/contracts.go internal/derivedstore/source.go internal/repository/sqlite/derived_migrate.go internal/repository/sqlite/derived_migrate_test.go internal/derivedstore/engine_test.go internal/store/derived_runtime_test.go
go test ./internal/repository/sqlite ./internal/derivedstore ./internal/store -run 'Test(MigrateDerived|EngineRetriesWhenPricingSnapshotChangesDuringRead|SeparateDerivedRuntimeBoundsVisibleTailDuringLargeBacklog)$' -count=1
```

Expected: PASS.

- [x] **Step 6: Commit the schema/contract slice.**

```powershell
git add apps/manager-server/internal/derivedstore/contracts.go apps/manager-server/internal/derivedstore/source.go apps/manager-server/internal/repository/sqlite/derived_migrate.go apps/manager-server/internal/repository/sqlite/derived_migrate_test.go apps/manager-server/internal/derivedstore/engine_test.go apps/manager-server/internal/store/derived_runtime_test.go
git commit -m "feat: separate pricing structure and value revisions"
```

### Task 2: Keep pricing coverage on rate-only edits and rebuild only on structure changes

**Files:**
- Modify: `apps/manager-server/internal/derivedstore/engine.go`
- Modify: `apps/manager-server/internal/derivedstore/sink.go`
- Modify: `apps/manager-server/internal/derivedstore/engine_test.go`
- Modify: `apps/manager-server/internal/derivedstore/sink_test.go`

- [ ] **Step 1: Write failing no-replay and structural-rebuild tests.**

Extend `scriptedSource` in `engine_test.go` with `eventsAfterCalls int` and `archivedAfterCalls int`, make its source-read methods pointers, and increment those counters. Add this table-driven regression:

```go
func TestEngineRateOnlyPriceChangeKeepsPricingCoverageWithoutReplay(t *testing.T) {
    db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = db.Close() })
    source := &scriptedSource{
        epoch: "rate-only", events: []EventRecord{{ID: 1, EventHash: "live", TimestampMS: 1,
            Provider: "codex", AuthProviderSnapshot: "codex", AuthIndex: "a",
            Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 200, CacheReadTokens: 20,
            CacheCreationTokens: 10, OutputTokens: 5, TotalTokens: 235}},
        archived: []EventRecord{{ID: 2, EventHash: "archive", TimestampMS: 2,
            Provider: "codex", AuthProviderSnapshot: "codex", AuthIndex: "a",
            Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 120, TotalTokens: 120}},
        prices: map[string]model.ModelPrice{"gpt-test": {
            Prompt: 1, Completion: 1, Cache: 1, CacheRead: 1, CacheCreation: 1,
            ContextTiers: []model.ModelPriceContextTier{{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true}},
            ServiceTiers: []model.ModelPriceServiceTier{{Mode: "priority", Prompt: 3, PromptConfigured: true}},
        }},
    }
    engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 10, MaxBatches: 4})
    if _, err := engine.RunCycle(context.Background()); err != nil { t.Fatal(err) }
    before, err := NewSink(db).State(context.Background(), StreamPricing)
    if err != nil { t.Fatal(err) }
    reads := [2]int{source.eventsAfterCalls, source.archivedAfterCalls}
    cases := []struct { name string; mutate func(*model.ModelPrice) }{
        {"prompt", func(p *model.ModelPrice) { p.Prompt = 7 }},
        {"completion", func(p *model.ModelPrice) { p.Completion = 8 }},
        {"cache", func(p *model.ModelPrice) { p.Cache = 9 }},
        {"cache-read", func(p *model.ModelPrice) { p.CacheRead = 10 }},
        {"cache-creation", func(p *model.ModelPrice) { p.CacheCreation = 11 }},
        {"context-rate", func(p *model.ModelPrice) { p.ContextTiers[0].Prompt = 12 }},
        {"service-tier-rate", func(p *model.ModelPrice) { p.ServiceTiers[0].Prompt = 13 }},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            p := source.prices["gpt-test"]; tc.mutate(&p); source.prices["gpt-test"] = p
            if _, err := engine.RunCycle(context.Background()); err != nil { t.Fatal(err) }
            after, err := NewSink(db).State(context.Background(), StreamPricing)
            if err != nil { t.Fatal(err) }
            if after.Generation != before.Generation || after.CoverageEventID != before.CoverageEventID || after.ProcessedEvents != before.ProcessedEvents {
                t.Fatalf("rate-only change replayed state: before=%#v after=%#v", before, after)
            }
            if after.PricingStructureRevision != before.PricingStructureRevision || after.PricingValueRevision == before.PricingValueRevision {
                t.Fatalf("bad revision split: before=%#v after=%#v", before, after)
            }
            if after.LastTransition != "rate_only_refresh" { t.Fatalf("transition=%q", after.LastTransition) }
            if [2]int{source.eventsAfterCalls, source.archivedAfterCalls} != reads {
                t.Fatalf("rate-only change read source events: before=%v after=%v", reads, [2]int{source.eventsAfterCalls, source.archivedAfterCalls})
            }
            before = after
        })
    }
}
```

Add a separate threshold test that changes `ContextTiers[0].ThresholdTokens` from `100` to `200`, runs one cycle, then verifies a new pricing generation, `LastTransition == "structural_rebuild"`, and increased live/archive source-read counters.

- [ ] **Step 2: Verify current value-based generation fails the no-replay test.**

```powershell
go test ./internal/derivedstore -run 'TestEngine(RateOnlyPriceChangeKeepsPricingCoverageWithoutReplay|ContextThresholdChangeCreatesPricingReplayGeneration)$' -count=1
```

Expected: FAIL. Current `pricing-v1` includes the value hash, so any rate edit creates a new generation and starts from zero.

- [ ] **Step 3: Change engine generation selection and snapshot race validation.**

In `engine.go`, preserve legacy recognition but make the active generation v2:

```go
const (
    accountHistoryGenerationPrefix = "account-history-v1:"
    legacyPricingGenerationPrefix  = "pricing-v1:"
    pricingGenerationPrefix        = "pricing-v2:"
)
```

Replace pricing generation creation with:

```go
func GenerationFor(stream Stream, snapshot SourceSnapshot) string {
    identityRevision := usageidentity.CodexIdentityRevision
    switch stream {
    case StreamPricing:
        return pricingGenerationPrefix + snapshot.Identity.Epoch + ":codex-" + identityRevision + ":" + snapshot.PricingStructureRevision
    default:
        return accountHistoryGenerationPrefix + snapshot.Identity.Epoch + ":codex-" + identityRevision
    }
}
```

In `snapshotAndPrices`, validate both revisions against the exact second price map:

```go
structureRevision := model.ModelPriceStructureRevision(prices)
valueRevision := model.ModelPriceValueRevision(prices)
if snapshot.PricingStructureRevision == structureRevision && snapshot.PricingValueRevision == valueRevision {
    return snapshot, prices, nil
}
```

Keep retry semantics. Expand the final error to include both snapshot/map structure and value hashes; do not include raw price data.

In `pricingCatchupPlan`, make the mismatch condition include only epoch, active v2 generation, structure revision, and archive generation. A value mismatch intentionally must not demand a catch-up burst.

- [ ] **Step 4: Persist a value change without resetting pricing coverage.**

In `sink.go`, include these state columns in every select/scan in this exact order:

```sql
pricing_structure_revision, pricing_revision, archive_generation,
processed_events, coalesce(last_success_at_ms, 0), last_error_code, last_transition
```

Map `pricing_revision` to `PricingValueRevision`. Before upsert, load `existingValueRevision`. Select transition as follows:

```go
transition := ""
switch {
case batch.Stream != StreamPricing:
case !existing:
    transition = "initial_build"
case generationChanged:
    transition = "structural_rebuild"
case existingValueRevision != batch.PricingValueRevision:
    transition = "rate_only_refresh"
}
```

For generation change, reset coverage and store both revisions and `last_transition`. For same-generation `INSERT ... ON CONFLICT`, add these assignments:

```sql
pricing_structure_revision=excluded.pricing_structure_revision,
pricing_revision=excluded.pricing_revision,
last_transition=case when excluded.last_transition <> ''
    then excluded.last_transition else derived_stream_state.last_transition end
```

Create all `ApplyBatch` values in `processStream` with:

```go
PricingStructureRevision: snapshot.PricingStructureRevision,
PricingValueRevision: snapshot.PricingValueRevision,
```

A zero-event `Apply` is the required cheap value-revision metadata update; it must not call `eventsAfter` or `ArchivedEventsAfter` first.

- [ ] **Step 5: Add sink-specific transition tests and run focused suite.**

Add `TestSinkRetainsPricingCoverageWhenOnlyValueRevisionChanges` to `sink_test.go`: apply a v2 pricing row at coverage `10`, apply an empty same-generation batch with value `values-v2`, then assert coverage `10`, processed count unchanged, one materialized row retained, and `LastTransition == "rate_only_refresh"`.

```powershell
gofmt -w internal/derivedstore/engine.go internal/derivedstore/sink.go internal/derivedstore/engine_test.go internal/derivedstore/sink_test.go
go test ./internal/derivedstore -run 'Test(Engine|Sink)' -count=1
```

Expected: PASS. Rate-only cases perform zero new event/archive reads; threshold change begins normal bounded replay.

- [ ] **Step 6: Commit engine/sink behavior.**

```powershell
git add apps/manager-server/internal/derivedstore/engine.go apps/manager-server/internal/derivedstore/sink.go apps/manager-server/internal/derivedstore/engine_test.go apps/manager-server/internal/derivedstore/sink_test.go
git commit -m "fix: avoid pricing replay for rate-only changes"
```

### Task 3: Safely promote the deployed v1 pricing generation without source replay

**Files:**
- Create: `apps/manager-server/internal/derivedstore/pricing_generation_migration.go`
- Create: `apps/manager-server/internal/derivedstore/pricing_generation_migration_test.go`
- Modify: `apps/manager-server/internal/derivedstore/codex_identity_migration.go`
- Modify: `apps/manager-server/internal/derivedstore/codex_identity_migration_test.go`
- Modify: `apps/manager-server/internal/store/derived_runtime.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`

- [ ] **Step 1: Write failing promotion tests.**

Create `pricing_generation_migration_test.go` with these tests:

```go
func TestMigrateLegacyPricingGenerationPromotesCurrentCheckpoint(t *testing.T) {
    ctx := context.Background()
    db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = db.Close() })
    snapshot := SourceSnapshot{
        Identity: SourceIdentity{Epoch: "epoch-a"}, TargetEventID: 1000,
        ArchiveGeneration: "archive-a", PricingStructureRevision: "structure-a",
        PricingValueRevision: "value-a",
    }
    legacy := currentLegacyPricingGeneration(snapshot)
    sink := NewSink(db)
    _, err = sink.Apply(ctx, ApplyBatch{
        Stream: StreamPricing, SourceEpoch: snapshot.Identity.Epoch, Generation: legacy,
        PricingValueRevision: snapshot.PricingValueRevision, ArchiveGeneration: snapshot.ArchiveGeneration,
        ToEventID: 812, SourceTargetEventID: 900,
        Events: []EventRecord{{ID: 812, EventHash: "event-812"}},
        Rows: []MaterializedRow{{Key: "account-a", SubjectKey: "account-a", PayloadJSON: `{"calls":1}`}},
    })
    if err != nil { t.Fatal(err) }
    promoted, err := MigrateLegacyPricingGeneration(ctx, sink, snapshot)
    if err != nil || !promoted { t.Fatalf("promotion=%v err=%v", promoted, err) }
    state, err := sink.State(ctx, StreamPricing)
    if err != nil { t.Fatal(err) }
    if state.Generation != GenerationFor(StreamPricing, snapshot) || state.CoverageEventID != 812 ||
        state.TargetEventID != 1000 || state.ProcessedEvents != 1 ||
        state.PricingStructureRevision != "structure-a" || state.PricingValueRevision != "value-a" ||
        state.LastTransition != "legacy_promotion" {
        t.Fatalf("promoted state=%#v", state)
    }
    rows, err := sink.Rows(ctx, StreamPricing, state.Generation)
    if err != nil || len(rows) != 1 { t.Fatalf("promoted rows=%#v err=%v", rows, err) }
    var ledger int
    if err := db.QueryRow(`select count(*) from derived_applied_events where stream_name=? and generation=?`, string(StreamPricing), legacy).Scan(&ledger); err != nil { t.Fatal(err) }
    if ledger != 1 { t.Fatalf("legacy ledger was rewritten: %d", ledger) }
}

func TestMigrateLegacyPricingGenerationRejectsDifferentValueRevision(t *testing.T) {
    // Seed the same canonical v1 format using "value-old", pass snapshot with
    // "value-current", then assert migrated=false and original rows/state stay intact.
}
```

Add to `codex_identity_migration_test.go` assertions that an old pre-email identity fold ends with `PricingStructureRevision == snapshot.PricingStructureRevision`, `PricingValueRevision == snapshot.PricingValueRevision`, and `LastTransition == "legacy_promotion"`.

- [ ] **Step 2: Verify missing promotion API fails.**

```powershell
go test ./internal/derivedstore -run 'TestMigrate(LegacyPricingGeneration|CodexEmailIdentity)' -count=1
```

Expected: FAIL because `MigrateLegacyPricingGeneration` and `currentLegacyPricingGeneration` do not exist.

- [ ] **Step 3: Implement current-v1 promotion as one derived-only transaction.**

Create `pricing_generation_migration.go`:

```go
package derivedstore

import (
    "context"
    "errors"
    "fmt"
    "strings"
    "time"

    "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func MigrateLegacyPricingGeneration(ctx context.Context, sink Sink, snapshot SourceSnapshot) (bool, error) {
    sqliteSink, ok := sink.(*sqlSink)
    if !ok { return false, nil }
    return sqliteSink.migrateLegacyPricingGeneration(ctx, snapshot)
}

func currentLegacyPricingGeneration(snapshot SourceSnapshot) string {
    return legacyPricingGenerationPrefix + snapshot.Identity.Epoch + ":codex-" +
        usageidentity.CodexIdentityRevision + ":" + snapshot.PricingValueRevision
}
```

In `migrateLegacyPricingGeneration`, validate nonempty epoch/structure/value values, begin a transaction, call the existing `derivedStreamStateTx`, and return `(false, nil)` unless all are true:

```go
state.Stream == StreamPricing
state.SourceEpoch == snapshot.Identity.Epoch || state.SourceEpoch == ""
state.Generation == currentLegacyPricingGeneration(snapshot)
state.PricingValueRevision == "" || state.PricingValueRevision == snapshot.PricingValueRevision
```

Then perform only these materialization changes:

```go
expected := GenerationFor(StreamPricing, snapshot)
_, err = tx.ExecContext(ctx, `delete from derived_materialized_rows
    where stream_name=? and generation <> ? and generation <> ?`, string(StreamPricing), state.Generation, expected)
if err != nil { return false, fmt.Errorf("clear orphan pricing rows: %w", err) }
_, err = tx.ExecContext(ctx, `delete from derived_materialized_rows
    where stream_name=? and generation=?`, string(StreamPricing), expected)
if err != nil { return false, fmt.Errorf("clear orphan v2 pricing rows: %w", err) }
_, err = tx.ExecContext(ctx, `update derived_materialized_rows set generation=?
    where stream_name=? and generation=?`, expected, string(StreamPricing), state.Generation)
if err != nil { return false, fmt.Errorf("promote pricing rows: %w", err) }
```

Update the state atomically, retaining coverage and processed count:

```go
now := time.Now().UnixMilli()
_, err = tx.ExecContext(ctx, `update derived_stream_state set
    generation=?, target_event_id=max(target_event_id, ?),
    pricing_structure_revision=?, pricing_revision=?, archive_generation=?,
    last_transition='legacy_promotion', updated_at_ms=? where stream_name=?`,
    expected, snapshot.TargetEventID, snapshot.PricingStructureRevision,
    snapshot.PricingValueRevision, snapshot.ArchiveGeneration, now, string(StreamPricing))
```

Do **not** alter `derived_applied_events`; no source event is read and no 4-million-row ledger rewrite occurs. Commit, then return `true`.

- [ ] **Step 4: Update older identity folding and startup order.**

In `codex_identity_migration.go`, use `legacyPricingGenerationPrefix` only for historic v1 recognition and compare it to `snapshot.PricingValueRevision`. In `migrateCodexDerivedStream`, write both revision fields and `last_transition='legacy_promotion'` when folding into the v2 `GenerationFor` result.

In `Store.ConfigureDerivedStorage`, retain the identity fold first, then promote canonical v1 pricing before constructing the engine:

```go
if _, err := derivedstore.MigrateCodexEmailIdentity(ctx, runtime.sink, snapshot); err != nil {
    _ = runtime.close()
    s.markSeparateDerivedDegraded(fmt.Errorf("migrate separate derived codex identity: %w", err))
    return nil
}
if _, err := derivedstore.MigrateLegacyPricingGeneration(ctx, runtime.sink, snapshot); err != nil {
    _ = runtime.close()
    s.markSeparateDerivedDegraded(fmt.Errorf("promote separate derived pricing generation: %w", err))
    return nil
}
```

The order is mandatory: pre-email rows must be identity-folded before their price generation is rekeyed.

- [ ] **Step 5: Run compatibility regressions.**

```powershell
gofmt -w internal/derivedstore/pricing_generation_migration.go internal/derivedstore/pricing_generation_migration_test.go internal/derivedstore/codex_identity_migration.go internal/derivedstore/codex_identity_migration_test.go internal/store/derived_runtime.go internal/store/derived_runtime_test.go
go test ./internal/derivedstore ./internal/store -run 'Test(Migrate|ConfigureDerivedStorageMigratesLegacyCodexGenerationBeforeBootstrap)' -count=1
```

Expected: PASS. The direct-promotion test proves a partial/current checkpoint keeps its coverage and requires neither a source object nor an applied-ledger rewrite.

- [ ] **Step 6: Commit legacy compatibility.**

```powershell
git add apps/manager-server/internal/derivedstore/pricing_generation_migration.go apps/manager-server/internal/derivedstore/pricing_generation_migration_test.go apps/manager-server/internal/derivedstore/codex_identity_migration.go apps/manager-server/internal/derivedstore/codex_identity_migration_test.go apps/manager-server/internal/store/derived_runtime.go apps/manager-server/internal/store/derived_runtime_test.go
git commit -m "fix: promote current legacy pricing generation without replay"
```

### Task 4: Keep current-page price reads available and calculate with current rates

**Files:**
- Modify: `apps/manager-server/internal/store/derived_runtime.go`
- Modify: `apps/manager-server/internal/store/derived_bootstrap.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`
- Modify: `apps/manager-server/internal/service/monitoring/service_test.go`

- [ ] **Step 1: Turn stale-rate regression into current-price availability regression.**

Rename `TestSeparateDerivedRuntimeMarksPricingSnapshotStaleAfterPriceRevisionChanges` to `TestSeparateDerivedRuntimeKeepsPricingSnapshotAvailableAfterRateOnlyChange` and replace its final expectation with:

```go
after, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
if err != nil { t.Fatalf("load after rate change: %v", err) }
if !after.Available { t.Fatalf("rate-only change made derived counters unavailable: %#v", after) }
if len(after.Rows) != 1 || after.Rows[0].Calls != initial.Rows[0].Calls ||
    after.Rows[0].InputTokens != initial.Rows[0].InputTokens {
    t.Fatalf("rate-only change changed counters: before=%#v after=%#v", initial.Rows, after.Rows)
}
if after.Prices["gpt-test"].Prompt != 2 { t.Fatalf("current prices=%#v", after.Prices) }
```

Add to `internal/service/monitoring/service_test.go`:

```go
func TestBuildPricingAccountHistoryTotalsUsesCurrentRatesForExistingCounters(t *testing.T) {
    rows := []store.UsagePricingAccountRow{{
        AccountKey: "account-a", Model: "gpt-test", BillingModel: "gpt-test",
        PricingModel: "gpt-test", InputTokens: 1_000_000, Calls: 1,
    }}
    first := buildPricingAccountHistoryTotals(rows, map[string]store.ModelPrice{
        "gpt-test": {Prompt: 1, PromptConfigured: true},
    })["account-a"].cost
    second := buildPricingAccountHistoryTotals(rows, map[string]store.ModelPrice{
        "gpt-test": {Prompt: 2, PromptConfigured: true},
    })["account-a"].cost
    if first != 1 || second != 2 { t.Fatalf("costs first=%v second=%v", first, second) }
}
```

- [ ] **Step 2: Verify the old readiness condition fails.**

```powershell
go test ./internal/store ./internal/service/monitoring -run 'Test(SeparateDerivedRuntimeKeepsPricingSnapshotAvailableAfterRateOnlyChange|BuildPricingAccountHistoryTotalsUsesCurrentRatesForExistingCounters)$' -count=1
```

Expected: store test FAILS while `derivedStateReady` rejects a changed value revision.

- [ ] **Step 3: Make read readiness structural rather than monetary.**

In `derived_runtime.go`, preserve epoch, generation, archive, and coverage checks. Replace the pricing value check with:

```go
if stream == derivedstore.StreamPricing &&
    state.PricingStructureRevision != snapshot.PricingStructureRevision {
    return false
}
```

Pass the structural marker to the CPAM pricing state:

```go
State: toUsagePricingState(pricingState, snapshot.PricingStructureRevision),
```

In `derived_bootstrap.go`, fill both values whenever a fresh separate-derived pricing state is seeded:

```go
PricingStructureRevision: snapshot.PricingStructureRevision,
PricingValueRevision:     snapshot.PricingValueRevision,
```

Do not add a synchronous currency rebuild to an HTTP path. `LoadUsagePricingAccountSnapshot` already loads the current price map through the query-only source, and `buildPricingAccountHistoryTotals` applies it against saved token buckets.

- [ ] **Step 4: Run current-page/read-path regressions.**

```powershell
gofmt -w internal/store/derived_runtime.go internal/store/derived_bootstrap.go internal/store/derived_runtime_test.go internal/service/monitoring/service_test.go
go test ./internal/store ./internal/service/monitoring -run 'Test(SeparateDerivedRuntime|BuildPricingAccountHistoryTotals)' -count=1
```

Expected: PASS. Rate-only changes preserve current-page request/token rows and calculated money updates through the current price map.

- [ ] **Step 5: Commit read-path behavior.**

```powershell
git add apps/manager-server/internal/store/derived_runtime.go apps/manager-server/internal/store/derived_bootstrap.go apps/manager-server/internal/store/derived_runtime_test.go apps/manager-server/internal/service/monitoring/service_test.go
git commit -m "fix: serve current prices from existing derived counters"
```

### Task 5: Expose truthful pricing transition/status metadata

**Files:**
- Modify: `apps/manager-server/internal/http/controller/system/handler.go`
- Modify: `apps/manager-server/internal/store/derived_runtime_test.go`

- [ ] **Step 1: Write failing status metadata assertions.**

Extend `TestSeparateDerivedStatusUsesLivePrimaryTarget` so its seeded pricing state includes:

```go
PricingStructureRevision: "structure-v1",
PricingValueRevision:     "value-v2",
LastTransition:           "rate_only_refresh",
```

After `SeparateDerivedStatus`, find the pricing stream and assert its values exactly match those strings while `TargetEventID` still equals the newest main-source ID. This preserves the existing live-source target test and proves value/structure metadata does not fake a zero lag.

- [ ] **Step 2: Verify status projection is incomplete.**

```powershell
go test ./internal/store -run '^TestSeparateDerivedStatusUsesLivePrimaryTarget$' -count=1
```

Expected: FAIL until state scanning carries the new fields through the status result.

- [ ] **Step 3: Add status-only fields without exposing sensitive data.**

Add to `derivedStorageStreamStatus` in `handler.go`:

```go
PricingStructureRevision string `json:"pricingStructureRevision,omitempty"`
PricingValueRevision     string `json:"pricingValueRevision,omitempty"`
LastTransition           string `json:"lastTransition,omitempty"`
```

When building the status object, assign the three values directly from `stream`. Keep the existing `SeparateDerivedStatus` source snapshot refresh unchanged: it already replaces stale worker targets with the real main-source target before the handler computes lag. Do not return model prices, raw events, archives, account identifiers, credentials, proxies, or request payloads.

- [ ] **Step 4: Run status and focused full regression suite.**

```powershell
gofmt -w internal/http/controller/system/handler.go internal/store/derived_runtime_test.go
go test ./internal/store ./internal/http/controller/system -run 'TestSeparateDerivedStatusUsesLivePrimaryTarget' -count=1
go test ./internal/derivedstore ./internal/repository/sqlite ./internal/store ./internal/service/monitoring ./internal/http/controller/system -count=1
```

Expected: PASS. The second command is intentionally restricted to the five changed CPAM packages, not the entire repository.

- [ ] **Step 5: Commit status observability.**

```powershell
git add apps/manager-server/internal/http/controller/system/handler.go apps/manager-server/internal/store/derived_runtime_test.go
git commit -m "feat: report pricing revision transitions"
```

### Task 6: Verify release safety and deploy only CPAM

**Files:**
- No source edits unless a focused test discovers a defect.

- [ ] **Step 1: Inspect candidate scope and commits.**

```powershell
git status --short
git log --oneline d903098d..HEAD
git diff --check d903098d..HEAD
git diff --name-only d903098d..HEAD
```

Expected: clean worktree; changed code is limited to CPAM manager-server derived pricing paths/tests plus the approved design/plan documents.

- [ ] **Step 2: Build only the CPAM candidate.**

From `apps/manager-server`:

```powershell
go build ./cmd/manager-server
```

Expected: exit code `0`. Do not build CPA Core or `cli-proxy-api`.

- [ ] **Step 3: Record immutable CPA Core identities before deployment.**

Record local CPA Core Docker container ID/image and server `systemctl show cpa.service -p MainPID -p ActiveEnterTimestamp`. These are verification baselines only; do not restart, modify, or build CPA Core.

- [ ] **Step 4: Deploy only the same verified CPAM candidate.**

1. From the clean worktree, rebuild/restart only the local `cpa-manager-plus` Docker service.
2. Transfer the exact candidate release to `/home/cpam` using the existing CPAM release path.
3. Preserve `/home/cpam/config/cpam.env`, `/home/cpam/secrets/*`, `/home/cpam/data/*`, `usage.sqlite`, `usage-derived.sqlite`, WAL/SHM files, `data.key`, archives, OpenResty, and certificates.
4. Restart only `cpam.service`.

- [ ] **Step 5: Verify deployment and cheap rate change behavior.**

Verify all conditions below:

```text
- Local CPA Core container identity unchanged.
- Server cpa.service PID/activation unchanged.
- cpam.service is active.
- database-maintenance reports a pricing-v2 generation, distinct structure/value hashes,
  real main-source target watermark, and non-negative lag.
- Edit one existing model's cache/prompt/completion rate only: pricing coverage and
  processed count remain stable; a known account's displayed money changes.
- Do not alter any context threshold during the rate-only smoke test.
```

If rollback is required, restore only the preceding CPAM release and restart `cpam.service`. Keep `usage-derived.sqlite`; the migration is additive and no source facts/billing were modified.

## Plan self-review

- **Spec coverage:** Task 1 implements the explicit two-revision contract and additive migration. Task 2 proves all five monetary buckets plus context/service-tier rate changes do not replay and proves threshold changes do replay. Task 3 safely handles both current legacy and older Codex identity layouts. Task 4 keeps visible-page current pricing available from token counters. Task 5 makes status report real target/revision/transition. Task 6 preserves CPAM-only deployment and rollback boundaries.
- **Placeholder scan:** No `TODO`, `TBD`, or unspecified test action remains. Each behavior-changing task has a named failing test, a command that demonstrates the pre-change failure, concrete code/SQL to implement, and a focused passing command.
- **Type consistency:** `PricingStructureRevision`, `PricingValueRevision`, and `LastTransition` are the sole new Go names throughout. Database `pricing_revision` maps only to `PricingValueRevision`; `pricing_structure_revision` is the only new revision column. `GenerationFor(StreamPricing, snapshot)` is consistently v2/structure-based, while `currentLegacyPricingGeneration(snapshot)` is consistently v1/value-based.

