# Current-Page Account History Only

## Goal

Keep the full credential inventory and global recent-request features, while
eliminating every automatic full-inventory `account-history` request. The
credential list must load exact history and price only for the visible page
(normally at most 50 credentials).

## Observed failure

The local CPAM log recorded `POST /v0/management/monitoring/account-history`
returning HTTP 500 after `3m59.837s`, which exhausts the configured four-minute
request budget. The usage SQLite database is approximately 17.4 GB. A first
load currently builds a full-inventory target list, batches it in groups of 200,
and runs two history requests concurrently. With 5,000 credentials that creates
about 25 history batches even though only one visible page needs prices.

## Chosen behavior

| Trigger | `account-history` / exact price target |
| --- | --- |
| First credential-list entry | Current visible page only |
| Page or filter change | Current visible page only |
| Enabling live refresh | Current visible page only |
| Live-refresh interval | Current visible page only |
| Single credential refresh | That credential only |
| Top-right credential-list refresh | Current visible page only |

`account-window-usage` remains current-page only. The full CPA
`auth-files?include_proxy_url=1` inventory remains unchanged so browser-side
global filters, enabled totals, proxy selection, and global recent-request
ordering retain their established semantics. `account-recent-index` also
retains its independent global and visible-page behavior.

## Data integrity

The backend price path remains the current exact read-only implementation:
pricing rollups are read first, and only incomplete account keys in the actual
request may use coverage-checked retained/raw event recovery. PR #1083's rule
remains mandatory: live raw events are authoritative; a retained projection is
used only for archive rows proved deleted. This work changes no CPA Core
process, credential file, usage event, Credits, quota, model-price write,
scheduler, or billing fact.

## Implementation boundary

Only the AccountsPage client scheduling and its focused tests/documentation
change. The obsolete `all` account-history scope, its full target list, full
abort state, and full-load effects are removed rather than left reachable.
Current-page abort/deduplication continues to prevent stale page results from
rendering. The four-minute request timeout remains unchanged.

## Verification

Use a focused frontend test proving initial load, live-refresh enablement, and
the toolbar refresh send only visible-page targets. Run the focused test file,
TypeScript type-check, and production web build. Do not run the full test suite
for this urgent performance correction.
