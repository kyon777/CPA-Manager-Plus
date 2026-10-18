# Inspection Operation Feedback and EOF Priority Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make local inspection-page current-page recovery/deletion actions visibly stateful and non-duplicable, and recognize one exact HTTP 500 Codex EOF failure as a priority-demotion event.

**Architecture:** A small pure web model converts existing redacted recovery task rows and delete response rows into panel-ready operation state. `CodexInspectionPage` owns the ephemeral confirmation/submission state and delegates pure presentation to `CodexInspectionResultsPanel`; it continues using the existing two-second Manager task query, rather than adding a CPA request. The Go priority worker gets one narrow predicate evaluated before its existing identity verification and peer-priority rebalance path.

**Tech Stack:** React 18, TypeScript, Vitest, SCSS modules, Go, `net/http`, existing CPAM Manager Server task APIs.

---

## File Structure

- Create: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts`
  - Pure recovery-state and deletion-result projection, with no React or network dependency.
- Create: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts`
  - Unit tests for task classification, persistent completion summary, and safe delete-result detail projection.
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
  - Own confirmation/submission gates, bind the existing task metadata map to the recovery operation, and provide operation views to the results panel.
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`
  - Reproduce the early-unlock defect and prove controls remain locked through asynchronous completion.
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
  - Render busy labels and compact, persistent operation result/status sections with stable test selectors.
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.module.scss`
  - Lay out the compact result/status block without breaking narrow-screen toolbar wrapping.
- Modify: `apps/web/src/i18n/locales/en.json`, `zh-CN.json`, `zh-TW.json`, `ru.json`
  - Add all new status and result labels; no hard-coded human-facing strings.
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion.go`
  - Add the precise HTTP 500 Codex responses unexpected-EOF predicate.
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion_test.go`
  - Establish the narrow 500 behavior and non-matching negative cases.

## Task 1: Define Pure Inspection Batch Operation Presentation

**Files:**
- Create: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts`
- Test: `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts`

- [ ] **Step 1: Write failing model tests for recovery task aggregation**

```ts
it('keeps a submitted recovery operation active while any submitted task is queued or running', () => {
  const progress = summarizeInspectionPageRecoveryOperation(
    { targetKeys: ['a', 'b', 'c'], submissionFailures: [] },
    new Map([
      ['a', { clientKey: 'a', recoveryTask: task('manual_queued') }],
      ['b', { clientKey: 'b', recoveryTask: task('manual_running') }],
      ['c', { clientKey: 'c', recoveryTask: task('succeeded') }],
    ])
  );

  expect(progress).toMatchObject({
    active: true, queued: 1, running: 1, succeeded: 1, failed: 0,
  });
});

it('retains terminal failures and reasons after submitted recovery completes', () => {
  const progress = summarizeInspectionPageRecoveryOperation(
    {
      targetKeys: ['a', 'b'],
      submissionFailures: [{ clientKey: 'b', reason: 'credential_not_found' }],
    },
    new Map([['a', { clientKey: 'a', recoveryTask: task('succeeded') }]])
  );

  expect(progress).toMatchObject({ active: false, succeeded: 1, failed: 1 });
  expect(progress.failureReasons).toEqual(['credential_not_found']);
});
```

- [ ] **Step 2: Run the focused model test to verify RED**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/model/inspectionPageBatchOperation.test.ts
```

Expected: FAIL because `inspectionPageBatchOperation` and its exports do not exist.

- [ ] **Step 3: Implement the smallest pure projection module**

```ts
export type InspectionPageRecoveryOperation = {
  targetKeys: readonly string[];
  submissionFailures: readonly { clientKey: string; reason: string }[];
};

export type InspectionPageRecoveryProgress = {
  active: boolean;
  queued: number;
  running: number;
  succeeded: number;
  failed: number;
  failureReasons: readonly string[];
};

export type InspectionPageDeletePhase =
  | 'idle'
  | 'awaiting_first_confirmation'
  | 'awaiting_second_confirmation'
  | 'deleting'
  | 'completed';

export type InspectionPageDeleteOperationResult = {
  deleted: number;
  skipped: number;
  failed: number;
  details: readonly { fileName: string; code: string; message: string }[];
};

export type InspectionPageDeleteOperation = {
  phase: InspectionPageDeletePhase;
  result: InspectionPageDeleteOperationResult | null;
};

export const idleInspectionPageDeleteOperation: InspectionPageDeleteOperation = {
  phase: 'idle',
  result: null,
};

export const summarizeInspectionPageRecoveryOperation = (
  operation: InspectionPageRecoveryOperation | null,
  metadataByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): InspectionPageRecoveryProgress | null => {
  if (!operation) return null;
  // Classify only operation.targetKeys. queued/running use existing
  // isPendingRecoveryStatus semantics; a missing returned task is terminal
  // failure rather than an endless lock.
};
```

