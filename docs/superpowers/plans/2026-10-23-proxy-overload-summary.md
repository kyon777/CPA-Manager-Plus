# Proxy Overload Summary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Add a read-only, on-demand Credential List modal which aggregates the last 72 hours of each credential's latest nine requests by its current proxy_url, flags only exact Codex HTTP 502 + server_is_overloaded, and copies selected proxy URLs.

**Architecture:** CPAM fetches one projected CPA inventory (auth-files?include_proxy_url=1), maps immutable credential identities to bounded SQLite recent-request evidence in chunks of 500, then returns only proxy-level aggregates. The monitoring endpoint adds a 15-second process cache/singleflight; a local AccountsPage modal consumes it without entering the list's periodic loading path.

**Tech Stack:** Go, SQLite, net/http, existing CPA auth-file client, React, TypeScript, Vitest, CPAM i18n.

---

## Non-negotiable boundaries

- Do not change CPA Core, cli-proxy-api, credentials, token recovery, inspection, pricing, Credits, quota collection, proxy replacement, or priority demotion.
- No raw credential-file scan, /download, credential-runtime-metadata, per-account CPA call, or browser-side aggregation.
- One summary calculation may make one FetchWithProxyURLs request and SQLite reads only in batches of at most 500 targets. No account/file/error-body/trace/token leaves the backend.
- Attribute events to the credential's current proxy. proxy_url grouping removes surrounding whitespace and every trailing slash, while the first original URL is retained for display/copy.
- Exact signal: failed=true && fail_status_code=502 && (header_error_code == "server_is_overloaded" || fail_summary has that exact token bounded by non [a-z0-9_] characters).
- Preserve the existing 代理 IP 筛选 modal and saved proxy-pool behavior unchanged.

## File map

| File | Change |
| --- | --- |
| apps/manager-server/internal/service/cpaauthfiles/client.go | Parse projected proxy_url and project_id into File. |
| apps/manager-server/internal/repository/usageevent/proxy_overload.go | New bounded read-only evidence query. |
| apps/manager-server/internal/store/store.go | Expose evidence API/type. |
| apps/manager-server/internal/service/proxyoverload/service.go | New aggregation/cache/singleflight service. |
| apps/manager-server/internal/app/context.go | Construct/expose only the new CPAM service. |
| apps/manager-server/internal/http/controller/monitoring/handler.go | Protected GET /v0/management/monitoring/proxy-overload-summary. |
| apps/web/src/services/api/usageService.ts | DTO and one aggregate GET client. |
| apps/web/src/features/accounts/model/proxyOverloadSummary.ts | Pure sort/filter/selection/copy helpers. |
| apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.* | Local-only aggregate modal. |
| apps/web/src/features/accounts/AccountsPage.tsx | Separate toolbar button/state/request abort wiring. |
| apps/web/src/i18n/locales/{en,zh-CN,zh-TW}.json | Labels and safe state messages. |

---

### Task 1: Projected inventory fields

**Files:**
- Modify: apps/manager-server/internal/service/cpaauthfiles/client.go:95-116,1686-1707
- Modify: apps/manager-server/internal/service/cpaauthfiles/proxy_patch_test.go

- [ ] **Step 1: Write a failing test**

    func TestFetchWithProxyURLsParsesProjectedProxyAndProjectID(t *testing.T) {
        // httptest CPA response contains name/provider/auth_index/project_id/proxy_url.
        // Assert query include_proxy_url=1, preserved raw ProxyURL, and ProjectID.
    }

- [ ] **Step 2: Verify RED**

    Set-Location apps/manager-server
    go test ./internal/service/cpaauthfiles -run TestFetchWithProxyURLsParsesProjectedProxyAndProjectID -count=1

Expected: missing File.ProxyURL and File.ProjectID symbols.

