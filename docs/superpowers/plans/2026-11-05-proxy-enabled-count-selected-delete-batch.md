# Proxy Enabled Counts and Selected Credential Batch Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Expose the enabled subset of the existing 72-hour proxy-overload accounts, and replace Manager-enabled selected permanent deletion with one-snapshot, lock-protected, eight-worker batch deletion.

**Architecture:** Proxy-overload already holds the current CPA inventory during 72-hour evidence aggregation, so enabled counters add only O(1) work to each accepted active item. A dedicated Manager endpoint receives stable identities only, holds shared physical-file mutation locks, verifies all current source members using one inventory snapshot, and deletes independent physical sources using at most eight workers. The browser retains both confirmations and chooses the Manager endpoint only when a Manager scope exists; standalone CPA retains the current safe serial fallback.

**Tech Stack:** Go Manager Server, CPA auth-file client and mutation coordinator, React/TypeScript, Axios, Vitest, Docker local Manager image, native systemd CPAM server.

---

## Files and Responsibilities

| File | Responsibility |
| --- | --- |
| apps/manager-server/internal/service/proxyoverload/service.go | Enabled-active response counters without altering active/overload semantics. |
| apps/manager-server/internal/service/proxyoverload/service_test.go | Mixed enabled/disabled regression coverage. |
| apps/manager-server/internal/service/authfilesselecteddelete/service.go | New verified selected-delete service. |
| apps/manager-server/internal/service/authfilesselecteddelete/service_test.go | One snapshot, shared source, eight-worker, fallback, invalidation tests. |
| apps/manager-server/internal/http/controller/authfilesselecteddelete/handler.go | Strict Manager POST decoder and panel authorization. |
| apps/manager-server/internal/http/controller/authfilesselecteddelete/handler_test.go | Authorization, method, size, cap, and strict-schema tests. |
| apps/manager-server/internal/app/context.go | New service construction with existing dependencies. |
| apps/manager-server/internal/http/router/router.go and router_test.go | Manager route registration only. |
| apps/web/src/services/api/usageService.ts | New API types, request builder, demo transport, and proxy response types. |
| apps/web/src/features/authFiles/hooks/useAuthFilesData.ts | Manager-scope routing and confirmed-row removal. |
| apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts | Manager, partial, and fallback behavior. |
| apps/web/src/pages/AccountsPage.tsx | Pass managerRequestScope into the auth-file hook. |
| apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx | Display summary/row enabled subsets. |
| apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx | Display and old-response defaulting checks. |
| apps/web/src/i18n/locales/*.json | Localized summary and row labels. |
| docs/LO-CPAM-CUSTOM-MEMORY.md | Durable behavioral invariant record. |

## Fixed Contracts

~~~
type Summary struct {
    ActiveAccountCount        int json:"active_account_count"
    EnabledActiveAccountCount int json:"enabled_active_account_count"
}
type ProxyRow struct {
    ActiveAccountCount        int json:"active_account_count"
    EnabledActiveAccountCount int json:"enabled_active_account_count"
}
const (
    MaxTargets     = 100
    MaxConcurrency = 8
)
type Target struct {
    FileName        string json:"fileName"
    AuthIndex       string json:"authIndex,omitempty"
    Provider        string json:"provider"
    RuntimeID       string json:"runtimeId,omitempty"
    AccountID       string json:"accountId,omitempty"
    AccountSnapshot string json:"accountSnapshot,omitempty"
}
~~~

~~~
export interface SelectedAuthFileDeleteTargetRequest {
  fileName: string;
  provider: string;
  authIndex?: string;
  runtimeId?: string;
  accountId?: string;
  accountSnapshot?: string;
}
~~~

Only an object containing targets is accepted. Raw auth JSON, access tokens, proxy credentials, and management keys are never sent by the browser.

### Task 1: Test and implement the enabled-active aggregation

**Files:**
- Modify: apps/manager-server/internal/service/proxyoverload/service.go
- Modify: apps/manager-server/internal/service/proxyoverload/service_test.go

- [ ] **Step 1: Verify the unmodified focused package**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -count=1
~~~
Expected: PASS.

- [ ] **Step 2: Add a failing mixed-enabled-state test**

Create three active-evidence files: enabled proxy A, disabled proxy A, enabled proxy B. Mark only disabled proxy A as overloaded. Assert active remains 3, enabled-active is 2, overload remains 1, proxy A is active 2/enabled 1/overloaded 1, and proxy B is active 1/enabled 1/overloaded 0.

~~~
func TestSummaryCountsEnabledSubsetWithoutChangingExistingTotals(t *testing.T) {
    got, err := service.Summary(context.Background(), true)
    require.NoError(t, err)
    assert.Equal(t, 3, got.ActiveAccountCount)
    assert.Equal(t, 2, got.EnabledActiveAccountCount)
    assert.Equal(t, 1, got.OverloadedAccountCount)
    assertProxyRow(t, got.Proxies, "http://proxy-a:8888", 2, 1, 1)
}
~~~

Keep the existing no-72-hour-evidence case unchanged.

- [ ] **Step 3: Run the single new test**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -run TestSummaryCountsEnabledSubsetWithoutChangingExistingTotals -count=1
~~~
Expected: FAIL because enabled fields are absent.

- [ ] **Step 4: Add only the aggregation fields**

Add response fields plus private group enabledActiveCount. The loop must count the summary subset before missing-proxy handling and the row subset after grouping:

~~~
result.ActiveAccountCount++
if !filesByTarget[index].Disabled {
    result.EnabledActiveAccountCount++
}
key, displayURL := normalizeProxyURL(filesByTarget[index].ProxyURL)
if key == "" {
    result.MissingProxyAccountCount++
    continue
}
group.activeCount++
if !filesByTarget[index].Disabled {
    group.enabledActiveCount++
}
~~~

Populate ProxyRow.EnabledActiveAccountCount. Do not change overload rate, ordering, saved-list logic, or the active definition.

- [ ] **Step 5: Verify and commit**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/service/proxyoverload -count=1
Set-Location ..
git add apps/manager-server/internal/service/proxyoverload
git commit -m "feat: expose enabled proxy overload accounts"
~~~
Expected: PASS then one focused commit.

### Task 2: Test and implement the verified selected-delete service

**Files:**
- Create: apps/manager-server/internal/service/authfilesselecteddelete/service.go
- Create: apps/manager-server/internal/service/authfilesselecteddelete/service_test.go
- Reference: apps/manager-server/internal/service/authfilesbanneddelete/service.go
- Reference: apps/manager-server/internal/service/cpaauthfiles/client.go

- [ ] **Step 1: Write failing service tests**

Use fake setup, CPA auth-files, and invalidator dependencies. Cover:
1. a two-source request calls Fetch exactly once, deletes both physical sources, and invalidates once;
2. a shared source with an unselected current member is skipped with no Delete;
3. blocking fake deletes never exceed eight concurrent calls;
4. a plugin virtual mutation conflict retries only that verified physical source;
5. stale identity resolution skips, rather than deletes;
6. more than 100 targets returns ErrTooManyTargets before CPA access.

Target API:

~~~
result, err := service.Delete(context.Background(), []Target{
    {FileName: "a.json", Provider: "codex", AuthIndex: "a", RuntimeID: "a"},
    {FileName: "b.json", Provider: "codex", AuthIndex: "b", RuntimeID: "b"},
})
require.NoError(t, err)
assert.Equal(t, 1, fakeAuthFiles.fetchCalls)
assert.ElementsMatch(t, []string{"a.json", "b.json"}, fakeAuthFiles.deletedSelectors())
assert.Len(t, result.Deleted, 2)
assert.Equal(t, 1, fakeInvalidator.calls)
~~~

Use a barrier and release channel in the peak-worker test, not sleeps.

- [ ] **Step 2: Run the absent-package test**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/service/authfilesselecteddelete -count=1
~~~
Expected: FAIL due to the missing package.

- [ ] **Step 3: Implement the minimal service**

Implement Options, Service, Target, Item, Result, and these interfaces:

~~~
type AuthFilesClient interface {
    Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
    Delete(context.Context, string, string, string) error
}
type InventoryInvalidator interface {
    InvalidateAuthFileInventory(context.Context)
}
func (s *Service) Delete(ctx context.Context, targets []Target) (Result, error)
~~~

Execution sequence is fixed:

~~~
release, err := s.mutationCoordinator.Acquire(ctx, lockedPhysicalNames...)
if err != nil { /* write per-target failed outcomes and return */ }
defer release()

