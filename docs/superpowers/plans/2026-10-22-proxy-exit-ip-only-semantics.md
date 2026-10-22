
# Proxy Exit-IP-Only Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make “代理异常” mean only a direct CPAM-to-proxy exit-IP failure while preserving account inspection, quota/billing, token recovery, priority demotion, and all existing CPAM 二改。

**Architecture:** Ordinary account/API responses may nominate a credential for a bounded direct exit-IP check but can never set `proxy_suspect`, quarantine a pool entry, or appear under “代理异常” by themselves. CPAM uses the existing fixed `https://api.ipify.org?format=json` checker through the current server-side `proxy_url`; browser calls contain only strict credential locators, never a raw proxy URL or arbitrary target. Replacement rechecks use the same checker and distinguish healthy, direct failure, and inconclusive.

**Tech Stack:** Go, SQLite, React, TypeScript, Vitest, existing CPAM manager-server API, existing `proxyreplacement.ExitIPChecker`.

---

## Invariants

- Never change CPA Core, `cli-proxy-api`, request billing/usage persistence, quota capture, priority demotion, token acquisition, OpenResty, or credential JSON other than the existing explicitly-confirmed proxy replacement write.
- Exit-IP health stays fixed at two 5-second attempts through `https://api.ipify.org?format=json`, with at most eight workers.
- 401, 429, Credits/quota, 500/502/503, overload, and account API timeout may nominate a direct check but never themselves create a proxy anomaly.
- Only `ProxyExitIPOutcomeProxyFailed` sets `CodexInspectionResult.ProxySuspect=true` or pool `suspect`.
- `healthy` clears stale inspection proxy state and restores an existing suspect pool fingerprint. `inconclusive` clears stale inspection state after a replacement but neither restores nor quarantines the new pool URL.
- Legacy inferred state is cleaned exactly once. Usage events, price data, quota snapshots, request history, and account files remain untouched.

## File map

| Path | Change |
| --- | --- |
| `internal/model/proxy_replacement.go` | Direct credential-check result types; add `recheck_inconclusive`; retire inference-only result/evidence types. |
| `internal/service/codexinspection/{service.go,proxy_recheck.go}` | Candidate-only detection plus direct exit-IP proof worker phase. |
| `internal/service/proxyreplacement/service.go` | Server-side strict credential resolution and direct replacement rechecks. |
| `internal/app/context.go` | Construct one checker and inject it into both services. |
| `internal/http/controller/proxyreplacement/handler.go` | Add strict credential check route; remove raw inspection signal route. |
| `internal/repository/{proxyreplacement,sqlite}` | Persist new task count; one-time stale inferred-state cleanup. |
| `apps/web/src/services/api/usageService.ts` | Typed strict endpoint client; delete raw-signal client. |
| `apps/web/src/features/monitoring/*` | Local-inspection direct evidence, storage sanitization, replacement reconciliation. |
| `apps/web/src/features/accounts/components/ProxyReplacementModal.*` | Original/new proxy and direct recheck status UI. |
| `apps/web/src/i18n/locales/*.json` | Direct-check and inconclusive labels. |

### Task 1: Establish the direct-verification contract

**Files:**
- Modify: `apps/manager-server/internal/model/proxy_replacement.go`
- Modify: `apps/manager-server/internal/model/proxy_replacement_test.go`
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/features/accounts/model/proxyReplacement.ts`
- Test: `apps/web/src/features/accounts/model/proxyReplacement.test.ts`

- [ ] **Step 1: Write failing Go model tests.**

```go
func TestProxyReplacementRecheckInconclusiveIsDistinct(t *testing.T) {
    if model.ProxyReplacementItemRecheckInconclusive == model.ProxyReplacementItemRecheckFailed {
        t.Fatal("inconclusive must not be a direct proxy failure")
    }
}

func TestCredentialExitIPCheckResultKeepsOnlyLocatorAndDirectOutcome(t *testing.T) {
    got := model.CredentialProxyExitIPCheckResult{
        ClientKey: "row-1", Outcome: model.ProxyExitIPOutcomeProxyFailed, Reason: "proxy_connect_timeout",
    }
    if got.ClientKey != "row-1" || got.Outcome != model.ProxyExitIPOutcomeProxyFailed {
        t.Fatalf("result=%#v", got)
    }
}
```

- [ ] **Step 2: Verify RED.**

Run: `go test ./internal/model -run 'TestProxyReplacementRecheckInconclusiveIsDistinct|TestCredentialExitIPCheckResultKeepsOnlyLocatorAndDirectOutcome' -count=1` from `apps/manager-server`.

Expected: compile failure because the direct credential batch/result and `recheck_inconclusive` do not exist.

- [ ] **Step 3: Implement the smallest shared types.**

In `internal/model/proxy_replacement.go` add:

```go
const ProxyReplacementItemRecheckInconclusive ProxyReplacementItemStatus = "recheck_inconclusive"