- [ ] **Step 3: Implement the smallest parser change**

    type File struct {
        // existing immutable identity fields
        ProjectID string
        ProxyURL  string
        Raw       map[string]any
    }

    // In FromMap; do not normalize here.
    ProjectID: stringField(file, "project_id", "projectId"),
    ProxyURL:  stringField(file, "proxy_url", "proxyUrl", "proxy-url"),

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/manager-server
    go test ./internal/service/cpaauthfiles -count=1
    git add internal/service/cpaauthfiles/client.go internal/service/cpaauthfiles/proxy_patch_test.go
    git commit -m "feat: expose projected proxy inventory fields"

### Task 2: Bounded latest-request evidence repository

**Files:**
- Create: apps/manager-server/internal/repository/usageevent/proxy_overload.go
- Create: apps/manager-server/internal/repository/usageevent/proxy_overload_test.go
- Modify: apps/manager-server/internal/repository/usageevent/repository.go
- Modify: apps/manager-server/internal/store/store.go:100-115,917-920

- [ ] **Step 1: Write failing repository tests**

Test all of these against seeded SQLite data before writing production code:

    func TestRecentProxyOverloadEvidenceUsesLatestNineEventsInsideWindow(t *testing.T) {
        // An exact overload as the 10th newest event must not count.
        // An exact overload among newest 9 inside 72h must produce Active=true, ServerOverloaded=true.
    }
    func TestRecentProxyOverloadEvidenceRequiresExactServerOverloadedSignal(t *testing.T) {
        // 502 EOF, 500, 503, 429, 407, 401, failed=false and not_server_is_overloaded never count.
    }
    func TestRecentProxyOverloadEvidenceSplitsFiveHundredTargetBatches(t *testing.T) {
        // 1001 targets must use exactly 3 bounded VALUES queries, not 1001 queries.
    }
    func TestRecentProxyOverloadEvidenceFailsClosedWhenLatestRequestIndexesAreMissing(t *testing.T) {
        // Require errors.Is(err, ErrLatestRequestIndexesUnavailable).
    }

Add a Codex workspace/member conflict test matching the existing RecentAccountRequests safe identity rules and a test asserting schema and usage_events count do not change.

- [ ] **Step 2: Verify RED**

    Set-Location apps/manager-server
    go test ./internal/repository/usageevent -run ProxyOverload -count=1

Expected: missing RecentProxyOverloadEvidence, ProxyOverloadEvidence, and index sentinel.

- [ ] **Step 3: Implement the read-only API**

    const proxyOverloadTargetBatchSize = 500

    type ProxyOverloadEvidence struct {
        RequestIndex     int
        Active           bool
        ServerOverloaded bool
    }

    var ErrLatestRequestIndexesUnavailable = errors.New("latest request indexes are unavailable")

    func (r *repository) RecentProxyOverloadEvidence(
        ctx context.Context, targets []LatestAccountRequestQuery, sinceMS int64, perCredentialLimit int,
    ) ([]ProxyOverloadEvidence, error)

Implementation rules:
1. Require perCredentialLimit == 9 from the service and call existing latestRequestIndexesReady; missing indexes return the sentinel, never the existing per-target fallback.
2. Slice targets into 500-record groups. A VALUES CTE carries the same identity columns as recentAccountRequestsBatched; reuse latestRequestBatchedIdentityPredicate so Codex identity behavior stays fail-closed.
3. Union current and legacy sources, apply timestamp_ms >= sinceMS before ROW_NUMBER() OVER (PARTITION BY request_index ORDER BY timestamp_ms DESC, id DESC), then keep ranks <= 9.
4. Select only request index, failure bit, status, header_error_code, and safe fail_summary; no response event is returned to HTTP.
5. Run all chunks inside a short read-only transaction; mark each target active if any sampled row exists and overloaded once if an exact predicate matches.

Implement token matching exactly:

    func isExactServerOverloaded(failed bool, status int, headerCode, summary string) bool {
        return failed && status == http.StatusBadGateway &&
            (strings.EqualFold(strings.TrimSpace(headerCode), "server_is_overloaded") ||
                containsErrorCodeToken(summary, "server_is_overloaded"))
    }

containsErrorCodeToken lowercases input and accepts the match only if both neighbors are not ASCII letters, digits, or underscore.

