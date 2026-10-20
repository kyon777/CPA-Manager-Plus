# Recent Request Anomaly Filters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two global credential-list operational filters: recent nine requests containing at least two exact `HTTP 502 + server_is_overloaded` failures, and recent nine requests containing at least two failures of any kind.

**Architecture:** Extend CPAM's existing authenticated `POST /v0/management/monitoring/account-recent-index` contract with an optional bounded `recent_limit`; it remains a read-only SQLite indexed projection and never calls CPA Core. The web page asks for nine records only while one of the new filters is selected, keeps the current one-record request for every other list state, and applies a pure in-memory classifier only after a complete global nine-record index is available.

**Tech Stack:** Go 1.24 Manager Server, SQLite `usage_events` indexed recent-request projection, React 19, TypeScript, Vitest, i18next.

---

## Scope and invariants

- This is **CPAM-only** work. Do not modify, build, restart, deploy, or call into CPA Core / `cli-proxy-api`.
- The existing `account-recent-index` route continues to read only CPAM's safe request projection. It must not start `account-history` catch-up, quota refreshes, Credits reads, inspection, credential writes, billing aggregation, or priority-demotion work.
- The default payload remains `{ "accounts": [...] }`; it must not carry `recent_limit` unless a new anomaly filter is selected.
- The strict overload filter must require all three facts: `failed=true`, HTTP 502, and the complete safe-code token `server_is_overloaded`. Do not match generic 502, EOF, proxy 407, 500, 503, 429, or prose with no error code.
- `fail_body`, `raw_json`, tokens, proxy URLs, and unredacted response bodies remain outside the API response.
- Preserve all existing CPAM private customizations listed in `docs/LO-CPAM-CUSTOM-MEMORY.md`.

## File map

| Path | Change responsibility |
| --- | --- |
| `apps/manager-server/internal/service/monitoring/service.go` | Optional `recent_limit`, bounded resolver, and index query depth selection. |
| `apps/manager-server/internal/service/monitoring/service_test.go` | Default-depth, nine-depth, ordering, bounds, and safe-response regression tests. |
| `apps/manager-server/internal/http/controller/monitoring/handler.go` | Reject invalid `recent_limit` as HTTP 400 before service execution. |
| `apps/manager-server/internal/http/controller/monitoring/handler_test.go` | Controller validation regression test for 0 and 10. |
| `apps/web/src/services/api/usageService.ts` | Type the optional JSON field without changing the default request body. |
| `apps/web/src/features/accounts/model/accountRecentRequestAnomaly.ts` | New pure filter/depth/request-builder model. |
| `apps/web/src/features/accounts/model/accountRecentRequestAnomaly.test.ts` | Exact nine-request matching and request-shape tests. |
| `apps/web/src/features/accounts/model/accountsWorkspaceUiState.ts` | Persist and validate the two operational-filter values. |
| `apps/web/src/features/accounts/model/accountsWorkspaceUiState.test.ts` | Local preference round-trip regression. |
| `apps/web/src/features/accounts/model/accountsWorkspaceUrlState.ts` | Accept and serialize the two `operation` URL values. |
| `apps/web/src/features/accounts/model/accountsWorkspaceUrlState.test.ts` | URL round-trip regression. |
| `apps/web/src/features/accounts/AccountsPage.tsx` | Demand-driven depth selection, abort-safe coverage state, dropdown options, blocking load/error presentation, and in-memory intersection filtering. |
| `apps/web/src/features/accounts/AccountsPage.test.tsx` | Page wiring: default depth stays compact; new filters request 9 and filter only from complete global evidence. |
| `apps/web/src/i18n/locales/{zh-CN,zh-TW,en,ru}.json` | Labels and index-loading/error text for the two options. |
| `docs/LO-CPAM-CUSTOM-MEMORY.md` | Durable private-customization record for the feature and CPAM-only boundaries. |

### Task 1: Bound the Manager Server recent-index contract

