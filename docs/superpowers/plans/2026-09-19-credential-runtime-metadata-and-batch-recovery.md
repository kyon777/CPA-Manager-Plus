# Credential Runtime Metadata and Page Batch Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Permanently show each account JSON's proxy URL and TokenAcquisition recovery state in the Accounts page, then queue every eligible current-page Codex `reauth` credential with one batch action.

**Architecture:** Manager Server projects only page-scoped, redacted credential metadata. It downloads CPA JSON in server memory, extracts only the selected `proxy_url` variant, and reads durable recovery task state. Existing Token Recovery gains strict batch query/manual endpoints. React loads metadata per visible page, polls only queued/running tasks, and renders it beside `备注:`.

**Tech Stack:** Go 1.24, net/http, SQLite, React, TypeScript, Axios, Vitest, SCSS.

---

## File map

- `apps/manager-server/internal/service/tokenrecovery/auth_json.go` / `_test.go`: safe selected-record proxy reader.
- `apps/manager-server/internal/service/credentialruntime/{service.go,service_test.go}`: new metadata projection with source-file coalescing.
- `apps/manager-server/internal/http/controller/credentialruntime/{handler.go,handler_test.go}`: authenticated strict metadata endpoint.
- `apps/manager-server/internal/http/controller/tokenrecovery/{handler.go,handler_test.go}`: batch task query/manual routes.
- `apps/manager-server/internal/{app/context.go,http/router/{router.go,router_test.go}}`: runtime wiring and route precedence.
- `apps/web/src/services/api/usageService.ts` plus `usageService.credentialRuntime.test.ts`: typed HTTP client.
- `apps/web/src/features/accounts/{model/credentialRuntimeMetadata.ts,model/credentialRuntimeMetadata.test.ts,hooks/useCredentialRuntimeMetadata.ts,hooks/useCredentialRuntimeMetadata.test.tsx}`: pure selection/status rules and page data lifecycle.
- `apps/web/src/features/accounts/{AccountsPage.tsx,AccountsPage.module.scss,AccountsPage.test.tsx}`: UI and one-click action.
- `apps/web/src/features/oauth/{codexReauthModel.ts,CodexReauthDialog.tsx}`: reuse one token recovery locator builder.
- `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json`: labels.

