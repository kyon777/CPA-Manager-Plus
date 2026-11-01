# Codex Quota Refresh Parallelism and First-Paint Protection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refresh up to forty distinct Codex credentials concurrently while preserving credential deduplication, stopping queued provider work after a rate limit, and keeping heavy current-page usage aggregation off the initial interactive paint.

**Architecture:** Extend the existing provider/credential scheduler with a provider-key concurrency resolver. `AccountsPage` supplies `40` only for Codex and retains `1` for all other providers. The existing batch state gates only automatic list-level `account-window-usage` scheduling; it never cancels a running request or changes a detail drawer request. The first list-level window aggregation is submitted through an idle scheduler after the credential list has painted.

**Tech Stack:** React 19, TypeScript, Vitest, existing Manager monitoring APIs.

---

### Task 1: Lock provider-specific scheduler semantics

**Files:**
- Modify: `apps/web/src/utils/quota/providerRefreshScheduler.test.ts`
- Modify: `apps/web/src/utils/quota/providerRefreshScheduler.ts`

- [ ] **Step 1: Write failing scheduler tests**

Add a test with Codex and non-Codex tasks that passes a `(providerKey) => number`
resolver. Track active calls by provider and assert Codex reaches its requested
parallelism while the other provider remains at one. Keep the existing
same-provider dedupe assertion as the permanent unique-credential proof.

- [ ] **Step 2: Run the focused scheduler test and observe RED**

```powershell
npm --workspace apps/web run test -- src/utils/quota/providerRefreshScheduler.test.ts --reporter=dot
```

Expected before implementation: TypeScript/test failure because
`perProviderConcurrency` accepts only a number.

- [ ] **Step 3: Add the smallest resolver-compatible scheduler type**

Allow `perProviderConcurrency` to be either a number or a function receiving
the normalized provider key. Resolve that value immediately before each
provider queue is passed to `mapWithConcurrency`; clamp invalid values using
the existing queue helper.

- [ ] **Step 4: Re-run the focused scheduler test and verify GREEN**

Run the Task 1 command. Expected: all scheduler tests pass.

### Task 2: Apply Codex 40-way batch refresh and retain 429 queue stop

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`

- [ ] **Step 1: Write failing AccountsPage regressions**

Add a test with 41 distinct Codex files and deferred summary requests. Click
the batch refresh button and assert the first forty unique credentials start
before any deferred response resolves; resolve one and assert the forty-first
starts. Add a second 41-file test where the first completion returns HTTP 429
while the other initial requests remain deferred; assert the queued forty-first
task is never dispatched.

- [ ] **Step 2: Run each focused regression and observe RED**

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --reporter=dot
```

Expected before implementation: only one Codex summary request is active, so
the forty-way assertion fails.

- [ ] **Step 3: Make only Codex use 40-way concurrency**

Replace the fixed per-provider batch value with a resolver returning `40` for
normalized `CODEX_CONFIG.type` and `1` otherwise. Keep the existing batch
promise, normalized credential task plan, and `blockedProviders` check. Do not
add retries or cancellation for HTTP 429.

- [ ] **Step 4: Re-run the focused AccountsPage test and verify GREEN**

Run the Task 2 command. Expected: the existing single/batch quota flows and
the two new concurrency/rate-limit regressions pass.

### Task 3: Protect the list analytics request from batch and first-paint contention

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`

- [ ] **Step 1: Write failing behavior tests**

Use a deferred Codex summary batch and mock `getAccountWindowUsage`. Assert a
batch success revision does not start the second list usage request until the
batch resolves. Add an initial-list test with a mocked idle callback to prove
the visible list renders before the first `getAccountWindowUsage` call and
then starts it when idle work runs.

- [ ] **Step 2: Run the focused test and observe RED**

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --reporter=dot
```

Expected before implementation: the list effect invokes the heavy query
immediately and can consume its refresh revision during the active batch.

- [ ] **Step 3: Add narrow list-only scheduling gates**

Use a first-paint idle callback (with a finite timeout fallback) only for the
initial list-level request. While `quotaRefreshing` is true, retain the current
auto-load key as pending without starting/cancelling a list request. Once the
batch ends, schedule exactly one ordinary request for that key. Do not alter
`loadAccountWindowUsage`, drawer logic, history requests, timeout values, or
in-flight cancellation semantics.

- [ ] **Step 4: Re-run the focused AccountsPage test and verify GREEN**

Run the Task 3 command. Expected: list usage begins after idle first paint,
does not duplicate inside the batch, and refreshes normally after it ends.

### Task 4: Preserve the deployment contract and verify the web package

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Verify: `apps/web/src/utils/quota/providerRefreshScheduler.test.ts`
- Verify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Verify: `apps/web`

- [ ] **Step 1: Append the permanent custom behavior**

Record Codex=40, non-Codex=1, strict credential dedupe, queued-only 429 stop,
batch list-usage deferral, and first-paint idle scheduling. State that no
detail drawer, billing, pricing, or CPA Core behavior changes.

- [ ] **Step 2: Run focused verification**

```powershell
npm --workspace apps/web run test -- src/utils/quota/providerRefreshScheduler.test.ts src/features/accounts/AccountsPage.test.tsx --reporter=dot
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

- [ ] **Step 3: Commit the bounded frontend-only change**

```powershell
git add apps/web/src/utils/quota/providerRefreshScheduler.ts apps/web/src/utils/quota/providerRefreshScheduler.test.ts apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/specs/2026-11-01-quota-refresh-parallel-first-paint-design.md docs/superpowers/plans/2026-11-01-quota-refresh-parallel-first-paint.md
git commit -m "fix: parallelize codex quota refresh"
```