**Files:**
- Modify: `apps/manager-server/internal/service/monitoring/service.go:28-37, 218-245, 1789-1832`
- Modify: `apps/manager-server/internal/service/monitoring/service_test.go:2267-2356`
- Modify: `apps/manager-server/internal/http/controller/monitoring/handler.go:210-240`
- Modify: `apps/manager-server/internal/http/controller/monitoring/handler_test.go`

- [ ] **Step 1: Write failing service and controller validation tests.**

Add a local integer-pointer helper in `service_test.go`, then assert omitted uses one, explicit nine returns the newest nine in descending timestamp order, and bad values never reach the query:

```go
func intPointer(value int) *int { return &value }

func TestResolveAccountRecentIndexLimit(t *testing.T) {
	cases := []struct {
		name string
		value *int
		want  int
		valid bool
	}{
		{name: "omitted defaults to one", value: nil, want: 1, valid: true},
		{name: "nine is accepted", value: intPointer(9), want: 9, valid: true},
		{name: "zero is rejected", value: intPointer(0), valid: false},
		{name: "ten is rejected", value: intPointer(10), valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAccountRecentIndexLimit(tc.value)
			if tc.valid && (err != nil || got != tc.want) {
				t.Fatalf("ResolveAccountRecentIndexLimit(%v) = %d, %v", tc.value, got, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("ResolveAccountRecentIndexLimit(%v) succeeded", tc.value)
			}
		})
	}
}
```

Add an `AccountRecentIndex` fixture with ten unique timestamps, `FailBody: "private-body"`, and `RawJSON: "private-json"`; call it with `RecentLimit: intPointer(9)`. Assert `len(item.RecentRequests) == 9`, timestamps descend from the ninth newest through the newest, and `json.Marshal(response)` contains neither private marker. Keep the existing omitted-limit test and assert it remains one record.

In `handler_test.go`, add a table-driven direct validator test using a valid file target:

```go
func TestValidateAccountRecentIndexRequestRejectsOutOfRangeRecentLimit(t *testing.T) {
	base := monitoringsvc.AccountRecentIndexRequest{Accounts: []monitoringsvc.AccountHistoryTarget{{
		RowKey: "row-1", AuthFileSnapshot: "credential.json", AuthProviderSnapshot: "codex",
	}}}
	for _, limit := range []int{0, 10} {
		req := base
		req.RecentLimit = &limit
		if err := validateAccountRecentIndexRequest(req); err == nil {
			t.Fatalf("recent_limit=%d was accepted", limit)
		}
	}
}
```

- [ ] **Step 2: Run the focused Go tests and confirm the new tests fail because the field/resolver do not exist.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/monitoring -run 'TestResolveAccountRecentIndexLimit|TestAccountRecentIndex' -count=1
go test ./internal/http/controller/monitoring -run TestValidateAccountRecentIndexRequestRejectsOutOfRangeRecentLimit -count=1
```

Expected: compile failure mentioning `RecentLimit` and/or `ResolveAccountRecentIndexLimit` before implementation.

- [ ] **Step 3: Implement the safe optional depth resolver and use it in both controller and service.**

In `service.go`, keep detailed-history's `accountRecentRequestLimit = 10` unchanged and add distinct compact-index constants:

```go
const (
	// existing constants ...
	defaultAccountRecentIndexLimit = 1
	maxAccountRecentIndexLimit     = 9
)

type AccountRecentIndexRequest struct {
	Accounts    []AccountHistoryTarget `json:"accounts"`
	RecentLimit *int                   `json:"recent_limit,omitempty"`
}

