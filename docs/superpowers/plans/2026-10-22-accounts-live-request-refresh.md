# Accounts list live request refresh

> **For ENI:** Execute this plan in the isolated `feat/accounts-live-request-refresh-20261022` worktree. Do not modify CPA Core, `cli-proxy-api`, billing paths, or the upstream remote.

**Goal:** Keep the Accounts list's visible request evidence current during high-concurrency use without returning to whole-page heavy refreshes or blocking the browser UI.

**Architecture:** Split passive list polling into independent, scoped read-only data streams. Current-page compact request evidence refreshes every 10 seconds; current-page history and quota-window usage refresh every 60 seconds; the global compact recent-request index refreshes every 30 seconds only while sorting by latest request. Each stream has a context-aware single-flight request guard so a slow prior request is not repeatedly aborted and restarted. Header/inspection polling remains independent.

**Tech stack:** React + TypeScript, Vitest, existing CPAM monitoring APIs (`account-recent-index`, `account-history`, `account-window-usage`).

## Execution tasks

### 1. Establish polling behavior with failing focused tests

- [ ] Update `apps/web/src/features/accounts/AccountsPage.test.tsx` to cover visible-page recent evidence at 10 seconds, page history/window usage at 60 seconds, and global recent-index refresh only for latest-request sorting.
- [ ] Cover slow `account-window-usage` behavior: a timer must not cancel/restart the same in-flight current-page request.
- [ ] Run the focused AccountsPage test and confirm the new assertions fail before production code changes.

### 2. Split compact request evidence from history refresh

- [ ] Add a current-page recent-index loader with `recent_limit: 9`, scoped abort handling, and immediate load on page/context changes.
- [ ] Use its recent evidence for visible card timestamps/bars and status rendering, while retaining detailed history totals for historical usage display.
- [ ] Preserve a separate global compact index for latest-request sorting; poll it only when that sort is active.

### 3. Make polling non-disruptive

- [ ] Add 10s/30s/60s visibility-gated intervals for the three streams.
- [ ] Keep the existing passive 60s header/inspection polling, but do not let header snapshot churn restart quota-window requests.
- [ ] Make current-page quota-window loading context-aware and single-flight; abort only when the page/target context changes.

### 4. Verify and ship

- [ ] Run focused UI tests, TypeScript/build checks, and inspect the diff for billing/API mutation regressions.
- [ ] Commit the verified CPAM-only change in the feature branch.
- [ ] Deploy the same candidate only to CPAM local Docker and server `/home/cpam`; preserve CPA Core/container/service identities and server data/config.