files, err := s.authFiles.Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
if err != nil { /* write inventory_unavailable outcomes and return */ }

resolved, err := cpaauthfiles.ResolveVerifiedPhysicalFileDeleteTargetFromInventory(files, identities)
// unresolved groups become skipped; they never call Delete.
~~~

Rules:
- trim strings and lowercase provider; require fileName, provider, and authIndex or runtimeId;
- group by normalized physical file name and preserve an outcome for every submitted target;
- lock all candidate sources before the single fetch;
- resolve a shared source using every submitted member identity;
- run sorted verified physical jobs with min(concurrency, jobs) workers, capped at 8;
- on IsPluginVirtualMutationConflict retry only the verified physical source, without refetch;
- invalidate once after any confirmed physical delete;
- no token recovery dependency and no account_banned gate.

- [ ] **Step 4: Verify and commit**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/service/authfilesselecteddelete -count=1
Set-Location ..
git add apps/manager-server/internal/service/authfilesselecteddelete
git commit -m "feat: add verified selected auth-file batch delete"
~~~
Expected: PASS then one service commit.

### Task 3: Add strict Manager HTTP wiring

**Files:**
- Create: apps/manager-server/internal/http/controller/authfilesselecteddelete/handler.go
- Create: apps/manager-server/internal/http/controller/authfilesselecteddelete/handler_test.go
- Modify: apps/manager-server/internal/app/context.go
- Modify: apps/manager-server/internal/http/router/router.go
- Modify: apps/manager-server/internal/http/router/router_test.go

