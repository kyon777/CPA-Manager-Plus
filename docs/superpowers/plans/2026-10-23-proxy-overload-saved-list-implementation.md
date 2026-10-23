# 已保存代理差集与成功率排序 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the CPAM proxy-overload modal with server-side saved-proxy difference data, a selectable/copyable “未统计” view, and deterministic success-rate sorting without changing CPA Core or billing traffic.

**Architecture:** The existing `proxyoverload.Service` remains the only aggregate source. It will read one encrypted Manager Server proxy-list settings row during its existing shared calculation, decorate the existing summary with a normalized saved-list difference, and invalidate its cache when the list is saved. The web modal consumes the same one aggregate HTTP response and performs only local filtering, selection, copying, and sorting.

**Tech Stack:** Go 1.24 Manager Server, SQLite settings/usage repositories, React 19, TypeScript, Vitest, Sass, existing CPAM Docker/native deployment flow.

---

## Scope and invariants

- Only modify CPAM. Never alter, build, restart, deploy, or query-write CPA Core / `cli-proxy-api`.
- The existing strict overload predicate, 72-hour window, nine-request-per-account limit, current proxy grouping, 15-second shared cache, and 90-second worker deadline remain intact.
- “成功率” means `non_overloaded_account_count / active_account_count`, not total HTTP success rate.
- “未统计” means a saved proxy URL has no proxy group in the current 72-hour aggregate. It does not imply a bad proxy.
- No authorization JSON scan, no per-account CPA request, no proxy exit-IP test, no additional billing request, and no N+1 data path.
- Preserve existing selected-auth-JSON export completely.

## File map

| File | Responsibility |
| --- | --- |
| `apps/manager-server/internal/service/proxyoverload/service.go` | Add saved-list reader, difference fields, cache generation invalidation, cloning. |
| `apps/manager-server/internal/service/proxyoverload/service_test.go` | Prove normalization, safe settings failure, cache invalidation, and stale-flight safety. |
| `apps/manager-server/internal/app/context.go` | Inject the existing Store as the saved-proxy settings reader. |
| `apps/manager-server/internal/http/controller/proxyfilter/handler.go` | Invalidate the aggregate cache after a successful saved-list write. |
| `apps/manager-server/internal/http/controller/proxyfilter/handler_test.go` | Preserve authorization/normalization and prove save invalidation causes a fresh aggregate. |
| `apps/manager-server/internal/http/controller/monitoring/handler_test.go` | Assert the protected aggregate response exposes only aggregate saved-list fields. |
| `apps/web/src/services/api/usageService.ts` | Extend response type and demo response only; keep one GET contract unchanged. |
| `apps/web/src/features/accounts/model/proxyOverloadSummary.ts` | Add unobserved filter, generic selectable rows, and success-rate sort semantics. |
| `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts` | Test default order, both success-rate directions, unobserved selection/copy. |
| `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx` | Render saved-list summary, fourth filter, sortable success column, selectable unobserved rows. |
| `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.module.scss` | Style the sortable table header and unobserved-state row without impacting account list styles. |
| `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx` | Verify UI filters, sorting, selection/copy, and no parent refresh during local interactions. |
| `apps/web/src/features/accounts/AccountsPage.test.tsx` | Keep one aggregate request and existing auth-export coexistence coverage valid. |
| `apps/web/src/i18n/locales/{en,ru,zh-CN,zh-TW}.json` | Add every user-visible label in all supported locales. |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | Preserve this completed customization for future upstream merges. |

### Task 1: Add backend saved-proxy difference and cache invalidation semantics

**Files:**
- Modify: `apps/manager-server/internal/service/proxyoverload/service.go`
- Modify: `apps/manager-server/internal/service/proxyoverload/service_test.go`

- [ ] **Step 1: Write failing service tests for settings-backed difference data.**

Add a thread-safe fake reader matching the new narrow interface:

```go
type fakeProxySettingsReader struct {
    mu       sync.Mutex
    settings store.ProxyFilterSettings
    present  bool
    err      error
    calls    int
}

func (r *fakeProxySettingsReader) LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.calls++
    return r.settings, r.present, r.err
}
```

