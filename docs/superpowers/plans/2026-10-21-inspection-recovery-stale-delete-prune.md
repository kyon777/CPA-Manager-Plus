# Inspection Recovery and Stale Delete Prune Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent duplicate local inspection recovery submissions, reconcile already-absent banned credential sources out of the UI, and raise verified banned-file deletion throughput to eight concurrent physical operations.

**Architecture:** Keep all UI reconciliation in CPAM's local inspection model and presentation layer. The Manager Server remains the authority for terminal delete outcomes; the browser only prunes a physical source when the fresh-inventory response says either `deleted` or exact `skipped/credential_not_found`. Raise only the Manager Server's bounded physical-delete worker cap, preserving the existing shared per-file mutation coordinator and one inventory snapshot.

**Tech Stack:** React 19, TypeScript, Vitest, Go, CPAM Manager Server.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `apps/web/src/features/monitoring/codexInspection.ts` | Pure immutable helpers that prune exact inspection account keys or physical files and recompute inspection summary. |
| `apps/web/src/features/monitoring/codexInspection.test.ts` | Regression coverage for exact-key pruning and physical-source summary recomputation. |
| `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts` | Pure classification of post-inspection manual recovery success and delete-response reconciliation/counts. |
| `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts` | Tests for timestamp/mode eligibility and `credential_not_found` reconciliation. |
| `apps/web/src/features/monitoring/CodexInspectionPage.tsx` | Connects runtime task polling and delete responses to immutable inspection/cache pruning. |
| `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx` | Verifies visible local-page behavior, persistence path, and parent-cache notification. |
| `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx` | Renders delete feedback with a separate already-absent count. |
| `apps/web/src/i18n/locales/{en,zh-CN,zh-TW,ru}.json` | Localized delete-summary text with `absent` interpolation. |
| `apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts` | Verifies the translated account workspace strings retain all required placeholders. |
| `apps/manager-server/internal/service/authfilesbanneddelete/service.go` | Bounded physical-file delete worker cap. |
| `apps/manager-server/internal/service/authfilesbanneddelete/service_test.go` | Concurrent worker regression test for the default eight-worker cap. |
| `C:\kyon\docker\cpa-manager\CPAM_UPDATE_RUNBOOK.md` | Non-repository CPAM custom-change memory updated after verified deployment. |

### Task 1: Add pure recovery-success pruning

**Files:**
- Modify: `apps/web/src/features/monitoring/codexInspection.ts:1104-1134`
- Modify: `apps/web/src/features/monitoring/codexInspection.test.ts:6905-6945`
- Modify: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts:1-172`
- Modify: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts`

- [ ] **Step 1: Write failing pure tests for exactly one recovered account removal**

Add a fixture with two result rows sharing `shared.json`, then test the wished-for APIs:

```ts
const next = removeCodexInspectionResultKeys(base, ['shared.json::auth-a']);

expect(next.files.map((file) => file.name)).toEqual(['shared.json']);
expect(next.results.map((item) => item.key)).toEqual(['shared.json::auth-b']);
expect(next.summary.reauthCount).toBe(1);
```

Add a model test for recovery eligibility:

```ts
expect(
  selectPostInspectionManualSuccessKeys(1_000, new Map([
    ['fresh', { recoveryTask: { mode: 'manual', status: 'succeeded', completedAtMs: 1_001 } }],
    ['stale', { recoveryTask: { mode: 'manual', status: 'succeeded', completedAtMs: 1_000 } }],
    ['auto', { recoveryTask: { mode: 'auto', status: 'succeeded', completedAtMs: 1_002 } }],
  ]))
).toEqual(['fresh']);
```

- [ ] **Step 2: Run the focused tests and verify expected RED failures**

Run:

```powershell
Set-Location apps/web
npx vitest run src/features/monitoring/codexInspection.test.ts src/features/monitoring/model/inspectionPageBatchOperation.test.ts --maxWorkers=1 --no-file-parallelism
```

Expected: failures because `removeCodexInspectionResultKeys` and `selectPostInspectionManualSuccessKeys` do not yet exist.