type CredentialProxyExitIPCheckResult struct {
    ClientKey string             `json:"clientKey"`
    Outcome   ProxyExitIPOutcome `json:"outcome"`
    ExitIP    string             `json:"exitIp,omitempty"`
    LatencyMS int64              `json:"latencyMs,omitempty"`
    Reason    string             `json:"reason,omitempty"`
}
type CredentialProxyExitIPCheckBatch struct {
    Results []CredentialProxyExitIPCheckResult `json:"results"`
}
```

Add `RecheckInconclusiveCount int 'json:"recheckInconclusiveCount"'` to `ProxyReplacementTask`. Mirror exact values in `usageService.ts` and `features/accounts/model/proxyReplacement.ts`:

```ts
export type ProxyReplacementItemStatus =
  | 'draft' | 'queued' | 'running' | 'updated' | 'skipped_stale' | 'write_failed'
  | 'recheck_running' | 'recheck_succeeded' | 'recheck_failed' | 'recheck_inconclusive';
export interface CredentialProxyExitIPCheckResult {
  clientKey: string; outcome: ProxyExitIPOutcome; exitIp?: string; latencyMs?: number; reason?: string;
}
```

Do not allow model inference to write `ProxySuspect`; remove `ProxyReplacementProbeResult`, `ProxyFailureEvidence`, and `IsSuspectedProxyTransportFailure` after their callers move in Tasks 2–4.

- [ ] **Step 4: Verify GREEN.**

Run:

```powershell
Push-Location apps/manager-server; go test ./internal/model -count=1; Pop-Location
npm --workspace apps/web run test -- src/features/accounts/model/proxyReplacement.test.ts
```

Expected: both commands exit 0.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/model/proxy_replacement.go apps/manager-server/internal/model/proxy_replacement_test.go apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/model/proxyReplacement.ts apps/web/src/features/accounts/model/proxyReplacement.test.ts
git commit -m "feat: add direct credential proxy verification contract"
```

### Task 2: Change server inspection from inference to direct proof

**Files:**
- Modify: `apps/manager-server/internal/service/codexinspection/service.go`
- Replace: `apps/manager-server/internal/service/codexinspection/proxy_recheck.go`
- Modify: `apps/manager-server/internal/service/codexinspection/proxy_recheck_test.go`
- Modify: `apps/manager-server/internal/app/context.go`

- [ ] **Step 1: Write failing direct-evidence tests.**

Create a deterministic fake checker/reporter and assert all three outcomes:

```go
func TestApplyExitIPProxyEvidenceOnlyMarksDirectProxyFailure(t *testing.T) {
    svc := newInspectionServiceForProxyEvidence(t, fakeExitIPChecker{
        "http://candidate:8080": {Outcome: model.ProxyExitIPOutcomeHealthy},
    })
    got := svc.applyExitIPProxyEvidence(context.Background(), []account{candidateAccount()}, []model.CodexInspectionResult{timeoutResult()})
    if got[0].ProxySuspect { t.Fatalf("healthy check must not mark proxy abnormal: %#v", got[0]) }
}

func TestApplyExitIPProxyEvidenceMarksDirectFailure(t *testing.T) {
    svc := newInspectionServiceForProxyEvidence(t, fakeExitIPChecker{
        "http://candidate:8080": {Outcome: model.ProxyExitIPOutcomeProxyFailed, Reason: "proxy_connect_timeout"},
    })
    got := svc.applyExitIPProxyEvidence(context.Background(), []account{candidateAccount()}, []model.CodexInspectionResult{timeoutResult()})
    if !got[0].ProxySuspect { t.Fatalf("direct proxy_failed must mark abnormal: %#v", got[0]) }
}

func TestApplyExitIPProxyEvidenceDoesNotQuarantine401OrInconclusive(t *testing.T) {
    svc := newInspectionServiceForProxyEvidence(t, fakeExitIPChecker{
        "http://candidate:8080": {Outcome: model.ProxyExitIPOutcomeInconclusive, Reason: "ip_echo_timeout"},
    })
    got := svc.applyExitIPProxyEvidence(context.Background(), []account{candidateAccount()}, []model.CodexInspectionResult{timeoutResult()})
    if got[0].ProxySuspect { t.Fatalf("inconclusive check must not mark abnormal: %#v", got[0]) }
    got = svc.applyExitIPProxyEvidence(context.Background(), []account{candidateAccount()}, []model.CodexInspectionResult{http401Result()})
    if got[0].ProxySuspect { t.Fatalf("HTTP 401 must not mark proxy abnormal: %#v", got[0]) }
}
```

