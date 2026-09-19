# TokenAcquisition Server-Side Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a Codex credential is classified as requiring reauthentication, have Manager Server acquire one replacement token set from TokenAcquisition, safely merge it into the same CPA Core auth JSON, and expose a manual retry control without disclosing secrets to the browser.

**Architecture:** A new Manager Server recovery service owns a durable SQLite state machine and a small worker. It obtains the target credential under the existing per-auth-file lock, releases that lock while TokenAcquisition is polled, then reacquires it to merge and verify the same physical CPA Core auth file. The browser only submits an opaque locator and polls redacted task status; all API keys, token material, full JSON, and proxy credentials remain in Manager Server memory.

**Tech Stack:** Go 1.24, SQLite, `net/http`, existing Manager Server services/controllers, React/TypeScript/Vitest, Docker Compose secret-file configuration.

---

## File structure

| File | Responsibility |
| --- | --- |
| `apps/manager-server/internal/config/config.go` | Load external base URL and API key from direct env or `_FILE` secret input. |
| `apps/manager-server/internal/model/token_recovery.go` | Redacted target/task/status API model. |
| `apps/manager-server/internal/repository/tokenrecovery/repository.go` | Atomic SQLite task persistence and queue claiming. |
| `apps/manager-server/internal/repository/sqlite/migrate.go` | Create the durable task table and indexes. |
| `apps/manager-server/internal/store/store.go` | Construct and expose the recovery repository. |
| `apps/manager-server/internal/service/tokenacquisition/client.go` | One-email TokenAcquisition POST/poll client; no logging of secrets. |
| `apps/manager-server/internal/service/tokenrecovery/auth_json.go` | Pure in-memory JSON locator, proxy extraction, merge and verification codec. |
| `apps/manager-server/internal/service/tokenrecovery/service.go` | State transitions, lock discipline, Core download/upload/verify orchestration. |
| `apps/manager-server/internal/service/cpaauthfiles/client.go` | Upload original physical file name through CPA Core's auth-files endpoint. |
| `apps/manager-server/internal/http/controller/tokenrecovery/handler.go` | Authorized redacted status/signal/manual endpoints. |
| `apps/manager-server/internal/worker/token_recovery_signal.go` | Feed classified Codex reauth monitoring events to durable automatic signal intake. |
| `apps/manager-server/internal/service/codexinspection/service.go` | Feed successful Codex inspection `reauth` results to recovery signal intake. |
| `apps/manager-server/internal/app/context.go`, `cmd/cpa-manager-plus/main.go`, `internal/http/router/router.go` | Compose, start and route the feature. |
| `apps/web/src/services/api/usageService.ts` | Browser methods carrying only opaque locators and redacted task state. |
| `apps/web/src/features/oauth/CodexReauthDialog.tsx` | Manual server acquisition button and polling UI alongside existing OAuth flow. |
| `apps/web/src/pages/*CodexInspection*.tsx`, `AccountsPage.tsx` | Pass Manager connection and emit local inspection reauth signals. |

### Task 1: Configuration and pure credential JSON codec

**Files:**
- Modify: `apps/manager-server/internal/config/config.go`
- Create: `apps/manager-server/internal/config/config_token_acquisition_test.go`
- Create: `apps/manager-server/internal/service/tokenrecovery/auth_json.go`
- Create: `apps/manager-server/internal/service/tokenrecovery/auth_json_test.go`

- [ ] **Step 1: Write failing configuration tests.**

```go
func TestLoadReadsTokenAcquisitionSecretFile(t *testing.T) {
    t.Setenv("TOKEN_ACQUISITION_API_KEY", "")
    path := filepath.Join(t.TempDir(), "token-acquisition-key")
    require.NoError(t, os.WriteFile(path, []byte("server-only-key\n"), 0o600))
    t.Setenv("TOKEN_ACQUISITION_API_KEY_FILE", path)
    t.Setenv("TOKEN_ACQUISITION_BASE_URL", "https://tokens.test")
    cfg, err := LoadWithoutCreatingDefault()
    require.NoError(t, err)
    require.Equal(t, "server-only-key", cfg.TokenAcquisitionAPIKey)
    require.Equal(t, "https://tokens.test", cfg.TokenAcquisitionBaseURL)
}
```

