# CPAM Accounts UI Request Storm Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop credential-list refresh churn from repeatedly aborting/restarting expensive Manager SQLite monitoring reads while preserving current-page live request, history, quota, recovery, and priority-update behavior.

**Architecture:** Stabilize request identities against harmless row reordering; gate one-shot operational/inspection reads per Manager scope; slow only the low-value priority-update long poll. Explicit toolbar refreshes and mutation-driven reloads retain their existing direct calls. No raw usage events, pricing records, CPA Core calls, or billing code changes.

**Tech Stack:** React 19, TypeScript, Vitest, Go Manager Server (unchanged for this patch).

---

### Task 1: Stable visible-page request identity

**Files:**
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts`
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Test: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Write failing tests** proving that recovery-task lookup does not restart when only visible-row order changes, and that current-page history/window requests are not cancelled/restarted for the same identity set.
- [ ] **Step 2: Run focused Vitest tests** and confirm failures are due to the current order-sensitive identities.
- [ ] **Step 3: Implement canonical identity signatures** for read lifecycles only; preserve original visual/request ordering where the response mapping needs it.
- [ ] **Step 4: Run focused tests** and confirm the same-identity reorder no longer generates a second backend read.

### Task 2: Gate automatic operational and inspection reads

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Write failing tests** proving mount effects load inspection/cooldown/action candidates once per scope/context rather than on callback recreation.
- [ ] **Step 2: Run focused Vitest tests** and confirm the duplicate auto-load behavior fails the assertion.
- [ ] **Step 3: Add refs keyed by Manager connection and relevant detail/filter scope** to suppress duplicate automatic loads while retaining explicit mutation/toolbar loads.
- [ ] **Step 4: Run focused tests** and confirm the initial UI still loads the needed data once.

### Task 3: Reduce noncritical delta polling pressure

**Files:**
- Modify: `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.ts`
- Modify: `apps/web/src/features/accounts/hooks/useServerErrorPriorityUpdates.test.tsx`

- [ ] **Step 1: Write failing test** that expects no second priority-update poll before five seconds and one at five seconds.
- [ ] **Step 2: Run the hook test** and observe the existing one-second poll violates it.
- [ ] **Step 3: Change only the delta poll interval to five seconds**; immediate poll and cursor semantics stay intact.
- [ ] **Step 4: Run the hook test** and confirm it passes.

### Task 4: Verification and deployment

**Files:**
- Modify: none beyond the above.

- [ ] **Step 1: Run focused frontend tests, type-check, and production frontend build.**
- [ ] **Step 2: Build the native/local candidate and run relevant Go tests without changing CPA Core.**
- [ ] **Step 3: Deploy exactly the verified CPAM candidate locally and natively on `/home/cpam`; preserve `usage.sqlite`, `data.key`, `/home/cpa`, and `cpa.service`.**
- [ ] **Step 4: Verify health, CPAM identity, unchanged CPA Core service identity, and post-deploy request timing.**
- [ ] **Step 5: Commit and push only `origin lo/custom`; do not push upstream.**