Add `toInspectionPageDeleteOperationResult(response)` that returns only
`deleted`, `skipped`, `failed`, and a deduplicated, bounded list of
`{ fileName, code, message }` display records. It must never copy tokens,
auth JSON, management keys, or proxy credentials because none are in the
response contract.

- [ ] **Step 4: Run the focused model tests to verify GREEN**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/model/inspectionPageBatchOperation.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit the pure model and tests**

```powershell
git add apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts apps/web/src/features/monitoring/model/inspectionPageBatchOperation.test.ts
git commit -m "feat: model inspection batch operation state"
```

## Task 2: Add the Exact HTTP 500 EOF Priority Candidate

**Files:**
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion_test.go`
- Modify: `apps/manager-server/internal/worker/server_error_priority_demotion.go`

- [ ] **Step 1: Write failing worker cases before changing the predicate**

Extend `TestServerErrorPriorityDemotionCandidateAccepts502503AndQualified429`:

```go
qualifiedEOF := base
qualifiedEOF.FailStatusCode = http.StatusInternalServerError
qualifiedEOF.FailBody = `Post "https://chatgpt.com/backend-api/codex/responses": unexpected EOF`
if _, ok := serverErrorPriorityDemotionCandidateFromEvent(qualifiedEOF, "http://cpa", "management-key"); !ok {
    t.Fatal("exact HTTP 500 Codex responses unexpected EOF must demote")
}

for _, failBody := range []string{
    `Post "https://chatgpt.com/backend-api/codex/responses": response body unavailable`,
    `Post "https://chatgpt.com/backend-api/other": unexpected EOF`,
    "unexpected EOF",
} {
    event := base
    event.FailStatusCode = http.StatusInternalServerError
    event.FailBody = failBody
    if _, ok := serverErrorPriorityDemotionCandidateFromEvent(event, "http://cpa", "management-key"); ok {
        t.Fatalf("non-qualified 500 body %q must not demote", failBody)
    }
}
```

- [ ] **Step 2: Run the focused worker test to verify RED**

Run:

```powershell
go test ./apps/manager-server/internal/worker -run TestServerErrorPriorityDemotionCandidateAccepts502503AndQualified429 -count=1
```

Expected: FAIL at the exact HTTP 500 EOF assertion; existing code rejects all 500 responses.

- [ ] **Step 3: Add a narrow, case-insensitive body predicate**

```go
const codexResponsesEOFMarker = `post "https://chatgpt.com/backend-api/codex/responses":`

func isQualifiedCodexResponsesEOF500(failBody string) bool {
    normalized := strings.ToLower(strings.TrimSpace(failBody))
    return strings.Contains(normalized, codexResponsesEOFMarker) &&
        strings.Contains(normalized, "unexpected eof")
}

func isServerErrorPriorityDemotionEvent(event usage.Event) bool {
    if !event.Failed {
        return false
    }
    switch event.FailStatusCode {
    case http.StatusBadGateway, http.StatusServiceUnavailable:
        return true
    case http.StatusInternalServerError:
        return isQualifiedCodexResponsesEOF500(event.FailBody)
    case http.StatusTooManyRequests:
        return isQualifiedRateLimit429(event.FailBody)
    default:
        return false
    }
}
```

Use only local `FailBody`; do not widen public failure APIs or use a generic
status-500 rule.

- [ ] **Step 4: Run the focused worker package to verify GREEN**

Run:

```powershell
go test ./apps/manager-server/internal/worker -run 'TestServerErrorPriority(DemotionCandidateAccepts502503AndQualified429|AfterPeerRebalance|PeerMaximum)' -count=1
```

Expected: PASS, including existing 502/503/qualified-429 rules and peer rebalance.

- [ ] **Step 5: Commit the narrow worker behavior**

```powershell
git add apps/manager-server/internal/worker/server_error_priority_demotion.go apps/manager-server/internal/worker/server_error_priority_demotion_test.go
git commit -m "feat: demote exact codex eof 500 failures"
```

## Task 3: Wire Recovery and Delete Operation Gates into the Inspection Page

**Files:**
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`

