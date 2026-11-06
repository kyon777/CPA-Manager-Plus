# Proxy Current-Enabled Account Count Design

## Goal

Keep all proxy-overload calculations on their existing rolling 72-hour request-evidence definition, while making the displayed `已启用` number mean what an operator needs it to mean: the number of credentials **currently enabled and currently bound to that proxy**, including credentials whose credential-list UI state is `待判定` / `未判定` or that have no request in the last 72 hours.

This is CPAM-only. CPA Core / `cli-proxy-api`, usage billing, historical request storage, pricing, quota calculations, proxy copy, and proxy replacement remain unchanged.

## Confirmed Product Decision

Use approach A: introduce a distinct current-inventory counter rather than redefining the existing active-only API field.

For every currently observed proxy row, the UI becomes conceptually:

```text
活跃账号：15（当前已启用：42）
HTTP 502 过载：3
无过载账号：12
成功率：80%
```

Definitions:

| Metric | Definition | Includes `待判定` / no 72-hour request? |
| --- | --- | --- |
| `活跃账号` | Current credential with a request event matching the existing strict identity rules in the last 72 hours | No |
| `HTTP 502 过载` | Existing exact classification: an active credential whose inspected recent request is HTTP 502 with `server_is_overloaded` | No |
| `无过载账号` / `成功率` | Existing 72-hour active-account outcome calculation | No |
| `当前已启用` | Current CPA auth-file inventory item where `Disabled == false` and normalized current `proxy_url` equals the proxy row | Yes |

`待判定` is a credential-list presentation state. It does not alter the current-inventory enabled test and must therefore be included whenever its underlying CPA auth file is not disabled.

## Existing Behavior and Root Cause

The proxy service already fetches the complete current CPA auth-file inventory once through `FetchWithProxyURLs`. It then builds `filesByTarget` only from credentials eligible for strict request-history matching and increments `enabled_active_account_count` inside the later `Active == true` loop.

That makes the old displayed value:

```text
not disabled ∩ strict current identity ∩ request evidence in 72 hours
```

It excludes an otherwise enabled credential if it is not in the 72-hour event set. This is correct for an active-request subset but not for the operator question, “does this IP currently have enabled accounts bound to it?”

The fix is not to weaken request identity matching or alter the 72-hour aggregate. The fix is to aggregate current enabled proxy bindings directly from the complete inventory before history filtering.

## API Contract

### Preserve existing fields

These fields keep their existing active-only semantics for backward compatibility and are not repurposed:

```text
ProxyOverloadSummary.enabled_active_account_count
ProxyOverloadSummaryRow.enabled_active_account_count
```

They remain useful to non-UI consumers that need the enabled subset of the 72-hour active set.

### Add current-inventory fields

Add these fields to the Manager response:

```text
ProxyOverloadSummary.current_enabled_account_count
ProxyOverloadSummaryRow.current_enabled_account_count
```

Semantics:

- The summary count is the number of current inventory credentials that are enabled and have a non-empty normalized proxy URL.
- The row count is the number of those credentials bound to that normalized proxy URL.
- The summary is deliberately proxy-bound only; enabled credentials with an empty proxy URL are not attributed to an IP and are not included in this displayed total.
- Every physical/virtual credential element delivered by the current CPA inventory is counted once, consistent with existing proxy assignment display behavior.
- The fields are additive. Older web clients can ignore them; a newer web client must fall back to the old active-only field when talking to an older Manager binary, so staged deployment never produces an undefined value.

## Backend Data Flow

1. Resolve persisted CPA setup as today.
2. Fetch the current credential inventory once through `FetchWithProxyURLs`.
3. Immediately scan the full `files` slice, before `latestRequestTarget` filtering:
   - skip `file.Disabled == true`;
   - normalize `file.ProxyURL` with the existing trailing-slash-only normalization;
   - skip empty normalized URLs;
   - increment `Summary.CurrentEnabledAccountCount`;
   - increment an internal `currentEnabledCount` on the aggregate keyed by normalized proxy URL.
4. Build strict history targets and execute the existing single `RecentProxyOverloadEvidence` query unchanged.
5. Build observed proxy rows only from active 72-hour evidence, exactly as today.
6. When serializing each observed row, attach its already-computed `currentEnabledCount` from the full-inventory proxy aggregate.
7. Leave active count, enabled-active count, missing-proxy count, overload count, non-overload count, rate calculation, saved-proxy difference, filtering, copy selection, and sorting untouched.