- [ ] **Step 3: Implement minimal immutable helpers**

In `codexInspection.ts`, extract the repeated sampled-account projection and add:

```ts
export const removeCodexInspectionResultKeys = (
  previousResult: CodexInspectionRunResult,
  accountKeys: readonly string[]
): CodexInspectionRunResult => {
  const removed = new Set(accountKeys.map((key) => key.trim()).filter(Boolean));
  if (removed.size === 0) return previousResult;
  const results = previousResult.results.filter((item) => !removed.has(item.key.trim()));
  if (results.length === previousResult.results.length) return previousResult;
  return {
    ...previousResult,
    results,
    summary: buildSummary(
      previousResult.files,
      toInspectionAccounts(results),
      results,
      previousResult.settings
    ),
  };
};
```

In `inspectionPageBatchOperation.ts`, add a pure selector that returns a key only when `mode === 'manual'`, `status === 'succeeded'`, and a numeric `completedAtMs` is strictly greater than the supplied inspection completion time. Normalize client keys and preserve map iteration order.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run the same Vitest command from Step 2.

Expected: both files pass, including sibling preservation and stale/automatic exclusion.

- [ ] **Step 5: Commit the isolated helper behavior**

```powershell
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-success-prune-20261021
git add apps/web/src/features/monitoring/codexInspection.ts apps/web/src/features/monitoring/codexInspection.test.ts apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts
git commit -m "feat: prune completed inspection recoveries"
```

### Task 2: Model CPA-absent delete outcomes distinctly

**Files:**
- Modify: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts:24-172`
- Modify: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts`

- [ ] **Step 1: Write the failing delete-response reconciliation test**

Create a response containing one real deletion, one exact `skipped` item with `code: 'credential_not_found'`, one other skipped item, and one failed item. Assert:

```ts
const reconciliation = reconcileInspectionPageDeleteResponse(response);
expect(reconciliation.physicalNames).toEqual(['deleted.json', 'missing.json']);
expect(reconciliation.alreadyAbsent).toBe(1);
expect(reconciliation.result).toMatchObject({
  deleted: 1,
  alreadyAbsent: 1,
  skipped: 1,
  failed: 1,
});
expect(reconciliation.result.details).toEqual([
  { fileName: 'failed.json', code: 'delete_failed' },
  { fileName: 'kept.json', code: 'shared_file_incomplete' },
]);
```

- [ ] **Step 2: Run the model test and verify expected RED failure**

Run:

```powershell
Set-Location apps/web
npx vitest run src/features/monitoring/model/inspectionPageBatchOperation.test.ts --maxWorkers=1 --no-file-parallelism
```

Expected: failure because `reconcileInspectionPageDeleteResponse` and `alreadyAbsent` are missing.

- [ ] **Step 3: Implement delete reconciliation without broadening prune rules**

Add `alreadyAbsent: number` to `InspectionPageDeleteOperationResult` and implement:

```ts
export const reconcileInspectionPageDeleteResponse = (
  response: AccountBannedAuthFileDeleteResponse
): { physicalNames: readonly string[]; alreadyAbsent: number; result: InspectionPageDeleteOperationResult } => {
  const missing = response.skipped.filter((item) => normalizedText(item.code) === 'credential_not_found');
  const keptSkipped = response.skipped.filter((item) => normalizedText(item.code) !== 'credential_not_found');
  const physicalNames = uniquePhysicalNames([...response.deleted, ...missing]);
  return {
    physicalNames,
    alreadyAbsent: missing.length,
    result: {
      requested: response.requested,
      deleted: response.deleted.length,
      alreadyAbsent: missing.length,
      skipped: keptSkipped.length,
      failed: response.failed.length,
      details: [...response.failed, ...keptSkipped].slice(0, maxDeleteDetails).map(toDeleteDetail),
    },
  };
};
```

Use `physicalName` when non-empty and otherwise `fileName`; normalize/unique source names. Do not include `identity_changed`, `shared_file_incomplete`, or any other skipped code in the prune set.

- [ ] **Step 4: Run the model test and verify GREEN**