Add `TestSummaryIncludesSavedProxyDifferenceWithoutExtraCPAReads` with a saved list:

```go
[]string{
    " http://proxy.example:8080/// ",
    "http://saved-without-request.example:8080/",
    "http://proxy.example:8080",
}
```

and one active credential using `http://proxy.example:8080/`. Assert:

```go
result.SavedProxyListAvailable == true
result.SavedProxyCount == 2
result.ObservedSavedProxyCount == 1
result.UnobservedProxyCount == 1
reflect.DeepEqual(result.UnobservedProxies, []string{"http://saved-without-request.example:8080"})
inventory.callCount() == 1
evidence.callCount() == 1
settings.callCount() == 1
```

Add `TestSummaryKeepsAggregateWhenSavedProxySettingsCannotBeRead`: make `LoadProxyFilterSettings` return a private-looking error, assert the normal aggregate row still exists while `SavedProxyListAvailable == false` and all saved-list fields are empty/zero.

Add `TestInvalidateDropsCompletedCacheAndPreventsStaleFlightRecache`: block an in-flight evidence read, call `Invalidate`, release it, then call `Summary(..., false)` and assert the inventory/evidence readers are invoked again. This proves a pre-save calculation cannot repopulate the new saved-list cache after a successful PUT.

- [ ] **Step 2: Run the new tests and verify they fail before implementation.**

Run:

```powershell
Set-Location apps/manager-server
go test -count=1 ./internal/service/proxyoverload -run 'TestSummary(IncludesSavedProxyDifferenceWithoutExtraCPAReads|KeepsAggregateWhenSavedProxySettingsCannotBeRead|InvalidateDropsCompletedCacheAndPreventsStaleFlightRecache)'
```

Expected: compilation failure because the new `ProxySettings` option, response fields, and `Invalidate` method do not exist yet.

- [ ] **Step 3: Implement the narrow settings reader and response fields.**

In `service.go`, add the dependency without coupling the service to an HTTP handler:

```go
type ProxySettingsReader interface {
    LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error)
}

type Options struct {
    SetupResolver SetupResolver
    AuthFiles     AuthFileInventoryReader
    Evidence      EvidenceReader
    ProxySettings ProxySettingsReader
    Now           func() time.Time
    WorkTimeout   time.Duration
}
```

Extend `Summary` exactly as follows:

```go
SavedProxyListAvailable  bool     `json:"saved_proxy_list_available"`
SavedProxyCount          int      `json:"saved_proxy_count"`
ObservedSavedProxyCount  int      `json:"observed_saved_proxy_count"`
UnobservedProxyCount     int      `json:"unobserved_proxy_count"`
UnobservedProxies        []string `json:"unobserved_proxies"`
```

Keep `ProxySettings` optional. A nil reader, a missing setting, or a settings-read error must never replace a valid usage aggregate with `ErrUnavailable`:

```go
func (s *Service) readSavedProxyURLs(ctx context.Context) ([]string, bool) {
    if s.proxySettings == nil {
        return nil, false
    }
    settings, present, err := s.proxySettings.LoadProxyFilterSettings(ctx)
    if err != nil {
        return nil, false
    }
    if !present {
        return []string{}, true
    }
    return model.NormalizeProxyFilterURLs(settings.URLs), true
}
```

Call it once per aggregate calculation, including the zero-target path. Build an observed-key set from `result.Proxies` using the existing `normalizeProxyURL`; walk normalized saved URLs in saved-list order; append only missing keys to `UnobservedProxies`. Set all four count fields from that difference. Initialize slices as empty instead of nil for stable JSON and cloned results.

- [ ] **Step 4: Implement generation-safe cache invalidation.**

Extend the service cache state so an invalidation during a running flight cannot allow the old flight to write a stale saved-list difference back into cache:

```go
type summaryFlight struct {
    done       chan struct{}
    result     Summary
    err        error
    generation uint64
}

type Service struct {
    // existing dependencies
    generation uint64
    mu         sync.Mutex
    cache      *cachedSummary
    inflight   *summaryFlight
}

func (s *Service) Invalidate() {
    if s == nil {
        return
    }
    s.mu.Lock()
    s.generation++
    s.cache = nil
    s.mu.Unlock()
}
```

