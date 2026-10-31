# Codex Email Identity Merge Implementation Plan

> **For agentic workers:** Execute task-by-task with verification checkpoints.

**Goal:** Make Codex account-level usage aggregation email-based, rebuild primary and separate derived projections, and prevent old/new workspace metadata from creating two buckets.

**Architecture:** Keep raw events immutable. Change the shared Codex aggregation key to normalized member email, bump the identity revision, let primary rollups rebuild from retained source, and include the revision in separate-derived generations so stale materializations are discarded before replay.

**Tech Stack:** Go, SQLite, `database/sql`, existing CPAM rollup/derived-store workers.

---

### Task 1: Align the canonical identity algorithm

**Files:**
- Modify: `apps/manager-server/internal/usageidentity/identity.go`
- Test: `apps/manager-server/internal/usageidentity/identity_test.go`

- [x] Add a regression test for no-workspace and workspace-present Codex events with the same email producing one key.
- [x] Change `CodexIdentityRevision` and make Go `AccountKey` use normalized email for valid Codex member snapshots, independent of workspace ID.
- [x] Make `SQLAccountKeyExpression` emit the same provider+email key and retain physical fallback only for invalid email.
- [x] Update the identity matrix tests for changed same-email semantics and keep non-Codex behavior unchanged.
- [x] Run focused identity tests.

### Task 2: Version and rebuild primary account/pricing rollups

**Files:**
- Inspect/modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Test: `apps/manager-server/internal/repository/sqlite/migrate_test.go`

- [x] Recognize the deployed custom `codex-2` revision and schedule a non-destructive account-history/pricing rebuild from immutable source.
- [x] Park the old account-history projection under a new revision-specific name and clear only the disposable per-account price cache.
- [x] Run migration, account-history, and pricing regressions; assert one current email-key row with summed calls/tokens.

### Task 3: Version and clear stale separate-derived generations

**Files:**
- Modify: `apps/manager-server/internal/derivedstore/engine.go`
- Modify: `apps/manager-server/internal/derivedstore/sink.go`
- Test: `apps/manager-server/internal/derivedstore/engine_test.go`
- Test: `apps/manager-server/internal/derivedstore/sink_test.go`

- [x] Add a regression that plants both the state-tracked and an orphan stale generation.
- [x] Include `usageidentity.CodexIdentityRevision` in account-history and pricing generation strings.
- [x] Delete all stale generations' materialized rows and applied-event ledger transactionally when the new generation is first applied.
- [x] Run derived-store tests and verify only the current generation remains visible.

### Task 4: End-to-end account aggregation regression

**Files:**
- Test: `apps/manager-server/internal/store/derived_runtime_test.go`
- Test: `apps/manager-server/internal/repository/usagepricing/repository_test.go`
- Test: `apps/manager-server/internal/repository/usagerollup/repository_test.go`

- [x] Seed old/new identity events with matching email and different workspace presence.
- [x] Rebuild primary and separate projections.
- [x] Assert one account-history row, one pricing account row, and summed counters.
- [x] Assert different emails remain separate.

### Task 5: Verification and candidate packaging

- [x] Run focused Go tests.
- [ ] Run the Linux full-suite/build gate before a deployment request.
- [x] Inspect diff and ensure no CPA Core files/configs changed.
- [x] Record migration/rebuild behavior and deployment preconditions.