Run the same focused Vitest command from Step 2.

Expected: all model tests pass and generic skipped/failed outcomes remain diagnostic items.

- [ ] **Step 5: Commit the delete classification model**

```powershell
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-success-prune-20261021
git add apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts
git commit -m "feat: reconcile absent banned credential sources"
```

### Task 3: Wire reconciliation into the local inspection UI

**Files:**
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx:88-96,1205-1280,1403-1487`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx:570-653`
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx:230-253`
- Modify: `apps/web/src/i18n/locales/en.json:1524-1544`
- Modify: `apps/web/src/i18n/locales/zh-CN.json:1522-1542`
- Modify: `apps/web/src/i18n/locales/zh-TW.json:1522-1542`
- Modify: `apps/web/src/i18n/locales/ru.json:1528-1548`
- Modify: `apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts:275-289`

- [ ] **Step 1: Write failing lifecycle tests**

Add one test in `CodexInspectionPage.lifecycle.test.tsx` that changes the mocked runtime task from pending to:

```ts
{ mode: 'manual', status: 'succeeded', completedAtMs: finishedAt + 1 }
```

Assert its article is gone after the metadata state is applied, while a sibling `reauth` article remains.

Add a second test whose delete API response is:

```ts
{
  requested: 1,
  deleted: [],
  skipped: [{ clientKey: 'missing.json::auth-1', fileName: 'missing.json', code: 'credential_not_found' }],
  failed: [],
}
```

Assert the inspection card disappears, `onAuthFilesDeleted` receives `['missing.json']`, and the rendered feedback includes the already-absent count rather than a skipped failure.

- [ ] **Step 2: Run lifecycle tests and verify expected RED failures**

Run:

```powershell
Set-Location apps/web
npx vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx --maxWorkers=1 --no-file-parallelism
```

Expected: the recovered and CPA-absent cards remain because the page has not yet applied the two reconciliations.

- [ ] **Step 3: Add minimal page effects and feedback wiring**

Import the new pure helpers. Add an effect after runtime metadata is available that computes successful keys using `result.finishedAt` and calls:

```ts
setResult((previous) =>
  previous ? removeCodexInspectionResultKeys(previous, successfulKeys) : previous
);
```

Guard the effect with the active connection fingerprint and result completion time so a stale or automatic task never prunes a later inspection.

Replace the current `deletedPhysicalNames` block with `reconcileInspectionPageDeleteResponse(response)`. For its `physicalNames` union, keep the existing `setCurrentAuthFiles`, `removeCodexInspectionFiles`, and `onAuthFilesDeleted` cache updates. Feed the reconciliation result into `setPageBannedDeleteOperationState`.

Update both delete result strings in every locale to receive `{{absent}}`, for example Chinese:

```json
"inspection_page_delete_result": "当前页删除完成：已删除 {{deleted}}，CPA 已不存在 {{absent}}，跳过 {{skipped}}，失败 {{failed}}"
```

Pass `absent: pageBannedDeleteOperation.result.alreadyAbsent` to the result panel interpolation. Update the account-workspace translation test to assert the new placeholder.

- [ ] **Step 4: Run lifecycle, model, and translation-wiring tests and verify GREEN**

Run:

```powershell
Set-Location apps/web
npx vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/model/inspectionPageBatchOperation.test.ts src/features/monitoring/codexInspection.test.ts src/features/accounts/accountsWorkspaceWiring.test.ts --maxWorkers=1 --no-file-parallelism
```

Expected: all selected tests pass; only exact manual post-inspection success and exact CPA-absent delete results disappear.

- [ ] **Step 5: Commit UI reconciliation**

```powershell
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-success-prune-20261021
git add apps/web/src/features/monitoring/CodexInspectionPage.tsx apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/ru.json apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts
git commit -m "feat: reconcile local inspection delete outcomes"
```

### Task 4: Raise verified physical delete concurrency to eight

**Files:**
- Modify: `apps/manager-server/internal/service/authfilesbanneddelete/service.go:17-20`
- Modify: `apps/manager-server/internal/service/authfilesbanneddelete/service_test.go:98-160`

- [ ] **Step 1: Write the failing default-concurrency service test**

Add `TestServiceDefaultsToEightIndependentPhysicalDeletes`. Construct eight distinct physical files and terminal `account_banned` recovery records, then construct `NewWithOptions` without `Concurrency`. Use `newBlockingAuthFilesClient`, wait for eight started deletes with a one-second timeout, and assert:

```go
if active, maximum := client.activeCounts(); active != 8 || maximum != 8 {
    t.Fatalf("active=%d maximum=%d, want default concurrency 8", active, maximum)
}
```

Use `sync.Once` to close the test client's release channel exactly once in both the normal and timeout paths, then verify `Deleted == 8`, `Failed == 0`, and exactly one inventory fetch.

- [ ] **Step 2: Run the Go test and verify expected RED failure**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/authfilesbanneddelete -run TestServiceDefaultsToEightIndependentPhysicalDeletes -count=1
```

