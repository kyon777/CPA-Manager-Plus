# Proxy Exit IP Self-Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace timeout-only proxy exclusion with a server-side, authenticated proxy exit-IP verification flow that only excludes a proxy after a direct, controlled transport failure.

**Architecture:** A new CPAM-only verifier uses a fixed JSON IP Echo target through a selected saved proxy URL. The proxy pool receives a third `pending_verification` state; inspection timeouts enter that state without allocation exclusion, while successful exit-IP checks restore `normal` and explicit transport failures become `suspect`. The existing proxy-filter modal sends fingerprints only, shows bounded batch progress/results, and never sends raw proxy URLs to the new endpoint.

**Tech Stack:** Go 1.x (`net/http`, `net/url`, `net`, `context`), CPAM SQLite repository, React/TypeScript/Vitest, existing Manager Server administrator authentication and Axios service client.

---

## File map

| File | Responsibility |
| --- | --- |
| `apps/manager-server/internal/model/proxy_replacement.go` | Pool-state enum plus request/result DTOs for exit-IP verification. |
| `apps/manager-server/internal/model/proxy_replacement_test.go` | Model-level status and fingerprint regression tests. |
| `apps/manager-server/internal/service/proxypoolstate/service.go` | Persist a non-excluding `pending_verification` state using only a proxy fingerprint. |
| `apps/manager-server/internal/service/proxypoolstate/service_test.go` | Verify pending/suspect/normal persistence transitions. |
| `apps/manager-server/internal/repository/proxyreplacement/repository.go` | Accept the new status in SQLite upserts without schema or raw-URL changes. |
| `apps/manager-server/internal/repository/proxyreplacement/repository_test.go` | Verify SQLite round-trip for the new status. |
| `apps/manager-server/internal/service/proxyreplacement/exit_ip_check.go` | Isolated fixed-target IP Echo transport checker and result classification. |
| `apps/manager-server/internal/service/proxyreplacement/exit_ip_check_test.go` | Unit tests for valid IP, 407, transport errors, bad echo responses, and retry limits. |
| `apps/manager-server/internal/service/proxyreplacement/service.go` | Resolve saved URLs by fingerprint, run at most eight verifications, and persist result state. |
| `apps/manager-server/internal/service/proxyreplacement/service_test.go` | TDD coverage for signal-to-pending and batch state transitions. |
| `apps/manager-server/internal/config/config.go` / `config_test.go` | Add and validate the optional fixed Echo endpoint configuration. |
| `apps/manager-server/internal/app/context.go` | Wire the configured verifier into CPAM only. |
| `apps/manager-server/internal/http/controller/proxyreplacement/handler.go` / `_test.go` | Strict fingerprint-only endpoint under existing administrator authorization. |
| `apps/web/src/services/api/usageService.ts` | Typed client call and DTOs for the verification endpoint. |
| `apps/web/src/features/accounts/components/ProxyFilterModal.tsx` / `.test.tsx` / `.module.scss` | Pending/suspect grouping, one-row and batch self-check controls, progress, and output. |
| `apps/web/src/i18n/locales/{zh-CN,zh-TW,en,ru}.json` | Localized state, result, and button strings. |
| `CPAM_UPDATE_RUNBOOK.md` | Persist the custom feature contract so future upstream merges retain it. |

## Task 1: Add the non-excluding pending state

**Files:**
- Modify: `apps/manager-server/internal/model/proxy_replacement.go`
- Modify: `apps/manager-server/internal/model/proxy_replacement_test.go`
- Modify: `apps/manager-server/internal/service/proxypoolstate/service.go`
- Modify: `apps/manager-server/internal/service/proxypoolstate/service_test.go`
- Modify: `apps/manager-server/internal/repository/proxyreplacement/repository.go`
- Modify: `apps/manager-server/internal/repository/proxyreplacement/repository_test.go`

- [ ] **Step 1: Write the failing tests for a pending state.**