The fake state controller must observe `MarkSuspect` only on `proxy_failed`, `Restore` only after `healthy` for a known suspect fingerprint, and no `MarkPending` use.

- [ ] **Step 2: Verify RED.**

Run: `go test ./internal/service/codexinspection -run TestApplyExitIPProxyEvidence -count=1` from `apps/manager-server`.

Expected: compile failure because the checker/controller and direct worker phase do not exist.

- [ ] **Step 3: Implement candidate-only detection plus direct checker workers.**

In `service.go`, replace `ProxyFailureReporter` with:

```go
type ProxyExitIPChecker interface { Check(context.Context, string) model.ProxyExitIPCheckResult }
type ProxyPoolStateController interface {
    MarkSuspect(context.Context, string, string) error
    Restore(context.Context, string) (model.ProxyPoolState, error)
}
const maxInspectionExitIPCheckConcurrency = 8
```

Inject both through `ServiceOptions`. In `executeRun`, directly after `inspectAccounts`, invoke:

```go
results = s.applyExitIPProxyEvidence(ctx, sampled, results, logger)
```

`applyExitIPProxyEvidence` must use an indexed worker pool capped at eight and only nominate rows with a configured proxy plus the narrow existing transport-shaped signal (`request_error` timeout/deadline or CPA `api-call failed: 502` + `request failed`). For each direct result:

```go
switch check.Outcome {
case model.ProxyExitIPOutcomeProxyFailed:
    row.ProxySuspect = true
    _ = s.proxyPoolState.MarkSuspect(ctx, proxyURL, check.Reason)
case model.ProxyExitIPOutcomeHealthy:
    row.ProxySuspect = false
    _, _ = s.proxyPoolState.Restore(ctx, model.ProxyFingerprint(proxyURL))
case model.ProxyExitIPOutcomeInconclusive, model.ProxyExitIPOutcomeNotSaved:
    row.ProxySuspect = false
}
```

Log state-write failures only. Never modify the account action, quota/Credits, auto-recovery eligibility, history, or billing state. Remove `markProxySuspect`, `proxyFailureReason`, and account-API replacement recheck code from `proxy_recheck.go`.

In `context.go`, construct exactly one `exitIPChecker := proxyreplacementsvc.NewExitIPChecker(cfg.ProxyExitIPCheckURL)` and inject it into both inspection and replacement services.

- [ ] **Step 4: Verify GREEN.**

Run: `go test ./internal/service/codexinspection ./internal/service/proxypoolstate -count=1` from `apps/manager-server`.