Expected: failure after the timeout because the current default reaches only four blocked delete workers.

- [ ] **Step 3: Raise only the bounded worker cap**

Change:

```go
MaxConcurrency = 4
```

To:

```go
MaxConcurrency = 8
```

Keep the existing `NewWithOptions` clamp, one inventory fetch, physical-job grouping, and mutation coordinator unchanged.

- [ ] **Step 4: Run focused and package-level Go tests and verify GREEN**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/authfilesbanneddelete -count=1
```

Expected: the new default test reaches eight concurrent workers; existing configured-concurrency-two coverage remains green.

- [ ] **Step 5: Commit the concurrency adjustment**

```powershell
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-success-prune-20261021
git add apps/manager-server/internal/service/authfilesbanneddelete/service.go apps/manager-server/internal/service/authfilesbanneddelete/service_test.go
git commit -m "perf: raise banned auth file delete concurrency"
```

### Task 5: Full verification, deployment record, and custom-change memory

**Files:**
- Modify after verified deployment: `C:\kyon\docker\cpa-manager\CPAM_UPDATE_RUNBOOK.md`

- [ ] **Step 1: Run web type-check and production build**

Run:

```powershell
Set-Location apps/web
npm run type-check
npm run build
```

Expected: exit code 0 for both commands.

- [ ] **Step 2: Run final focused regression suite**

Run:

```powershell
Set-Location apps/web
npx vitest run src/features/monitoring/codexInspection.test.ts src/features/monitoring/model/inspectionPageBatchOperation.test.ts src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/accounts/accountsWorkspaceWiring.test.ts --maxWorkers=1 --no-file-parallelism
Set-Location ..\manager-server
go test ./internal/service/authfilesbanneddelete -count=1
```

Expected: all selected tests pass.

- [ ] **Step 3: Verify changeset scope before deployment**

Run:

```powershell
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\inspection-success-prune-20261021
git status --short
git log --oneline 22f5143d..HEAD
git diff --stat 22f5143d..HEAD
```

Expected: only CPAM web/manager-server/docs paths are changed; no CPA Core path is present.

- [ ] **Step 4: Deploy only after explicit deployment instruction**

Use the existing CPAM-only guarded deployment flow. Capture CPA Core container/process identity before and after; rebuild/restart only `cpa-manager-plus` locally and only `cpam.service` remotely. Do not touch `cli-proxy-api` or CPA Core.

- [ ] **Step 5: Record permanent custom-change memory after verified deployment**

Append one concise runbook entry stating:

```text
- Local inspection: newer manual Token Recovery success prunes only the matching inspection key.
- Banned delete: exact skipped credential_not_found means CPA inventory already lacks the physical source; reconcile it out of local inspection and credential cache, but report it separately from actual deletions.
- authfilesbanneddelete: bounded physical-file delete concurrency is 8; keep per-file mutation locks and one inventory snapshot.
```

- [ ] **Step 6: Commit only repository-owned implementation files and push only when explicitly requested**

Do not push upstream. If the user explicitly asks to push, push only the final merged CPAM branch to `origin lo/custom`.