When starting a flight, capture the current generation. Only join a flight with the same generation. In `runSummaryFlight`, only write `s.cache` when `flight.generation == s.generation`; only clear `s.inflight` when it still equals this exact flight. This permits a post-save request to start a fresh calculation while an old one finishes harmlessly.

Extend `cloneSummary` with:

```go
value.Proxies = append([]ProxyRow(nil), value.Proxies...)
value.UnobservedProxies = append([]string(nil), value.UnobservedProxies...)
if value.Proxies == nil { value.Proxies = []ProxyRow{} }
if value.UnobservedProxies == nil { value.UnobservedProxies = []string{} }
```

- [ ] **Step 5: Run the service test suite and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test -count=1 ./internal/service/proxyoverload
```

Expected: PASS, including the existing strict-overload, caching, force-refresh, cancellation, and safe-error cases.

Commit:

```powershell
git add apps/manager-server/internal/service/proxyoverload/service.go apps/manager-server/internal/service/proxyoverload/service_test.go
git commit -m "feat: add saved proxy difference to overload summary"
```

### Task 2: Wire Store settings and invalidate only after a successful saved-list write

**Files:**
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/http/controller/proxyfilter/handler.go`
- Modify: `apps/manager-server/internal/http/controller/proxyfilter/handler_test.go`
- Modify: `apps/manager-server/internal/http/controller/monitoring/handler_test.go`

- [ ] **Step 1: Write failing controller/integration tests.**

In `handler_test.go` for proxy filters, warm a real `proxyoverload.Service` cache with a fake inventory/evidence pair, attach it as `Context.ProxyOverloadService`, then:

1. issue an authorized `PUT /usage-service/proxy-filter` with a newly saved URL;
2. call `Summary(..., false)`;
3. assert a fresh aggregate read occurred and the new URL appears in `UnobservedProxies`.

Keep the existing unauthorized-write test. The PUT must not invalidate when decoding or persistence fails.

In monitoring handler tests, instantiate `proxyoverload.New` with `ProxySettings: st`, issue the protected GET, decode the JSON into `proxyoverload.Summary`, and assert:

```go
result.SavedProxyListAvailable == true
result.SavedProxyCount == 0
len(result.UnobservedProxies) == 0
```

Also assert response body does not contain credential identity, token, or fake raw failure text.

- [ ] **Step 2: Run targeted controller tests and verify they fail.**

Run:

```powershell
Set-Location apps/manager-server
go test -count=1 ./internal/http/controller/proxyfilter ./internal/http/controller/monitoring
```

Expected: the cache-invalidating PUT expectation fails until the handler invokes `Invalidate`, and the monitoring response test fails until Store injection is added.

- [ ] **Step 3: Inject the Store and invoke invalidation after persistence success.**

In `internal/app/context.go`, modify only the existing construction:

```go
proxyOverloadService := proxyoverloadsvc.New(proxyoverloadsvc.Options{
    SetupResolver: managerConfigService,
    AuthFiles:     authFiles,
    Evidence:      st,
    ProxySettings: st,
})
```

In `proxyfilter/handler.go`, preserve authorization and save behavior, then add immediately after the successful `SaveProxyFilterSettings` call and before writing HTTP 200:

```go
if h.App.ProxyOverloadService != nil {
    h.App.ProxyOverloadService.Invalidate()
}
```

Do not invalidate on unauthorized, malformed, or failed-save requests. Do not change the endpoint path, body, response schema, or any proxy pool logic.

- [ ] **Step 4: Run controller tests and commit.**

Run:

```powershell
Set-Location apps/manager-server
go test -count=1 ./internal/http/controller/proxyfilter ./internal/http/controller/monitoring ./internal/app
```

Expected: PASS; the protected aggregate remains one GET and only the successful saved-list write invalidates it.

Commit:

```powershell
git add apps/manager-server/internal/app/context.go apps/manager-server/internal/http/controller/proxyfilter/handler.go apps/manager-server/internal/http/controller/proxyfilter/handler_test.go apps/manager-server/internal/http/controller/monitoring/handler_test.go
git commit -m "feat: invalidate overload summary after proxy list save"
```

### Task 3: Extend frontend aggregate types and pure view-model behavior