Expose via store:

    type ProxyOverloadEvidence = usageevent.ProxyOverloadEvidence
    func (s *Store) RecentProxyOverloadEvidence(ctx context.Context, targets []LatestAccountRequestQuery, sinceMS int64, limit int) ([]ProxyOverloadEvidence, error) {
        return s.UsageEvents.RecentProxyOverloadEvidence(ctx, targets, sinceMS, limit)
    }

- [ ] **Step 4: Verify GREEN, inspect query plan, commit**

    Set-Location apps/manager-server
    go test ./internal/repository/usageevent -run 'ProxyOverload|RecentAccountRequests' -count=1
    go test ./internal/repository/usageevent -run 'ProxyOverload.*Plan|ProxyOverload.*Batch' -count=1 -v
    go test ./internal/store -run ProxyOverload -count=1
    git add internal/repository/usageevent internal/store/store.go
    git commit -m "feat: add bounded proxy overload evidence query"

### Task 3: CPAM-only summary service

**Files:**
- Create: apps/manager-server/internal/service/proxyoverload/service.go
- Create: apps/manager-server/internal/service/proxyoverload/service_test.go
- Modify: apps/manager-server/internal/app/context.go

- [ ] **Step 1: Write failing service tests**

    func TestSummaryFetchesCurrentProxyInventoryOnceAndGroupsCurrentURLs(t *testing.T) {
        // Fixture has three active evidence records: two with current proxy A and
        // one with an empty proxy. Assert one inventory call, active=3, missing=1,
        // proxy A overloaded=1 and non_overloaded=1.
    }
    func TestSummaryNormalizesTrailingSlashesOnlyForGrouping(t *testing.T) {
        // Fixtures " http://proxy.example:8080/ " and "http://proxy.example:8080////"
        // form one row whose displayed/copy URL is the first original value.
    }
    func TestSummaryDoesNotClassifyNoEventCredentialAsActive(t *testing.T) {
        // Inventory target without evidence must not increment active, missing, or proxy counters.
    }
    func TestSummaryCachesCompletedResultForFifteenSeconds(t *testing.T) {
        // Two calls at fixed clock t and t+14s return the same result with one inventory/evidence call.
    }
    func TestSummaryForceRefreshBypassesCompletedCacheButJoinsInFlightWork(t *testing.T) {
        // Start a blocked refresh, call normal Summary concurrently, release it, and assert one run.
    }
    func TestSummaryReturnsSafeUnavailableErrors(t *testing.T) {
        // Setup/index/upstream failures expose a static error; their message cannot contain a
        // fixture proxy URL, email, upstream body, raw JSON, trace, or token.
    }

- [ ] **Step 2: Verify RED**

    Set-Location apps/manager-server
    go test ./internal/service/proxyoverload -count=1

Expected: package or symbol missing.

- [ ] **Step 3: Implement strict interfaces/cache/aggregation**

    type SetupResolver interface { ResolveSetup(context.Context) (store.Setup, bool, error) }
    type AuthFileInventoryReader interface { FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error) }
    type EvidenceReader interface { RecentProxyOverloadEvidence(context.Context, []store.LatestAccountRequestQuery, int64, int) ([]store.ProxyOverloadEvidence, error) }

Use a Summary DTO with generated_at_ms, window_start_ms, active_account_count, missing_proxy_account_count, proxy_count, overloaded_account_count, and ProxyRow values proxy_url, active_account_count, overloaded_account_count, non_overloaded_account_count, overloaded_rate.

Use fixed 72-hour, 9-event, 15-second constants and singleflight.Group. Cache only complete summaries. refresh=true bypasses completed cache but joins existing inflight work. Build LatestAccountRequestQuery from immutable inventory evidence (including raw Codex account/project values if present) rather than email. Aggregate only active records, count empty current proxies under missing_proxy_account_count, stable-sort zero-overload first then active descending then normalized proxy key. Never log raw URL, account identity, error body, or token.

Wire exactly once in app/context.go after managerConfigService/authFiles creation:

    proxyOverloadService := proxyoverloadsvc.New(proxyoverloadsvc.Options{
        SetupResolver: managerConfigService,
        AuthFiles: authFiles,
        Evidence: st,
    })