func ResolveAccountRecentIndexLimit(value *int) (int, error) {
	if value == nil {
		return defaultAccountRecentIndexLimit, nil
	}
	if *value < 1 || *value > maxAccountRecentIndexLimit {
		return 0, fmt.Errorf("recent_limit must be between 1 and %d", maxAccountRecentIndexLimit)
	}
	return *value, nil
}
```

At the start of `AccountRecentIndex`, resolve before constructing targets and pass that exact result to the existing indexed repository call:

```go
recentLimit, err := ResolveAccountRecentIndexLimit(req.RecentLimit)
if err != nil {
	return AccountRecentIndexResponse{}, err
}
// Preserve the existing target identity logic unchanged.
recentRequests, err := s.store.RecentAccountRequests(ctx, latestRequestTargets, recentLimit)
```

Update the nearby comment to state that the default is one for global sorting and the caller can request at most nine safe records for anomaly filtering. Do not change `AccountHistory`'s ten-record limit.

At the end of `validateAccountRecentIndexRequest` in `handler.go`, call the same resolver so invalid client JSON gets a 400 rather than a service-layer 500:

```go
if _, err := monitoringsvc.ResolveAccountRecentIndexLimit(req.RecentLimit); err != nil {
	return err
}
return nil
```

- [ ] **Step 4: Re-run focused backend tests and then the Manager Server suite.**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/monitoring -run 'TestResolveAccountRecentIndexLimit|TestAccountRecentIndex' -count=1
go test ./internal/http/controller/monitoring -run TestValidateAccountRecentIndexRequestRejectsOutOfRangeRecentLimit -count=1
go test ./...
```

Expected: all commands exit 0. The existing default compact-index test must still assert exactly one record.

- [ ] **Step 5: Commit the backend contract independently.**

```powershell
git add apps/manager-server/internal/service/monitoring/service.go apps/manager-server/internal/service/monitoring/service_test.go apps/manager-server/internal/http/controller/monitoring/handler.go apps/manager-server/internal/http/controller/monitoring/handler_test.go
git commit -m "feat: support bounded recent request index depth"
```

### Task 2: Create the pure TypeScript anomaly classifier and persisted filter values

**Files:**
- Create: `apps/web/src/features/accounts/model/accountRecentRequestAnomaly.ts`
- Create: `apps/web/src/features/accounts/model/accountRecentRequestAnomaly.test.ts`
- Modify: `apps/web/src/services/api/usageService.ts:1330-1335`
- Modify: `apps/web/src/features/accounts/model/accountsWorkspaceUiState.ts:1-40`
- Modify: `apps/web/src/features/accounts/model/accountsWorkspaceUiState.test.ts`
- Modify: `apps/web/src/features/accounts/model/accountsWorkspaceUrlState.ts:1-45`
- Modify: `apps/web/src/features/accounts/model/accountsWorkspaceUrlState.test.ts`

- [ ] **Step 1: Write failing pure-model tests for both filters, the exact error-code rule, ninth-record boundary, and request shape.**

Use a minimal request factory in the new test file:

```ts
const request = (
  timestamp_ms: number,
  overrides: Partial<MonitoringAccountLatestRequest> = {}
): MonitoringAccountLatestRequest => ({ timestamp_ms, failed: false, ...overrides });

const overloaded = (timestamp_ms: number) =>
  request(timestamp_ms, {
    failed: true,
    fail_status_code: 502,
    header_error_code: 'server_is_overloaded',
  });
```

Cover these exact assertions:

```ts
expect(matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
  overloaded(9), overloaded(8), request(7), request(6), request(5), request(4), request(3), request(2), request(1),
])).toBe(true);

expect(matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
  request(9, { failed: true, fail_status_code: 502, fail_summary: 'unexpected EOF' }),
  request(8, { failed: true, fail_status_code: 502, header_error_code: 'proxy_connect_407' }),
])).toBe(false);

expect(matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
  request(9, { failed: true, fail_status_code: 500, header_error_code: 'server_is_overloaded' }),
  overloaded(8),
])).toBe(false);

expect(matchesAccountRecentRequestAnomaly('recent_failures', [
  request(9, { failed: true }), request(8, { failed: true }), request(7),
])).toBe(true);

expect(matchesAccountRecentRequestAnomaly('recent_failures', [
  ...Array.from({ length: 9 }, (_, offset) => request(9 - offset)),
  request(0, { failed: true }), request(-1, { failed: true }),
])).toBe(false);

expect(buildAccountRecentIndexRequest([target], 'all')).toEqual({ accounts: [target] });
expect(buildAccountRecentIndexRequest([target], 'recent_failures')).toEqual({
  accounts: [target], recent_limit: 9,
});
```