- [ ] **Step 1: Write failing controller tests**

Test panel-authenticated POST success, unauthenticated rejection, GET method-not-allowed, unknown root/target keys rejection, 64 KiB body cap, and 100 target cap. Use this exact wire shape:

~~~
{
  "targets": [{
    "fileName": "alpha.json",
    "provider": "codex",
    "authIndex": "alpha",
    "runtimeId": "alpha",
    "accountSnapshot": "alpha@example.com",
    "accountId": "account-1"
  }]
}
~~~

- [ ] **Step 2: Run the absent controller package**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/http/controller/authfilesselecteddelete -count=1
~~~
Expected: FAIL because Handler is absent.

- [ ] **Step 3: Implement strict decoding and context/router registration**

Use banned-delete handler conventions with:
- POST path /v0/management/auth-files/delete-selected-batch;
- root target fields only fileName, provider, authIndex, runtimeId, accountId, accountSnapshot;
- io.LimitReader at 64 KiB;
- non-exposing selected-auth-file-delete messages.

Construct in context with:

~~~
authfilesselecteddeletesvc.NewWithOptions(authfilesselecteddeletesvc.Options{
    SetupResolver:       managerConfigService,
    AuthFiles:           authFiles,
    MutationCoordinator: authFileMutationCoordinator,
    Invalidator:         proxyService,
})
~~~

Thread the handler through newRouter and attach it to the Manager mux next to banned delete. It must not route to CPA Core.

- [ ] **Step 4: Verify and commit**

Run:
~~~
Set-Location apps/manager-server
go test ./internal/http/controller/authfilesselecteddelete ./internal/http/router -count=1
Set-Location ..
git add apps/manager-server/internal/http/controller/authfilesselecteddelete apps/manager-server/internal/http/router apps/manager-server/internal/app/context.go
git commit -m "feat: expose selected auth-file batch delete"
~~~
Expected: PASS then one transport commit.

### Task 4: Test and render proxy enabled counts

**Files:**
- Modify: apps/web/src/services/api/usageService.ts
- Modify: apps/web/src/services/api/usageService.proxyOverloadSummary.test.ts
- Modify: apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx
- Modify: apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
- Modify: every locale listed by rg -l proxy_overload_active apps/web/src/i18n/locales

- [ ] **Step 1: Write failing API/modal tests**

Add this fixture to the API/modal tests:

~~~
enabled_active_account_count: 2,
proxies: [{
  proxy_url: "http://proxy-a:8888",
  active_account_count: 3,
  enabled_active_account_count: 2,
  overloaded_account_count: 1,
  non_overloaded_account_count: 2,
  overloaded_rate: 1 / 3,
}],
~~~

Assert Chinese rendering:
~~~
活跃账号 3 个（已启用 2 个）
3（已启用 2）
~~~
Also prove an old response without the new fields renders 0 rather than undefined or NaN.

- [ ] **Step 2: Run the focused web test**

Run:
~~~
Set-Location apps/web
npm test -- --run src/services/api/usageService.proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
~~~
Expected: FAIL because types/rendering are missing.

- [ ] **Step 3: Implement compatible types, fixtures, render, and locale strings**

Add optional response fields:
~~~
enabled_active_account_count?: number;
~~~

Use rendering-only defaults:
~~~
const enabledActiveCount = result.enabled_active_account_count ?? 0;
const enabledRowCount = row.enabled_active_account_count ?? 0;
~~~