Add ProxyOverloadService *proxyoverloadsvc.Service to Context; leave existing monitoring constructors untouched.

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/manager-server
    go test ./internal/service/proxyoverload -count=1
    go test ./internal/app -count=1
    git add internal/service/proxyoverload internal/app/context.go
    git commit -m "feat: aggregate proxy overload summaries in CPAM"

### Task 4: Protected, privacy-safe monitoring endpoint

**Files:**
- Modify: apps/manager-server/internal/http/controller/monitoring/handler.go
- Modify: apps/manager-server/internal/http/controller/monitoring/handler_test.go

- [ ] **Step 1: Write failing HTTP tests**

    func TestProxyOverloadSummaryRequiresPanelAuthorization(t *testing.T) {
        // GET without panel bearer uses existing authorization failure behavior.
    }
    func TestProxyOverloadSummaryRejectsNonGET(t *testing.T) {
        // POST with valid panel bearer receives MethodNotAllowed.
    }
    func TestProxyOverloadSummaryRejectsUnknownQuery(t *testing.T) {
        // GET ?hours=1 with valid panel bearer receives 400 and does not invoke service.
    }
    func TestProxyOverloadSummaryReturnsNoStoreAndAggregateOnly(t *testing.T) {
        // 200, Cache-Control=no-store; no seeded email/file/raw_json/fail_body/trace/token in body.
    }
    func TestProxyOverloadSummaryForceRefreshPassesRefreshFlag(t *testing.T) {
        // GET ?refresh=1 invokes the service once with force=true.
    }

- [ ] **Step 2: Verify RED**

    Set-Location apps/manager-server
    go test ./internal/http/controller/monitoring -run ProxyOverloadSummary -count=1

Expected: route/method assertion failure.

- [ ] **Step 3: Add the route and handler**

Add a path branch immediately after panel authorization and before analytics fall-through. Handler accepts only GET and either no query or exactly refresh=1. It calls h.App.ProxyOverloadService.Summary(ctx, refresh), sends static safe errors (maintenance => 409, unavailable => 503), and sets Cache-Control: no-store before JSON success. Never return upstream error text.

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/manager-server
    go test ./internal/http/controller/monitoring -count=1
    git add internal/http/controller/monitoring
    git commit -m "feat: expose proxy overload summary endpoint"


### Task 5: Typed frontend transport and pure modal state

**Files:**
- Modify: apps/web/src/services/api/usageService.ts
- Create: apps/web/src/features/accounts/model/proxyOverloadSummary.ts
- Create: apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts

- [ ] **Step 1: Write failing pure-model tests**

Use immutable aggregate fixtures and write these executable tests:

    it('sorts zero-overload rows before overloaded rows, then active count then normalized proxy', () => {
      expect(sortProxyOverloadRows(rows).map((row) => row.proxy_url)).toEqual([
        'http://healthy-b:8080',
        'http://healthy-a:8080',
        'http://loaded:8080',
      ]);
    });

    it('filters all, zero, and overloaded rows without mutating result data', () => {
      expect(filterProxyOverloadRows(rows, 'zero')).toHaveLength(2);
      expect(filterProxyOverloadRows(rows, 'overloaded')).toHaveLength(1);
      expect(rows).toHaveLength(3);
    });

    it('copies only selected URLs with LF separators and no trailing newline', () => {
      expect(proxyURLsForCopy(rows, new Set(['http://a', 'http://b']))).toBe('http://a\nhttp://b');
    });

- [ ] **Step 2: Verify RED**

    Set-Location apps/web
    npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts --pool=forks --maxWorkers=1

Expected: module-not-found or missing-export failure.

- [ ] **Step 3: Add typed API call and pure helpers**

In usageService.ts define exported response DTOs:
- ProxyOverloadSummaryRow: proxy_url, active_account_count, overloaded_account_count, non_overloaded_account_count, overloaded_rate.
- ProxyOverloadSummaryResponse: generated_at_ms, window_start_ms, active_account_count, missing_proxy_account_count, proxy_count, overloaded_account_count, proxies.

