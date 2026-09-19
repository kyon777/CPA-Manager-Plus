# Account-Banned Page Deletion and Localization Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair literal-question-mark account messages and add a safe current-page action that deletes only Codex credentials with terminal error code `account_banned`.

**Architecture:** Derive eligible rows in a pure Accounts model helper. Pass their existing `AuthFileItem` values to `useAuthFilesData().batchDelete`, preserving existing confirmations, source-file membership checks, identity verification, and refresh behavior. Repair locale JSON at the source and use the existing proxy-label key.

**Tech Stack:** React, TypeScript, Vitest, react-test-renderer, react-i18next JSON resources, existing CPA auth-files deletion API.

---

### Task 1: Add the banned-row selector with TDD

**Files:** `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts` and its `.test.ts`.

- [ ] Write a failing test importing `buildPageAccountBannedRows`. Construct Codex rows with terminal `manual_failed_manual_only` + `account_banned`, terminal `auto_failed_manual_only` + spaced/case-variant `ACCOUNT_BANNED`, a pending task, a runtime-only row, an xAI row, and missing/blank codes. Assert only the first two original rows are returned in input order.
- [ ] Run `$env:TZ='Asia/Shanghai'; npm --workspace apps/web run test -- src/features/accounts/model/credentialRuntimeMetadata.test.ts --maxWorkers=1 --no-file-parallelism`; expect failure because the export is absent.
- [ ] Export the selector. Reuse `normalizeProvider`; exclude runtime-only/non-Codex rows; accept only `auto_failed_manual_only` or `manual_failed_manual_only`; compare `String(lastErrorCode ?? '').trim().toLowerCase()` exactly to `account_banned`; return original rows.
- [ ] Run the focused test again and require green.
- [ ] Commit with `git add apps/web/src/features/accounts/model/credentialRuntimeMetadata.*; git commit -m "feat: identify current-page banned credentials"`.

### Task 2: Add the Accounts danger action with TDD

**Files:** `apps/web/src/features/accounts/AccountsPage.tsx` and `.test.tsx`.

- [ ] Add a failing page test with one visible banned runtime task and one healthy row. Assert a danger button containing `accounts.batch_delete_banned_page:1`; invoke it and assert `mocks.batchDelete` receives only the banned row's raw file and options with `confirmText: 'common.delete'`. Add a zero-candidate assertion that the button is disabled.
- [ ] Run `$env:TZ='Asia/Shanghai'; npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --maxWorkers=1 --no-file-parallelism`; expect failure because selector/button/key are absent.
- [ ] Import the selector, derive `pageAccountBannedRows` from `pageRows` plus `credentialRuntimeItemsByClientKey`, and map to raw files. In `renderBatchBar`, when no selection/selection mode is active, render a `variant="danger"` trash button with count and disabled state for controls/zero candidates. Its handler calls existing `batchDelete` with a localized `AccountsBatchDeletePreview` and `confirmText: t('common.delete')`; do not add a new API.
- [ ] Rerun the focused Accounts test and require green.
- [ ] Commit with `git add apps/web/src/features/accounts/AccountsPage.*; git commit -m "feat: delete current-page banned credentials"`.

### Task 3: Repair localization and proxy-label wiring with regression tests

**Files:** `apps/web/src/i18n/locales/{zh-CN,zh-TW,ru}.json`, `AccountsPage.tsx`, `AccountsPage.test.tsx`, and `accountsWorkspaceWiring.test.ts`.

- [ ] Add locale assertions over `[en, ru, zhCN, zhTW]` for the eight recovery keys (`batch_recovery_page`, `recovery_auto_processing`, `recovery_manual_processing`, `recovery_succeeded`, `recovery_failed_reason`, `recovery_failed_generic`, `batch_recovery_result`, `batch_recovery_unavailable`): strings, no literal `?`, and required `{{count}}`/`{{reason}}` placeholders. Update proxy expectations to `auth_files.proxy_url_label`.
- [ ] Run `$env:TZ='Asia/Shanghai'; npm --workspace apps/web run test -- src/features/accounts/accountsWorkspaceWiring.test.ts src/features/accounts/AccountsPage.test.tsx --maxWorkers=1 --no-file-parallelism`; expect RED from `????` values and old proxy key.
- [ ] Replace only the eight corrupted values in each affected locale. Use Chinese Simplified: `更新当前页凭证 ({{count}})`, `自动更新中`, `手动更新中`, `凭证已更新`, `更新失败：{{reason}}`, `更新失败`, `已提交 {{count}} 个凭证更新任务`, `批量更新请求失败`; Traditional Chinese: `更新目前頁憑證 ({{count}})`, `自動更新中`, `手動更新中`, `憑證已更新`, `更新失敗：{{reason}}`, `更新失敗`, `已提交 {{count}} 個憑證更新任務`, `批次更新請求失敗`; Russian equivalents preserving placeholders. Change both grid/table renderers to `t('auth_files.proxy_url_label')`.
- [ ] Rerun the focused command and require green.
- [ ] Commit with `git add apps/web/src/i18n/locales apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts; git commit -m "fix: restore account recovery localization and proxy label"`.

### Task 4: Full verification and handoff

- [ ] Run `$env:TZ='Asia/Shanghai'; npm --workspace apps/web run test -- --maxWorkers=1 --no-file-parallelism`; all tests must pass.
- [ ] Run `npm --workspace apps/web run type-check`, `npm --workspace apps/web run build`, and `git diff --check`; all must exit 0.
- [ ] Review `git status --short` and `git log --oneline -5`; feature worktree must be clean.
- [ ] Use `finishing-a-development-branch` for merge/deploy choices. Any later push targets the user's fork `origin` only; never push `upstream`.