Add locale keys:
~~~
"proxy_overload_active": "活跃账号 {{count}} 个（已启用 {{enabled}} 个）",
"proxy_overload_active_cell": "{{count}}（已启用 {{enabled}}）"
~~~

Translate in every existing locale. Preserve success rate, overload rate, sorting, and proxy selection behavior.

- [ ] **Step 4: Verify and commit**

Run:
~~~
Set-Location apps/web
npm test -- --run src/services/api/usageService.proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
Set-Location ../..
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.proxyOverloadSummary.test.ts apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx apps/web/src/i18n/locales
git commit -m "feat: show enabled counts in proxy overload summary"
~~~
Expected: PASS then one web-display commit.

### Task 5: Test and route selected browser deletion through Manager

**Files:**
- Modify: apps/web/src/services/api/usageService.ts
- Create: apps/web/src/services/api/usageService.selectedDelete.test.ts
- Modify: apps/web/src/features/authFiles/hooks/useAuthFilesData.ts
- Modify: apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts
- Modify: apps/web/src/pages/AccountsPage.tsx

- [ ] **Step 1: Write failing transport and hook tests**

Transport test must assert exactly one POST to /v0/management/auth-files/delete-selected-batch with:

~~~
{"targets":[{"fileName":"alpha.json","provider":"codex","authIndex":"alpha","runtimeId":"alpha","accountSnapshot":"alpha@example.com","accountId":"account-1"}]}
~~~

Hook tests must prove:
1. Manager scope plus two independent sources makes one batch request and no per-source list/delete after confirmation;
2. one deleted/one skipped response removes only the confirmed physical source and leaves skipped row visible;
3. no Manager scope keeps the current deleteVerifiedAuthFileSnapshots fallback.

- [ ] **Step 2: Run the focused tests red**

Run:
~~~
Set-Location apps/web
npm test -- --run src/services/api/usageService.selectedDelete.test.ts src/features/authFiles/hooks/useAuthFilesData.test.ts
~~~
Expected: FAIL because client and Manager scope are absent.

- [ ] **Step 3: Implement transport and hook selection**

Add:
~~~
deleteSelectedAuthFilesBatch(
  base: string,
  managementKey: string | undefined,
  targets: SelectedAuthFileDeleteTargetRequest[],
  signal?: AbortSignal
): Promise<SelectedAuthFileDeleteResponse>
~~~

Mirror banned-delete request conventions for base URL, headers, demo response, typed error wrapping, and cancellation signal.

Add optional managerRequestScope to useAuthFilesData. Flatten targets from the already-created AuthFileDeleteSnapshot.members using getAuthFilePatchTarget. Use Manager only if scope exists and target count is <= 100. After a Manager request starts, never fall back to direct CPA delete. Merge skipped and failed endpoint results into existing failures, and apply only returned physicalName or fileName from deleted outcomes. Preserve confirmation UI and the no-Manager/over-limit serial fallback. Pass managerRequestScope from AccountsPage. Do not change single-item delete.

- [ ] **Step 4: Verify and commit**

Run:
~~~
Set-Location apps/web
npm test -- --run src/services/api/usageService.selectedDelete.test.ts src/features/authFiles/hooks/useAuthFilesData.test.ts
Set-Location ../..
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.selectedDelete.test.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts apps/web/src/pages/AccountsPage.tsx
git commit -m "feat: batch selected credential deletion through manager"
~~~
Expected: PASS then one browser-routing commit.

### Task 6: Memory, full verification, and deployment

**Files:**
- Modify: docs/LO-CPAM-CUSTOM-MEMORY.md
- Reference: C:\kyon\docker\cpa-manager\CPAM_UPDATE_RUNBOOK.md
- Reference: C:\kyon\docker\cpa-manager\deploy-cpa-manager-key-policy.ps1

- [ ] **Step 1: Record the behavioral invariants**

Append:

~~~
## 2026-11-05 — proxy enabled subset and selected permanent delete

- Proxy overload remains all-current-inventory, last-72-hours evidence. enabled_active_account_count is display-only; disabled accounts remain in active, overload, non-overload, success-rate, missing-proxy, saved-proxy, and sorting calculations.
- Generic selected permanent deletion retains two confirmations. With Manager it verifies one fresh CPA inventory snapshot under shared physical-file mutation locks, deletes only fully-selected verified physical sources at eight-way maximum concurrency, and removes browser rows only after confirmed deletion. CPA Core and cli-proxy-api are untouched.
~~~