```go
func TestRegistryMarkPendingStoresFingerprintAndFiniteReason(t *testing.T) {
    state := mustMarkPending(t, "http://user:secret@198.51.100.20:8080/", "transport_timeout")
    if state.Status != model.ProxyPoolStatusPendingVerification {
        t.Fatalf("status = %q", state.Status)
    }
    if state.Fingerprint != model.ProxyFingerprint("http://user:secret@198.51.100.20:8080") {
        t.Fatal("stored state did not use normalized fingerprint")
    }
    if state.LastFailureReason != "transport_timeout" {
        t.Fatalf("reason = %q", state.LastFailureReason)
    }
}

func TestRepositoryAcceptsPendingVerificationPoolStatus(t *testing.T) {
    got := upsertPoolState(t, model.ProxyPoolState{
        Fingerprint: "fingerprint", Status: model.ProxyPoolStatusPendingVerification,
        LastFailureReason: "transport_timeout", UpdatedAtMS: 1,
    })
    if got.Status != model.ProxyPoolStatusPendingVerification {
        t.Fatalf("status = %q", got.Status)
    }
}
```

- [ ] **Step 2: Run the focused tests and verify RED.**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/proxypoolstate ./internal/repository/proxyreplacement -run 'PendingVerification|MarkPending' -count=1
```

Expected: compile failure because `ProxyPoolStatusPendingVerification` and `MarkPending` do not exist.

- [ ] **Step 3: Add the smallest state implementation.**

```go
const (
    ProxyPoolStatusNormal              ProxyPoolStatus = "normal"
    ProxyPoolStatusPendingVerification ProxyPoolStatus = "pending_verification"
    ProxyPoolStatusSuspect             ProxyPoolStatus = "suspect"
)

func (s *Service) MarkPending(ctx context.Context, proxyURL, reason string) error {
    fingerprint := model.ProxyFingerprint(proxyURL)
    if fingerprint == "" { return errors.New("proxy URL is required") }
    now := time.Now().UnixMilli()
    _, err := s.store.UpsertProxyPoolState(ctx, model.ProxyPoolState{
        Fingerprint: fingerprint, Status: model.ProxyPoolStatusPendingVerification,
        LastFailureAtMS: now, LastFailureReason: normalizeReason(reason), UpdatedAtMS: now,
    })
    return err
}

func validPoolStatus(status model.ProxyPoolStatus) bool {
    return status == model.ProxyPoolStatusNormal ||
        status == model.ProxyPoolStatusPendingVerification ||
        status == model.ProxyPoolStatusSuspect
}
```

Keep `buildPoolCandidates` unchanged: it must continue to exclude **only** `suspect` entries.

- [ ] **Step 4: Run the focused tests and verify GREEN.**

Run the Step 2 command again.

Expected: PASS. Confirm the candidate-allocation tests still assert that `pending_verification` remains eligible.

- [ ] **Step 5: Commit the state-only change.**

```powershell
git add apps/manager-server/internal/model/proxy_replacement.go `
  apps/manager-server/internal/model/proxy_replacement_test.go `
  apps/manager-server/internal/service/proxypoolstate `
  apps/manager-server/internal/repository/proxyreplacement
git commit -m "feat: track proxy verification pending state"
```

## Task 2: Build an isolated fixed-target exit-IP checker

**Files:**
- Create: `apps/manager-server/internal/service/proxyreplacement/exit_ip_check.go`
- Create: `apps/manager-server/internal/service/proxyreplacement/exit_ip_check_test.go`
- Modify: `apps/manager-server/internal/model/proxy_replacement.go`

- [ ] **Step 1: Write failing checker tests using a controlled HTTP transport.**

```go
type fakeExitIPTransport struct { results []exitIPHTTPResult; calls int }
func (f *fakeExitIPTransport) Fetch(context.Context, *url.URL, *url.URL) exitIPHTTPResult {
    result := f.results[f.calls]
    f.calls++
    return result
}

func TestExitIPCheckerReturnsHealthyForValidIPv4(t *testing.T) {
    checker := newCheckerForTest(fakeTransport(200, `{"ip":"203.0.113.7"}`))
    got := checker.Check(context.Background(), "http://proxy.example:8080")
    if got.Outcome != model.ProxyExitIPOutcomeHealthy || got.ExitIP != "203.0.113.7" { t.Fatal(got) }
}

func TestExitIPCheckerClassifies407AsProxyFailure(t *testing.T) {
    checker := newCheckerForTest(fakeTransport(407, ``))
    got := checker.Check(context.Background(), "http://proxy.example:8080")
    if got.Outcome != model.ProxyExitIPOutcomeProxyFailed || got.Reason != "proxy_auth_407" { t.Fatal(got) }
}

func TestExitIPCheckerLeavesEcho429Inconclusive(t *testing.T) {
    checker := newCheckerForTest(fakeTransport(429, ``))
    got := checker.Check(context.Background(), "http://proxy.example:8080")
    if got.Outcome != model.ProxyExitIPOutcomeInconclusive { t.Fatal(got) }
}
```

