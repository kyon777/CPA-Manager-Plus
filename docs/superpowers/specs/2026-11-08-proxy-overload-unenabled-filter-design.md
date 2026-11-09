# Proxy Overload “Unenabled” Filter Design

## Goal

Add an **Unenabled** filter to the proxy-overload statistics modal. It shows only observed proxy rows whose current enabled-binding count is zero, so an operator can identify proxy URLs that have recent 72-hour request evidence but no currently enabled account still bound to them.

## Scope

- The filter operates exclusively on the already returned `result.proxies` array.
- A row belongs in **Unenabled** exactly when its `current_enabled_account_count` resolves to `0`.
- For compatibility with an older Manager response that lacks `current_enabled_account_count`, use `enabled_active_account_count`; if that is also absent, use `0`, matching the existing display fallback.
- The existing **Unobserved** filter remains an independent view of `unobserved_proxies`; it is never included in **Unenabled**.
- Existing all / zero-overload / overloaded filtering, selection, copying, success-rate sorting, refresh behavior, API payloads, database queries, and backend aggregation remain unchanged.

## Data Flow

1. CPAM already returns observed rows in `ProxyOverloadSummaryResponse.proxies`, including optional current enabled counts.
2. `filterProxyOverloadRows()` applies the new `unenabled` predicate in memory.
3. `ProxyOverloadSummaryModal` includes the new filter button with other observed-row filters regardless of saved-list availability.
4. Switching to the filter does not call `onRefresh`; selected visible rows retain the normal checkbox/copy behavior.
5. `unobserved` continues to render only saved proxy URLs that have no 72-hour observed record.

## UI Copy

- zh-CN: `未启用`
- zh-TW: `未啟用`
- en: `Unenabled`
- ru fallback resource: `Unenabled`, following the existing proxy-overload English entries.

## Verification

- Model test covers both native current-count rows and the older response fallback, and proves unobserved rows remain out of this filter.
- Modal test clicks the filter and verifies only zero-current-enabled observed rows render, with no parent refresh.
- Focused model/modal test suite, TypeScript type-check, and web production build pass.