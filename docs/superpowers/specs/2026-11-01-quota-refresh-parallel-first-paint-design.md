# Codex Quota Refresh Parallelism and First-Paint Protection

## Goal

Make the credential-list **Refresh quota** action useful for a 50-row Codex
page without allowing the related Manager analytics read to monopolize the
first paint or be launched repeatedly during the same manual batch.

## Observed cause

The toolbar already targets only the selected credentials or the current page,
but its provider scheduler has a fixed per-provider concurrency of `1`. A
50-row Codex page therefore performs fifty serial upstream summary requests.
At the same time, a successful batch increments the current-page
`account-window-usage` revision while the batch is still active. The
current-page analytics endpoint is a deliberately exact SQLite aggregation and
can take seconds under a busy usage database, competing with the visible list
for server CPU and browser rendering time.

The existing credential task plan already deduplicates by normalized
`provider + credential store key`; the batch promise already coalesces repeated
toolbar clicks for the same connection. Its 429 guard already marks a provider
blocked, but with serial Codex work it has no high-concurrency behavior to
prove.

## Chosen behavior

| Area | Contract |
| --- | --- |
| Codex batch quota refresh | Up to 40 distinct Codex credentials run concurrently. Other providers retain the existing serial per-provider limit. |
| Credential identity | A duplicate normalized `(provider, credential store key)` is scheduled once, even if it appears twice in a target list or the toolbar is clicked again. |
| HTTP 429 | Once an outcome for a provider reports 429/rate-limited, no further **queued** tasks for that provider start. Already in-flight requests are not cancelled or retried. |
| `account-window-usage` during batch | A batch may leave a previously running current-page request alone, but it must not start another one while the batch is active. When the batch ends, one normal current-page refresh consumes the resulting revision. |
| First credential-list paint | Existing list rows, cached quota state, buttons, and scrolling render before the first heavy current-page window-usage request is scheduled. The request is deferred through browser idle time with a bounded fallback. |

## Explicit non-goals

- Do not alter the per-account drawer request path or add a broad history/window
  single-flight rule. This preserves the operator's existing detail-display
  behavior.
- Do not change `account-history`, pricing, derived SQLite, billing, quota
  semantics, upstream timeout, CPA Core, `cli-proxy-api`, or credential bytes.
- Do not make the credential list fetch upstream quota automatically on entry;
  **Refresh quota** remains the explicit upstream action.

## Data and failure semantics

The code remains client scheduling only. It sends no new persistent writes and
does not change the Manager endpoint payload. A provider's first 429 can only
stop work that has not yet been dispatched; a network request already handed to
the browser cannot be safely recalled and must complete normally. Cached quota
and existing usage values remain visible during deferred/queued reads.

## Verification

Focused Vitest coverage must prove provider-specific concurrency, strict
deduplication, queued-work suppression after 429, and that the analytics
revision is consumed only after a batch completes. Run the focused scheduler
and AccountsPage tests, TypeScript checking, and the production web build.