Add monitoringAnalyticsApi.getProxyOverloadSummary(base, managementKey, refresh, signal). It makes exactly one axios GET to /v0/management/monitoring/proxy-overload-summary, supplies refresh=1 only for a user-triggered refresh, existing authorization headers, existing usage timeout, and signal. Demo mode returns a small fixed aggregate response and never asks for history/window/runtime data.

Implement and export:
- ProxyOverloadFilter = 'all' | 'zero' | 'overloaded';
- sortProxyOverloadRows(rows), sorted zero overload first, then active count descending, normalized URL ascending;
- filterProxyOverloadRows(rows, filter);
- selectAllVisibleProxyKeys(rows);
- toggleProxyOverloadSelection(selected, key);
- proxyURLsForCopy(rows, selected), LF separated with no terminal LF.

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/web
    npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts --pool=forks --maxWorkers=1
    npm run typecheck
    git add src/services/api/usageService.ts src/features/accounts/model/proxyOverloadSummary.ts src/features/accounts/model/proxyOverloadSummary.test.ts
    git commit -m "feat: add proxy overload summary client model"

### Task 6: Aggregate-only modal and translations

**Files:**
- Create: apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx
- Create: apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.module.scss
- Create: apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
- Modify: apps/web/src/i18n/locales/en.json
- Modify: apps/web/src/i18n/locales/zh-CN.json
- Modify: apps/web/src/i18n/locales/zh-TW.json

- [ ] **Step 1: Write failing modal tests**

Use existing ProxyFilterModal test utilities and static aggregate results. The tests must prove:
1. aggregate count cards and zero-overload-first rows render;
2. all/zero/overloaded filter controls change only local rows and do not execute a fetch callback;
3. select visible plus copy calls onCopy with exact LF-only URL text;
4. a refresh error leaves the last full result visible;
5. initial empty result shows recent-3-day empty wording and missing proxy count.

- [ ] **Step 2: Verify RED**

    Set-Location apps/web
    npx vitest run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx --pool=forks --maxWorkers=1

Expected: module-not-found failure.

- [ ] **Step 3: Implement a presentational local-state modal**

Use props:
- open, loading, refreshing, result, error;
- onClose, onRefresh, onCopy.

Keep filter and selected proxy URLs only inside the modal; clear selections when generated_at_ms changes. Render:
- title 代理过载统计;
- fixed-window description: recent 3 days, at most 9 requests per account, grouped by current proxy;
- generated time, active-account total, missing-proxy total, proxy total, overloaded-account total;
- filter controls all / zero overload / overloaded;
- table checkbox, current proxy URL, active accounts, overloaded accounts, non-overloaded accounts, overload rate;
- select-all only for current filtered rows;
- copy selected URL button using proxyURLsForCopy;
- first-load skeleton, no-data state, safe retry error banner, and stale successful table retained during refresh failure.

The component has no effect which performs transport or polling. Its CSS module is scoped only to this modal. Add matching accounts.proxy_overload_* labels to all three locale files. Do not edit the existing proxy filter component or styles.

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/web
    npx vitest run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/features/accounts/components/ProxyFilterModal.test.tsx --pool=forks --maxWorkers=1
    npm run typecheck
    git add src/features/accounts/components/ProxyOverloadSummaryModal.tsx src/features/accounts/components/ProxyOverloadSummaryModal.module.scss src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/i18n/locales/en.json src/i18n/locales/zh-CN.json src/i18n/locales/zh-TW.json
    git commit -m "feat: add proxy overload statistics modal"

### Task 7: Isolated AccountsPage integration

**Files:**
- Modify: apps/web/src/features/accounts/AccountsPage.tsx:100,1692-1700,2678-2710,9240-9260,11814-11855
- Modify: apps/web/src/features/accounts/AccountsPage.test.tsx

- [ ] **Step 1: Write failing integration tests**

