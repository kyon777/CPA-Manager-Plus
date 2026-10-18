# Health Inspection Current-Page Account Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the existing account-list batch actions to the *local Health Inspection → Re-login* result toolbar: manually update credentials for the visible page and delete only visible-page credentials whose latest recovery task is terminal `account_banned`. Preserve the original two account-list buttons and make banned-account deletion bounded, safe, and materially faster than the existing N × full-inventory delete loop.

**Architecture:** The browser will submit minimal, identity-only candidates to a new CPAM Manager Server endpoint. The endpoint verifies each candidate against its latest Token Recovery task, snapshots the CPA auth-file inventory once, verifies full physical-file membership before mutation, deletes independent physical files through the existing per-file mutation coordinator with four bounded workers, and invalidates the auth-file cache once after actual mutation. The web app uses one shared account-banned mapper/classifier for the account list and inspection page, updates its local list/snapshot optimistically from the server result, and never triggers a quota fetch, health inspection, or CPA Core scheduler action as a side effect.

**Tech Stack:** Go (Manager Server, `net/http`, existing CPAAuthorFiles client/mutation coordinator), React/TypeScript, Vitest/react-test-renderer, existing i18n JSON catalogs, Docker/native deployment unchanged.

---

## Non-negotiable constraints

- Only change CPAM. Do **not** modify, build, restart, or deploy CPA Core / `cli-proxy-api`.
- Keep both existing account-list controls: `更新当前页凭证 (N)` and `删除当前页已封禁账号 (M)`. The latter can migrate to the safer batch API, but generic selected-file deletion remains untouched.
- Add the two controls only to **local** Health Inspection when its active action filter is `reauth`; do not add them to the server-side inspection page.
- Never send, persist, log, or return token material, management keys, raw credential JSON, or proxy credentials.
- A physical CPA file may be deleted only when every current member in its fresh CPA inventory entry is included in the request and independently verified as terminal `account_banned`.
- The new endpoint may read CPA auth-file inventory once per batch and invalidate derived auth-file caches once only after one or more physical files were actually deleted.
- No post-action quota refresh, historical usage fetch, active inspection run, scheduler signal, or billing/usage mutation. Import/deletion remains independent of CPA scheduling.
- Keep the batch cap at 100 logical candidates and deletion concurrency at 4 physical files; do not move concurrency control into the browser.
- No deployment or remote push is part of this plan.

## Baseline and acceptance evidence

- Baseline branch: `work/inspection-batch-actions-20261017` from `lo/custom` commit `d399e2d5cd51153c9dd5c13bfc4e22aacbef090d`.
- Design record: `docs/superpowers/specs/2026-10-17-inspection-batch-actions-design.md` (`0188dea4`).
- `npm ci` and `npm run manager-server:test` pass at baseline. `npm test` has a pre-existing `DemoPage.test.tsx` relative-date failure and can stall after reporting it; treat that separately from this change.
- Success means a re-login-filter page can submit a one-click manual recovery batch and a one-click terminal-ban delete batch, both controls show exact current-page counts, account-list controls still exist, shared/single physical-file safety holds, and a batch of N banned files performs one CPA inventory read instead of N reads.

## API contract

### `POST /v0/management/auth-files/delete-account-banned-batch`

Authenticated with the existing panel authorization middleware. Request body is strictly bounded (64 KiB) and has this root shape:

```json
{
  "targets": [
    {
      "clientKey": "inspection:auth-file.json:0",
      "fileName": "auth-file.json",
      "authIndex": "0",
      "accountEmail": "account@example.com",
      "provider": "codex",
      "runtimeId": "runtime-row-id",
      "accountId": "account-id-snapshot",
      "accountSnapshot": "account@example.com"
    }
  ]
}
```

`clientKey`, `fileName`, normalized `provider=codex`, and at least one recovery locator (`authIndex` or `accountEmail`) are required. `runtimeId` is optional at input but an unresolvable target is returned as a skipped item rather than permitting filename-only deletion. Maximum 100 targets. Unknown root/target fields, malformed JSON, oversized bodies, unsupported providers, and structurally ambiguous requests return `400`; missing authorization returns `401`.