Expected: direct healthy/failure/inconclusive tests and prior 401/quota/auto-action tests pass.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/service/codexinspection apps/manager-server/internal/app/context.go
git commit -m "fix: require exit IP proof for proxy inspection anomalies"
```

### Task 3: Directly recheck the replacement proxy instead of calling the account API

**Files:**
- Modify: `apps/manager-server/internal/service/proxyreplacement/service.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/manager-server/internal/repository/proxyreplacement/repository.go`
- Modify: `apps/manager-server/internal/repository/proxyreplacement/repository_test.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate_test.go`
- Modify: `apps/manager-server/internal/app/context.go`

- [ ] **Step 1: Write failing replacement tests.**

```go
func TestRunRechecksUsesHealthyExitIPDespiteAccountHTTP401(t *testing.T) {
    // Seed an updated task item and an old proxy_suspect row.
    // Fake checker returns healthy for the newly saved proxy.
    // Expect recheck_succeeded, inspection flag cleared, pool fingerprint restored.
}
func TestRunRechecksMarksNewProxySuspectOnlyForDirectExitFailure(t *testing.T) {
    // Fake checker returns proxy_failed/proxy_auth_407.
    // Expect recheck_failed and the new fingerprint becomes suspect with proxy_auth_407.
}
func TestRunRechecksClearsOldInspectionFlagOnInconclusive(t *testing.T) {
    // Fake checker returns inconclusive/ip_echo_timeout.
    // Expect recheck_inconclusive, old inspection flag cleared, no pool restore/quarantine.
}
func TestCompleteTaskCountsRecheckInconclusiveSeparately(t *testing.T) {
    taskID := createTaskWithStatuses(t, repo, []model.ProxyReplacementItemStatus{
        model.ProxyReplacementItemRecheckSucceeded,
        model.ProxyReplacementItemRecheckInconclusive,
        model.ProxyReplacementItemRecheckFailed,
    })
    got, err := repo.CompleteTask(context.Background(), taskID)
    if err != nil { t.Fatalf("complete task: %v", err) }
    if got.RecheckSuccessCount != 1 || got.RecheckInconclusiveCount != 1 || got.RecheckFailedCount != 1 {
        t.Fatalf("task=%#v", got)
    }
}
```

- [ ] **Step 2: Verify RED.**

Run: `go test ./internal/service/proxyreplacement ./internal/repository/proxyreplacement -run 'TestRunRechecks|TestCompleteTaskCountsRecheckInconclusiveSeparately' -count=1` from `apps/manager-server`.

Expected: failure because `runRechecks` still delegates to `Rechecker` and SQLite has no inconclusive count/status.

- [ ] **Step 3: Implement server-side target resolution and direct recheck.**

Delete `Rechecker` from `proxyreplacement.Options`/`Service`; remove `Rechecker: codexInspectionService` from `context.go`. Use one direct helper:

```go
const maxCredentialExitIPCheckConcurrency = 8

