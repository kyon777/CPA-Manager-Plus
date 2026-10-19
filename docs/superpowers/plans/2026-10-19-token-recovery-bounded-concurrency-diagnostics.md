# Token Recovery Bounded Concurrency and Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent a 40-account manual recovery burst from overloading Token Acquisition and replace opaque `recovery_failed` rows with redacted, actionable stage-specific outcomes.

**Architecture:** The durable recovery queue remains able to accept the UI's forty requests at once.  The live application wires twelve recovery workers, so only twelve Token Acquisition calls execute concurrently; the existing four-slot CPA auth-file write gate and per-file mutation lock remain unchanged.  Typed errors at the Token Acquisition and CPA auth-file boundaries carry only safe category/status metadata, which Token Recovery converts into durable redacted reason codes and messages.

**Tech Stack:** Go 1.24, Go `net/http`, SQLite-backed Token Recovery task store, existing CPAM Manager Server test suites.

---

## File map

- `apps/manager-server/internal/service/tokenacquisition/client.go` — normalize outbound transport errors without leaking endpoint or credentials.
- `apps/manager-server/internal/service/tokenacquisition/client_test.go` — prove transport failures receive the new typed category.
- `apps/manager-server/internal/service/cpaauthfiles/client.go` — expose a redacted typed CPA auth-file operation error for download/upload failures.
- `apps/manager-server/internal/service/cpaauthfiles/client_test.go` — prove operation/status metadata and `errors.Is` compatibility.
- `apps/manager-server/internal/service/tokenrecovery/service.go` — translate safe boundary errors into durable recovery codes/messages and log only redacted diagnostics.
- `apps/manager-server/internal/service/tokenrecovery/service_test.go` — prove recovery code/message mapping and preserve timeout/write-gate behavior.
- `apps/manager-server/internal/app/context.go` — wire the live external Token Acquisition worker count to twelve.
- `docs/LO-CPAM-CUSTOM-MEMORY.md` — record the retained CPAM-only behavior for future upstream merges.

### Task 1: Normalize Token Acquisition transport failures

**Files:**
- Modify: `apps/manager-server/internal/service/tokenacquisition/client.go`
- Modify: `apps/manager-server/internal/service/tokenacquisition/client_test.go`

- [ ] **Step 1: Write the failing transport-error test**

```go
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAcquireClassifiesHTTPTransportFailure(t *testing.T) {
    client := New(Config{
        BaseURL: "https://token-acquisition.invalid",
        APIKey: "test-admin-key",
        HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
            return nil, errors.New("dial tcp proxy-user:secret@proxy.invalid: connect: reset")
        })},
        Timeout: time.Second,
    })

    _, err := client.Acquire(context.Background(), Request{Email: "person@example.com"})
    if !errors.Is(err, ErrTransportFailed) {
        t.Fatalf("Acquire() error = %v, want ErrTransportFailed", err)
    }
    if FailureReason(err) != "" {
        t.Fatalf("FailureReason() must not expose transport data, got %q", FailureReason(err))
    }
}
```

- [ ] **Step 2: Run the targeted test and verify it fails**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/tokenacquisition -run TestAcquireClassifiesHTTPTransportFailure -count=1
```

Expected: FAIL because `ErrTransportFailed` does not exist and raw `client.Do` errors are returned.

- [ ] **Step 3: Add the sentinel and normalize only raw request transport failures**

```go
var ErrTransportFailed = errors.New("token acquisition transport failed")

// In doJSON, retain ctx deadline/cancellation for normalizeContextError and
// replace only an http.Client transport error with ErrTransportFailed.
resp, err := client.Do(req)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
        return err
    }
    return ErrTransportFailed
}
```

Do not include `endpoint`, `err.Error()`, proxy URL, or API key in the returned error.

- [ ] **Step 4: Run the package test suite**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/tokenacquisition -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the focused boundary change**

```powershell
git add apps/manager-server/internal/service/tokenacquisition/client.go apps/manager-server/internal/service/tokenacquisition/client_test.go
git commit -m "fix: classify token recovery transport failures"
```

### Task 2: Carry redacted CPA auth-file operation metadata

**Files:**
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client.go`
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client_test.go`

- [ ] **Step 1: Write failing tests for download and upload operation errors**

```go
func TestClientDownloadReturnsRedactedOperationError(t *testing.T) {
    // httptest server returns 503 with a body containing credential-like text.
    _, err := New(server.Client()).Download(context.Background(), server.URL, "mgmt", "account.json")
    var operationErr *OperationError
    if !errors.As(err, &operationErr) || operationErr.Operation != OperationDownload || operationErr.StatusCode != http.StatusServiceUnavailable {
        t.Fatalf("operation error = %#v", operationErr)
    }
    if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "proxy") {
        t.Fatalf("operation error leaked body: %q", err)
    }
}

func TestClientUploadReturnsRedactedOperationError(t *testing.T) {
    // httptest server returns 502.
    err := New(server.Client()).Upload(context.Background(), server.URL, "mgmt", "account.json", []byte(`{}`))
    var operationErr *OperationError
    if !errors.As(err, &operationErr) || operationErr.Operation != OperationUpload || operationErr.StatusCode != http.StatusBadGateway {
        t.Fatalf("operation error = %#v", operationErr)
    }
}
```

- [ ] **Step 2: Run the targeted tests and verify they fail**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/cpaauthfiles -run 'TestClient(Download|Upload)ReturnsRedactedOperationError' -count=1
```

Expected: FAIL because `OperationError` and operation constants do not exist.

- [ ] **Step 3: Add the typed redacted operation error and wrap Download/Upload paths**