Successful request response is always a safe summary:

```json
{
  "requested": 2,
  "deleted": [{"clientKey":"…","fileName":"…","physicalName":"…"}],
  "skipped": [{"clientKey":"…","fileName":"…","code":"shared_file_incomplete","message":"当前共享凭证文件存在未验证成员，已跳过删除"}],
  "failed": [{"clientKey":"…","fileName":"…","code":"delete_failed","message":"删除凭证文件失败"}]
}
```

Result arrays remain deterministic in request order. Messages must be bounded/redacted and never echo sensitive upstream responses.

## Implementation tasks

### Task 1 — Extract a preloaded CPAAuthorFiles physical-delete resolver

**Files:**
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client.go`
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client_test.go`

- [ ] **Write failing tests first** for a new pure exported helper, e.g. `ResolveVerifiedPhysicalFileDeleteTargetFromInventory(files []File, identities []Identity) (DeleteMutationTarget, error)`.
  - Provide a multi-member physical file in a supplied `[]File`; assert it chooses the physical selector only when every current member has a matching identity.
  - Assert an incomplete supplied identity set fails rather than selecting a physical file.
  - Assert runtime-ID/physical-name selector collisions fail.
  - Assert a one-member file uses the verified runtime selector.
  - Assert the helper causes no HTTP request; run the test with an `httptest.Server` counter and verify it remains zero.
- [ ] Run the focused test before implementation and record its expected compile/failure state:
  ```powershell
  go test ./apps/manager-server/internal/service/cpaauthfiles -run 'TestResolveVerifiedPhysicalFileDeleteTargetFromInventory' -count=1
  ```
- [ ] Refactor the existing network-backed `resolveVerifiedPhysicalFileDeleteTarget` path so it fetches its inventory as before, then delegates all membership, collision, identity, and selector selection decisions to the new pure helper.
- [ ] Preserve current exact semantics: all identities must name the same physical file; membership count must exactly match fresh inventory; each identity is verified; multi-member files use physical-file selector only when safe; one-member files require a runtime selector.
- [ ] Run the package test suite:
  ```powershell
  go test ./apps/manager-server/internal/service/cpaauthfiles -count=1
  ```