- [ ] **Step 2: Run the configuration test and verify it fails because the new fields do not exist.**

Run: `go test ./internal/config -run TestLoadReadsTokenAcquisitionSecretFile -count=1` from `apps/manager-server`.

- [ ] **Step 3: Implement the configuration fields with a non-secret default URL.**

```go
const defaultTokenAcquisitionSecretFile = "/run/secrets/token_acquisition_api_key"

// in Config
TokenAcquisitionBaseURL string
TokenAcquisitionAPIKey  string

// in LoadWithOptions return literal
TokenAcquisitionBaseURL: env("TOKEN_ACQUISITION_BASE_URL", "https://401.kyon888.xyz"),
TokenAcquisitionAPIKey:  readSecret("TOKEN_ACQUISITION_API_KEY", "TOKEN_ACQUISITION_API_KEY_FILE", defaultTokenAcquisitionSecretFile),
```

- [ ] **Step 4: Run configuration tests and commit.**

Run: `go test ./internal/config -count=1`

```bash
git add apps/manager-server/internal/config
git commit -m "feat: load token acquisition server configuration"
```

- [ ] **Step 5: Write failing JSON codec tests before adding codec code.** The test fixture must include `note`, `priority`, `proxy_url`, unknown object data, a legacy `account_id`, and a sibling array record. Assert that a matching `auth_index` only changes its record, preserves all unrelated data and HTTP proxy, writes three token fields, writes a nonempty returned `chatgpt_account_id`, and synchronizes only pre-existing aliases. Add separate assertions that an email mismatch, empty token, ambiguous email-only array target, and non-HTTP proxy fail without producing output.

```go
merged, err := MergeAuthJSON(source, Locator{AuthIndex: "7", AccountEmail: "a@example.com"}, Result{
    Email: "A@example.com", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", ChatGPTAccountID: "new-id",
})
require.NoError(t, err)
require.JSONEq(t, `{"note":"keep","priority":20000,"proxy_url":"http://u:p@proxy:8080","account_id":"new-id","chatgpt_account_id":"new-id"}`, selectedRecord(merged, "7"))
```

- [ ] **Step 6: Run codec test and verify it fails because `MergeAuthJSON` is missing.**

Run: `go test ./internal/service/tokenrecovery -run TestMergeAuthJSON -count=1`

- [ ] **Step 7: Implement the pure codec.** Define `Locator`, `Result`, `CredentialView`, `ParseCredential`, `MergeAuthJSON`, and `VerifyMergedAuthJSON`. Decode with `json.Decoder.UseNumber`; accept root object or root array; select exact `auth_index` (`auth_index`, `authIndex`, `auth-index`) first and otherwise require exactly one normalized-email match. Recognize email aliases `email`, `account`, `account_email`, `accountEmail`, `username`. Read only `proxy_url`, `proxyUrl`, `proxy-url` as a TokenAcquisition proxy when `url.Parse` reports `http`; do not modify proxy fields. Merge only `access_token`, `refresh_token`, `id_token`, and nonempty `chatgpt_account_id`; if account-ID aliases already exist, set those existing aliases to the same new value. Return sentinel errors without embedding source JSON, proxy credentials, or token values.

- [ ] **Step 8: Run codec tests and commit.**

Run: `go test ./internal/service/tokenrecovery -run 'Test(MergeAuthJSON|ParseCredential)' -count=1`

```bash
git add apps/manager-server/internal/service/tokenrecovery
git commit -m "feat: add safe auth json token merge codec"
```

### Task 2: TokenAcquisition and CPA Core transport clients