Also add a case where `fail_summary` is a JSON-safe string containing `"code":"server_is_overloaded"`, proving the fallback works; a string `not_server_is_overloaded` must not match.

- [ ] **Step 2: Run the model test and confirm it fails because the module does not exist.**

Run:

```powershell
npm --workspace apps/web exec vitest run src/features/accounts/model/accountRecentRequestAnomaly.test.ts
```

Expected: FAIL with module-not-found/import failure.

- [ ] **Step 3: Implement the single-purpose model and type the wire field.**

Create `accountRecentRequestAnomaly.ts` with these exported boundaries:

```ts
import type {
  MonitoringAccountHistoryTarget,
  MonitoringAccountLatestRequest,
  MonitoringAccountRecentIndexRequest,
} from '@/services/api/usageService';

export const ACCOUNT_RECENT_REQUEST_WINDOW_SIZE = 9;
export const ACCOUNT_RECENT_REQUEST_MATCH_THRESHOLD = 2;
export const ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS = [
  'recent_server_overloaded_502',
  'recent_failures',
] as const;
export type AccountRecentRequestOperationalFilter =
  (typeof ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS)[number];

export const isAccountRecentRequestOperationalFilter = (
  value: string
): value is AccountRecentRequestOperationalFilter =>
  ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS.includes(
    value as AccountRecentRequestOperationalFilter
  );
```

Implement `getAccountRecentIndexLimitForOperationalFilter`, `buildAccountRecentIndexRequest`, and `matchesAccountRecentRequestAnomaly` as follows:

```ts
const SERVER_OVERLOADED_CODE = 'server_is_overloaded';

const hasExactSafeErrorCode = (value: string | null | undefined): boolean =>
  String(value ?? '')
    .toLowerCase()
    .split(/[^a-z0-9_]+/)
    .includes(SERVER_OVERLOADED_CODE);

const isServerOverloaded502 = (value: MonitoringAccountLatestRequest): boolean =>
  value.failed === true &&
  value.fail_status_code === 502 &&
  (hasExactSafeErrorCode(value.header_error_code) ||
    hasExactSafeErrorCode(value.fail_summary));

export const getAccountRecentIndexLimitForOperationalFilter = (value: string): number =>
  isAccountRecentRequestOperationalFilter(value) ? ACCOUNT_RECENT_REQUEST_WINDOW_SIZE : 1;

export const buildAccountRecentIndexRequest = (
  accounts: MonitoringAccountHistoryTarget[],
  operationalFilter: string
): MonitoringAccountRecentIndexRequest => {
  const recentLimit = getAccountRecentIndexLimitForOperationalFilter(operationalFilter);
  return recentLimit === 1 ? { accounts } : { accounts, recent_limit: recentLimit };
};

export const matchesAccountRecentRequestAnomaly = (
  filter: AccountRecentRequestOperationalFilter,
  requests: readonly MonitoringAccountLatestRequest[] | null | undefined
): boolean => {
  const recent = (requests ?? []).slice(0, ACCOUNT_RECENT_REQUEST_WINDOW_SIZE);
  const matches = recent.filter((item) =>
    filter === 'recent_server_overloaded_502' ? isServerOverloaded502(item) : item.failed === true
  ).length;
  return matches >= ACCOUNT_RECENT_REQUEST_MATCH_THRESHOLD;
};
```

In `usageService.ts`, add only this optional field:

```ts
export interface MonitoringAccountRecentIndexRequest {
  accounts: MonitoringAccountHistoryTarget[];
  recent_limit?: number;
}
```

In `accountsWorkspaceUiState.ts`, import the new runtime list and type, extend `AccountOperationalFilter`, and build `OPERATIONAL_FILTERS` from it:

```ts
export type AccountOperationalFilter =
  | 'all'
  | 'reauth'
  | 'cooldown'
  | 'automation'
  | 'recovered'
  | AccountRecentRequestOperationalFilter;

const OPERATIONAL_FILTERS = new Set<AccountOperationalFilter>([
  'all', 'reauth', 'cooldown', 'automation', 'recovered',
  ...ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS,
]);
```

Mirror the same accepted values in `accountsWorkspaceUrlState.ts`. The URL token names must be exactly `recent_server_overloaded_502` and `recent_failures`; do not introduce alternate spellings.

- [ ] **Step 4: Add persistence and URL regression cases.**

Add these assertions to the existing UI-state and URL suites:

```ts
expect(
  normalizeAccountsWorkspaceUiState({ operationalFilter: 'recent_failures' }).operationalFilter
).toBe('recent_failures');

const search = writeAccountsWorkspaceUrlSearch('', {
  ...DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE,
  view: 'accounts', healthMode: 'local', account: null, detailTab: 'overview',
  editor: null, editorProvider: '', operationalFilter: 'recent_server_overloaded_502',
}, DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE);
expect(search).toBe('?operation=recent_server_overloaded_502');
expect(readAccountsWorkspaceUrlState(search, DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE).operationalFilter)
  .toBe('recent_server_overloaded_502');
```

- [ ] **Step 5: Run the focused frontend model/state tests.**

Run:

```powershell
npm --workspace apps/web exec vitest run src/features/accounts/model/accountRecentRequestAnomaly.test.ts src/features/accounts/model/accountsWorkspaceUiState.test.ts src/features/accounts/model/accountsWorkspaceUrlState.test.ts
```

Expected: all listed test files pass; the default request-shape assertion proves normal list loads remain one-record compact requests.

- [ ] **Step 6: Commit the pure model and persisted state.**

```powershell
git add apps/web/src/services/api/usageService.ts apps/web/src/features/accounts/model/accountRecentRequestAnomaly.ts apps/web/src/features/accounts/model/accountRecentRequestAnomaly.test.ts apps/web/src/features/accounts/model/accountsWorkspaceUiState.ts apps/web/src/features/accounts/model/accountsWorkspaceUiState.test.ts apps/web/src/features/accounts/model/accountsWorkspaceUrlState.ts apps/web/src/features/accounts/model/accountsWorkspaceUrlState.test.ts
git commit -m "feat: add recent request anomaly filter model"
```

### Task 3: Wire demand-driven global depth and visible list behavior

**Files:**
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:1508-1515, 1559-1569, 2306-2348, 3850-3866, 4615-4661, 4722-4786, 6396-6460, 8377-8393, 10987-11000`
- Modify: `apps/web/src/features/accounts/AccountsPage.test.tsx:180-220, 547-558, 4712-4824`

- [ ] **Step 1: Write the page-level failing tests for compact default requests, nine-record anomaly requests, and global filtering.**

Update the test-only request shape to include `recent_limit?: number`:

```ts
type AccountRecentIndexRequestForTest = AccountHistoryRequestForTest & {
  recent_limit?: number;
};
```

Add a test with three enabled Codex files and a mocked recent-index response containing an item for every target. Its first call must omit `recent_limit`; after selecting `recent_server_overloaded_502`, the next call must carry `recent_limit: 9` and only the row with two exact overload records may render. Then switch to `recent_failures` and verify its next request also carries 9 and rows with at least two `failed: true` records render.

The request assertions must use the real Select contract:

```ts
const operationalSelect = renderer.root
  .findAllByType(Select)
  .find((node) => node.props.ariaLabel === 'accounts.operational_filter');