- [ ] **Step 2: Run the checker test file and verify RED.**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/proxyreplacement -run 'ExitIPChecker' -count=1
```

Expected: compile failure because the checker and outcome DTOs do not exist.

- [ ] **Step 3: Define DTOs and implement a testable transport boundary.**

```go
type ProxyExitIPOutcome string
const (
    ProxyExitIPOutcomeHealthy      ProxyExitIPOutcome = "healthy"
    ProxyExitIPOutcomeProxyFailed  ProxyExitIPOutcome = "proxy_failed"
    ProxyExitIPOutcomeInconclusive ProxyExitIPOutcome = "inconclusive"
    ProxyExitIPOutcomeNotSaved     ProxyExitIPOutcome = "not_saved"
)

type ProxyExitIPCheckResult struct {
    Fingerprint string             `json:"fingerprint"`
    Outcome     ProxyExitIPOutcome `json:"outcome"`
    ExitIP      string             `json:"exitIp,omitempty"`
    LatencyMS   int64              `json:"latencyMs,omitempty"`
    Reason      string             `json:"reason,omitempty"`
}

type exitIPTransport interface {
    Fetch(context.Context, *url.URL, *url.URL) exitIPHTTPResult
}

type ExitIPChecker interface {
    Check(context.Context, string) model.ProxyExitIPCheckResult
}
```

The production `Fetch` implementation must create an isolated `http.Transport` with `Proxy: http.ProxyURL(proxyURL)`, disable ambient proxy inheritance, enforce TLS verification, cap the body with `io.LimitReader(..., 4096)`, and issue a GET to the configured fixed Echo URL.

Use exactly two attempts only for a retryable network failure. Stop immediately on a valid IP, `407`, invalid proxy URL, or an Echo HTTP response. Treat Echo 4xx/5xx/429 and malformed JSON as `inconclusive`; classify network errors into finite redacted reasons. Never retain `proxyURL` in a result or log it.

- [ ] **Step 4: Add complete behavior tests.**

Add cases for IPv6 parsing, malformed `ip`, EOF/timeout retry exhaustion, connection-refused classification, invalid URL, response-body cap, and a proof that the fake transport sees the configured fixed Echo URL rather than a browser supplied target.

- [ ] **Step 5: Run checker tests and verify GREEN.**

```powershell
cd apps/manager-server
go test ./internal/service/proxyreplacement -run 'ExitIPChecker' -count=1
```

Expected: PASS with all valid-IP, explicit-proxy-failure, and inconclusive cases.

- [ ] **Step 6: Commit the isolated checker.**

```powershell
git add apps/manager-server/internal/model/proxy_replacement.go `
  apps/manager-server/internal/service/proxyreplacement/exit_ip_check.go `
  apps/manager-server/internal/service/proxyreplacement/exit_ip_check_test.go
git commit -m "feat: add proxy exit IP checker"
```

## Task 3: Wire pending signals and bounded batch verification

**Files:**
- Modify: `apps/manager-server/internal/service/proxyreplacement/service.go`
- Modify: `apps/manager-server/internal/service/proxyreplacement/service_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/config/config.go`
- Modify: `apps/manager-server/internal/config/config_test.go`

- [ ] **Step 1: Write service-level failing tests.**

```go
func TestReportInspectionSignalMarksPendingWithoutExcludingPool(t *testing.T) {
    service, pool := newServiceWithPool(t)
    marked, err := service.ReportInspectionSignal(context.Background(), timeoutEvidence())
    if err != nil || !marked { t.Fatalf("marked=%v err=%v", marked, err) }
    if pool.last.Status != model.ProxyPoolStatusPendingVerification { t.Fatal(pool.last) }
}

