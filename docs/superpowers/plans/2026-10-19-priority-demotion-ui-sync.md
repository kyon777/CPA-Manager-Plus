# Priority Demotion UI Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reflect completed server-error priority demotions in the visible credential list within seconds without reloading CPA credentials or invoking billing-related paths.

**Architecture:** Add a narrow authenticated Manager Server read endpoint over completed durable queue rows, then poll it only while the accounts workspace is active and visible. Apply returned values only when the visible row still equals the queue's recorded previous priority, preserving manual changes and avoiding full inventory refreshes.

**Tech Stack:** Go 1.24, SQLite, net/http, React 19, TypeScript, Axios, Vitest.

---

## File structure

- `apps/manager-server/internal/model/server_error_priority_demotion.go`: cursor and redacted update DTOs.
- `apps/manager-server/internal/repository/servererrorpriority/repository.go`: completed-demotion cursor query.
- `apps/manager-server/internal/repository/servererrorpriority/repository_test.go`: ordering/filtering/cursor tests.
- `apps/manager-server/internal/repository/sqlite/migrate.go`: terminal update index.
- `apps/manager-server/internal/http/controller/servererrorpriority/handler.go`: authenticated read-only endpoint.
- `apps/manager-server/internal/http/controller/servererrorpriority/handler_test.go`: handler tests.
- `apps/manager-server/internal/http/router/router.go`: route before generic CPA proxy.
- `apps/manager-server/internal/http/router/router_test.go`: proxy-bypass route test.
- `apps/web/src/services/api/usageService.ts`: typed Axios method and DTOs.
- `apps/web/src/services/api/usageService.serverErrorPriority.test.ts`: request contract test.
- `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.ts`: pure guarded local merge.
- `apps/web/src/features/accounts/model/serverErrorPriorityUpdates.test.ts`: local merge tests.
- `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts`: visibility-aware single-flight poller.
- `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx`: lifecycle tests.
- `apps/web/src/features/accounts/AccountsPage.tsx`: poller wiring.
- `docs/LO-CPAM-CUSTOM-MEMORY.md`: future merge retention record.

### Task 1: Repository cursor contract

**Files:** model, repository, repository tests, SQLite migration.

- [ ] **Step 1: Write the failing test**