**Files:**
- Create: `apps/manager-server/internal/service/tokenacquisition/client.go`
- Create: `apps/manager-server/internal/service/tokenacquisition/client_test.go`
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client.go`
- Modify: `apps/manager-server/internal/service/cpaauthfiles/client_test.go`

- [ ] **Step 1: Write failing TokenAcquisition HTTP tests.** Use `httptest.NewServer`; require `X-Api-Key`, reject CDK fields, decode `POST /v1/tokens`, and assert `direct:false` plus the exact HTTP proxy for a proxy credential. Return a batch ID, then two poll responses (`done:false`, followed by matching `status:"ok"` result). Add a direct credential test that asserts `direct:true` and absence of `proxy`; add mismatch/failed-job/incomplete-token tests that return redacted errors.

```go
result, err := client.Acquire(ctx, tokenacquisition.Request{Email: "a@example.com", HTTPProxy: "http://u:p@p:80"})
require.NoError(t, err)
require.Equal(t, "access", result.AccessToken)
require.Equal(t, "new-chatgpt-id", result.ChatGPTAccountID)
```

- [ ] **Step 2: Run the transport tests and verify compile failure for missing client.**

Run: `go test ./internal/service/tokenacquisition -count=1`

- [ ] **Step 3: Implement the client.** Expose `Client` interface and `New(Config)`. `Acquire` sends exactly one structured account to `POST /v1/tokens`, polls `GET /v1/batches/{batchID}` every two seconds until `done`, uses a bounded context, and accepts only an `ok` job whose normalized email matches request email and has all three nonempty token fields. Use short failure codes (`token_acquisition_conflict`, `token_acquisition_failed`, `token_acquisition_timeout`, `token_acquisition_invalid_result`) rather than raw response bodies. Never log request/response bodies or authorization headers.

- [ ] **Step 4: Run TokenAcquisition tests.**

Run: `go test ./internal/service/tokenacquisition -count=1`

- [ ] **Step 5: Write a failing Core upload test.** Build a fake Core endpoint that accepts `POST /v0/management/auth-files`, checks bearer authorization, parses multipart field `file`, and asserts both the exact physical filename and bytes.

```go
err := client.Upload(ctx, server.URL, "core-key", "account.json", []byte(`{"email":"a@example.com"}`))
require.NoError(t, err)
```

- [ ] **Step 6: Run upload test and verify it fails for missing `Upload`.**

Run: `go test ./internal/service/cpaauthfiles -run TestUpload -count=1`

- [ ] **Step 7: Implement `cpaauthfiles.Client.Upload`.** Create a multipart request with field name `file`, retain the passed filename exactly, authorize with the Core management key, enforce existing request/response bounds, and return a sanitized non-2xx error.

- [ ] **Step 8: Run client tests and commit.**

Run: `go test ./internal/service/tokenacquisition ./internal/service/cpaauthfiles -count=1`

```bash
git add apps/manager-server/internal/service/tokenacquisition apps/manager-server/internal/service/cpaauthfiles
git commit -m "feat: add token acquisition and auth file transports"
```

### Task 3: Durable task state and repository

**Files:**
- Create: `apps/manager-server/internal/model/token_recovery.go`
- Create: `apps/manager-server/internal/repository/tokenrecovery/repository.go`
- Create: `apps/manager-server/internal/repository/tokenrecovery/repository_test.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate.go`
- Modify: `apps/manager-server/internal/repository/sqlite/migrate_test.go`
- Modify: `apps/manager-server/internal/store/store.go`

- [ ] **Step 1: Write failing repository tests.** Test atomic automatic deduplication, an automatic failure rejecting subsequent automatic signals, an explicit manual retry transitioning a failed task to `manual_queued`, and exactly one atomic `ClaimNextQueued` winner with concurrent claimers.

```go
first, err := repo.SignalAutomatic(ctx, tokenrecovery.Target{FileName: "A.json", AuthIndex: "7", AccountEmail: "a@example.com", Provider: "codex", ObservedAtMS: 100})
second, err := repo.SignalAutomatic(ctx, sameTarget)
require.NoError(t, err)
require.Equal(t, first.ID, second.ID)
require.Equal(t, tokenrecovery.StatusAutoQueued, second.Status)
```

- [ ] **Step 2: Run repository tests and verify they fail for missing model/repository.**

Run: `go test ./internal/repository/tokenrecovery ./internal/repository/sqlite -run 'Test(TokenRecovery|Migrate)' -count=1`

- [ ] **Step 3: Implement model, migration, repository, and store wiring.** Define statuses `auto_queued`, `auto_running`, `succeeded`, `auto_failed_manual_only`, `manual_queued`, `manual_running`, and `manual_failed_manual_only`; mode values `auto`/`manual`; and a redacted `Task` with no credential JSON, proxy, API key, or token fields. Create `token_recovery_tasks` with unique `identity_key`, file/auth/email columns, status/mode/error-code/timestamps, status/updated indexes, and transactional claim using `UPDATE ... WHERE status IN (...)` inside an immediate transaction. Generate `identity_key` from lowercased file name, auth index, and email; require auth index or email. Make the repository expose `SignalAutomatic`, `RequestManual`, `Get`, `ClaimNextQueued`, `Complete`, `Fail`, and `FailRunningOnStartup`.

- [ ] **Step 4: Run repository/migration tests and commit.**

Run: `go test ./internal/repository/tokenrecovery ./internal/repository/sqlite ./internal/store -count=1`

```bash
git add apps/manager-server/internal/model apps/manager-server/internal/repository apps/manager-server/internal/store
git commit -m "feat: persist token recovery task state"
```

### Task 4: Recovery orchestration and state-machine worker

**Files:**
- Create: `apps/manager-server/internal/service/tokenrecovery/service.go`
- Create: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: Write a failing end-to-end service test with real in-memory codec and fake HTTP Core/TokenAcquisition servers.** It must prove the file lock is released while the external request waits, the preflight HTTP proxy is forwarded, the same original filename is uploaded, a changed `chatgpt_account_id` is written without comparing it to its old value, and `note`, priority, proxy and unknown data survive. Add the negative case: first automatic acquisition fails, a repeated automatic signal does not issue another external POST, then `RequestManual` does issue one new POST.

```go
task, err := service.SignalAutomatic(ctx, target)
require.NoError(t, err)
waitForStatus(t, service, task.ID, tokenrecovery.StatusSucceeded)
require.Equal(t, 1, tokenServer.PostCount())
require.Contains(t, uploadedJSON, `"note":"keep"`)
require.Contains(t, uploadedJSON, `"chatgpt_account_id":"new-id"`)
```

- [ ] **Step 2: Run service test and verify it fails because the orchestration service is absent.**

Run: `go test ./internal/service/tokenrecovery -run TestService -count=1`

- [ ] **Step 3: Implement the worker and orchestration with injected interfaces.** `Service` receives `*store.Store`, `*cpaauthfiles.MutationCoordinator`, a Core client, a TokenAcquisition client, and a clock. `Start` first turns recovered `*_running` records into the corresponding `*_failed_manual_only` state, then wakes on durable queued work. For every task: acquire the exact file lock, load setup, download/parse target and capture email + HTTP proxy, release lock; acquire external tokens outside the lock; acquire the same file lock again, re-download, re-locate and re-check only email, merge, upload using the exact physical file name, re-download and verify expected email/tokens, then finish. Convert all errors to fixed redacted codes; no `err.Error()` should store API responses, token data or proxy credentials.

- [ ] **Step 4: Run recovery tests.**

Run: `go test ./internal/service/tokenrecovery -count=1`

- [ ] **Step 5: Commit.**

```bash
git add apps/manager-server/internal/service/tokenrecovery
git commit -m "feat: recover codex credentials server side"
```

### Task 5: Manager Server endpoints and automatic signal sources

**Files:**
- Create: `apps/manager-server/internal/http/controller/tokenrecovery/handler.go`
- Create: `apps/manager-server/internal/http/controller/tokenrecovery/handler_test.go`
- Create: `apps/manager-server/internal/worker/token_recovery_signal.go`
- Create: `apps/manager-server/internal/worker/token_recovery_signal_test.go`
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/http/router/router.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service.go`
- Modify: `apps/manager-server/internal/service/codexinspection/service_test.go`
- Modify: `apps/manager-server/cmd/cpa-manager-plus/main.go`

