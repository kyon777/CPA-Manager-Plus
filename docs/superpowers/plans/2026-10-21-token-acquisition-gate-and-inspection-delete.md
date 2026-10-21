# Token Acquisition Gate and Inspection Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound active Token Acquisition calls at forty without reducing the eighty-worker recovery pool, remove the unintended thirty-second request deadline, and repair credential-list deletion's two-confirmation chain.

**Architecture:** `tokenrecovery.Service` gets a dedicated acquisition semaphore independent from the existing write semaphore. The Token Acquisition client relies on its outer acquisition context instead of a conflicting client deadline. The credential-list page passes both delete warnings through the generic confirmation component's supported two-step contract.

**Tech Stack:** Go 1.24 service tests; React 19, Zustand, Vitest.

---

### Task 1: Protect Token Acquisition latency and capacity

**Files:**
- Modify: `apps/manager-server/internal/service/tokenacquisition/client.go`
- Modify: `apps/manager-server/internal/service/tokenacquisition/client_test.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: Write failing tests**

Add a client assertion that `New(Config{}).httpClient.Timeout == 0`, a service
assertion that eighty workers admit only forty blocking `Acquire` calls before
release, and an assertion that `ErrTimedOut` renders a specific redacted
message.

- [ ] **Step 2: Run focused Go tests and verify RED**

Run: `cd apps/manager-server && go test ./internal/service/tokenacquisition ./internal/service/tokenrecovery -count=1`

Expected: the new default-client, acquisition-gate, and timeout-message
assertions fail against the existing thirty-second client and unlimited
acquisition behavior.

- [ ] **Step 3: Implement the minimal gate and message**

Use `http.Client{}` for CPAM's default Token Acquisition client. Add
`AcquisitionConcurrency`, an `acquisitionGate`, and context-aware
acquire/release helpers to `tokenrecovery.Service`; default and maximum active
acquisitions are forty. Hold this gate only around `s.acquirer.Acquire`. Add
the bounded Chinese `ErrTimedOut` message before the generic fallback.

- [ ] **Step 4: Run focused Go tests and verify GREEN**

Run: `cd apps/manager-server && go test ./internal/service/tokenacquisition ./internal/service/tokenrecovery -count=1`

Expected: all selected package tests pass.

### Task 2: Repair credential-list deletion's two-confirmation chain

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Write a failing lifecycle test**

Update the current-page banned-delete test to assert one confirmation object
contains `secondConfirmation`, then invoke its final `onConfirm` callback and
assert one delete request. Assert a nested second `showConfirmation` call is
not made.

- [ ] **Step 2: Run focused Vitest and verify RED**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx`

Expected: the test fails because current code opens a second confirmation from
inside the first confirmation's loading callback instead of passing the
supported `secondConfirmation` configuration.

- [ ] **Step 3: Implement the explicit sequence**

Replace the nested `openSecondConfirmation` callback with a single
`showConfirmation` call containing `secondConfirmation`. Keep the existing
destructive target snapshot and invoke `deletePageAccountBannedCredentials`
only in the final confirmation callback.

- [ ] **Step 4: Run focused Vitest and verify GREEN**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx`

Expected: all lifecycle tests pass.

### Task 3: Verify production build boundaries

**Files:**
- No additional files.

- [ ] **Step 1: Run manager-server tests**

Run: `cd apps/manager-server && go test ./... -count=1`

- [ ] **Step 2: Run web type-check and bundle build**

Run: `npm --workspace apps/web run type-check && npm --workspace apps/web run build`

- [ ] **Step 3: Inspect the diff**

Run: `git diff --check && git diff --stat`

Expected: no whitespace errors; only CPAM source, focused tests, and this
design/plan documentation are changed.