if (!operationalSelect) throw new Error('Accounts operational filter not found');

expect(mocks.getAccountRecentIndex.mock.calls[0]?.[2]).toMatchObject({ accounts: expect.any(Array) });
expect(mocks.getAccountRecentIndex.mock.calls[0]?.[2]).not.toHaveProperty('recent_limit');

await act(async () => {
  operationalSelect.props.onChange('recent_server_overloaded_502');
  await flushPromises();
});
expect(mocks.getAccountRecentIndex.mock.calls.at(-1)?.[2]).toMatchObject({ recent_limit: 9 });
```

Assert an intermediate state with an old one-record response does not display an empty "no matches" result as final truth: it must render the new index loading marker. Add an error mock that rejects the nine-record request and assert the page shows the anomaly-index error marker rather than treating missing data as zero matching accounts.

- [ ] **Step 2: Run the page test and confirm it fails before wiring changes.**

Run:

```powershell
npm --workspace apps/web exec vitest run src/features/accounts/AccountsPage.test.tsx
```

Expected: the new assertions fail because the Select has no anomaly values and all `account-recent-index` requests remain compact.

- [ ] **Step 3: Add explicit recent-index coverage state and request depth selection.**

Import the pure model functions into `AccountsPage.tsx`. Define the required depth from the effective filter and make it part of the load key:

```ts
const accountRecentIndexRequiredLimit = getAccountRecentIndexLimitForOperationalFilter(
  effectiveOperationalFilter
);
const accountRecentIndexScopeKey = `${accountRecentIndexAutoContextKey}\u0000${accountHistoryAutoRefreshRevision}`;
const accountRecentIndexAutoLoadKey = `${accountRecentIndexScopeKey}\u0000${accountRecentIndexRequiredLimit}`;
```

Add focused state; it must be invalidated with the existing connection-scope reset and when the view becomes inactive:

```ts
type AccountRecentIndexCoverage = {
  scopeKey: string;
  recentLimit: number;
  itemCount: number;
};

const [accountRecentIndexCoverage, setAccountRecentIndexCoverage] =
  useState<AccountRecentIndexCoverage | null>(null);
const [accountRecentIndexLoading, setAccountRecentIndexLoading] = useState(false);
const [accountRecentIndexError, setAccountRecentIndexError] = useState('');
```

In `loadAccountRecentIndex`, build the body through the pure model instead of an inline object, set loading before `mapWithConcurrency`, and only mark coverage after every batch finishes and the current `AbortController` still owns the request:

```ts
const request = buildAccountRecentIndexRequest(
  batch.map((entry) => entry.target),
  effectiveOperationalFilter
);
return monitoringAnalyticsApi.getAccountRecentIndex(
  managerServiceBase,
  managementKey,
  request,
  controller.signal
);
```

After merging all batches, use:

```ts
setAccountRecentIndexByRowKey(next);
setAccountRecentIndexCoverage({
  scopeKey: accountRecentIndexScopeKey,
  recentLimit: accountRecentIndexRequiredLimit,
  itemCount: next.size,
});
setAccountRecentIndexError('');
```

In the non-abort error path, retain the old map but clear/avoid claiming new coverage and set `accountRecentIndexError` to the localized request-index failure message. Always clear the loading state only if this controller still owns it. Preserve the current 1,000-target batching, `MAX_CONCURRENT_ACCOUNT_HISTORY_REQUESTS`, connection-change clearing, and AbortController cancellation behavior.

- [ ] **Step 4: Gate filtering on complete global nine-record coverage and add the two dropdown options.**

Compute readiness only for the new filter values:

```ts
const operationalFilterNeedsRecentRequestIndex =
  isAccountRecentRequestOperationalFilter(effectiveOperationalFilter);