Insert terminal rows with equal `completed_at_ms`, mixed terminal reasons and absent priority columns. Assert `ListCompletedDemotions(ctx, Cursor{CompletedAtMS: 1000, ID: 10}, 50)` returns only demoted rows ordered by `(completed_at_ms,id)` and advances to the final row.

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd apps/manager-server && go test ./internal/repository/servererrorpriority -run TestListCompletedDemotions -count=1`

Expected: FAIL because the cursor/page API does not exist.

- [ ] **Step 3: Write minimal implementation**

Add `ServerErrorPriorityDemotionCursor`, `ServerErrorPriorityDemotionUpdate`, `ServerErrorPriorityDemotionUpdatePage`, extend the repository interface, and query with:

```sql
and (completed_at_ms > ? or (completed_at_ms = ? and id > ?))
order by completed_at_ms asc, id asc
limit ?
```

Clamp `limit` to `1..200`, select only redacted fields, and add:

```sql
create index if not exists idx_server_error_priority_demotion_updates
on server_error_priority_demotion_jobs(state, completed_reason, completed_at_ms, id)
```

- [ ] **Step 4: Run focused tests**

Run: `cd apps/manager-server && go test ./internal/repository/servererrorpriority ./internal/repository/sqlite -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/manager-server/internal/model/server_error_priority_demotion.go apps/manager-server/internal/repository/servererrorpriority apps/manager-server/internal/repository/sqlite/migrate.go
git commit -m "feat: expose completed priority demotion deltas"
```

### Task 2: Authenticated Manager endpoint and route isolation

**Files:** new controller/handler tests; router/router test.

- [ ] **Step 1: Write failing tests**

Test `GET /v0/management/server-error-priority-demotions/updates` with a Panel Bearer key. Assert 200 returns only six redacted update fields; invalid numeric cursor or limit returns 400 and non-GET returns 405. Add a router test whose CPA upstream fails if reached; the Manager route must return the local response.

- [ ] **Step 2: Run failing tests**

Run: `cd apps/manager-server && go test ./internal/http/controller/servererrorpriority ./internal/http/router -run 'Test.*PriorityDemotion' -count=1`

Expected: FAIL because controller and route do not exist.

- [ ] **Step 3: Write minimal implementation**

Use `middleware.AuthorizePanel`, parse non-negative `after_completed_at_ms`, `after_id`, and `limit`, call the repository method, and serialize an update page. Insert the exact route branch before generic `/v0/management/` CPA proxy.

- [ ] **Step 4: Run focused tests**

Run: `cd apps/manager-server && go test ./internal/http/controller/servererrorpriority ./internal/http/router -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/manager-server/internal/http/controller/servererrorpriority apps/manager-server/internal/http/router
git commit -m "feat: add priority demotion update endpoint"
```

### Task 3: Frontend contract and guarded state merge

**Files:** `usageService.ts`, API test, new model/helper and model test.

- [ ] **Step 1: Write failing API and merge tests**

Assert Axios calls the Manager Server URL with the existing 240-second timeout, Panel Bearer key and cursor params. Assert the pure helper changes only a `fileName + authIndex + provider` match whose priority still equals `priorityBefore`; changed, provider-mismatched and unmatched rows remain unchanged.

- [ ] **Step 2: Run failing tests**

Run: `npm --workspace apps/web run test -- src/services/api/usageService.serverErrorPriority.test.ts src/features/accounts/model/serverErrorPriorityUpdates.test.ts`

Expected: FAIL because API method/helper do not exist.

- [ ] **Step 3: Write minimal implementation**

Add response/cursor types and `usageServiceApi.listServerErrorPriorityDemotionUpdates`. Implement a pure helper using `normalizeAuthIndex`; preserve the original file array when no row changes and always expose the newest cursor.

- [ ] **Step 4: Run focused tests**

Run: `npm --workspace apps/web run test -- src/services/api/usageService.serverErrorPriority.test.ts src/features/accounts/model/serverErrorPriorityUpdates.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.serverErrorPriority.test.ts apps/web/src/features/accounts/model/serverErrorPriorityUpdates.ts apps/web/src/features/accounts/model/serverErrorPriorityUpdates.test.ts
git commit -m "feat: merge priority demotion deltas locally"
```

### Task 4: Visibility-aware poller and page wiring

**Files:** new hook/hook test; `AccountsPage.tsx`; page test.

- [ ] **Step 1: Write failing hook/page tests**

With fake timers, assert polling starts only for active, visible, loaded account view with Manager scope; it remains single-flight and aborts on deactivation. Assert received deltas update the visible file array through `replaceFiles`, without calling `loadFiles`, quota refresh or history loading.

- [ ] **Step 2: Run failing tests**

Run: `npm --workspace apps/web run test -- src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx src/features/accounts/AccountsPage.test.tsx`

Expected: FAIL because poller/wiring do not exist.

- [ ] **Step 3: Write minimal implementation**

Use a one-second interval, an `AbortController`, a five-second startup overlap and no concurrent fetches. Feed the pure helper's output to `replaceFiles` only if a visible row changed. Do not call `loadFiles()` in this path.

- [ ] **Step 4: Run tests and static check**

Run:

```bash
npm --workspace apps/web run test -- src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx src/features/accounts/AccountsPage.test.tsx
npm --workspace apps/web run type-check
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "feat: sync visible priorities after server errors"
```

### Task 5: Preserve customization and verify/deploy

**Files:** `docs/LO-CPAM-CUSTOM-MEMORY.md`, design, plan.

- [ ] **Step 1: Record the customization**

Document the endpoint, read-only/no-billing invariant, `priorityBefore` guard, visibility gating, and retention during future CPAM merges.

- [ ] **Step 2: Run full verification**

Run:

```bash
cd apps/manager-server && go test ./...
cd ../..
npm --workspace apps/web run type-check
npm --workspace apps/web run test -- src/services/api/usageService.serverErrorPriority.test.ts src/features/accounts/model/serverErrorPriorityUpdates.test.ts src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx
npm --workspace apps/web run build
```

Expected: PASS. Capture unrelated pre-existing failures separately.

- [ ] **Step 3: Commit and inspect**

```bash
git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers
git commit -m "docs: record priority demotion UI sync"
git status --short
git log --oneline origin/lo/custom..HEAD
```

- [ ] **Step 4: Deploy only CPAM after verification**

Record CPA Core container/service identity before and after. Build the CPAM candidate from this clean worktree; deploy only local `cpa-manager-plus` and remote `cpam.service`; migrate only CPAM `usage.sqlite`; health-check the new endpoint and public panel. Never rebuild/restart/deploy CPA Core or `cli-proxy-api`.

## Plan self-review

- **Spec coverage:** Tasks 1–2 create the bounded authenticated redacted delta path; Tasks 3–4 implement safe local mutation and visibility-aware polling; Task 5 preserves and verifies the customization.
- **Placeholder scan:** no TODO/TBD or implicit test steps remain.
- **Type consistency:** backend uses `ServerErrorPriorityDemotionCursor`, `ServerErrorPriorityDemotionUpdate`, and `ServerErrorPriorityDemotionUpdatePage`; frontend uses camelCase serialization and the same `(completedAtMs,id)` cursor.