func (s *Service) verifyCurrentCredentialExitIPs(
    ctx context.Context, setup store.Setup, targets []model.ProxyReplacementTarget,
) []model.CredentialProxyExitIPCheckResult
```

For each target, it must call existing `resolveCurrentProxyMutationTarget`, read `proxyURLFromFile(resolved.File)` only inside Manager Server, then call `s.exitIPChecker.Check`. Preserve input order and return one result for every target. Resolution errors return `OutcomeInconclusive` with a finite non-secret reason (`credential_not_found`, `credential_ambiguous`, or `credential_lookup_failed`); response data never includes a raw URL.

Rewrite `runRechecks` to invoke that helper after the existing write phase:

```go
switch result.Outcome {
case model.ProxyExitIPOutcomeHealthy:
    mark(item.ID, model.ProxyReplacementItemRecheckSucceeded, model.ProxyReplacementItemOutcome{})
    clearTargets = append(clearTargets, item.Target)
    restoreExistingSuspect(item.NewProxyFingerprint)
case model.ProxyExitIPOutcomeProxyFailed:
    mark(item.ID, model.ProxyReplacementItemRecheckFailed, model.ProxyReplacementItemOutcome{
        RecheckErrorCode: result.Reason, RecheckErrorMessage: directFailureMessage(result.Reason),
    })
    _ = s.poolState.MarkSuspect(ctx, item.NewProxyURL, result.Reason)
case model.ProxyExitIPOutcomeInconclusive, model.ProxyExitIPOutcomeNotSaved:
    mark(item.ID, model.ProxyReplacementItemRecheckInconclusive, model.ProxyReplacementItemOutcome{
        RecheckErrorCode: result.Reason, RecheckErrorMessage: inconclusiveMessage(result.Reason),
    })
    clearTargets = append(clearTargets, item.Target)
}
```

If strict credential lookup fails, mark only that item `recheck_failed/credential_not_found`; do not falsely clear a missing credential’s inspection row and do not quarantine a URL. Group pool reconciliation by fingerprint so a direct failure wins over healthy results sharing one new proxy. Call `InspectionResultStore.ClearProxySuspects` once for healthy/inconclusive targets.

- [ ] **Step 4: Persist the status/count compatibly.**

Add `recheck_inconclusive_count integer not null default 0` to the `proxy_replacement_tasks` create statement and `ensureColumns`. Extend every task INSERT, SELECT, scan, and completion UPDATE. Completion aggregation must include:

```sql
sum(case when status = 'recheck_inconclusive' then 1 else 0 end)
```

No credential/usage schema changes are permitted here.

- [ ] **Step 5: Verify GREEN.**

Run: `go test ./internal/service/proxyreplacement ./internal/repository/proxyreplacement ./internal/repository/sqlite -count=1` from `apps/manager-server`.

Expected: direct healthy/failure/inconclusive task tests, count persistence, and old task behavior all pass.

- [ ] **Step 6: Commit.**

```powershell
git add apps/manager-server/internal/service/proxyreplacement apps/manager-server/internal/repository/proxyreplacement apps/manager-server/internal/repository/sqlite apps/manager-server/internal/app/context.go
git commit -m "fix: recheck replacement proxies through exit IP"
```

### Task 4: Add the protected credential exit-IP endpoint and remove raw browser signals

**Files:**
- Modify: `apps/manager-server/internal/http/controller/proxyreplacement/handler.go`
- Modify: `apps/manager-server/internal/http/controller/proxyreplacement/handler_test.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/services/api/usageService.proxyPool.test.ts`

- [ ] **Step 1: Write failing handler/API-client tests.**

```go
func TestCredentialExitIPChecksAcceptOnlyStrictCredentialLocators(t *testing.T) {
    // POST valid {targets:[{clientKey,fileName,provider,...}]} => 200.
    // Reject proxyUrl, targetUrl, unknown key, blank/duplicate clientKey, inspection IDs, and >500 targets.
}
func TestCredentialExitIPChecksRequireAdmin(t *testing.T) {
    request := httptest.NewRequest(http.MethodPost, basePath+"/credential-exit-ip-checks", strings.NewReader(`{"targets":[]}`))
    recorder := httptest.NewRecorder()
    handler.Handle(recorder, request)
    if recorder.Code != http.StatusUnauthorized { t.Fatalf("status=%d want %d", recorder.Code, http.StatusUnauthorized) }
}
```

In the frontend test, spy on Axios and expect exactly:

```ts
expect(postSpy).toHaveBeenCalledWith(
  'https://manager.example/v0/management/proxy-replacements/credential-exit-ip-checks',
  { targets },
  expect.objectContaining({ headers: expect.anything() })
);
```

Assert response rows contain `clientKey`, outcome/exit IP/latency/reason and no `proxyUrl`.

- [ ] **Step 2: Verify RED.**

Run:

```powershell
Push-Location apps/manager-server; go test ./internal/http/controller/proxyreplacement -run TestCredentialExitIPChecks -count=1; Pop-Location
npm --workspace apps/web run test -- src/services/api/usageService.proxyPool.test.ts
```

Expected: route and client method are missing.

- [ ] **Step 3: Implement strict decoding and route binding.**

Add service interface method:

```go
VerifyCredentialExitIPs(context.Context, []model.ProxyReplacementTarget) (model.CredentialProxyExitIPCheckBatch, error)
```

Route before generic task matching:

```go
case path == basePath+"/credential-exit-ip-checks":
    h.requireAdmin(w, r, h.verifyCredentialExitIPs)
```

`decodeCredentialExitIPCheckRequest` must use `decodeOneJSON`, allow only `{ "targets": [...] }`, cap at `maxExitIPChecks`, require unique nonblank `clientKey`, and reject all unknown/raw URL fields and `inspectionRunId`/`inspectionAccountKey`. Bind it to `verifyCurrentCredentialExitIPs` and expose the results as `CredentialProxyExitIPCheckBatch`.

Delete `/inspection-signals`, `inspectionSignal`, `ReportInspectionSignal`, its implementation, and frontend `signalProxyInspectionFailure`. This removes the browser-controlled raw-proxy mutation path completely.

Add client method:

```ts
verifyCredentialExitIPs: async (base, managementKey, targets) =>
  withUsageServiceError(async () => {
    const response = await axios.post<CredentialProxyExitIPCheckBatch>(
      buildUrl(base, '/v0/management/proxy-replacements/credential-exit-ip-checks'),
      { targets }, { headers: managementHeaders(managementKey) }
    );
    return response.data;
  }),
```

Follow existing demo/error behavior; do not create a browser raw-proxy overload.

- [ ] **Step 4: Verify GREEN.**

Run:

```powershell
Push-Location apps/manager-server; go test ./internal/http/controller/proxyreplacement ./internal/service/proxyreplacement -count=1; Pop-Location
npm --workspace apps/web run test -- src/services/api/usageService.proxyPool.test.ts
```

Expected: strict input is accepted, URL-bearing/oversized input is rejected with 400, admin protection works, and the browser sends only strict targets.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/http/controller/proxyreplacement apps/manager-server/internal/service/proxyreplacement apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.proxyPool.test.ts
git commit -m "feat: verify credential proxies through protected exit IP API"
```