**Files:**
- Modify: `apps/web/src/services/api/usageService.ts`
- Modify: `apps/web/src/services/api/usageService.proxyOverloadSummary.test.ts`
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.ts`
- Modify: `apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts`

- [ ] **Step 1: Write failing model tests.**

Add a saved-list fixture with:

```ts
const unobserved = [
  { proxy_url: 'http://never-used-a:8080' },
  { proxy_url: 'http://never-used-b:8080' },
];
```

Test that `filterProxyOverloadRows(rows, 'unobserved')` is not used for aggregate rows, while a dedicated `visibleProxyOverloadRows` helper returns only `unobserved` for the new filter. Test both rate directions:

```ts
expect(sortProxyOverloadRows(rows, 'success-desc').map((row) => row.proxy_url)).toEqual([
  'http://healthy-b:8080',
  'http://healthy-a:8080',
  'http://loaded:8080',
]);
expect(sortProxyOverloadRows(rows, 'success-asc').map((row) => row.proxy_url)).toEqual([
  'http://loaded:8080',
  'http://healthy-b:8080',
  'http://healthy-a:8080',
]);
```

Use an equal-rate fixture with different active-account counts to prove tie-breaking remains the prior stable default. Prove `selectAllVisibleProxyKeys` and `proxyURLsForCopy` accept unobserved rows and yield LF-separated text without a final newline.

- [ ] **Step 2: Run the model test and verify it fails.**

Run:

```powershell
npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts --pool=forks --maxWorkers=1
```

Working directory: `apps/web`.

Expected: TypeScript/Vitest failure because the new filter and success-sort inputs do not exist.

- [ ] **Step 3: Add backward-compatible response fields and pure helpers.**

In `usageService.ts`, extend the public response type with optional fields so a browser connected to an older CPAM does not misrender a missing field as a false healthy result:

```ts
saved_proxy_list_available?: boolean;
saved_proxy_count?: number;
observed_saved_proxy_count?: number;
unobserved_proxy_count?: number;
unobserved_proxies?: string[];
```

Set all fields in the demo response. Keep `monitoringAnalyticsApi.getProxyOverloadSummary` exactly one protected GET; do not add a saved-list request.

In `proxyOverloadSummary.ts`, define:

```ts
export type ProxyOverloadFilter = 'all' | 'zero' | 'overloaded' | 'unobserved';
export type ProxyOverloadSuccessSort = 'success-desc' | 'success-asc' | null;
export type ProxyURLRow = Pick<ProxyOverloadSummaryRow, 'proxy_url'>;
```

Implement a `successRate(row)` helper from integer counts, not arbitrary incoming floats:

```ts
const successRate = (row: ProxyOverloadSummaryRow): number =>
  row.active_account_count > 0
    ? row.non_overloaded_account_count / row.active_account_count
    : 0;
```

Make `sortProxyOverloadRows(rows, successSort = null)` preserve the existing zero-overload/default ordering whenever `successSort` is null; when set, compare `successRate`, then call the exact default comparator for ties. Keep `filterProxyOverloadRows` limited to observed rows. Add `unobservedProxyRows` that de-duplicates normalized URLs and `visibleProxyOverloadRows` that chooses observed versus unobserved rows by filter. Generalize selection/copy helpers to `readonly ProxyURLRow[]` so they never need account count fields.

- [ ] **Step 4: Run frontend model and API tests.**

Run:

```powershell
npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts src/services/api/usageService.proxyOverloadSummary.test.ts --pool=forks --maxWorkers=1
```

Working directory: `apps/web`.

Expected: PASS. The HTTP test must still show exactly one `GET /v0/management/monitoring/proxy-overload-summary` and only add `refresh=1` for explicit refresh.

- [ ] **Step 5: Commit the API/type/model work.**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/services/api/usageService.proxyOverloadSummary.test.ts apps/web/src/features/accounts/model/proxyOverloadSummary.ts apps/web/src/features/accounts/model/proxyOverloadSummary.test.ts
git commit -m "feat: support saved proxy summary views"
```

### Task 4: Render saved-list metrics, unobserved rows, and success-rate sorting