Mock only monitoringAnalyticsApi.getProxyOverloadSummary and assert:
1. with no selection/mode, both old 代理 IP 筛选 and new 代理过载统计 buttons exist;
2. opening statistics makes one aggregate request and does not invoke getAccountHistory, getAccountWindowUsage, credential runtime metadata, or loadFiles;
3. closing the modal or changing connection scope aborts just the statistics request signal.

- [ ] **Step 2: Verify RED**

    Set-Location apps/web
    npx vitest run src/features/accounts/AccountsPage.test.tsx --pool=forks --maxWorkers=1

Expected: new button/action assertion failure.

- [ ] **Step 3: Wire only local state and one aggregate request**

Add states adjacent to existing proxyFilterOpen:
- proxyOverloadSummaryOpen;
- proxyOverloadSummary;
- proxyOverloadSummaryLoading;
- proxyOverloadSummaryRefreshing;
- proxyOverloadSummaryError;
- proxyOverloadSummaryAbortRef.

Implement loadProxyOverloadSummary(force):
1. require managerRequestScope, otherwise set a local unavailable message;
2. abort only a preceding statistics request, create an AbortController, call getProxyOverloadSummary exactly once;
3. replace stored result only on full success; on error keep last success and set safe message; ignore cancellation;
4. distinguish initial loading from manual refreshing;
5. clear controller only if it is still current.

openProxyOverloadSummary opens then calls loadProxyOverloadSummary(false). Render a second idle-toolbar button after existing 代理 IP 筛选, disabled only by disableControls or unavailable manager scope. Render the new modal beside ProxyFilterModal. On close/fingerprint change abort this reference only. Copy feedback reuses copyToClipboard and showNotification. Do not call loadFiles, refreshQuotaRows, history, account-window usage, runtime metadata, inspection, or token actions.

- [ ] **Step 4: Verify GREEN and commit**

    Set-Location apps/web
    npx vitest run src/features/accounts/AccountsPage.test.tsx src/features/accounts/components/ProxyFilterModal.test.tsx src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx --pool=forks --maxWorkers=1
    npm run typecheck
    git add src/features/accounts/AccountsPage.tsx src/features/accounts/AccountsPage.test.tsx
    git commit -m "feat: open proxy overload statistics from credentials"

### Task 8: Full verification and preservation record

**Files:**
- Modify only if needed: CPAM_UPDATE_RUNBOOK.md or the existing permanent customization-memory document.

- [ ] **Step 1: Backend verification**

    Set-Location apps/manager-server
    go test ./internal/service/cpaauthfiles ./internal/repository/usageevent ./internal/service/proxyoverload ./internal/http/controller/monitoring ./internal/app -count=1
    go test ./... -count=1

Expected: exit code 0.

- [ ] **Step 2: Frontend verification**

    Set-Location apps/web
    npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/features/accounts/components/ProxyFilterModal.test.tsx src/features/accounts/AccountsPage.test.tsx --pool=forks --maxWorkers=1
    npm run typecheck
    npm run build

Expected: exit code 0.

- [ ] **Step 3: Mechanical boundary inspection**

    Set-Location <worktree-root>
    rg -n -S 'credential-runtime-metadata|/download|Fetch\(' apps/manager-server/internal/service/proxyoverload apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/AccountsPage.tsx
    rg -n -S 'proxy-overload-summary' apps/manager-server apps/web/src

Expected: new backend uses FetchWithProxyURLs only; no raw-file or per-account endpoint is introduced.

- [ ] **Step 4: Record durable customization invariant and inspect final state**

Append only this preservation rule to the established custom-change/runbook file if such a document exists: independent proxy overload statistics; one projected auth-files snapshot; 500-target bounded read-only SQLite aggregation; no N+1 and no file scan. Then run:

    Set-Location <worktree-root>
    git diff --check
    git status --short
    git log --oneline --decorate -8

Commit the preservation note separately if it changed.

- [ ] **Step 5: Stop before deployment/push**

Report commits and verification evidence. Do not build/restart CPAM, touch CPA Core, push upstream, or push origin/lo/custom unless LO separately asks for deploy/push.