```go
type Operation string

const (
    OperationDownload Operation = "download"
    OperationUpload   Operation = "upload"
)

type OperationError struct {
    Operation  Operation
    StatusCode int
    cause      error
}

func (e *OperationError) Unwrap() error { return e.cause }
```

`Error()` must expose only operation and optional HTTP status.  Preserve `errors.Is(err, ErrAuthFileNotFound)`, `ErrResponseTooLarge`, and `ErrAuthFileUploadRejected` by wrapping the original cause.  Never copy a CPA response body into the typed error.

- [ ] **Step 4: Run the package test suite**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/cpaauthfiles -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the focused boundary change**

```powershell
git add apps/manager-server/internal/service/cpaauthfiles/client.go apps/manager-server/internal/service/cpaauthfiles/client_test.go
git commit -m "fix: retain redacted CPA auth-file failure stages"
```

### Task 3: Persist safe recovery categories and cap live acquisition concurrency

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [ ] **Step 1: Write failing recovery mapping tests**

```go
func TestRecoveryFailureClassifiesTransportError(t *testing.T) {
    if got := recoveryFailureCode(tokenacquisition.ErrTransportFailed); got != "token_acquisition_transport_failed" {
        t.Fatalf("code = %q", got)
    }
    if got := recoveryFailureMessage(tokenacquisition.ErrTransportFailed); !strings.Contains(got, "网络") {
        t.Fatalf("message = %q", got)
    }
}

func TestRecoveryFailureClassifiesCPAOperationByStage(t *testing.T) {
    err := &cpaAuthFileOperationError{operation: "upload", statusCode: http.StatusBadGateway, err: errors.New("hidden")}
    if got := recoveryFailureCode(err); got != "cpa_auth_file_upload_failed" {
        t.Fatalf("code = %q", got)
    }
    if got := recoveryFailureMessage(err); got != "CPA Core 凭证写入失败：HTTP 502" {
        t.Fatalf("message = %q", got)
    }
}
```

- [ ] **Step 2: Run the targeted tests and verify they fail**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/tokenrecovery -run 'TestRecoveryFailureClassifies(TransportError|CPAOperationByStage)' -count=1
```

Expected: FAIL because the transport and stage-specific mappings do not exist.

- [ ] **Step 3: Add redacted mapping and safe log output**

```go
const RecommendedTokenAcquisitionWorkerCount = 12

case errors.Is(err, tokenacquisition.ErrTransportFailed):
    return "token_acquisition_transport_failed"
case isCPAAuthFileOperationError(err, "upload"):
    return "cpa_auth_file_upload_failed"
```

Map read, upload, and verify separately; use only fixed Chinese text plus a valid HTTP status (`>= 300`) when present.  `process` must pass the same safe code/message into `FailTokenRecovery` and log only those values plus task ID.  Add `TestRecommendedTokenAcquisitionWorkerCount` in `service_test.go` to lock the production recommendation at `12`. Preserve current timeout precedence and automatic one-shot behavior.

- [ ] **Step 4: Wire the live worker count to twelve**

```go
WorkerCount: tokenrecoverysvc.RecommendedTokenAcquisitionWorkerCount,
```

Do not change `maxWorkerCount` or the UI queueing endpoint.  The durable queue still accepts the current page immediately; twelve workers only constrain active external acquisition work.

- [ ] **Step 5: Run targeted and broad manager-server tests**

Run:

```powershell
cd apps/manager-server
go test ./internal/service/tokenrecovery ./internal/service/tokenacquisition ./internal/service/cpaauthfiles ./internal/app -count=1
go test ./... -count=1
```

Expected: PASS.  Existing 40-worker unit tests continue to prove the service can be configured to 40; production wiring alone uses the recommended twelve-worker value.

- [ ] **Step 6: Update durable custom memory and commit**

Append the live `12 / 4` recovery boundary, safe error-code mapping, and no-retry guarantees to `docs/LO-CPAM-CUSTOM-MEMORY.md`, then run:

```powershell
git add apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/tokenrecovery/service_test.go apps/manager-server/internal/app/context.go docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "fix: bound token recovery acquisition and report stages"
```

### Task 4: Build, deploy only CPAM, and verify

**Files:**
- No source edits; use the clean `lo/custom` worktree only.

- [ ] **Step 1: Verify formatting and diff hygiene**

Run:

```powershell
cd apps/manager-server
gofmt -w internal/service/tokenacquisition/client.go internal/service/tokenacquisition/client_test.go internal/service/cpaauthfiles/client.go internal/service/cpaauthfiles/client_test.go internal/service/tokenrecovery/service.go internal/service/tokenrecovery/service_test.go internal/app/context.go
git -C .. diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Build the CPAM candidate**

Run the existing CPAM-only Docker build path from the clean worktree.  Record the target image tag; do not alter `source`, CPA Core, `cli-proxy-api`, or `usage.sqlite`.

- [ ] **Step 3: Deploy local CPAM only and verify Core identity is unchanged**

Capture `cli-proxy-api` container ID and `StartedAt`, deploy only `cpa-manager-plus` with `--no-build --no-deps`, then verify `/health`, CPAM logs, and the unchanged Core identity.

- [ ] **Step 4: Deploy remote CPAM only and verify service health**

Upload only the built CPAM artifact to `/home/cpam`, restart `cpam.service`, check its health endpoint and journal, and verify `cpa.service` PID/start timestamp did not change.

- [ ] **Step 5: Push only the private branch**

```powershell
git push origin lo/custom
```

Expected: private `origin/lo/custom` advances.  Never push to `upstream`.