**Files:**
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.module.scss`
- Modify: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: Write failing modal tests for the confirmed interaction.**

Extend the response fixture:

```ts
saved_proxy_list_available: true,
saved_proxy_count: 5,
observed_saved_proxy_count: 3,
unobserved_proxy_count: 2,
unobserved_proxies: ['http://never-used-a:8080', 'http://never-used-b:8080'],
```

Add tests that:

1. render all three saved-list metric labels;
2. click `accounts.proxy_overload_filter_unobserved`, obtain only `data-proxy-overload-row` entries tagged with `data-proxy-overload-kind="unobserved"`, select visible, and copy exactly the two unobserved URLs joined by `\n`;
3. click a `data-proxy-overload-success-sort` header twice and prove observed rows change from high-to-low success rate to low-to-high;
4. local filter, selection, and sort do not call `onRefresh`;
5. with `saved_proxy_list_available: false`, the unobserved filter is absent and `accounts.proxy_overload_saved_unavailable` is visible.

Update `AccountsPage.test.tsx` mock response fixture with the optional fields and keep its invariant that opening this modal does not invoke `loadFiles`, history, window-usage, runtime metadata, or auth JSON export work.

- [ ] **Step 2: Run modal tests and verify they fail.**

Run:

```powershell
npx vitest run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/features/accounts/AccountsPage.test.tsx --pool=forks --maxWorkers=1
```

Working directory: `apps/web`.

Expected: failure because the fourth filter, metric labels, and sort control are absent.

- [ ] **Step 3: Implement the modal state and table branches.**

In `ProxyOverloadSummaryModal.tsx`:

```ts
const [filter, setFilter] = useState<ProxyOverloadFilter>('all');
const [successSort, setSuccessSort] = useState<ProxyOverloadSuccessSort>(null);
const savedListAvailable = result?.saved_proxy_list_available === true;
const observedRows = useMemo(
  () => sortProxyOverloadRows(result?.proxies ?? [], successSort),
  [result?.proxies, successSort]
);
const visibleRows = useMemo(
  () => visibleProxyOverloadRows(observedRows, result?.unobserved_proxies ?? [], filter),
  [filter, observedRows, result?.unobserved_proxies]
);
```

Reset `filter`, `successSort`, and selection whenever `generated_at_ms` changes. If a refreshed result loses saved-list availability, reset an active `unobserved` filter to `all`.

Render the first three filters unconditionally and render the fourth only when `savedListAvailable`. Add summary cards for saved, observed-saved, and unobserved counts. If unavailable, render only the stable saved-list-unavailable hint; never claim zero missing URLs.

For observed rows, retain all numeric columns and show:

```tsx
<th aria-sort={successSort === 'success-asc' ? 'ascending' : successSort === 'success-desc' ? 'descending' : 'none'}>
  <button
    type="button"
    className={styles.sortButton}
    data-proxy-overload-success-sort
    onClick={() => setSuccessSort((current) => current === 'success-desc' ? 'success-asc' : 'success-desc')}
    disabled={busy || observedRows.length === 0}
  >
    {t('accounts.proxy_overload_success_rate_label')}
  </button>
</th>
```

Display success rate from `non_overloaded_account_count / active_account_count`. For unobserved rows, use the same checkbox and URL columns, render a localized “近三天无请求” status spanning numeric columns, and do not render a sortable numeric success value. Mark rows with `data-proxy-overload-kind="observed"` or `"unobserved"`.

- [ ] **Step 4: Add all translations and narrow styling.**

Add keys with equivalent wording to every locale:

```text
proxy_overload_filter_unobserved
proxy_overload_saved_count
proxy_overload_observed_saved_count
proxy_overload_unobserved_count
proxy_overload_saved_unavailable
proxy_overload_success_rate_label
proxy_overload_unobserved_status
```

Chinese values must communicate that “未统计” means “近 3 天无请求”, not proxy failure. In SCSS, scope `.sortButton` under `.table th`; use transparent background, inherited color/font, pointer cursor, and visible focus outline. Add `.unobservedStatus` muted text styling. Do not modify global table, account-list, or card styles.

- [ ] **Step 5: Run frontend tests, type-check, and production build.**

Run:

```powershell
npx vitest run src/features/accounts/model/proxyOverloadSummary.test.ts src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx src/features/accounts/AccountsPage.test.tsx src/services/api/usageService.proxyOverloadSummary.test.ts --pool=forks --maxWorkers=1
npm --workspace apps/web run type-check
npm run build
```

Expected: all targeted tests pass, TypeScript emits no errors, and Vite produces the embedded panel bundle.

- [ ] **Step 6: Commit the UI work.**

```powershell
git add apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.tsx apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.module.scss apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx apps/web/src/features/accounts/AccountsPage.test.tsx apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json
git commit -m "feat: filter unobserved proxies in overload summary"
```

### Task 5: Preserve customization memory and perform complete verification

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`
- Verify: all files in Tasks 1–4