func TestVerifyExitIPsUsesSavedURLForFingerprintAndRestoresHealthy(t *testing.T) {
    service, pool := newServiceWithSavedURLAndChecker(t, "http://secret@proxy.example:8080", healthy("203.0.113.7"))
    got, err := service.VerifyProxyPoolExitIPs(context.Background(), []string{model.ProxyFingerprint("http://secret@proxy.example:8080")})
    if err != nil || got.Results[0].Outcome != model.ProxyExitIPOutcomeHealthy { t.Fatal(got, err) }
    if pool.restoreCalls != 1 { t.Fatal(pool.restoreCalls) }
}

func TestVerifyExitIPsNeverCallsCheckerForUnknownFingerprint(t *testing.T) {
    service, _, checker := newServiceWithSavedURLAndChecker(t, "http://saved.example:8080", healthy("203.0.113.7"))
    got, err := service.VerifyProxyPoolExitIPs(context.Background(), []string{"unknown"})
    if err != nil || got.Results[0].Outcome != model.ProxyExitIPOutcomeNotSaved || checker.calls != 0 { t.Fatal(got, err) }
}
```

- [ ] **Step 2: Run the service tests and verify RED.**

```powershell
cd apps/manager-server
go test ./internal/service/proxyreplacement -run 'ReportInspectionSignalMarksPending|VerifyExitIPs' -count=1
```

Expected: failure because `MarkPending` is not used and `VerifyProxyPoolExitIPs` does not exist.

- [ ] **Step 3: Add service interfaces and bounded coordinator.**

```go
type PoolStateStore interface {
    List(context.Context) ([]model.ProxyPoolState, error)
    MarkPending(context.Context, string, string) error
    MarkSuspect(context.Context, string, string) error
}

type ExitIPChecker interface { Check(context.Context, string) model.ProxyExitIPCheckResult }

func (s *Service) VerifyProxyPoolExitIPs(ctx context.Context, fingerprints []string) (model.ProxyExitIPCheckBatch, error) {
    // Deduplicate/trim at most 500 fingerprints.
    // Load saved settings once and build fingerprint -> raw URL in memory only.
    // Spawn min(8, len(valid)) workers; preserve input order in results.
    // healthy -> Restore; proxy_failed -> MarkSuspect; inconclusive/not_saved -> no state write.
}
```

Change `ReportInspectionSignal` from `MarkSuspect` to `MarkPending`. Do not alter `IsSuspectedProxyTransportFailure`; authentication, quota, Credits, 429, and ordinary business outcomes must remain excluded from this signal.

Add config field `ProxyExitIPCheckURL`, defaulting to `https://api.ipify.org?format=json`, sourced from `CPAMP_PROXY_EXIT_IP_CHECK_URL`. Parse/validate it once in `NewWithOptions` or in the checker constructor and fail closed to `inconclusive` rather than attempting arbitrary browser-provided targets.

Wire the constructed checker only into `ProxyReplacementService` in `app/context.go`; do not modify the CPA client, collector, scheduler, or usage services.

- [ ] **Step 4: Run service and config tests and verify GREEN.**

```powershell
cd apps/manager-server
go test ./internal/service/proxyreplacement ./internal/config -run 'ReportInspectionSignalMarksPending|VerifyExitIPs|ProxyExitIPCheckURL' -count=1
```

Expected: PASS. Confirm no test invokes CPA management APIs or reads account tokens.

- [ ] **Step 5: Commit coordinator and configuration wiring.**

```powershell
git add apps/manager-server/internal/service/proxyreplacement/service.go `
  apps/manager-server/internal/service/proxyreplacement/service_test.go `
  apps/manager-server/internal/app/context.go `
  apps/manager-server/internal/config/config.go `
  apps/manager-server/internal/config/config_test.go
git commit -m "feat: verify proxy pool through exit IP"
```

## Task 4: Expose a strict administrator endpoint