- [ ] **Step 2: Format and run complete focused verification**

Run:
~~~
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\upstream-v1.13.2-merge
gofmt -w apps/manager-server/internal/service/proxyoverload/service.go apps/manager-server/internal/service/proxyoverload/service_test.go apps/manager-server/internal/service/authfilesselecteddelete/*.go apps/manager-server/internal/http/controller/authfilesselecteddelete/*.go apps/manager-server/internal/app/context.go apps/manager-server/internal/http/router/router.go apps/manager-server/internal/http/router/router_test.go
Set-Location apps/manager-server
go test ./internal/service/proxyoverload ./internal/service/authfilesselecteddelete ./internal/http/controller/authfilesselecteddelete ./internal/http/router -count=1
Set-Location ../web
npm test -- --run src/services/api/usageService.proxyOverloadSummary.test.ts src/services/api/usageService.selectedDelete.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/features/authFiles/hooks/useAuthFilesData.test.ts
npm run typecheck
npm run build
~~~
Expected: all commands exit zero.

- [ ] **Step 3: Diff check and commit memory**

Run:
~~~
Set-Location C:\Users\Administrator\.config\superpowers\worktrees\cpa-manager\upstream-v1.13.2-merge
git diff --check
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: record proxy and selected delete invariants"
git status --short
~~~
Expected: no whitespace errors and a clean source worktree. No CPA Core or cli-proxy-api change.

- [ ] **Step 4: Capture CPAM/CPA identities, build, and promote locally**

Record:
~~~
docker inspect -f '{{.Id}}|{{.Image}}|{{.State.StartedAt}}' cli-proxy-api
docker inspect -f '{{.Id}}|{{.Image}}|{{.State.StartedAt}}' cpa-manager-plus
~~~

Build only the Manager:
~~~
$commit = git rev-parse HEAD
$tag = "cpa-manager-plus:lo-custom-proxy-enabled-delete-batch-20261105-$($commit.Substring(0,8))"
docker build --platform linux/amd64 --build-arg VERSION="1.14.12-lo-custom-proxy-enabled-delete-batch-20261105" --build-arg SOURCE_COMMIT=$commit -t $tag -f Dockerfile.manager-server .
& C:\kyon\docker\cpa-manager\deploy-cpa-manager-key-policy.ps1 -Promote -CandidateImage $tag
~~~

Expected: cpa-manager-plus alone changes. Re-inspect cli-proxy-api and require identical ID/image/start time.

- [ ] **Step 5: Deploy same candidate to LACPA-VPS**

Before:
~~~
systemctl show cpa.service -p MainPID -p ActiveState -p SubState --no-pager
systemctl show cpam.service -p MainPID -p ActiveState -p SubState --no-pager
sha256sum /home/cpam/app/cpa-manager-plus
~~~

Use the CPAM_UPDATE_RUNBOOK staged binary transfer to a timestamped /home/cpam/_releases directory, preserve /home/cpam/config/cpam.env, /home/cpam/secrets, /home/cpam/data, OpenResty, and certificates, atomically activate only /home/cpam/app/cpa-manager-plus, and restart only cpam.service. Do not modify/restart/build cpa.service, /home/cpa, or cli-proxy-api.

- [ ] **Step 6: Verify deployed behavior**

Verify cpam.service active, cpa.service PID/state unchanged, health succeeds, proxy summary has enabled count fields, and GET on the new delete endpoint returns 405. In the panel, use disposable credentials to test double-confirmed delete and verify incomplete shared-source deletion is retained.

## Spec Coverage Review

- Last-72-hour active definition and all existing overload semantics: Task 1.
- Summary and proxy-row enabled counts without new CPA/SQLite calls: Tasks 1 and 4.
- One snapshot, full shared-member verification, shared locks, eight workers, physical fallback, one invalidation: Task 2.
- Manager-only strict payload and panel authorization: Task 3.
- Existing confirmations, confirmed-only client removal, partial retention, standalone fallback: Task 5.
- Custom memory and only-CPAM local/server deployment: Task 6.

## Pre-Implementation Quality Gate

Run:
~~~
Select-String -Path docs/superpowers/plans/2026-11-05-proxy-enabled-count-selected-delete-batch.md -Pattern ('TB'+'D|TO'+'DO|implement'+' later|appropriate'+' error handling|Write'+' tests for the above|Similar'+' to Task')
git diff --check
~~~

Expected: no placeholder match and no whitespace error.

