# Paged Credential List Semantics and Runtime Read Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Preserve bounded paged credential loading and direct proxy display while removing page-as-global health confusion and raw JSON runtime downloads from normal list rendering.

**Architecture:** The Manager list service enriches only current Codex rows with existing redacted Token Recovery task state read from CPAM SQLite. The browser seeds its runtime-state hook from those rows and uses the existing small task batch polling only for pending work. Metric cards state whether their evidence is global or current-page; an enabled query uses its complete server-side query total without relabeling it as health-confirmed.

**Tech Stack:** Go `net/http`/existing CPAM SQLite services, React/TypeScript, Vitest, Go `testing`.

---

### Task 1: Embed safe recovery task state in the paged Manager response

**Files:**
- Modify: `apps/manager-server/internal/service/credentiallist/service.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/service/credentiallist/service_test.go`

- [x] Write `TestListEmbedsVisibleCodexRecoveryTaskWithoutCredentialDownload`: a fake recovery reader returns a durable task for one visible Codex file; assert the page includes only the redacted task and the JSON response still omits `access_token` / `refresh_token`.
- [x] Run `go test ./internal/service/credentiallist -run TestListEmbedsVisibleCodexRecoveryTaskWithoutCredentialDownload -count=1` and confirm it fails before implementation.
- [x] Add a narrow `RecoveryReader` dependency to the list service, retain existing constructors for compatibility, and have the app wire the existing Token Recovery service into the new constructor.
- [x] Enrich cloned visible list items only; recover by `name`, `auth_index`, email/account snapshot, and provider `codex`; ignore individual lookup errors so inventory remains available.
- [x] Re-run the targeted Go test and commit the isolated backend change.

### Task 2: Seed browser recovery state from the list response without runtime metadata fetches

**Files:**
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts`
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts`
- Modify: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`

- [x] Write a hook test with a seeded pending task and `fetchMetadata: false`; assert `getCredentialRuntimeMetadata` is never called and the existing token-recovery batch poll still receives the exact current target.
- [x] Run the test and confirm it fails because the option/seed path does not exist.
- [x] Add a validated conversion from a list row's `recovery_task` to a redacted runtime item keyed by the existing client selection key.
- [x] Add hook options `seedItems` and `fetchMetadata` (default true); seed task state before polling, skip metadata fetch only when explicitly disabled, and preserve legacy behavior for fallback mode.
- [x] In AccountsPage, enable no-download seeded mode only when the Manager paged list is active; otherwise retain legacy runtime metadata fallback.
- [x] Re-run focused hook and Accounts tests and commit the isolated frontend runtime change.

### Task 3: Make metric scope truthful in paged mode

**Files:**
- Modify: `apps/web/src/features/accounts/components/AccountMetricsGrid.tsx`
- Modify: `apps/web/src/features/accounts/components/AccountMetricsGrid.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] Write a component test that passes paged metric scope and asserts health cards use current-page labels; pass enabled-query scope and assert the green card uses the full enabled label.
- [x] Run the test and confirm it fails before adding props/labels.
- [x] Add an explicit metric presentation mode. The global total remains unchanged; only a complete server enabled query can use its server `total` in the green card, labelled `Enabled`; all evidence-derived cards explicitly say `Current page` in paged mode.
- [x] Thread the mode from AccountsPage without modifying metric classification, pagination, scheduler, usage, or quota values.
- [x] Re-run component/model tests and commit the isolated semantics change.

### Task 4: Regression checks and memory

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Modify: `docs/superpowers/specs/2026-10-15-paged-list-semantics-runtime-repair-design.md`

- [x] Run Go credential-list tests, focused Vitest suites, `npm run build`, and `git diff --check`.
- [x] Inspect diff to confirm only CPAM files changed and no collector, usage event, billing, CPA Core, or Core deployment file changed.
- [x] Add the finalized behavior to custom memory so future CPAM updates preserve it.
- [x] Commit all changes on `lo/custom`; deployment/push remain a separate explicit user step.
