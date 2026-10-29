# Current-Page Account History Only Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every automatic full-inventory account-history request while preserving exact current-page price/history, full credential inventory, and global recent-request behavior.

**Architecture:** `AccountsPage` will have only `current` and `single` account-history scopes. The normal current-page scope continues to use cached/deduplicated visible targets, and explicit toolbar refresh forces the same visible targets by incrementing its revision. No server data model or CPA Core path changes.

**Tech Stack:** React, TypeScript, Vitest, existing Manager `/v0/management/monitoring/account-history` API.

---

### Task 1: Lock the no-full-history contract with a focused frontend test

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`

- [ ] **Step 1: Add a failing test with more than one visible page**

Add a test that supplies 51+ credentials, renders the account page, waits for
the initial monitoring request, and asserts that every initial
`getAccountHistory` call has at most the visible-page target count and never
contains an account from page two. Toggle live refresh and invoke the toolbar
refresh callback; assert both also request only the current visible page.

- [ ] **Step 2: Run the focused test and observe failure**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --reporter=dot
```

Expected before implementation: a call includes page-two account targets due
to the `all` scope's `accountHistoryFullTargets`.

### Task 2: Remove full-inventory history scheduling

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:579,1591,1887-1906,5172-5309,7280-7622,2645,2726`

- [ ] **Step 1: Reduce history scope state to `current | single`**

Remove `all` from `AccountHistoryLoadScope`, delete full-history state, refs,
context/load keys, full-target construction, and cleanup branches. Keep
`accountHistoryTargets` based on `pageRows` plus an explicitly opened detail
row.

- [ ] **Step 2: Make normal history loading current-page only**

Use `accountHistoryTargets` for ordinary loads, retain only current and
single abort controllers, and keep the existing current-page session cache,
request-version protection, and obsolete-page cancellation.

- [ ] **Step 3: Replace full-refresh revisions with current-page revisions**

In `refreshActiveWorkspace` and the live-refresh toggle handler, increment
`accountHistoryAutoRefreshRevision` only. Remove the initial/full-load effect
and let the ordinary current-page effect schedule initial, filter/page, live,
and toolbar-triggered reads.

- [ ] **Step 4: Run the focused test and verify it passes**

Run the Task 1 command. Expected: the test passes and all asserted history
payloads contain only visible-page targets.

### Task 3: Record the permanent merge constraint

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [ ] **Step 1: Append the current-page-only rule**

Document that normal account-history / price refreshes must never build
`accountHistoryFullTargets`, run `scope: 'all'`, or schedule all-inventory
history from initial load, live-refresh enablement, or toolbar refresh.
Document that this does not affect the full auth-file inventory, global recent
index, exact requested-account pricing recovery, or billing facts.

### Task 4: Bounded verification and release commit

**Files:**
- Verify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Verify: `apps/web`

- [ ] **Step 1: Run only the focused test file**

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --reporter=dot
```

- [ ] **Step 2: Run TypeScript verification**

```powershell
npm --workspace apps/web run type-check
```

- [ ] **Step 3: Run the production web build**

```powershell
npm --workspace apps/web run build
```

- [ ] **Step 4: Commit the change**

```powershell
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "fix: restrict account history to visible page"
```

No full test suite is part of this urgent performance correction.
