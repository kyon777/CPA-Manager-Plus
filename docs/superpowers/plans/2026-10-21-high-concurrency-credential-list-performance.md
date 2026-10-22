# High-Concurrency Credential List Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep CPAM's full-inventory credential list responsive for 4,000+ credentials and high request-error volume without changing CPA Core, usage-event writes, billing, Credits, or existing custom CPAM behavior.

**Architecture:** Keep the existing full-inventory/global-sort model, but remove the recurring expensive work that starves it. The Manager Server will execute the existing set-based recent-request query for the indexed path and will stop an aborted quota-window projection from falling back to the raw 29GB `usage_events` table. The web client will split unrelated refresh revisions, merge priority deltas in O(files + updates), keep recovery badges stable for the visible page, and retain matching window-usage data during an in-flight current-page refresh.

**Tech Stack:** Go 1.24, SQLite/modernc, React 19, TypeScript, Vitest, Vite.

---

## File structure

- `apps/manager-server/internal/repository/usageevent/latest_account_request.go` — selects the collection-oriented SQL path for all `account-recent-index` calls while preserving result semantics.
- `apps/manager-server/internal/repository/usageevent/latest_account_request_test.go` — proves indexed and collection paths remain equivalent for snapshot, legacy, and Codex-workspace identities.
- `apps/manager-server/internal/service/usagemonitoring/reader.go` — exposes a canceled projection read to the caller instead of hiding it as “unavailable”.
- `apps/manager-server/internal/service/usagemonitoring/reader_test.go` — distinguishes cancellation from a real projection-unavailable state.
- `apps/manager-server/internal/service/monitoring/service.go` — returns cancellation directly and retains raw fallback only for genuinely unavailable projection data.
- `apps/manager-server/internal/service/monitoring/service_test.go` — proves canceled account-window reads never invoke the raw fallback and normal results are unchanged.
- `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.ts` — builds a stable credential-key index once per update batch and preserves cursor-order/`priorityBefore` guards.
- `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.test.ts` — exercises large mixed batches, duplicate identity updates, and manual-priority protection.
- `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts` — retains matching visible-page recovery entries while a recovery lookup changes pages/sorts.
- `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx` — proves a seed/task badge does not briefly disappear for a still-visible credential.
- `apps/web/src/features/accounts/AccountsPage.tsx` — separates refresh revisions, schedules priority state as a React transition, and preserves current-page window data through a replacement request.
- `apps/web/src/features/accounts/AccountsPage.test.tsx` — proves passive refresh does not refetch global recent-index/window evidence while explicit data-domain refresh still does.

## Task 1: Use the collection query for indexed recent-request reads

**Files:**
- Modify: `apps/manager-server/internal/repository/usageevent/latest_account_request.go:90-165`
- Modify: `apps/manager-server/internal/repository/usageevent/latest_account_request_test.go`

- [ ] **Step 1: Write the failing parity test**

Add a test which prepares two otherwise identical temporary SQLite databases, one with latest-request indexes and one without. Seed a mixed target set containing a normal snapshot credential, an empty-index legacy credential, a complete Codex workspace/member target, and a conflicting Codex target. Invoke the direct indexed helper and the collection helper on the indexed database and compare the normalized results:

```go
func TestRecentAccountRequestsIndexedCollectionMatchesLegacyIndexedSemantics(t *testing.T) {
    indexed, err := repo.recentAccountRequestsIndexed(ctx, targets, 9)
    if err != nil { t.Fatalf("indexed: %v", err) }
    collection, err := repo.recentAccountRequestsBatched(ctx, targets, 9)
    if err != nil { t.Fatalf("collection: %v", err) }
    if !sameLatestAccountRequests(indexed, collection) {
        t.Fatalf("indexed=%#v collection=%#v", indexed, collection)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails for the intended path-selection gap**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/usageevent -run TestRecentAccountRequestsIndexedCollectionMatchesLegacyIndexedSemantics -count=1
```

Expected: test initially exposes any collection-path mismatch that must be corrected before routing the production indexed path to it. If it already passes because the helpers are semantically equivalent, add a second request-selection seam/test that asserts `RecentAccountRequests` delegates to the set-oriented helper when indexes exist; do not alter production code before a red test exists.

- [ ] **Step 3: Make the smallest production routing change**

Replace the per-target indexed branch in `RecentAccountRequests` with a set-based branch. Preserve empty inputs and the index-read error contract, but route both indexed and non-indexed conditions to the one-CTE collection function. Keep `recentAccountRequestsIndexed` private as a parity/reference implementation until future cleanup.

