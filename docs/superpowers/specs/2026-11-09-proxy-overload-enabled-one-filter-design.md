# Proxy Overload Exactly-One Enabled Filter Design

## Goal

Add a local-only **Enabled (1)** filter to the proxy-overload statistics modal. It shows observed proxy rows bound to exactly one currently enabled account, so the operator can promptly identify proxy capacity suitable for adding another account.

## Scope

- The filter operates only on the already-returned `result.proxies` observed-row array.
- A row belongs in **Enabled (1)** exactly when `current_enabled_account_count` resolves to `1`.
- For an older response that lacks `current_enabled_account_count`, the compatibility fallback is `enabled_active_account_count`; if that is also absent, the resolved count is `0`, matching the existing display fallback.
- **Unenabled** remains exactly resolved enabled count `0`.
- Rows whose resolved enabled count is `2` or more are excluded from both the new filter and **Unenabled**.
- **Unobserved** remains an independent saved-list view and is never included in this filter.
- Existing overload classification, success-rate sorting, selection, copy, refresh behavior, API payloads, database reads, and backend aggregation remain unchanged.

## Data Flow

1. CPAM already returns observed proxy rows and optional enabled-count fields.
2. `filterProxyOverloadRows()` applies an in-memory `enabled_one` predicate using the same count resolution as the existing **Unenabled** predicate.
3. `ProxyOverloadSummaryModal` renders the tab after **Unenabled** and before optional **Unobserved**.
4. Changing the tab only changes local React state; it does not invoke `onRefresh` or issue a new request.
5. The existing checkbox and copy behavior operate on the filtered visible rows.

## UI Copy

- zh-CN: `已启用(1)`
- zh-TW: `已啟用(1)`
- en: `Enabled (1)`
- ru fallback resource: `Enabled (1)`

## Verification

- Model tests prove exact-one selection, legacy fallback selection, zero exclusion, and two-or-more exclusion.
- Modal tests click the new tab and verify only the exact-one row renders without calling `onRefresh`.
- Focused model/modal tests, TypeScript type-check, and production web build verify the change.