- [ ] **Step 1: Write failing handler tests.** Require `AuthorizePanel`; test `POST /v0/management/token-recovery/signals`, `POST /v0/management/token-recovery/manual`, and `GET /v0/management/token-recovery`. Assert JSON inputs/outputs contain only `fileName`, `authIndex`, `accountEmail`, provider, timestamps and redacted state. Explicitly reject an `accountId` payload field rather than accepting it as identity material.

- [ ] **Step 2: Run handler test and verify the route/controller is missing.**

Run: `go test ./internal/http/controller/tokenrecovery ./internal/http/router -count=1`

- [ ] **Step 3: Implement handler and route.** Build `tokenrecovery.Handler` around the application recovery service; make only exact codex provider targets valid; add its prefix before the generic `/v0/management/` CPA proxy branch; map malformed target to 400, disabled/missing setup to 412, and internal errors to 500 without raw secret-bearing error strings.

- [ ] **Step 4: Write failing source tests.** Add a recorder notifier to Codex inspection tests and prove a completed `reauth` Codex result signals once using file/auth-index/email only; prove a non-reauth or non-Codex result does not. Add a usage event worker test proving only classifier decision `ActionReauth` for Codex calls automatic signal intake.

- [ ] **Step 5: Run source tests and verify they fail before wiring.**