const accountRecentIndexCoverageComplete =
  accountRecentIndexTargets.length === 0 ||
  (accountRecentIndexCoverage?.scopeKey === accountRecentIndexScopeKey &&
    accountRecentIndexCoverage.recentLimit >= accountRecentIndexRequiredLimit &&
    accountRecentIndexCoverage.itemCount === accountRecentIndexTargets.length);
const recentAnomalyFilterPending =
  operationalFilterNeedsRecentRequestIndex && !accountRecentIndexCoverageComplete && !accountRecentIndexError;
```

Add the two branches before the existing `cooldown` branch, and read **only** `accountRecentIndexByRowKey` for their input so visible-page `account-history` data cannot change a global result:

```ts
if (isAccountRecentRequestOperationalFilter(effectiveOperationalFilter)) {
  return matchesAccountRecentRequestAnomaly(
    effectiveOperationalFilter,
    accountRecentIndexByRowKey.get(row.selectionKey)?.recent_requests
  );
}
```

Only execute this predicate after `recentAnomalyFilterPending` is false and coverage is complete. If coverage is not complete, do not show an empty filtered list as a real result.

Add Select choices after reauthentication:

```tsx
{ value: 'recent_server_overloaded_502', label: t('accounts.operational_recent_server_overloaded_502') },
{ value: 'recent_failures', label: t('accounts.operational_recent_failures') },
```

Keep existing cooldown/automation availability rules and every existing option unchanged.

- [ ] **Step 5: Render a clear loading/error state without triggering any account mutation or quota fetch.**

In `renderAccountsOverview`, ahead of `renderAccountCards()`, select one of these mutually exclusive branches:

```tsx
{recentAnomalyFilterPending ? (
  <div className={styles.loadingPanel} data-account-recent-index-loading="true">
    <LoadingSpinner />
    <span>{t('accounts.recent_request_anomaly_index_loading')}</span>
  </div>
) : accountRecentIndexError && operationalFilterNeedsRecentRequestIndex ? (
  <div className={styles.errorBox} data-account-recent-index-error="true">
    {t('accounts.recent_request_anomaly_index_error')}
  </div>
) : hasLoaded ? (
  renderAccountCards()
) : null}
```

Do not call `loadFiles`, `loadAccountHistory`, `loadAccountWindowUsage`, account refresh, or any mutation from this branch. It is presentation for the already-issued lightweight recent-index request only.

- [ ] **Step 6: Run page and related regression tests.**

Run:

```powershell
npm --workspace apps/web exec vitest run src/features/accounts/AccountsPage.test.tsx src/features/accounts/model/accountRows.test.ts src/features/accounts/model/accountHealthEvidence.test.ts
```

Expected: all pass. The new page test proves `recent_limit` is absent by default, equals 9 only for the two new values, and no incomplete one-record map is used for a final anomaly result.

- [ ] **Step 7: Commit the page wiring.**

```powershell
git add apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/AccountsPage.test.tsx
git commit -m "feat: filter credentials by recent request anomalies"
```

### Task 4: Localize, record private behavior, and verify the whole change

**Files:**
- Modify: `apps/web/src/i18n/locales/zh-CN.json:1416-1424`
- Modify: `apps/web/src/i18n/locales/zh-TW.json:1416-1424`
- Modify: `apps/web/src/i18n/locales/en.json:1418-1426`
- Modify: `apps/web/src/i18n/locales/ru.json:1422-1430`
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md`

- [ ] **Step 1: Add all locale strings adjacent to the existing operational-status strings.**

Use these exact labels:

```json
// zh-CN
"operational_recent_server_overloaded_502": "最近 9 个请求：服务过载 502 ≥ 2",
"operational_recent_failures": "最近 9 个请求：错误 ≥ 2",
"recent_request_anomaly_index_loading": "正在读取最近 9 个请求索引…",
"recent_request_anomaly_index_error": "最近请求索引读取失败，无法安全完成该筛选。"
```