### Task 1: Add a read-only selected-record proxy reader

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/auth_json.go:47-247`
- Test: `apps/manager-server/internal/service/tokenrecovery/auth_json_test.go`

- [ ] **Step 1: Write the failing parser tests.**

```go
func TestReadProxyURLSelectsArrayRecord(t *testing.T) {
    raw := []byte(`[{"auth_index":"1","email":"a@example.com","proxy_url":"http://a"},{"auth_index":"2","email":"b@example.com","proxy-url":"socks5://b"}]`)
    got, err := ReadProxyURL(raw, Locator{AuthIndex: "2", AccountEmail: "b@example.com"})
    if err != nil || got != "socks5://b" { t.Fatalf("got=%q err=%v", got, err) }
}
func TestReadProxyURLReadsSingleObjectWithoutLocator(t *testing.T) {
    got, err := ReadProxyURL([]byte(`{"email":"a@example.com","proxyUrl":"https://proxy"}`), Locator{})
    if err != nil || got != "https://proxy" { t.Fatalf("got=%q err=%v", got, err) }
}
func TestReadProxyURLRejectsUnlocatedArray(t *testing.T) {
    _, err := ReadProxyURL([]byte(`[{"email":"a@example.com"},{"email":"b@example.com"}]`), Locator{})
    if !errors.Is(err, ErrCredentialAmbiguous) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Verify RED.**

Run: `cd apps/manager-server; go test ./internal/service/tokenrecovery -run TestReadProxyURL -count=1`
Expected: compile failure because `ReadProxyURL` does not exist.

- [ ] **Step 3: Implement the minimum reader.**

Refactor raw JSON decoding into a shared helper. Add:

```go
func ReadProxyURL(raw []byte, locator Locator) (string, error) {
    doc, err := parseAuthDocumentForRead(raw, locator)
    if err != nil { return "", err }
    return strings.TrimSpace(firstNonEmptyField(doc.record, proxyFieldNames...)), nil
}
```

`parseAuthDocumentForRead` may select a root object when both locator fields are empty, but must return `ErrCredentialAmbiguous` for an unlocated root array. Existing `ReadCredential` and `extractHTTPProxy` stay unchanged; HTTP-only validation remains exclusively for TokenAcquisition outbound traffic.

- [ ] **Step 4: Verify GREEN.**

Run: `cd apps/manager-server; go test ./internal/service/tokenrecovery -run 'Test(ReadProxyURL|MergeAuthJSON|ReadCredential)' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/service/tokenrecovery/auth_json.go apps/manager-server/internal/service/tokenrecovery/auth_json_test.go
git commit -m "feat: expose selected credential proxy metadata"
```

### Task 2: Implement the credential-runtime metadata service

**Files:**
- Create: `apps/manager-server/internal/service/credentialruntime/service.go`
- Create: `apps/manager-server/internal/service/credentialruntime/service_test.go`
- Modify: `apps/manager-server/internal/app/context.go:17-207`

- [ ] **Step 1: Write failing service tests.**

The fake downloader returns one array JSON. Assert two targets from `shared.json` produce one download, each receives its selected proxy, a Codex item receives a redacted task, and a broken sibling target yields only an item-local error.

```go
func TestLookupCoalescesSourceAndProjectsSafeFields(t *testing.T) {
    downloads := 0
    svc := NewWithOptions(Options{/* fake setup, fake downloader, fake recovery lookup */})
    items, err := svc.Lookup(context.Background(), []Target{
        {ClientKey:"a", FileName:"shared.json", AuthIndex:"1", AccountEmail:"a@example.com", Provider:"codex"},
        {ClientKey:"b", FileName:"shared.json", AuthIndex:"2", AccountEmail:"b@example.com", Provider:"codex"},
    })
    if err != nil || downloads != 1 { t.Fatalf("err=%v downloads=%d", err, downloads) }
    if items[0].ProxyURL != "http://a" || items[1].RecoveryTask == nil { t.Fatalf("items=%#v", items) }
}
```

- [ ] **Step 2: Verify RED.**

Run: `cd apps/manager-server; go test ./internal/service/credentialruntime -count=1`
Expected: package missing.

- [ ] **Step 3: Implement redacted lookup with bounded concurrency.**

Define:

```go
type Target struct { ClientKey, FileName, AuthIndex, AccountEmail, Provider string }
type Item struct {
    ClientKey string `json:"clientKey"`
    ProxyURL string `json:"proxyUrl,omitempty"`
    RecoveryTask *model.TokenRecoveryTask `json:"recoveryTask,omitempty"`
    ErrorCode string `json:"errorCode,omitempty"`
}
type RecoveryLookup interface { Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) }
```

`Lookup` validates client key/file name, resolves setup once, groups input by physical file name, downloads each group once with a semaphore capacity of four, calls `tokenrecovery.ReadProxyURL` for each selected record, and attaches `RecoveryTask` only for normalized `codex` providers. Fixed item error codes are `credential_not_found`, `credential_ambiguous`, `credential_invalid`, `upstream_unavailable`, and `recovery_unavailable`; never return original JSON, token, authorization, upstream body, or management key.

Wire `CredentialRuntimeService` in `app.Context` using `managerConfigService`, `cpaauthfiles.New(nil)`, and existing `tokenRecoveryService`.

- [ ] **Step 4: Verify GREEN.**

Run: `cd apps/manager-server; go test ./internal/service/credentialruntime ./internal/service/tokenrecovery -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```powershell
git add apps/manager-server/internal/service/credentialruntime apps/manager-server/internal/app/context.go
git commit -m "feat: add credential runtime metadata service"
```

### Task 3: Add authenticated metadata HTTP endpoint and router precedence

**Files:**
- Create: `apps/manager-server/internal/http/controller/credentialruntime/handler.go`
- Create: `apps/manager-server/internal/http/controller/credentialruntime/handler_test.go`
- Modify: `apps/manager-server/internal/http/router/router.go:7-147`
- Modify: `apps/manager-server/internal/http/router/router_test.go`

- [ ] **Step 1: Write failing controller/router tests.**

Test panel authorization (`401`), accepted `POST` response (must not contain `access_token`/raw JSON), unknown target field (`400`), over-100 target request (`400`), and route delivery without the generic CPA management proxy.

- [ ] **Step 2: Verify RED.**

Run: `cd apps/manager-server; go test ./internal/http/controller/credentialruntime ./internal/http/router -run 'Test(Handler|Router).*CredentialRuntime' -count=1`
Expected: package/route missing.

- [ ] **Step 3: Implement strict endpoint.**

Add `POST /v0/management/credential-runtime-metadata`. Authenticate with `middleware.AuthorizePanel` first; decode exactly one JSON object through a 64 KiB `io.LimitReader`; permit only root `targets` and per-target `clientKey,fileName,authIndex,accountEmail,provider`; cap at 100. Return `{"items": items}`. Whole setup unavailability follows existing recovery status mapping; individual problems stay in their response item.

In `router.go`, construct the handler and route before generic management forwarding:

```go
if strings.HasPrefix(r.URL.Path, "/v0/management/credential-runtime-metadata") {
    middleware.WithCORS(appCtx.Config, credentialRuntimeHandler.Handle)(w, r)
    return
}
```

- [ ] **Step 4: Verify GREEN and commit.**

```powershell
cd apps/manager-server
go test ./internal/http/controller/credentialruntime ./internal/http/router -count=1
git add internal/http/controller/credentialruntime internal/http/router
git commit -m "feat: expose credential runtime metadata endpoint"
```

### Task 4: Add batch manual-recovery and batch-status endpoints

**Files:**
- Modify: `apps/manager-server/internal/http/controller/tokenrecovery/handler.go:19-149`
- Modify: `apps/manager-server/internal/http/controller/tokenrecovery/handler_test.go`

- [ ] **Step 1: Write failing batch tests.**

```go
func TestHandlerBatchManualIsolatesInvalidTarget(t *testing.T) { /* valid Codex => manual_queued; bad provider => item errorCode */ }
func TestHandlerBatchQueryReturnsExistingAndNullTask(t *testing.T) { /* signal one target, query existing + absent */ }
func TestHandlerBatchRejectsExtraFieldsAndMoreThan100Targets(t *testing.T) { /* 400 */ }
```

- [ ] **Step 2: Verify RED.**

Run: `cd apps/manager-server; go test ./internal/http/controller/tokenrecovery -run TestHandlerBatch -count=1`
Expected: routes are unhandled.

- [ ] **Step 3: Implement batch protocol.**

Add:

```text
POST /v0/management/token-recovery/query
POST /v0/management/token-recovery/manual/batch
```

Use an exact root `{ "targets": [...] }` decoder, 64 KiB cap, 100 targets, and per-item structure:

```go
type batchItem struct {
    ClientKey string `json:"clientKey"`
    Task *model.TokenRecoveryTask `json:"task"`
    ErrorCode string `json:"errorCode,omitempty"`
}
```

Query invokes `Service.Get`, yielding `task:null` if absent. Manual batch invokes `Service.RequestManual`; queued/running tasks remain deduplicated and terminal failed/succeeded tasks are explicitly requeued as manual work. Reject account IDs, proxy fields, raw JSON, or unknown fields. One invalid item must not cancel the batch.

- [ ] **Step 4: Verify GREEN and commit.**

```powershell
cd apps/manager-server
go test ./internal/http/controller/tokenrecovery ./internal/repository/tokenrecovery ./internal/service/tokenrecovery -count=1
git add internal/http/controller/tokenrecovery/handler.go internal/http/controller/tokenrecovery/handler_test.go
git commit -m "feat: add batched token recovery operations"
```
### Task 5: Add frontend endpoint types and pure recovery presentation rules

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts:389-425,1975-1989,2814-2885`
- Create: `apps/web/src/services/api/usageService.credentialRuntime.test.ts`
- Create: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts`
- Create: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.test.ts`
- Modify: `apps/web/src/features/oauth/codexReauthModel.ts:51-74`
- Modify: `apps/web/src/features/oauth/CodexReauthDialog.tsx:90-114`

- [ ] **Step 1: Write failing API/model tests.**

Assert that the API sends only redacted target fields and authorization:

```ts
expect(axios.post).toHaveBeenCalledWith(
  'http://manager.local:18317/v0/management/token-recovery/manual/batch',
  { targets: [{ clientKey: 'row-a', fileName: 'a.json', authIndex: '1', accountEmail: 'a@example.com', provider: 'codex' }] },
  expect.objectContaining({ headers: { Authorization: 'Bearer manager-key' } })
);
```

Assert status formatting and candidate selection:

```ts
expect(formatRecoveryState({ status: 'auto_failed_manual_only', lastErrorMessage: 'invalid_proxy' }))
  .toEqual({ tone: 'danger', labelKey: 'accounts.recovery_failed_reason', values: { reason: 'invalid_proxy' } });
expect(isPendingRecoveryStatus('manual_running')).toBe(true);
expect(buildPageRecoveryCandidates(rows, healthByKey)).toHaveLength(1);
```

- [ ] **Step 2: Verify RED.**

Run: `npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/credentialRuntimeMetadata.test.ts`
Expected: missing modules/methods or expected request assertion failure.

- [ ] **Step 3: Implement typed redacted contracts.**

In `usageService.ts` add these interfaces plus `TokenRecoveryBatchItem`/response types:

```ts
export interface CredentialRuntimeMetadataTarget {
  clientKey: string;
  fileName: string;
  authIndex?: string;
  accountEmail?: string;
  provider: string;
}
export interface CredentialRuntimeMetadataItem {
  clientKey: string;
  proxyUrl?: string;
  recoveryTask?: TokenRecoveryTask | null;
  errorCode?: string;
}
```

Add `getCredentialRuntimeMetadata`, `queryTokenRecoveryBatch`, and `requestTokenRecoveryManualBatch`, each using `buildUrl`, `authHeaders`, `withUsageServiceError`, 30-second timeout, optional `AbortSignal`, and no browser raw-JSON download path.

Create a pure model that:

1. Creates metadata targets from `AccountRow` (`selectionKey` only as client key; physical name/index/email as locator).
2. Extracts and reuses one `buildCodexTokenRecoveryTarget` from `codexReauthModel.ts`, so the dialog and batch action agree on locator data.
3. Defines queued/running statuses and labels: automatic processing, manual processing, success, and failure with `lastErrorMessage` then error code fallback.
4. Returns candidates only for `codex`, non-`runtimeOnly`, list-health `reauth`, and a valid recovery target.

- [ ] **Step 4: Verify GREEN and commit.**

```powershell
npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/credentialRuntimeMetadata.test.ts src/features/oauth/CodexReauthDialog.test.tsx
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.credentialRuntime.test.ts apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts apps/web/src/features/accounts/model/credentialRuntimeMetadata.test.ts apps/web/src/features/oauth/codexReauthModel.ts apps/web/src/features/oauth/CodexReauthDialog.tsx
git commit -m "feat: add credential runtime metadata client contracts"
```

### Task 6: Build page-scoped metadata lifecycle and pending-only polling

**Files:**
- Create: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts`
- Create: `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`

- [ ] **Step 1: Write failing hook tests.**

Add one test for initial visible-page fetch, one for no polling after terminal states, one that advances fake timers by 2000 ms while a `manual_running` task is present, and one with a deferred old page response that must not overwrite a newer page.

- [ ] **Step 2: Verify RED.**

Run: `npm --workspace apps/web run test -- src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx`
Expected: hook module missing.

- [ ] **Step 3: Implement focused hook.**

Expose:

```ts
type UseCredentialRuntimeMetadataResult = {
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>;
  loading: boolean;
  error: string;
  applyBatchTasks(items: TokenRecoveryBatchItem[]): void;
  refresh(): Promise<void>;
};
```

Build a deterministic signature from Manager connection plus visible targets. On signature change abort prior request, increment a generation, and ignore stale completion. Full metadata fetch runs only for an active Accounts page with configured manager base/key and targets. Preserve last known metadata on transient error. While any task status is pending, query only `/token-recovery/query` at 2 seconds; stop on terminal state, unmount, inactive view, signature change, or aborted request. `applyBatchTasks` must immediately merge the returned task so the row changes to `手动更新中` before polling.

- [ ] **Step 4: Verify GREEN and commit.**

```powershell
npm --workspace apps/web run test -- src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx
git add apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx
git commit -m "feat: load page scoped credential runtime metadata"
```

### Task 7: Render annotations and queue the current-page recovery batch

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:93-120,3509-3581,4373-4407,7907-7965,9235-9341,10220-10231`
- Modify: `apps/web/src/features/accounts/AccountsPage.module.scss:1298-1333`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json`

- [ ] **Step 1: Write failing page tests.**

Use the established `AccountsPage` fixture/mocks to prove:

```ts
it('renders server-projected proxy URL next to a credential note after reload', async () => { /* data-account-list-proxy */ });
it('queues only current-page Codex reauth candidates in one batch request', async () => { /* Codex reauth A only */ });
it('shows a safe recovery failure reason after a pending batch task becomes terminal', async () => { /* invalid_proxy text */ });
```

- [ ] **Step 2: Verify RED.**

Run: `npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --testNamePattern="(server-projected proxy|queues only current-page|safe recovery failure)"`
Expected: assertions fail because UI/action does not exist.

- [ ] **Step 3: Integrate the hook and action without duplicating recovery semantics.**

1. After `pageRows` is derived, create visible metadata targets and call `useCredentialRuntimeMetadata` with `managerRequestScope` only on the Accounts view.
2. Extract current `handleServerTokenRecoverySuccess` logic into a target-parameterized reconciliation helper. It invalidates source-file credential evidence, reloads inspection artifacts, finds the same file/index, and publishes exactly one `reauth` mutation revision per completed task ID/timestamp. Keep the dialog wrapper passing its active target.
3. In `renderBatchBar`, derive the page candidate list by calling existing `resolveAccountRowContext(row).item.health.status`, then `buildPageRecoveryCandidates`. Render:

```tsx
<Button
  variant="secondary"
  size="sm"
  onClick={() => void queuePageTokenRecovery(pageRecoveryCandidates)}
  disabled={disableControls || batchRecoverySubmitting || pageRecoveryCandidates.length === 0}
  loading={batchRecoverySubmitting}
  aria-label={t('accounts.batch_recovery_page', { count: pageRecoveryCandidates.length })}
>
  {!batchRecoverySubmitting ? <IconShield size={15} /> : null}
  {t('accounts.batch_recovery_page', { count: pageRecoveryCandidates.length })}
</Button>
```

`queuePageTokenRecovery` posts `requestTokenRecoveryManualBatch`, calls `applyBatchTasks`, reports submitted/skipped count, and never contacts CPA Core directly from the browser.

4. For table and grid annotations prefer manager projection and use list `row.proxyUrl` only as a fallback:

```ts
const runtime = itemsByClientKey.get(row.selectionKey);
const accountProxyURL = runtime?.proxyUrl?.trim() || row.proxyUrl?.trim() || '';
const recoveryPresentation = formatRecoveryState(runtime?.recoveryTask);
```

Render `备注:` + `代理 URL:` beside each other, and a status tag with `data-account-recovery-status={row.selectionKey}`. Failure tags open the current row's existing Codex reauth dialog; other tags are informational.

- [ ] **Step 4: Add responsive styles and locale keys.**

Change annotations to wrap rather than silently squeeze proxy text away:

```scss
.accountCardAnnotations { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
.accountCardNote, .accountCardProxyURL { flex: 0 1 auto; max-width: 100%; }
.accountRecoveryStatus { flex: 0 0 auto; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
```

Add stable keys in every supported locale:

```text
accounts.batch_recovery_page
accounts.recovery_auto_processing
accounts.recovery_manual_processing
accounts.recovery_succeeded
accounts.recovery_failed_reason
accounts.recovery_failed_generic
accounts.batch_recovery_result
accounts.batch_recovery_unavailable
```

- [ ] **Step 5: Verify GREEN and commit.**

```powershell
npm --workspace apps/web run test -- src/features/accounts/AccountsPage.test.tsx --testNamePattern="(server-projected proxy|queues only current-page|safe recovery failure)"
npm --workspace apps/web run test -- src/features/oauth/CodexReauthDialog.test.tsx
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.module.scss apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/i18n/locales
git commit -m "feat: show credential proxy and batch recovery status"
```

### Task 8: Format, verify, and prepare deployment handoff

**Files:**
- Verify all changed files; no planned behavior changes.

- [ ] **Step 1: Format and run server verification.**

```powershell
cd apps/manager-server
gofmt -w internal/service/tokenrecovery/auth_json.go internal/service/credentialruntime/*.go internal/http/controller/credentialruntime/*.go internal/http/controller/tokenrecovery/handler.go internal/http/router/router.go internal/app/context.go
go test ./internal/service/tokenrecovery ./internal/service/credentialruntime ./internal/http/controller/tokenrecovery ./internal/http/controller/credentialruntime ./internal/http/router -count=1
go vet ./internal/service/tokenrecovery ./internal/service/credentialruntime ./internal/http/controller/tokenrecovery ./internal/http/controller/credentialruntime ./internal/http/router
```

Expected: PASS and no vet diagnostics.

- [ ] **Step 2: Run frontend verification.**

```powershell
npm --workspace apps/web run test -- src/services/api/usageService.credentialRuntime.test.ts src/features/accounts/model/credentialRuntimeMetadata.test.ts src/features/accounts/hooks/useCredentialRuntimeMetadata.test.tsx src/features/accounts/AccountsPage.test.tsx src/features/oauth/CodexReauthDialog.test.tsx
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: tests pass, TypeScript exits 0, and Vite production build succeeds.

- [ ] **Step 3: Inspect and commit any formatter-only changes.**

```powershell
git diff --check
git status --short
git log --oneline -8
```

If formatting made uncommitted changes:

```powershell
git add -u
git commit -m "chore: format credential runtime metadata changes"
```

- [ ] **Step 4: Record deployment evidence.**

Before replacing the running container, back up `usage.sqlite`, `data.key`, and Compose configuration; set `SOURCE_COMMIT` to the verified implementation commit; rebuild/recreate `cpa-manager-plus`; verify `http://127.0.0.1:18317/health`; then validate a single page with a known proxy and a known `reauth` account. Report Windows-only unrelated full-suite limitations separately from the targeted feature results.