```go
ready, err := r.latestRequestIndexesReady(ctx)
if err != nil { return nil, err }
if ready {
    return r.recentAccountRequestsBatched(ctx, targets, limit)
}
return r.recentAccountRequestsBatched(ctx, targets, limit)
```

Immediately refactor that deliberate duplicate branch to the clear final form:

```go
if _, err := r.latestRequestIndexesReady(ctx); err != nil {
    return nil, err
}
return r.recentAccountRequestsBatched(ctx, targets, limit)
```

Do not change CTE result fields, target limit, response order, legacy fallback predicates, Codex workspace/member guards, or redaction fields.

- [ ] **Step 4: Run focused repository tests**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/repository/usageevent -count=1
```

Expected: all existing snapshot, legacy, index-plan, and Codex identity tests pass.

- [ ] **Step 5: Commit the verified server query change**

```powershell
git add apps/manager-server/internal/repository/usageevent/latest_account_request.go apps/manager-server/internal/repository/usageevent/latest_account_request_test.go
git commit -m "perf: batch credential recent request index queries"
```

## Task 2: Stop canceled quota-window reads from hitting the raw-event fallback

**Files:**
- Modify: `apps/manager-server/internal/service/usagemonitoring/reader.go:60-74`
- Modify: `apps/manager-server/internal/service/usagemonitoring/reader_test.go`
- Modify: `apps/manager-server/internal/service/monitoring/service.go:2125-2132`
- Modify: `apps/manager-server/internal/service/monitoring/service_test.go`

- [ ] **Step 1: Write a failing Reader cancellation test**

Add a test that passes an already-canceled context to `AccountWindowStats` and expects an error-bearing result instead of an indistinguishable `available=false` fallback signal:

```go
ctx, cancel := context.WithCancel(context.Background())
cancel()
_, available, err := New(db).AccountWindowStats(ctx, []store.AccountWindowUsageQuery{query})
if !errors.Is(err, context.Canceled) || available {
    t.Fatalf("available=%v err=%v", available, err)
}
```

- [ ] **Step 2: Run the Reader test and confirm it fails**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/usagemonitoring -run TestAccountWindowStatsReturnsCancellationWithoutFallback -count=1
```

Expected: FAIL because `AccountWindowStats` currently only returns `(rows, available)` and discards the cancellation error.

- [ ] **Step 3: Implement cancellation-aware Reader and service behavior**

Change the Reader signature to return `([]store.AccountWindowModelStat, bool, error)`. Preserve `available=false, err=nil` for a real unavailable schema/projection. On store error, return the original error after rate-limited diagnostic logging. In `accountWindowUsage`, short-circuit cancellation/deadline errors before raw fallback; only call `s.store.AccountWindowModelStats` when `available` is false and `err` is nil.

```go
stats, available, projectionErr := s.monitoringReader.AccountWindowStats(ctx, queries)
if projectionErr != nil {
    return AccountWindowUsageResponse{}, projectionErr
}
if !available {
    stats, err = s.store.AccountWindowModelStats(ctx, queries)
    if err != nil { return AccountWindowUsageResponse{}, err }
}
```

The HTTP handler may still naturally abandon a client-disconnected request. Do not convert an abort into an application-level success response, and do not change normal 200/500 contracts for non-cancellation failures.

- [ ] **Step 4: Write and run a service regression test**

Add a canceled-context test around `Service.AccountWindowUsage` with a valid window target. Verify it returns `context.Canceled`; add a normal-window test or reuse existing tests to prove normal projection results remain available. Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/usagemonitoring ./internal/service/monitoring -count=1
```

Expected: green; cancellation does not trigger a raw `usage_events` fallback.

- [ ] **Step 5: Commit cancellation handling**

```powershell
git add apps/manager-server/internal/service/usagemonitoring/reader.go apps/manager-server/internal/service/usagemonitoring/reader_test.go apps/manager-server/internal/service/monitoring/service.go apps/manager-server/internal/service/monitoring/service_test.go
git commit -m "fix: avoid raw usage fallback after window request cancellation"
```

## Task 3: Remove passive refresh fan-out from global recent-index and window evidence

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:1499-1503, 2445-2515, 4375-4376, 4901-4902, 5449`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Write failing refresh-domain tests**