- [ ] **Step 1: Update the CPAM customization memory.**

Add a dated section stating all of the following exact preservation rules:

- `proxy-overload-summary` uses saved Manager Server proxy URLs and reports a 72-hour no-request difference;
- URL matching trims whitespace and trailing slashes;
- “成功率” is `non_overloaded / active`, where overload is only strict HTTP 502 `server_is_overloaded`;
- saved-list writes invalidate the aggregate cache generation, including stale-flight protection;
- the modal has manual refresh only, selectable/copyable “未统计” URLs, and no N+1/JSON scan/CPA billing call;
- future upstream merges must retain this feature and the pre-existing selected auth JSON export feature.

- [ ] **Step 2: Run backend tests, including race-sensitive cache behavior.**

Run:

```powershell
Set-Location apps/manager-server
go test -count=1 ./internal/service/proxyoverload ./internal/http/controller/proxyfilter ./internal/http/controller/monitoring ./internal/app
go test -race -count=1 ./internal/service/proxyoverload
```

Expected: PASS. The race run must report no data race in cache invalidation or single-flight completion.

- [ ] **Step 3: Run full repository checks.**

Run:

```powershell
Set-Location ..\..
git diff --check
npm --workspace apps/web run type-check
npm run build
Set-Location apps/manager-server
go test ./... -count=1
```

Expected: no whitespace errors, type/build success, and all Go packages pass. If the host Go toolchain differs from the target image, also execute the repository’s existing Linux Docker Go-test command before deployment.

- [ ] **Step 4: Commit memory and verification-ready documentation.**

```powershell
Set-Location ..\..
git add docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: preserve saved proxy overload analytics"
git status --short
git log --oneline --max-count=5
```

Expected: clean feature branch with the design, plan, backend, UI, and memory commits visible.

- [ ] **Step 5: Integrate and deploy only after all verification succeeds.**

Fast-forward/merge the verified feature branch into local `lo/custom`; never merge into dirty `C:\kyon\docker\cpa-manager\source`. Build one CPAM candidate from the clean integration worktree, then follow the repository deployment runbook:

1. record local CPA Core container identity and server `cpa.service` PID/image before deployment;
2. locally recreate only `cpa-manager-plus` with the candidate image;
3. copy only the verified CPAM native binary to `/home/cpam` on `LACPA-VPS` atomically;
4. preserve `/home/cpam/config/cpam.env`, `/home/cpam/secrets/*`, `/home/cpam/data/*`, `data.key`, SQLite history, OpenResty, and certificates;
5. restart only `cpam.service`;
6. verify local and public CPAM health, new panel strings, candidate version/hash, and unchanged CPA Core / `cpa.service` identities;
7. push only `origin lo/custom` if LO requests it; never push upstream.

Commit integration only after feature commits are verified; do not create a release or deploy commit that changes CPA Core.

## Plan self-review

- **Spec coverage:** Tasks 1–2 implement all server-side difference, safety, cache, and persistence requirements. Tasks 3–4 implement all response, filter, sort, selection/copy, i18n, and no-extra-request requirements. Task 5 preserves memory and verifies/deploys under CPAM-only constraints.
- **Completeness scan:** Every behavior has a named file, test target, command, expected result, and concrete interface or rendering code; no unresolved markers remain.
- **Type consistency:** `ProxySettingsReader`, `Summary` fields, `ProxyOverloadFilter`, `ProxyOverloadSuccessSort`, `ProxyURLRow`, and the `Invalidate()` method are used consistently across all tasks.