**Files:**
- Modify: `apps/manager-server/internal/http/controller/proxyreplacement/handler.go`
- Modify: `apps/manager-server/internal/http/controller/proxyreplacement/handler_test.go`

- [ ] **Step 1: Write failing HTTP handler tests.**

```go
func TestVerifyExitIPRequiresAdmin(t *testing.T) {
    response := request(t, http.MethodPost, "/v0/management/proxy-replacements/proxy-pool/verify-exit-ip", `{"fingerprints":["abc"]}`)
    if response.Code != http.StatusUnauthorized { t.Fatalf("status=%d", response.Code) }
}

func TestVerifyExitIPRejectsRawURLAndAcceptsOnlyFingerprints(t *testing.T) {
    response := authorizedRequest(t, `{"proxyUrl":"http://user:secret@proxy.example:8080"}`)
    if response.Code != http.StatusBadRequest { t.Fatalf("status=%d", response.Code) }
}

func TestVerifyExitIPReturnsResultsWithoutProxyURL(t *testing.T) {
    response := authorizedRequest(t, `{"fingerprints":["abc"]}`)
    if bytes.Contains(response.Body.Bytes(), []byte("proxy.example")) { t.Fatal("response leaked proxy URL") }
}
```

- [ ] **Step 2: Run handler tests and verify RED.**

```powershell
cd apps/manager-server
go test ./internal/http/controller/proxyreplacement -run 'VerifyExitIP' -count=1
```

Expected: 404 or compile failure because the endpoint and interface method do not exist.

- [ ] **Step 3: Implement request validation and response.**

```go
case path == basePath+"/proxy-pool/verify-exit-ip":
    h.requireAdmin(w, r, h.verifyExitIP)

// Accepted JSON shape exactly: {"fingerprints":["..."]}.
// Reject duplicate extraneous keys, empty values, more than 500 fingerprints,
// non-POST methods, and malformed JSON before calling the service.
response.JSON(w, http.StatusOK, batch)
```

Extend the controller `Service` interface with:

```go
VerifyProxyPoolExitIPs(context.Context, []string) (model.ProxyExitIPCheckBatch, error)
```

Map unavailable service errors to 503; preserve existing redaction behavior in errors and never echo raw URLs.

- [ ] **Step 4: Run handler tests and verify GREEN.**

Run the Step 2 command again.

Expected: PASS for authorization, strict body decoding, limits, response redaction, and result serialization.

- [ ] **Step 5: Commit the HTTP boundary.**

```powershell
git add apps/manager-server/internal/http/controller/proxyreplacement/handler.go `
  apps/manager-server/internal/http/controller/proxyreplacement/handler_test.go
git commit -m "feat: expose proxy exit IP verification endpoint"
```

## Task 5: Add typed web API support

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: the existing API service test file that covers proxy pool APIs, or create `apps/web/src/services/api/usageService.proxyPool.test.ts`

- [ ] **Step 1: Write a failing client test.**

```ts
it('posts only proxy fingerprints to the exit-IP verifier', async () => {
  await usageServiceApi.verifyProxyPoolExitIPs('https://manager.example', 'key', ['abc']);
  expect(axios.post).toHaveBeenCalledWith(
    'https://manager.example/v0/management/proxy-replacements/proxy-pool/verify-exit-ip',
    { fingerprints: ['abc'] },
    expect.objectContaining({ headers: expect.any(Object) })
  );
});
```

- [ ] **Step 2: Run the focused web test and verify RED.**

```powershell
npm --workspace apps/web run test -- src/services/api/usageService.proxyPool.test.ts
```

Expected: TypeScript/test failure because `verifyProxyPoolExitIPs` and its result types do not exist.

- [ ] **Step 3: Add explicit web DTOs and client call.**

```ts
export type ProxyPoolStatus = 'normal' | 'pending_verification' | 'suspect';
export type ProxyExitIPOutcome = 'healthy' | 'proxy_failed' | 'inconclusive' | 'not_saved';
export type ProxyExitIPCheckResult = {
  fingerprint: string; outcome: ProxyExitIPOutcome; exitIp?: string; latencyMs?: number; reason?: string;
};