### Task 5: Clear legacy inferred proxy state exactly once without touching billing

**Files:**
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate_test.go`

- [ ] **Step 1: Write a failing migration regression test.**

Seed a temporary database with one `codex_inspection_results.proxy_suspect=1`, pool rows `pending_verification/transport_timeout`, `suspect/transport_502_request_failed`, `suspect/transport_unknown`, and `suspect/proxy_auth_407`, plus one `usage_events` row and one quota snapshot. Assert:

```go
// inferred inspection flag becomes false
// pending and three inference-only suspect rows become normal with cleared failure metadata
// proxy_auth_407 remains suspect
// usage event and quota snapshot values/counts are unchanged
// a second Migrate call changes nothing
```

- [ ] **Step 2: Verify RED.**

Run: `go test ./internal/repository/sqlite -run TestMigrateProxyExitIPOnlySemantics -count=1` from `apps/manager-server`.

Expected: fail because the one-time marker/cleanup migration does not exist.

- [ ] **Step 3: Implement the idempotent cleanup.**

After creation of both inspection and pool-state tables, create:

```sql
create table if not exists cpam_feature_migrations (
    name text primary key,
    applied_at_ms integer not null
)
```

Inside the normal `Migrate` transaction, run `ensureProxyExitIPOnlySemanticsMigration`. It must insert this marker first:

```sql
insert or ignore into cpam_feature_migrations(name, applied_at_ms)
values ('proxy_exit_ip_only_semantics_v1', ?)
```

Only if the insert affected one row, execute:

```sql
update codex_inspection_results set proxy_suspect = 0 where proxy_suspect <> 0;
update proxy_pool_state
set status = 'normal', last_failure_at_ms = 0, last_failure_reason = '', updated_at_ms = ?
where status = 'pending_verification'
   or (status = 'suspect' and lower(coalesce(last_failure_reason, '')) in (
       'transport_timeout', 'transport_502_request_failed', 'transport_unknown'
   ));
```

Do not delete rows or modify `usage_events`, snapshots, credential files, direct reasons (`proxy_auth_407`, `proxy_connect_timeout`, `proxy_dns_failure`, `proxy_url_invalid`), or CPA Core data.

- [ ] **Step 4: Verify GREEN.**

Run: `go test ./internal/repository/sqlite -count=1` from `apps/manager-server`.

Expected: stale inference is cleaned once, direct proof stays, and billing/usage assertions remain unchanged.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/repository/sqlite/migrate.go apps/manager-server/internal/repository/sqlite/migrate_test.go
git commit -m "fix: clear legacy inferred proxy anomaly state"
```

### Task 6: Make local browser inspection consume server direct evidence only