Run: `go test ./internal/service/codexinspection ./internal/worker -run 'Test.*(TokenRecovery|Reauth)' -count=1`

- [ ] **Step 6: Wire sources and runtime.** Add a small `ReauthRecoveryNotifier` interface to Codex inspection options. Add `TokenRecoverySignalWorker` to the top-level usage fanout independently of account-action feature flags. Construct `tokenrecovery.Service` in app context with configured external client, inject it into Codex inspection and controller context, start its worker from main, and cancel it with process context.

- [ ] **Step 7: Run focused server tests and commit.**

Run: `go test ./internal/http/controller/tokenrecovery ./internal/http/router ./internal/service/codexinspection ./internal/worker ./internal/app -count=1`

```bash
git add apps/manager-server/internal/http apps/manager-server/internal/worker apps/manager-server/internal/service/codexinspection apps/manager-server/internal/app apps/manager-server/cmd
git commit -m "feat: trigger token recovery from codex reauth"
```

### Task 6: Browser API and reauthentication dialog

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/services/api/usageService.test.ts` or existing focused API test
- Modify: `apps/web/src/features/oauth/CodexReauthDialog.tsx`
- Modify: `apps/web/src/features/oauth/CodexReauthDialog.test.tsx`
- Modify: `apps/web/src/pages/AccountsPage.tsx`
- Modify: `apps/web/src/pages/CodexInspectionPage.tsx`
- Modify: `apps/web/src/pages/ServerCodexInspectionPage.tsx`
- Modify locale resources only if the dialog already requires translated keys.

- [ ] **Step 1: Write failing browser API tests.** Assert the manual and automatic methods POST to Manager Server and their body contains an opaque target (`fileName`, `authIndex`, `accountEmail`, `provider`, `observedAtMs`) but not `access_token`, `refresh_token`, `id_token`, `chatgpt_account_id`, `proxy`, credential JSON, API key or `accountId`.

- [ ] **Step 2: Run the focused API test and verify it fails for absent methods.**

Run: `npm --workspace apps/web run test -- src/services/api/usageService.test.ts`

- [ ] **Step 3: Implement redacted API methods and tests.** Add `getTokenRecovery`, `signalTokenRecovery`, and `requestTokenRecoveryManual` types/methods. Preserve existing auth header behavior and return only the server’s redacted task shape. Demo mode returns a local redacted placeholder and never contacts TokenAcquisition.

- [ ] **Step 4: Write failing dialog tests.** Render a Codex reauth dialog with Manager Server connection data; assert the new button says server acquisition, initially retrieves task status, sends only opaque locator when clicked, shows failure with an enabled retry button, polls queued/running state, and calls existing `onSuccess` exactly once after server task success. Keep tests covering current OAuth copy/submit behavior.

- [ ] **Step 5: Run dialog test and verify it fails.**

Run: `npm --workspace apps/web run test -- src/features/oauth/CodexReauthDialog.test.tsx`

- [ ] **Step 6: Implement dialog and page wiring.** Add optional Manager connection props to `CodexReauthDialog`; do not make browser code aware of Key/TokenAcquisition fields. Poll status at a bounded interval only while queued/running. Add the server acquisition section alongside—not instead of—OAuth. Pass manager base/key from Accounts and both inspection pages. In the local inspection page, issue one in-memory-deduped automatic signal per completed Codex `reauth` result; backend durability remains the authoritative deduplicator.

- [ ] **Step 7: Run frontend tests/typecheck and commit.**

Run:
```bash
npm --workspace apps/web run test -- src/services/api/usageService.test.ts src/features/oauth/CodexReauthDialog.test.tsx
npm --workspace apps/web run type-check
```

```bash
git add apps/web/src
git commit -m "feat: add token recovery controls to codex reauth dialog"
```

### Task 7: Deployment wiring and final verification

**Files:**
- Modify: tracked deployment documentation or sample configuration under the source repository, if one exists.
- Do not add a real secret file, real API key, or a `secrets/` file to Git.

- [ ] **Step 1: Write/update a configuration documentation assertion or sample.** The sample must contain exactly the variable names below and a non-secret placeholder only:

```yaml
environment:
  TOKEN_ACQUISITION_BASE_URL: https://401.kyon888.xyz
  TOKEN_ACQUISITION_API_KEY_FILE: /run/secrets/token_acquisition_api_key