- [ ] **Step 1: Write failing lifecycle tests for the two current regressions**

Add a recovery test with a deferred `requestTokenRecoveryManualBatch` response,
then a queued response, and assert:

```tsx
expect(recoveryButton.props.disabled).toBe(true); // while POST unresolved
// Resolve with manual_queued; existing task-query polling still sees it queued.
expect(renderer.root.findByProps({
  'data-inspection-page-recovery-status': true,
}).props).toMatchObject({ 'data-active': 'true' });
expect(renderer.root.findByProps({
  'data-inspection-page-recovery': true,
}).props.disabled).toBe(true);
```

Then update the mocked task-query response to `succeeded` and assert the status
section reports terminal counts and the button can be deliberately retried.

For deletion, override `showConfirmation` so the test controls both callbacks:

```tsx
mocks.showConfirmation.mockImplementation((options) => pendingConfirmations.push(options));
deleteButton.props.onClick();
expect(deleteButton.props.disabled).toBe(true);
pendingConfirmations[0].onConfirm();
expect(deleteButton.props.disabled).toBe(true);
pendingConfirmations[1].onConfirm();
// Resolve delete response with one deleted and one skipped item.
expect(renderer.root.findByProps({
  'data-inspection-page-banned-delete-status': true,
})).toBeDefined();
```

Assert only the `deleted` physical name is removed from result cards; the
skipped row remains and the status block includes its existing safe code.

- [ ] **Step 2: Run lifecycle tests to verify RED**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
```

Expected: FAIL because post-submit queued tasks currently clear the button
lock, and no persistent status selectors/results exist.

- [ ] **Step 3: Add page-owned typed operation state and immediate refs**

In `CodexInspectionPage.tsx`, add state for:

```ts
const [pageRecoveryOperation, setPageRecoveryOperation] =
  useState<InspectionPageRecoveryOperation | null>(null);
const [pageBannedDeleteOperation, setPageBannedDeleteOperation] =
  useState<InspectionPageDeleteOperation>(idleInspectionPageDeleteOperation);
const pageBannedDeletePhaseRef = useRef<InspectionPageDeletePhase>('idle');
```

Rules to implement:

1. Set the recovery operation before the batch request, record target keys, and
   retain submission failures after its response.
2. Build recovery progress from the existing `pageRuntimeItemsByClientKey`
   map; a queued/running task keeps the control disabled after POST completion.
3. On connection scope change, reset locally owned operation state and
   increment existing request generations so old responses cannot mutate the
   new scope.
4. Before showing the first delete confirmation, set the ref/state to
   `awaiting_first_confirmation`; transition to `awaiting_second_confirmation`
   before the nested confirmation; reset to idle from either `onCancel`.
5. At second confirmation, transition to `deleting`, call the existing delete
   endpoint, update `currentAuthFiles`/inspection results only from
   `response.deleted`, and store the pure split result for persistent display.
6. In every error or stale-scope exit, release the phase gate deterministically.

Do not call `onCredentialsChanged`, a quota endpoint,
`executeCodexInspection`, or an auth-file full reload from these actions.

- [ ] **Step 4: Run lifecycle tests to verify GREEN**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
```

Expected: PASS, including existing stale-scope tests and new locks/result tests.

- [ ] **Step 5: Commit page lifecycle wiring**

```powershell
git add apps/web/src/features/monitoring/CodexInspectionPage.tsx apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
git commit -m "fix: retain inspection page batch operation state"
```

## Task 4: Render Accessible Operation Feedback and Localize It

**Files:**
- Modify: `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.module.scss`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [ ] **Step 1: Extend lifecycle test assertions for rendered labels and result details**

