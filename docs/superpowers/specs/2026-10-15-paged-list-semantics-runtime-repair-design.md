# Paged Credential List Semantics and Runtime Read Repair Design

## Goal

Keep CPAM's bounded, server-paged credential inventory and CPA `include_proxy_url=1` projection, while removing the misleading coupling between a visible page and global health cards. Ordinary list navigation must not download raw credential JSON merely to show `proxy_url` or Token Acquisition task state.

## Confirmed Root Cause

The paged list replaces the browser's `files` collection with only the current page. `buildAccountMetrics` then derives health cards from that page, while the total/provider tabs are already global. A visible page containing newly imported credentials has no request, quota, or inspection proof yet, so it can display a large `unconfirmed` count even though CPA Core scheduling is independent and continues to dispatch credentials.

The existing `credential-runtime-metadata` path also downloads each visible source JSON in order to obtain `proxy_url`; CPA now returns `proxy_url` in the list projection, so that work is redundant for normal list rendering.

## Non-goals and Immutable Boundaries

- Do not modify, restart, build, or deploy CPA Core / `cli-proxy-api`.
- Do not alter model forwarding, credential selection, Token Acquisition behavior, usage events, billing, collector, quota accounting, or 502/503/qualified-429 priority demotion.
- Do not expose token, header, secret, or full raw credential JSON through the new list response.

## Design

### 1. One bounded list response owns ordinary page display

`GET /usage-service/credential-list` remains the only normal Accounts-list inventory request. It keeps requesting CPA with `page`, `page_size`, and `include_proxy_url=1`; `note` and `proxy_url` render from that same response.

The Manager Server will additionally attach only the redacted durable Token Recovery task for each visible Codex row, if one exists. The task is read from CPAM SQLite through the existing recovery service; it never requires CPA raw-file download. The browser retains existing task polling only while a visible task is queued/running.

Legacy fallback remains unchanged: if the paged Manager endpoint is unavailable, the older runtime-metadata behavior can still serve old Core installations.

### 2. Metric scope is explicit

The global inventory count and provider totals stay global. In server-paged mode, detailed health categories (`needsAttention`, quota risk, unconfirmed) are true only for the visible page because their exact evidence is currently joined locally from header snapshots, inspection results, quota observations, and request history.

The UI will explicitly mark those card labels as current-page values. When the active filter is `enabled` and the server snapshot is complete, the green card will display the full filtered enabled total and use an `Enabled` label rather than claiming that every enabled row is independently health-confirmed.

This removes the false inference that `unconfirmed` means unscheduled or disabled.

### 3. Performance behavior

- No normal page request invokes raw-file runtime metadata when Manager paging is active.
- Recovery state arrives with the already-requested 50-row list response.
- Pending recovery tasks continue to poll the existing small token-recovery batch API every two seconds.
- Proxy filter retains its safe server-side projection and never treats an incomplete projection as unused.

## Validation

- Go service test proves a Codex recovery task is embedded in a sanitized page response and no auth tokens leak.
- React hook test proves seeded list task data does not call `credential-runtime-metadata` but still polls a pending recovery task.
- Metric component test proves paged health labels are page-scoped and enabled-filter labels are semantically accurate.
- Run targeted Go/Vitest suites, frontend build, `git diff --check`, and verify no CPA Core files/configs are changed.