Add tests with mocked `monitoringAnalyticsApi` that mount the accounts page, complete its first load, trigger passive evidence refresh, and assert no second calls to global `getAccountRecentIndex` or current-page `getAccountWindowUsage`. Add a separate explicit refresh assertion that increments its intended domain revision and causes the appropriate request.

```ts
expect(mocks.getAccountRecentIndex).toHaveBeenCalledTimes(initialRecentIndexCalls);
expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(initialWindowCalls);
await triggerPassiveEvidenceRefresh();
expect(mocks.getAccountRecentIndex).toHaveBeenCalledTimes(initialRecentIndexCalls);
expect(mocks.getAccountWindowUsage).toHaveBeenCalledTimes(initialWindowCalls);
```

- [ ] **Step 2: Run the new frontend test to verify it fails**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx -t "does not reload global evidence after passive refresh"
```

Expected: FAIL because `refreshPassiveAccountsEvidence` currently increments `accountHistoryAutoRefreshRevision`, which participates in both request keys.

- [ ] **Step 3: Split revision ownership by data domain**

Add `recentIndexRefreshRevision` beside existing state. Make these exact key changes:

```ts
const accountRecentIndexAutoLoadKey = `${accountRecentIndexAutoContextKey}\u0000${recentIndexRefreshRevision}`;
const accountRecentAnomalyIndexScopeKey = `${accountRecentAnomalyIndexContextKey}\u0000${recentIndexRefreshRevision}`;
const listWindowUsageAutoLoadKey = `${listWindowUsageAutoContextKey}\u0000${listWindowUsageRefreshRevision}`;
```

Remove `setAccountHistoryAutoRefreshRevision` from `refreshPassiveAccountsEvidence`. Keep it in explicit `refreshAccountsWorkspace`, because manual refresh intentionally refreshes history. In that explicit callback increment `recentIndexRefreshRevision` and `listWindowUsageRefreshRevision` together with the history revision only after its dependency work completes and the scope remains current. Preserve existing quota-success increments to `listWindowUsageRefreshRevision`.

- [ ] **Step 4: Run focused frontend tests and type-check**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx
npm --workspace apps/web run type-check
```

Expected: focused tests pass and TypeScript reports no errors.

- [ ] **Step 5: Commit refresh-domain isolation**

```powershell
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "perf: isolate passive credential evidence refreshes"
```

## Task 4: Merge priority demotion deltas without repeated full-array scans

**Files:**
- Modify: `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.ts`
- Modify: `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.test.ts`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:1, 1459-1471`

- [ ] **Step 1: Write failing high-cardinality merge tests**

Add a 4,000-file batch test with 200 matching updates and interleaved stale/manual updates. Assert only matching `priorityBefore` entries change, sequential same-credential cursor updates still chain, and untouched file object references are retained.

```ts
const result = applyServerErrorPriorityUpdates(files, updates);
expect(result.appliedCount).toBe(201);
expect(result.files[3999]).toBe(files[3999]);
expect(result.files[targetIndex]?.priority).toBe(expectedPriority);
```

- [ ] **Step 2: Run the priority model test and verify the pre-change behavior lacks the batch-index guarantee**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/serverErrorPriorityUpdates.test.ts
```

Expected: add a specific identity-index behavior assertion that fails until the model exposes/uses the precomputed lookup. Do not use wall-clock duration as a test oracle.

- [ ] **Step 3: Implement a per-batch credential index and transition commit**

Create a normalized composite key helper from physical file name, normalized auth index, and normalized provider. Build `Map<string, number>` once from `files`; for each valid update look up its index, then retain the existing `readPriority(current) === priorityBefore` guard before cloning only that object. Preserve update input order so chained deltas remain correct.

In `AccountsPage.tsx`, import `startTransition` and schedule the single `replaceFiles(result.files)` state update:

```ts
prioritySyncFilesRef.current = result.files;
startTransition(() => {
  replaceFiles(result.files);
});
```

Do not change the one-second polling cadence, cursor protocol, server queue, `priorityBefore` safety rule, or CPA priority write behavior.