volumes:
  - ./secrets/token_acquisition_api_key:/run/secrets/token_acquisition_api_key:ro
```

- [ ] **Step 2: Confirm no key or token was introduced.**

Run: `git grep -n -i -E 'TAK-[A-Za-z0-9]|access_token.*test-admin-key|token_acquisition_api_key: [^#]' -- ':!docs/superpowers'`

Expected: no real key/token material; only environment variable names and non-secret placeholders.

- [ ] **Step 3: Run full backend verification.**

Run: `go test ./...` from `apps/manager-server`.

- [ ] **Step 4: Run full frontend verification.**

Run:
```bash
npm --workspace apps/web run test
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

- [ ] **Step 5: Check diffs and commit documentation.**

Run: `git diff --check && git status --short`

```bash
git add <documented-sample-files>
git commit -m "docs: configure token acquisition recovery"
```

## Plan self-review

- **Spec coverage:** Tasks 1–4 cover email-only validation, HTTP proxy forwarding, exact-file in-memory Core update, `chatgpt_account_id` overwrite/synchronization, unknown-field preservation, durable once-only automatic behavior, and manual retry. Task 5 covers server inspection and observed request signals; Task 6 covers local inspection signals and a manual dialog action; Task 7 covers secret-file deployment and full verification.
- **No-secret invariant:** no task exposes API key, proxy, raw JSON, or tokens in SQLite, API responses, UI props, logs, test fixtures, or documentation.
- **Type consistency:** `tokenrecovery.Target` is the only browser/server target contract; `tokenacquisition.Result` is internal-only; `Task` is the only status response; `auth_json.Result` is constructed from the internal acquisition result. The worker calls `SignalAutomatic`; explicit browser action calls `RequestManual`.