### Task 2 — Centralize terminal-account-banned classification and build the bounded deletion service

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`
- Create: `apps/manager-server/internal/service/authfilesbanneddelete/service.go`
- Create: `apps/manager-server/internal/service/authfilesbanneddelete/service_test.go`

- [ ] **Write failing Token Recovery classifier tests first.** Add/export `IsTerminalAccountBanned(task model.TokenRecoveryTask) bool` and verify all of:
  - terminal `auto_failed_manual_only` / `manual_failed_manual_only` plus `last_error_code=account_banned` is true;
  - terminal failure with `account_banned:` prefix in the message is true;
  - `401`, a normal recovery failure, or a nonterminal queued/running task is false;
  - matching is case/whitespace tolerant only for the existing documented code/message form.
- [ ] Run classifier tests before implementation:
  ```powershell
  go test ./apps/manager-server/internal/service/tokenrecovery -run 'TestIsTerminalAccountBanned' -count=1
  ```
- [ ] Implement the classifier with the same semantics later used by the web mapper; do not inspect tokens, raw JSON, or proxy values.
- [ ] **Write failing deletion-service tests first** using fakes for setup resolution, recovery lookup, auth-file fetch/delete, mutation coordinator, and cache invalidator. Cover:
  1. a successful two-file request performs exactly one CPA `Fetch`, deletes each independent physical file once, and invalidates cache once;
  2. N targets do not call a re-validating helper / repeated inventory fetch;
  3. terminal `account_banned` is required; missing record, nonterminal record, normal `401`, and non-ban failure are skipped with safe codes;
  4. a shared physical file with a missing/ineligible member is skipped without any delete;
  5. stale/colliding identity is skipped without any delete;
  6. per-physical-file locking prevents duplicate physical deletion; worker count never exceeds four;
  7. delete failure produces a bounded safe `failed` item, while other independent files still complete;
  8. result item order follows original logical request order and cache invalidation is not called if zero physical files were deleted;
  9. serialized result does not contain token, management-key, raw JSON, or proxy-secret test sentinels.
- [ ] Run the service test before implementation:
  ```powershell
  go test ./apps/manager-server/internal/service/authfilesbanneddelete -count=1
  ```
- [ ] Implement `authfilesbanneddelete.Service` with these interfaces and defaults:
  - `MaxTargets = 100`, `MaxConcurrency = 4`;
  - resolve setup once only after at least one candidate is recovery-eligible;
  - use `tokenrecovery.Service.Get` and `IsTerminalAccountBanned` per logical candidate;
  - make exactly one fresh `cpaauthfiles.Client.Fetch` call for the eligible batch;
  - group accepted candidates by physical `fileName`, translate to `cpaauthfiles.Identity`, and use Task 1’s preloaded resolver;
  - acquire the existing shared `MutationCoordinator` lock per physical file, call the direct `cpaauthfiles.Client.Delete` only after safe resolution, and never fall back to the generic proxy delete path;
  - use a four-worker bounded queue for independent physical files;
  - invalidate `ProxyService.InvalidateAuthFileInventory` once after any successful physical delete;
  - return target-level safe `deleted`, `skipped`, and `failed` records.
- [ ] Re-run both packages:
  ```powershell
  go test ./apps/manager-server/internal/service/tokenrecovery ./apps/manager-server/internal/service/authfilesbanneddelete -count=1
  ```

### Task 3 — Expose the service through a strict Manager Server endpoint

**Files:**
- Create: `apps/manager-server/internal/http/controller/authfilesbanneddelete/handler.go`
- Create: `apps/manager-server/internal/http/controller/authfilesbanneddelete/handler_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/http/router/router.go`
- Modify/add router tests in `apps/manager-server/internal/http/router/` as appropriate

- [ ] **Write failing handler tests first** for:
  - unauthenticated request is rejected by the existing panel authorization behavior;
  - exact valid POST delegates targets and returns the safe service result;
  - invalid method/path body, invalid provider, missing required locator, unknown fields, >100 targets, and >64 KiB body return `400`;
  - no target data beyond safe identifiers/codes/messages is emitted;
  - the exact route reaches the dedicated handler before the generic `/v0/management/` proxy fallback.
- [ ] Run endpoint/router tests before implementation:
  ```powershell
  go test ./apps/manager-server/internal/http/controller/authfilesbanneddelete ./apps/manager-server/internal/http/router -count=1
  ```
- [ ] Add an `AccountBannedAuthFileDeleteService` field to `app.Context`, constructed once from the existing `managerConfigService`, `tokenRecoveryService`, shared `authFileMutationCoordinator`, CPAAuthorFiles client, and proxy cache invalidator.
- [ ] Register the handler on exact `POST /v0/management/auth-files/delete-account-banned-batch` before generic management proxy routing. Reuse `middleware.AuthorizePanel`.
- [ ] Keep existing auth-file batch-upload and generic deletion routes behavior unchanged.
- [ ] Run focused tests after implementation:
  ```powershell
  go test ./apps/manager-server/internal/http/controller/authfilesbanneddelete ./apps/manager-server/internal/http/router -count=1
  go test ./apps/manager-server/... -count=1
  ```

### Task 4 — Add one shared browser-side banned-delete contract and preserve local optimistic list state

**Files:**
- Create: `apps/web/src/features/authFiles/model/accountBannedDelete.ts`
- Create: `apps/web/src/features/authFiles/model/accountBannedDelete.test.ts`
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts`
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/authFiles/hooks/useAuthFilesData.ts`
- Modify/extend tests for `useAuthFilesData` if present

- [ ] **Write failing mapper/API tests first.** The shared mapper must:
  - classify terminal `account_banned` using the same code/message logic as Task 2;
  - include only normalized Codex targets with a client key, physical filename, recovery locator, and usable mutation locator (`authIndex` or `runtimeId`);
  - reject queued/running tasks, ordinary 401/re-login tasks, unsupported provider, and filename-only ambiguous targets;
  - preserve identity fields only and prove JSON serialization has no token/raw credential/proxy values;
  - create a `POST` request to the new endpoint using existing Manager Server headers/timeout and accept `deleted/skipped/failed` response arrays;
  - make demo mode return skipped, never fabricated deletion success.
- [ ] Run the test first:
  ```powershell
  npm test -- --run src/features/authFiles/model/accountBannedDelete.test.ts
  ```
- [ ] Implement the typed request/response contract and export a single selector used by **both** AccountsPage and the inspection page. Have existing `buildPageAccountBannedRows` call this shared classifier/selector so definitions cannot drift.
- [ ] Expose the existing `applyDeletedFiles(fileNames)` function from `useAuthFilesData` without changing legacy generic delete behavior. It must remove affected physical files/children from local state and selection but must not invoke `loadFiles`, history, quota, inspection, or CPA scheduler work.
- [ ] Run focused web tests:
  ```powershell
  npm test -- --run src/features/authFiles/model/accountBannedDelete.test.ts
  ```

### Task 5 — Migrate only the existing account-list banned-delete button to the batch endpoint

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx`
- Modify/add tests under `apps/web/src/features/accounts/` if an existing AccountsPage test harness is available