- [ ] **Step 4: Run model tests, hook-adjacent tests, and type-check**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/model/serverErrorPriorityUpdates.test.ts src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx
npm --workspace apps/web run type-check
```

Expected: all tests pass; ensure a missing hook test file is not referenced—if absent, run the model test plus the accounts workspace tests that exercise the hook.

- [ ] **Step 5: Commit the sparse priority merge**

```powershell
git add apps/web/src/features/accounts/model/serverErrorPriorityUpdates.ts apps/web/src/features/accounts/model/serverErrorPriorityUpdates.test.ts apps/web/src/features/accounts/AccountsPage.tsx
git commit -m "perf: batch priority demotion list updates"
```

## Task 5: Keep visible recovery badges and quota-window evidence stable

**Files:**
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts:269-300`
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:7538-7604`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Write failing badge-stability test**

Seed a terminal recovery item for `TARGET_A`, render `fetchMetadata={false}` with recovery lookup enabled, then re-render with equivalent `TARGET_A` plus a changed parent seed array. Assert `row-a` keeps its recovery task before the lookup settles.

```tsx
expect(latest?.itemsByClientKey.get('row-a')?.recoveryTask?.status).toBe('succeeded');
renderer?.update(<Harness targets={[TARGET_A]} seedItems={[]} fetchMetadata={false} lookupRecoveryTasks />);
expect(latest?.itemsByClientKey.get('row-a')?.recoveryTask?.status).toBe('succeeded');
```

- [ ] **Step 2: Run the hook test and confirm it fails**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx -t "keeps visible recovery state"
```

Expected: FAIL because initialization calls `replaceItems(seededItemsByClientKey)` and clears a still-current state when the seed is empty.

- [ ] **Step 3: Implement target-scoped retained initialization**

Add a pure helper that builds a new map only for currently normalized target keys, preferring a fresh seed item when present and otherwise retaining the existing item. Use it only for the no-raw-metadata/recovery-lookup list path; retain the full replacement behavior when `fetchMetadata=true` and on disabled scope.

```ts
const retainVisibleItems = (current, targets, seeds) => {
  const next = new Map<string, CredentialRuntimeMetadataItem>();
  for (const target of targets) {
    const seeded = seeds.get(target.clientKey);
    const existing = current.get(target.clientKey);
    if (seeded || existing) next.set(target.clientKey, { ...(existing ?? {}), ...(seeded ?? {}), clientKey: target.clientKey });
  }
  return next;
};
```

Only call `replaceItems` if a semantic map comparison detects a real change, preventing needless renders.

- [ ] **Step 4: Preserve matching list-window data while a replacement request is pending**

In `loadListWindowUsage`, do not clear `listWindowUsageByKey` in the `catch` path for a canceled/stale request. On a current non-abort failure, retain the previous map only when its saved `listWindowUsageQueryContext.pageKeys` exactly matches `currentPageKeys`; clear only if the data belongs to another page. Continue accepting a new response only through `isCurrentRequest()`.

- [ ] **Step 5: Run hook/page tests and type-check**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx src/features/accounts/AccountsPage.test.tsx
npm --workspace apps/web run type-check
```

Expected: recovery badge stays stable, stale window requests cannot overwrite a different page, and TypeScript passes.

- [ ] **Step 6: Commit visible-evidence stabilization**

```powershell
git add apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "perf: stabilize visible credential evidence state"
```

## Task 6: Full verification and regression review

**Files:**
- Modify: `docs/superpowers/plans/2026-10-21-high-concurrency-credential-list-performance.md` only to tick completed checks if work is performed in place.

- [ ] **Step 1: Run all affected Go tests**

```powershell
Set-Location apps/manager-server
go test ./internal/repository/usageevent ./internal/repository/usagemonitoring ./internal/service/usagemonitoring ./internal/service/monitoring ./internal/http/controller/monitoring -count=1
```

Expected: all packages pass.

- [ ] **Step 2: Run all affected frontend tests and production build**

```powershell
Set-Location ..\..
npm --workspace apps/web run test -- src/features/accounts
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: test suite, TypeScript check, and production build exit 0.

- [ ] **Step 3: Inspect scope and custom-feature preservation**

Run:

```powershell
git diff d2cc05ca -- apps/manager-server apps/web docs/superpowers
git status --short
git log --oneline d2cc05ca..HEAD
```

Verify no changes exist under CPA Core, `cli-proxy-api`, billing write paths, raw credential storage, proxy replacement, Token Acquisition, auto-recovery, or priority-demotion server rule code.

- [ ] **Step 4: Commit plan progress only after all verification evidence is clean**

```powershell
git add docs/superpowers/plans/2026-10-21-high-concurrency-credential-list-performance.md
git commit -m "docs: record credential list performance verification"
```

- [ ] **Step 5: Do not deploy or push without an explicit user instruction**

Report exact test/build evidence and wait for LO to request deployment. If deployment is requested later, use the CPAM runbook’s dual-target procedures without touching CPA Core.