**Files:**
- Modify: `apps/web/src/features/monitoring/codexInspection.ts`
- Modify: `apps/web/src/features/monitoring/model/codexInspectionStorage.ts`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.tsx`
- Modify: `apps/web/src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx`
- Modify: `apps/web/src/features/monitoring/codexInspection.test.ts`
- Modify: `apps/web/src/features/accounts/model/proxyReplacement.ts`
- Modify: `apps/web/src/features/accounts/model/proxyReplacement.test.ts`

- [ ] **Step 1: Write failing storage and lifecycle tests.**

```ts
it('drops a legacy proxySuspect value without exit-IP proof', () => {
  const hydrated = hydrateCodexInspectionLastRun(savedResult({
    proxySuspect: true, proxyVerificationSource: undefined,
  }));
  expect(hydrated?.result.results[0].proxySuspect).toBe(false);
});
```

Add page lifecycle cases:

```ts
// request_error timeout -> verifyCredentialExitIPs(strict targets only)
// server healthy -> proxySuspect false
// server proxy_failed -> proxySuspect true
// server inconclusive -> proxySuspect false and no raw signal request
// 401 alone -> no exit-IP request and proxySuspect false
```

- [ ] **Step 2: Verify RED.**

Run:

```powershell
npm --workspace apps/web run test -- src/features/monitoring/codexInspection.test.ts src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/accounts/model/proxyReplacement.test.ts
```

Expected: new provenance/request assertions fail because local inference and `signalProxyInspectionFailure` still exist.

- [ ] **Step 3: Implement bounded direct evidence consumption.**

Add to `CodexInspectionResultItem`:

```ts
proxyVerificationSource?: 'exit_ip';
proxyVerificationOutcome?: ProxyExitIPOutcome;
```

Hydration/serialization retains a proxy abnormal flag only when:

```ts
item.proxyVerificationSource === 'exit_ip' &&
item.proxyVerificationOutcome === 'proxy_failed'
```

Otherwise set `proxySuspect: false` while preserving every other result/action/quota/history field.

Remove `isLocalProxySuspect`, `proxyFailureSignalKeysRef`, and the `signalProxyInspectionFailure` effect. Add a cancellable effect which derives only narrow timeout/CPA-502 candidate rows, excludes rows already containing direct provenance, derives strict targets from current auth-file identity data, chunks at 500, calls `usageServiceApi.verifyCredentialExitIPs` sequentially, and merges result rows by `clientKey`:

```ts
proxyVerificationSource: 'exit_ip',
proxyVerificationOutcome: response.outcome,
proxySuspect: response.outcome === 'proxy_failed',
```

Reject late completions when connection fingerprint or run generation changed. The effect must never trigger account refresh, quota fetch, auto recovery, or list-wide history refresh.

- [ ] **Step 4: Verify GREEN.**

Run: `npm --workspace apps/web run test -- src/features/monitoring/codexInspection.test.ts src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/accounts/model/proxyReplacement.test.ts`.

Expected: stale stored flags vanish; direct failure remains visible; 401 stays an account/re-login result; no raw URL API is invoked.

- [ ] **Step 5: Commit.**

```powershell
git add apps/web/src/features/monitoring apps/web/src/features/accounts/model/proxyReplacement.ts apps/web/src/features/accounts/model/proxyReplacement.test.ts
git commit -m "fix: render local proxy anomalies from exit IP proof only"
```

### Task 7: Show original/new proxy and direct recheck state in the replacement modal

**Files:**
- Modify: `apps/web/src/features/accounts/components/ProxyReplacementModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyReplacementModal.module.scss`
- Modify: `apps/web/src/features/accounts/components/ProxyReplacementModal.test.tsx`
- Modify: `apps/web/src/features/monitoring/model/proxyRecheckReconciliation.ts`
- Modify: `apps/web/src/features/monitoring/model/proxyRecheckReconciliation.test.ts`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [ ] **Step 1: Write failing UI/reconciliation tests.**

Use task rows containing `recheck_succeeded`, `recheck_failed`, and `recheck_inconclusive`:

```tsx
expect(screen.getByText('原代理')).toBeInTheDocument();
expect(screen.getByText('新代理')).toBeInTheDocument();
expect(screen.getByText('出口 IP 复检未确认')).toBeInTheDocument();
```

Add reconciliation assertions: successful and inconclusive replacement targets clear their local `proxySuspect`; direct recheck failure retains it.

- [ ] **Step 2: Verify RED.**

Run: `npm --workspace apps/web run test -- src/features/accounts/components/ProxyReplacementModal.test.tsx src/features/monitoring/model/proxyRecheckReconciliation.test.ts`.

Expected: failure because the modal currently only shows the new proxy and reconciliation only handles `recheck_succeeded`.

- [ ] **Step 3: Implement the UI and wording.**

Render account + labeled original proxy + labeled replacement proxy + status, without truncating credentials from the authorized user’s own modal:

```tsx
<span className={styles.proxyLabel}>{t('proxy_replacement.old_proxy')}</span>
<code title={item.oldProxyUrl}>{item.oldProxyUrl || t('proxy_replacement.proxy_not_saved')}</code>
<span className={styles.proxyLabel}>{t('proxy_replacement.new_proxy')}</span>
<code title={item.newProxyUrl}>{item.newProxyUrl || t('proxy_replacement.proxy_not_saved')}</code>
<span className={styles.itemStatus} data-status={item.status}>{t(itemStatusKey(item.status))}</span>
```

Change `.mappingRow` to account + old proxy + new proxy + status. At `max-width:680px`, stack each label/code instead of hiding a URL. Style `recheck_inconclusive` as warning and direct `recheck_failed` as danger. Include recheck-inconclusive count in the summary.

In reconciliation:

```ts
const clearsProxyAbnormal = new Set(['recheck_succeeded', 'recheck_inconclusive']);
```

Add exact locale keys in all four files: `old_proxy`, `new_proxy`, `proxy_not_saved`, `item_status_recheck_inconclusive`, and an inconclusive summary count. The Chinese state must say “出口 IP 复检未确认（未判定代理异常）”, never “代理失败”.

- [ ] **Step 4: Verify GREEN.**

Run:

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyReplacementModal.test.tsx src/features/monitoring/model/proxyRecheckReconciliation.test.ts
npm --workspace apps/web run build
```