- [ ] **Write a failing UI/model integration test first** that asserts:
  - the existing `更新当前页凭证 (N)` control remains present and still uses `requestTokenRecoveryManualBatch`;
  - the existing `删除当前页已封禁账号 (M)` control remains present;
  - its candidates are the current page’s shared terminal-account-banned targets only;
  - successful API result applies only `deleted[].physicalName` locally through `applyDeletedFiles` and does not call generic `batchDelete`, `loadFiles`, quota refresh, history refresh, or inspection;
  - skipped/failed summary reports exact real counts and safe first reason.
- [ ] Run the focused test before implementation. If no AccountsPage harness exists, add a minimal pure callback/mapping test alongside Task 4 and explicitly retain the visual regression check in Task 8.
- [ ] Keep existing confirmation UX (including the current preview/second confirmation behavior). Replace only this account-banned action’s network mutation with `usageServiceApi.deleteAccountBannedAuthFilesBatch`.
- [ ] Preserve generic selected-file deletion and all other account list operations exactly as-is.
- [ ] Run focused web tests after implementation.

### Task 6 — Add local inspection snapshot pruning and toolbar controls

**Files:**
- Modify: `apps/web/src/features/monitoring/codexInspection.ts`
- Modify: `apps/web/src/features/monitoring/codexInspection.test.ts`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`
- Modify: `apps/web/src/features/monitoring/CredentialHealthInspectionWorkspace.tsx`
- Modify: `apps/web/src/features/monitoring/CredentialHealthInspectionWorkspace.test.tsx`

- [ ] **Write failing pure snapshot tests first** for `removeCodexInspectionFiles(previousResult, physicalNames)`:
  - remove all result/current-auth-file rows that map to deleted physical names;
  - recompute total, enabled/disabled, action counts, preview, and summary consistently;
  - retain settings/timestamps and unrelated result rows;
  - never start a new inspection or mutate quota data.
- [ ] **Write failing results-panel tests** that assert two exact controls with stable test identifiers render only when `actionFilter === 'reauth'`:
  - `更新当前页凭证 (N)`;
  - `删除当前页已封禁账号 (M)`;
  - counts are current visible-page target counts, callbacks are invoked, and busy/loading state disables rather than hides controls;
  - the pre-existing copy-page and global reauth planned-delete controls remain rendered.
- [ ] **Write failing lifecycle tests** that mock API calls and prove:
  - page recovery submits only current visible reauth targets through the existing manual Token Recovery batch API;
  - no `onCredentialsChanged`, quota/history request, active inspection, or generic auth-file full-list request follows recovery submission;
  - page banned-delete posts only shared terminal-ban mapped targets;
  - successful physical names call `onAuthFilesDeleted`, prune the local inspection result/snapshot, and show real result counts/reasons;
  - the existing automatic inspection re-login signal effect remains untouched and is not reused as a manual-action side effect.
- [ ] Run the focused tests first:
  ```powershell
  npm test -- --run src/features/monitoring/codexInspection.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/CredentialHealthInspectionWorkspace.test.tsx
  ```
- [ ] Implement a narrow `onAuthFilesDeleted?: (physicalNames: string[]) => void` prop chain from AccountsPage → `CredentialHealthInspectionWorkspace` → local `CodexInspectionPage`. Do not add it to server inspection behavior.
- [ ] In `CodexInspectionPage`, derive page-only Codex reauth/banned target candidates from `resultPagination.pageItems` and current recovery task metadata. Manual update uses existing batch-recovery API, updates task metadata locally, and reports its result; deletion calls the new batch-delete API after existing-style confirmation, prunes inspection state, and passes successful physical names outward.
- [ ] Render controls immediately after copy-current-page in the reauth toolbar and before existing global planned reauth deletion. Do not alter automatic signals, execution of planned inspection actions, or other filters.
- [ ] Run focused monitoring tests after implementation.

### Task 7 — Localize all new UI and preserve accessibility/observability

**Files:**
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify relevant locale/key coverage test(s)

- [ ] **Write/update a failing locale coverage test first** for every new action, confirmation, progress, success, partial-result, and safe-error interpolation key.
- [ ] Add consistent labels including:
  - `更新当前页凭证 ({{count}})`;
  - `删除当前页已封禁账号 ({{count}})`;
  - current-page-only confirmation language;
  - result summaries for deleted/skipped/failed and manual recovery submission;
  - no sensitive upstream error interpolation.
- [ ] Keep English, Simplified Chinese, Traditional Chinese, and Russian catalogs structurally aligned.
- [ ] Run locale and affected web tests:
  ```powershell
  npm test -- --run src/features/monitoring/codexInspection.test.ts src/features/authFiles/model/accountBannedDelete.test.ts
  ```

### Task 8 — End-to-end verification, regression checks, and review

**Files:**
- No feature code planned beyond test corrections required by the preceding tasks.

- [ ] Run Go formatting and all Manager Server tests:
  ```powershell
  gofmt -w apps/manager-server/internal/service/cpaauthfiles/client.go apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/authfilesbanneddelete/*.go apps/manager-server/internal/http/controller/authfilesbanneddelete/*.go apps/manager-server/internal/app/context.go apps/manager-server/internal/http/router/router.go
  go test ./apps/manager-server/... -count=1
  ```
- [ ] Run type/lint/build commands documented by the repository, plus targeted Vitest suites. Record the known baseline `DemoPage.test.tsx` failure separately if the full suite still reports it; do not hide or rewrite it.
  ```powershell
  npm run manager-server:test
  npm test -- --run src/features/authFiles/model/accountBannedDelete.test.ts src/features/monitoring/codexInspection.test.ts src/features/monitoring/components/CodexInspectionResultsPanel.test.tsx src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/CredentialHealthInspectionWorkspace.test.tsx
  ```
- [ ] Inspect the diff specifically for prohibited changes: no CPA Core files, no `cli-proxy-api`, no credentials, no quota/usage billing flow, no deletion of existing Accounts toolbar controls, and no generic delete path regression.
- [ ] Manually verify in a local CPAM instance with fixture/non-production credentials:
  1. switch Health Inspection to `重新登录`; the two new buttons appear with visible-page counts;
  2. other inspection filters show neither new button;
  3. manual update submits one current-page batch and leaves CPA scheduling/quotas untouched;
  4. terminal-ban delete removes only confirmed names and reports skip/failure reasons;
  5. a shared file with an omitted member is skipped;
  6. existing Accounts buttons remain and still work;
  7. a multi-file delete produces one auth-file inventory fetch and up to four concurrent physical deletes.
- [ ] Before any later deploy/push request, follow the CPAM update/deployment runbook; do not deploy or push merely because tests pass.

## Rollback plan

- This feature is additive except for routing the existing account-list *account-banned* button to the safer endpoint. If a regression occurs, revert the feature commit(s) together; that restores the existing serial client delete behavior without touching credential files or CPA Core.
- No schema migration, `usage.sqlite` mutation, or persisted configuration change is introduced.
- In-flight deletion remains intentionally non-transactional across physical files. The response identifies exactly which logical candidates were deleted, skipped, or failed so any recovery action is auditable without exposing credential contents.