verifyProxyPoolExitIPs: async (base, managementKey, fingerprints) =>
  withUsageServiceError(async () => {
    const response = await axios.post<ProxyExitIPCheckBatch>(
      buildUrl(base, '/v0/management/proxy-replacements/proxy-pool/verify-exit-ip'),
      { fingerprints }, { timeout: USAGE_SERVICE_TIMEOUT_MS, headers: authHeaders(managementKey) }
    );
    return response.data;
  }),
```

Demo mode must return a deterministic synthetic `inconclusive` response without making a network request.

- [ ] **Step 4: Run the focused web API test and verify GREEN.**

Run the Step 2 command again.

Expected: PASS and request payload contains no raw proxy URL.

- [ ] **Step 5: Commit the API client.**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.proxyPool.test.ts
git commit -m "feat: add proxy exit IP verification client"
```

## Task 6: Render self-check controls and results in the proxy filter modal

**Files:**
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.test.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyFilterModal.module.scss`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [ ] **Step 1: Write failing modal tests.**

```tsx
it('shows pending separately and sends only its fingerprint for a single exit-IP check', async () => {
  mocks.listProxyPoolStates.mockResolvedValue({ items: [{ fingerprint, status: 'pending_verification' }] });
  mocks.verifyProxyPoolExitIPs.mockResolvedValue({ results: [{ fingerprint, outcome: 'healthy', exitIp: '203.0.113.7', latencyMs: 31 }] });
  const view = renderProxyFilterModal();
  await waitFor(() => view.getByTestId(`proxy-pool-pending-${fingerprint}`));
  await user.click(view.getByRole('button', { name: 'accounts.proxy_pool_verify_one' }));
  expect(mocks.verifyProxyPoolExitIPs).toHaveBeenCalledWith('https://manager.example', 'key', [fingerprint]);
  expect(view.getByText('203.0.113.7')).toBeTruthy();
});

it('disables a second batch click while verification is running', async () => {
  mocks.verifyProxyPoolExitIPs.mockImplementation(() => new Promise(() => {}));
  const view = renderProxyFilterModalWithSuspects();
  await user.click(view.getByRole('button', { name: /proxy_pool_verify_all/ }));
  expect(view.getByRole('button', { name: /proxy_pool_verify_all/ })).toBeDisabled();
});
```

- [ ] **Step 2: Run modal tests and verify RED.**

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyFilterModal.test.tsx
```

Expected: missing API mock/property and missing controls.

- [ ] **Step 3: Implement modal state and precise grouping.**

Add `verificationBusy`, `verificationResults`, and a helper that maps saved URL to existing server state by fingerprint. Render two sections:

```text
待验证代理 N：巡检曾超时，仍参与分配；可自检
确认异常代理 N：出口 IP 自检失败，已从分配池排除；可自检 / 恢复使用
```

`handleVerify(fingerprints)` must deduplicate, submit fingerprints only, merge returned state/results into local modal state, and call `listProxyPoolStates` once after completion so healthy entries disappear from these sections. It must not call credential refresh, quota endpoints, account APIs, or CPA APIs.

Add one batch button and a per-row button. Keep existing per-row `恢复使用` untouched. Use `aria-live="polite"` for `已检查 / 正常 / 异常 / 不可判定` output. Add compact result styling that never logs or reveals credentials beyond the existing authenticated UI URL rendering.

- [ ] **Step 4: Add localization values.**

Add equivalent keys for pending count/hint, confirmed-suspect count/hint, verify all, verify one, progress, healthy exit IP, check duration, `inconclusive`, and each finite failure reason in all four locale files. The Chinese values must distinguish “待验证” from “确认异常”.

- [ ] **Step 5: Run modal tests, type-check, and verify GREEN.**

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyFilterModal.test.tsx
npm --workspace apps/web run type-check
```

Expected: PASS with no TypeScript errors.

- [ ] **Step 6: Commit the UI.**

```powershell
git add apps/web/src/features/accounts/components/ProxyFilterModal.tsx `
  apps/web/src/features/accounts/components/ProxyFilterModal.test.tsx `
  apps/web/src/features/accounts/components/ProxyFilterModal.module.scss `
  apps/web/src/i18n/locales
git commit -m "feat: self-check proxy exit IPs from filter modal"
```