Expected: old/new proxy URLs and all three direct outcomes render; typecheck/build exits 0.

- [ ] **Step 5: Commit.**

```powershell
git add apps/web/src/features/accounts/components/ProxyReplacementModal.tsx apps/web/src/features/accounts/components/ProxyReplacementModal.module.scss apps/web/src/features/accounts/components/ProxyReplacementModal.test.tsx apps/web/src/features/monitoring/model/proxyRecheckReconciliation.ts apps/web/src/features/monitoring/model/proxyRecheckReconciliation.test.ts apps/web/src/i18n/locales
git commit -m "feat: show direct proxy recheck evidence in replacement UI"
```

### Task 8: Regression verification and data-safety audit

**Files:**
- Modify only if a test proves a defect: exact files identified by the failure.
- Review: `docs/superpowers/specs/2026-10-22-proxy-exit-ip-only-semantics-design.md`

- [ ] **Step 1: Run backend regressions.**

```powershell
Push-Location apps/manager-server
go test ./internal/model ./internal/service/codexinspection ./internal/service/proxyreplacement ./internal/service/proxypoolstate ./internal/repository/codexinspection ./internal/repository/proxyreplacement ./internal/repository/sqlite ./internal/http/controller/proxyreplacement -count=1
Pop-Location
```

Expected: exit 0. If a test fails, add the smallest new failing regression test first, then repair through RED→GREEN.

- [ ] **Step 2: Run frontend regressions and production build.**

```powershell
npm --workspace apps/web run test -- src/features/monitoring/codexInspection.test.ts src/features/monitoring/CodexInspectionPage.lifecycle.test.tsx src/features/monitoring/model/proxyRecheckReconciliation.test.ts src/features/accounts/model/proxyReplacement.test.ts src/features/accounts/components/ProxyReplacementModal.test.tsx src/services/api/usageService.proxyPool.test.ts
npm --workspace apps/web run build
```

Expected: exit 0 with no TypeScript diagnostics.

- [ ] **Step 3: Verify removed inference and browser-sensitive inputs.**

```powershell
rg -n "signalProxyInspectionFailure|inspection-signals|IsSuspectedProxyTransportFailure|markProxySuspect|RecheckProxyReplacement" apps/manager-server apps/web/src
rg -n "proxyUrl|targetUrl" apps/manager-server/internal/http/controller/proxyreplacement/handler.go
```

Expected: first command has no production callers; second has only explicit rejection validation/tests, never an accepted request field.

- [ ] **Step 4: Audit scope before deployment.**

```powershell
git diff --check HEAD~7..HEAD
git status --short
git log --oneline -8
```

Review every changed path: no CPA Core, no billing/usage behavior modifications except migration test assertions proving non-modification, no browser-submitted raw proxy URL, and no upstream push.

- [ ] **Step 5: Commit only a verification repair if one was required.**

```powershell
git diff --name-only | ForEach-Object { git add -- $_ }
git commit -m "fix: complete direct proxy exit IP semantics"
```

Do not create an empty commit. Do not deploy, build Docker, restart a service, or push `origin/lo/custom` until LO explicitly requests that separate action.

## Self-review

- [x] Every approved requirement maps to a task: direct-only classification (1–2), replacement recheck (3), protected browser API (4), one-time stale cleanup (5), local panel behavior (6), original/new proxy UI (7), and regression/data safety (8).
- [x] The one existing fixed-target checker is the only health authority; account responses only nominate a check.
- [x] 401/429/quota/Credits/500/502/503 cannot directly enter “代理异常”.
- [x] Healthy, direct failure, inconclusive, and missing credential all have distinct task behavior.
- [x] SQLite cleanup protects direct evidence and does not touch billing/usage data.
- [x] Type/status names are consistent across model, repository, API, frontend client, modal, and reconciliation.