```tsx
expect(textContent(renderer.root.findByProps({
  'data-inspection-page-recovery-status': true,
}))).toContain('accounts.inspection_page_recovery_running');

expect(textContent(renderer.root.findByProps({
  'data-inspection-page-banned-delete-status': true,
}))).toContain('shared_file_incomplete');
```

- [ ] **Step 2: Run the affected lifecycle test to verify RED**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
```

Expected: FAIL because the panel does not accept or render operation views.

- [ ] **Step 3: Pass and render explicit operation views**

Add panel props for recovery progress and delete operation result/phase. Render one
`aria-live="polite"` compact block directly below the toolbar actions:

```tsx
{pageRecoveryProgress ? (
  <div
    data-inspection-page-recovery-status
    data-active={String(pageRecoveryProgress.active)}
    aria-live="polite"
  >
    <span>{t('accounts.inspection_page_recovery_counts', pageRecoveryProgress)}</span>
    {pageRecoveryProgress.failureReasons.map((reason) => <span key={reason}>{reason}</span>)}
  </div>
) : null}
```

For deletion, show `awaiting first confirmation`, `awaiting second
confirmation`, or `deleting` while non-terminal. When terminal, render the
three returned counts and an accessible `<details>` element for skipped/failed
items. Buttons use the phase/progress `active` value for both `loading` and
`disabled`; preserve current `result`, inspection-in-flight, execution, and
zero-count guards.

Add locale keys under `accounts`:

```json
{
  "inspection_page_recovery_submitting": "Submitting credential updatesâ€¦",
  "inspection_page_recovery_counts": "Credential update: queued {{queued}} Â· running {{running}} Â· succeeded {{succeeded}} Â· failed {{failed}}",
  "inspection_page_delete_waiting_first": "Waiting for delete confirmation",
  "inspection_page_delete_waiting_second": "Waiting for final delete confirmation",
  "inspection_page_delete_running": "Deleting banned credentialsâ€¦",
  "inspection_page_delete_counts": "Deleted {{deleted}} Â· skipped {{skipped}} Â· failed {{failed}}",
  "inspection_page_operation_details": "Operation details"
}
```

Translate the same semantic keys in all supported locale files. Add scoped SCSS
using flex-wrap, modest gap, danger/warning text tokens, and a max height for
details so it remains readable at 100-row page size and mobile breakpoints.

- [ ] **Step 4: Run focused UI/lifecycle tests to verify GREEN**

Run:

```powershell
pnpm --dir apps/web vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/model/inspectionPageBatchOperation.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit UI, styles, and translations**

```powershell
git add apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx apps/web/src/features/monitoring/CodexInspectionPage.module.scss apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/ru.json apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx
git commit -m "feat: show inspection batch operation progress"
```

## Task 5: Verify the Isolated CPAM Change Set

**Files:**
- Verify only; no new production files.

- [ ] **Step 1: Run all focused regression suites**

```powershell
go test ./apps/manager-server/internal/worker -count=1
pnpm --dir apps/web vitest run src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/model/inspectionPageBatchOperation.test.ts
```

Expected: PASS.

- [ ] **Step 2: Run web static and production checks**

```powershell
pnpm --dir apps/web type-check
pnpm --dir apps/web build
```

Expected: PASS with no TypeScript errors.

- [ ] **Step 3: Inspect final diff and scope**

```powershell
git diff 4648d73e..HEAD --check
git diff --stat 4648d73e..HEAD
git status --short
```

Expected: only CPAM web, Manager Server priority-worker, tests, locale, and
plan/spec files changed; source worktree outside this isolated branch remains
untouched.

- [ ] **Step 4: Commit verification-only documentation if needed**

No code-only verification commit is required. Preserve focused test output in
the deployment handoff and continue only after all targeted checks pass.

## Deployment Boundary

After implementation is verified, use the project CPAM deployment runbook:

1. Build from this clean worktree.
2. Rebuild/restart only local Docker `cpa-manager-plus`.
3. Verify CPA Core container identity is unchanged before and after.
4. Deploy the CPAM binary only to remote `/home/cpam/app/` and restart only
   `cpam.service`; do not touch `/home/cpa`, `cpa.service`, or
   `cli-proxy-api`.
5. Push only the private `origin lo/custom` branch; never push upstream.