## Task 7: Execute focused regression coverage

**Files:**
- Modify only if a regression test exposes a functional defect in Tasks 1–6.

- [ ] **Step 1: Run all touched Go packages.**

```powershell
cd apps/manager-server
go test ./internal/model ./internal/service/proxypoolstate ./internal/repository/proxyreplacement ./internal/service/proxyreplacement ./internal/http/controller/proxyreplacement ./internal/config -count=1
```

Expected: PASS on a Linux-compatible runner. On this Windows workstation, separately record existing permission/signal test limitations rather than weakening production assertions.

- [ ] **Step 2: Run web regression coverage and production build.**

```powershell
npm --workspace apps/web run test -- src/features/accounts/components/ProxyFilterModal.test.tsx src/services/api/usageService.proxyPool.test.ts
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: PASS.

- [ ] **Step 3: Perform a no-CPA-side-effect code audit.**

```powershell
rg -n 'api-call|auth-files|quota|usage|Token|token' apps/manager-server/internal/service/proxyreplacement/exit_ip_check.go apps/manager-server/internal/service/proxyreplacement/service.go
```

Expected: only unrelated existing interfaces/comments appear; the verifier itself must use no CPA HTTP client, no token, no quota/usage collector, and no auth-file mutation path.

- [ ] **Step 4: Commit any test-only regression repair.**

```powershell
git status --short
git add <only-the-verified-regression-files>
git commit -m "test: cover proxy exit IP verification regressions"
```

## Task 8: Preserve custom behavior in the CPAM update runbook

**Files:**
- Modify: `CPAM_UPDATE_RUNBOOK.md`

- [ ] **Step 1: Write the custom-feature memory entry.**

Add an immutable custom-feature entry stating:

```text
Proxy exit-IP self-check:
- inspection timeout/selected 502 evidence writes pending_verification and does not exclude allocation;
- only direct CPAM exit-IP verification can move a proxy to suspect;
- verifier uses fixed CPAMP_PROXY_EXIT_IP_CHECK_URL (default api.ipify JSON), eight workers, two five-second attempts;
- direct check never calls CPA Core, uses account tokens, modifies auth files, or affects billing/usage;
- keep the fingerprint-only management endpoint and proxy filter UI on future upstream merges.
```

- [ ] **Step 2: Verify the runbook entry is present and commit it.**

```powershell
rg -n -C 3 'Proxy exit-IP self-check|pending_verification|CPAMP_PROXY_EXIT_IP_CHECK_URL' CPAM_UPDATE_RUNBOOK.md
git add CPAM_UPDATE_RUNBOOK.md
git commit -m "docs: preserve proxy exit IP self-check customization"
```

## Task 9: Integration and deployment gate

**Files:**
- No source changes unless a verification failure requires a tested fix.

- [ ] **Step 1: Verify the feature branch contains only intended commits.**

```powershell
git log --oneline d0225ffe..HEAD
git diff --check d0225ffe...HEAD
git status --short
```

Expected: only the specification, plan, state/checker/service/API/UI/runbook commits; no secret, SQLite, auth JSON, or generated runtime artifact.

- [ ] **Step 2: Merge only after user directs deployment.**

```powershell
git -C C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\upstream-v1.13.2-merge merge --ff-only feat/proxy-exit-ip-self-check-20261021
```

Expected: fast-forward of `lo/custom`; no upstream push.

- [ ] **Step 3: Follow the existing CPAM update/deploy contract only when explicitly requested.**

Before and after deployment, capture local CPA Core container identity and remote `cpa.service` PID; rebuild/restart only local `cpa-manager-plus` and remote `cpam.service`; preserve all remote `/home/cpam/config`, `/home/cpam/secrets`, and `/home/cpam/data`; do not touch `/home/cpa`, `cpa.service`, or `cli-proxy-api`.

- [ ] **Step 4: Push only to the private repository when explicitly requested.**

```powershell
git -C C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\upstream-v1.13.2-merge push origin lo/custom
```

Expected: `origin/lo/custom` advances; no upstream remote receives a push.