```json
// en
"operational_recent_server_overloaded_502": "Last 9 requests: overloaded 502 ≥ 2",
"operational_recent_failures": "Last 9 requests: failures ≥ 2",
"recent_request_anomaly_index_loading": "Loading the last 9 request index…",
"recent_request_anomaly_index_error": "The recent request index could not be loaded, so this filter cannot be completed safely."
```

Use equivalent Traditional Chinese and Russian translations at the same keys. Keep valid JSON commas and UTF-8 text; do not alter unrelated localization keys.

- [ ] **Step 2: Add the durable CPAM customization record.**

Append a dated section to `docs/LO-CPAM-CUSTOM-MEMORY.md` with these concrete points:

```markdown
## 2026-10-20: Recent nine-request anomaly filters

- CPAM's authenticated `account-recent-index` accepts optional `recent_limit` only from 1 through 9; omission remains exactly one request for normal global recent-request sorting.
- The credential-list operational filters `recent_server_overloaded_502` and `recent_failures` request nine safe indexed request records globally, then apply local display-only matching with a threshold of two.
- The strict overload option requires `failed=true`, HTTP 502, and the complete safe code `server_is_overloaded`; generic 502, proxy errors, EOF, 500/503/429, and message-only matches are excluded.
- This path is read-only CPAM SQLite evidence. It does not call CPA Core, mutate credential JSON, touch usage/billing/quota data, or alter priority demotion.
- Keep 1,000-target batching, bounded concurrency, AbortController cancellation, and coverage gating so partial index replies are never interpreted as no matching credentials.
```

- [ ] **Step 3: Run formatting, focused tests, type checking, build, Go tests, and diff validation.**

Run:

```powershell
npm --workspace apps/web exec prettier --write src/features/accounts/AccountsPage.tsx src/features/accounts/AccountsPage.test.tsx src/features/accounts/model/accountRecentRequestAnomaly.ts src/features/accounts/model/accountRecentRequestAnomaly.test.ts src/features/accounts/model/accountsWorkspaceUiState.ts src/features/accounts/model/accountsWorkspaceUiState.test.ts src/features/accounts/model/accountsWorkspaceUrlState.ts src/features/accounts/model/accountsWorkspaceUrlState.test.ts src/services/api/usageService.ts src/i18n/locales/zh-CN.json src/i18n/locales/zh-TW.json src/i18n/locales/en.json src/i18n/locales/ru.json
npm --workspace apps/web exec vitest run src/features/accounts/model/accountRecentRequestAnomaly.test.ts src/features/accounts/model/accountsWorkspaceUiState.test.ts src/features/accounts/model/accountsWorkspaceUrlState.test.ts src/features/accounts/AccountsPage.test.tsx
npm run type-check
npm run build
Set-Location apps/manager-server
go test ./...
Set-Location ../..
git diff --check
```

Expected: each command exits 0. The build must succeed with all locale keys present. If an existing unrelated baseline test fails, capture its exact test name and failure output separately; do not weaken or delete it to make this feature appear green.

- [ ] **Step 4: Review the final diff against the invariants, then commit documentation and localization.**

Review with:

```powershell
git diff HEAD~3..HEAD -- apps/manager-server/internal/service/monitoring apps/manager-server/internal/http/controller/monitoring apps/web/src/features/accounts apps/web/src/services/api/usageService.ts apps/web/src/i18n/locales docs/LO-CPAM-CUSTOM-MEMORY.md
git status --short
git diff --check
```

Confirm there are no changes under CPA Core paths, no credential material, no raw response fields, and no changes to billing/quota/priority worker code. Then commit:

```powershell
git add apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-TW.json apps/web/src/i18n/locales/en.json apps/web/src/i18n/locales/ru.json docs/LO-CPAM-CUSTOM-MEMORY.md
git commit -m "docs: record recent request anomaly filters"
```

Deployment and private-repository push are deliberately separate operations. Do not deploy or push this feature until LO explicitly requests them; when requested, use the CPAM-only runbook and never push upstream.
