# Account History Price Cache + Background Exact Repricing

## Context

The account list renders at most the current page of credentials (normally 50
targets). `POST /v0/management/monitoring/account-history` currently computes
request totals, token totals, recent requests, and exact prices in one
synchronous read. While `usage_pricing` is rebuilding or has incomplete
coverage, the exact reader can scan a large raw-event range for those targets,
which makes the first byte of a 50-account response slow.

## Goals

1. Render the most recent known exact price for visible accounts without waiting
   for a pricing rebuild.
2. Continue calculating the authoritative current price in a bounded background
   path and replace the cached value when complete.
3. Make the UI distinguish `fresh`, `stale_repricing`, and `unavailable` price
   states instead of showing an unexplained perpetual loading label.
4. Preserve the existing global recent-request index and current-page history
   semantics.
5. Keep billing correctness unchanged: raw `usage_events`, Credits, CPA Core,
   and the accounting source of truth are read-only from this feature.

## Non-goals

- No changes to CPA Core, `cli-proxy-api`, request routing, or usage ingestion.
- No destructive migration or rewrite of `usage.sqlite` historical events.
- No increase of synchronous account-history request concurrency as a substitute
  for removing the large raw scan.

## Design

### 1. Derived price snapshot

Add a read-optimized per-account price snapshot keyed by:

- stable account key;
- pricing structure revision;
- covered usage event ID.

The snapshot is derived data only. It may be rebuilt or discarded without
changing raw usage events or billing records. Existing completed rollup rows
remain the first source; the new cache stores the last successfully completed
read result for each account.

### 2. Fast account-history response

For the visible page, `account-history` will:

1. read request/recent-request aggregates from the existing compact paths;
2. read the latest available per-account price snapshot;
3. return immediately with the snapshot's `total_cost` when available;
4. include explicit price metadata:
   - `price_state`: `fresh`, `stale_repricing`, or `unavailable`;
   - `price_revision`;
   - `price_covered_event_id`;
   - `price_updated_at_ms`.

If the current pricing revision is incomplete, the response uses the last
completed snapshot and marks it `stale_repricing`. It must not replay the full
raw event history inside the request.

### 3. Background exact repricing

When a response detects stale or missing coverage, it enqueues account keys into
a deduplicated in-process/server-backed work queue. Workers process bounded
batches, reuse the existing exact pricing logic, and persist only derived
snapshot/rollup data. The queue is idempotent and keyed by
`account_key + pricing_revision + target_event_id`.

The worker must:

- cap concurrency to avoid SQLite writer starvation;
- coalesce repeated requests for the same account;
- expose progress/state for the UI;
- retry transient lock/busy errors with bounded backoff;
- never mutate `usage_events`, Credits, or CPA Core state.

### 4. Frontend behavior

- Render cached price immediately when `price_state` is `fresh` or
  `stale_repricing`.
- Show a small `price recalculation pending` indicator for `stale_repricing`.
- Poll only the visible page's price state at a low frequency and patch changed
  rows in place; do not reload the credential inventory or recent-request index.
- Keep the existing automatic-refresh switch and current-page scope.
- If no snapshot exists, show the existing placeholder plus `price calculating`, not a
  blocking page spinner.

### 5. Failure and consistency rules

- A failed background job leaves the previous snapshot intact and marks the
  account `stale_repricing`; it never replaces a known price with zero.
- A pricing revision change invalidates freshness, not the cached value.
- Once the target event ID and revision are fully covered, the worker atomically
  promotes the new snapshot to `fresh`.
- Account-history remains read-only with respect to accounting source data.

## Verification plan

1. Unit tests for cache key/revision/coverage transitions and stale fallback.
2. Service tests proving a rebuilding pricing state returns quickly from the
   snapshot path and enqueues repricing.
3. Concurrency tests proving duplicate visible-page requests coalesce.
4. Regression tests proving raw event totals, Credits, and CPA Core identifiers
   are unchanged.
5. Browser/API test proving the page renders cached prices before background
   completion and patches them after completion.
6. Performance probe with 50 visible accounts: compare TTFB and total latency
   before/after while pricing rebuild is active.

## Deployment constraints

- Build and deploy CPAM only.
- Preserve all existing CPAM customizations and server data/config/secrets.
- Do not touch, restart, or rebuild CPA Core / `cli-proxy-api`.
- Validate local Docker CPAM and native `/home/cpam` CPAM separately before
  claiming completion.