The service must **not** create new observed table rows solely because a proxy has current enabled credentials but no 72-hour request evidence. That would change existing saved/unobserved proxy semantics and turn no-request proxies into artificial 0% success-rate rows. The new number answers the binding question on the existing statistically observed rows while preserving every other modal behavior.

This remains one CPA inventory fetch and one bounded historical evidence query. It adds O(number of credentials) in-memory aggregation only; it makes no per-account HTTP call and no new SQLite query.

## Frontend Presentation

### Types and demo response

Extend `ProxyOverloadSummaryResponse` and `ProxyOverloadSummaryRow` with optional `current_enabled_account_count` fields. Extend the demo response so development/demo rendering exercises the new contract.

### Modal

The modal resolves the displayed enabled count as:

```text
current_enabled_account_count ?? enabled_active_account_count ?? 0
```

This fallback is only for a brief mixed-version deployment window. The deployed Manager response provides the current-inventory value.

The existing top summary and active-account cell retain their positions but change wording from `已启用` to `当前已启用` so operators do not mistake the value for a 72-hour-only subset.

Suggested Chinese strings:

```text
活跃账号 {{count}} 个（当前已启用 {{enabled}} 个）
{{count}}（当前已启用 {{enabled}}）
```

The help text receives one concise clarification: current enabled counts include enabled credentials in pending/unknown list states and credentials without recent requests; only activity, overload and success rate remain 72-hour request metrics.

Equivalent wording is added to `en`, `ru`, and `zh-TW` locale files.

## Error Handling and Compatibility

- A blank proxy URL does not appear as a current-enabled proxy binding and does not create a phantom row.
- A disabled or inactive upstream auth-file state is excluded from the current-enabled count exactly as it is from current UI enabled state.
- A malformed Codex identity may be excluded from historical active evidence but remains eligible for current-enabled binding count if it is currently enabled and has a proxy; this is intentional because no historical identity match is required to answer current binding state.
- If the backend sends no new field, the UI uses the prior active-enabled value rather than showing `undefined`, crashing, or changing request-rate computations.
- Cache TTL, singleflight behavior, request cancellation, and refresh controls remain unchanged.

## Test Strategy

### Manager server: `apps/manager-server/internal/service/proxyoverload/service_test.go`

Add a focused regression with inventory members that share a proxy:

- one enabled account with 72-hour evidence;
- one enabled `待判定` equivalent with no 72-hour evidence;
- one enabled account with a malformed / history-ineligible Codex identity and no evidence;
- one disabled account with the same proxy;
- one enabled active account on another proxy.

Assert:

- active and overload metrics remain based only on evidence;
- the existing `EnabledActiveAccountCount` remains active-only;
- each active proxy row receives the full current-enabled binding count, including no-evidence and history-ineligible enabled accounts;
- disabled accounts do not contribute;
- the inventory reader remains called exactly once and evidence reader exactly once.

Keep existing no-event and saved-proxy tests, proving no current-only proxy row is materialized and saved/unobserved semantics do not drift.

### Web: `apps/web/src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx`

Extend the fixture with a current-enabled value greater than the active-enabled value. Assert that:

- summary and row text use the current-enabled count;
- fallback to `enabled_active_account_count` works when the new optional field is absent;
- overload counts, success-rate sorting, filters, checkbox selection, copy output, and unobserved-list rendering remain unchanged.

### Verification

Run:

```powershell
# Manager focused regression
podman run --rm -v "${PWD}:/workspace" -w /workspace/apps/manager-server golang:1.26.10-bookworm `
  go test ./internal/service/proxyoverload -count=1

# Web focused regression, type safety and production bundle
npm --prefix apps/web test -- --run src/features/accounts/components/ProxyOverloadSummaryModal.test.tsx
npm --prefix apps/web run type-check
npm --prefix apps/web run build
```

Before deployment, verify only CPAM source/artifacts are selected. After deployment, verify the Manager response has both old and new fields, a proxy row whose `current_enabled_account_count` exceeds its active count when applicable, and that the CPA Core process identity is unchanged.

## Out of Scope

- Altering the 72-hour window, the nine-request inspection cap, or exact `HTTP 502 + server_is_overloaded` rule.
- Changing active-account, missing-proxy, success-rate, sort, saved-list, unobserved-list, copy, proxy health, or selection behavior.
- Creating rows for proxies that have only current bindings but no active 72-hour request evidence.
- Any CPA Core / `cli-proxy-api` change, restart, rebuild, or deployment.