# Inspection Page Operation State and EOF Priority Design

## Goal

Make the local Codex inspection page show truthful, durable progress for its
current-page recovery and account-banned deletion actions, prevent accidental
duplicate submission while work is still active, and add one deliberately
narrow HTTP 500 priority-demotion signature.

The change remains entirely inside CPA Manager Plus (CPAM). It does not change
CPA Core scheduling, quota collection, billing, auth-file formats, or raw
token exposure.

## Confirmed Evidence

### Recovery action

`CodexInspectionPage.handleRecoverCurrentPage` currently sets
`pageRecoverySubmitting` only while
`POST /v0/management/token-recovery/manual/batch` is in flight. That endpoint
returns as soon as Manager Server has created/returned durable background
recovery tasks. The page clears the flag immediately afterward, even though
the task is still `queued` or `running`.

The existing `useCredentialRuntimeMetadata` hook already polls those redacted
task records every two seconds. Its result is used to derive deletion
eligibility, but is not currently used to control the inspection-page action
button or to render a user-visible batch summary.

Live container evidence confirms this boundary: the recent manual batch
submission returned HTTP 200 in about 320 ms, followed by task-query polling.

### Banned deletion action

The deletion service is synchronous and returns a split result:

- `deleted`: verified physical CPA auth files removed;
- `skipped`: no destructive mutation because verification was insufficient;
- `failed`: CPA/delete operation failed.

The UI removes rows only in `deleted`, which is correct. However, it currently
only presents a transient notification, and it has no operation state while
the two confirmation dialogs are open. Recent CPAM logs contain no call to
`POST /v0/management/auth-files/delete-account-banned-batch`; therefore the
observed latest delete attempt did not reach the deletion service. It is not
evidence of the service's four-worker limit or mutation-lock contention.

The service intentionally limits physical-file delete jobs to four concurrent
workers and takes a verified CPA inventory snapshot under mutation locks. That
safety behavior remains unchanged.

### Priority demotion

The worker currently accepts only:

1. HTTP 502;
2. HTTP 503;
3. HTTP 429 whose raw local `fail_body` parses as exactly
   `{"detail":"Rate limit exceeded"}`.

`usage.NormalizeRaw` copies `fail.body` / `fail_body` into local-only
`usage.Event.FailBody`, so the required EOF evidence is available to the
priority worker without expanding any public API. Existing tests explicitly
reject HTTP 500, so a regression test must change before implementation.

## Product Decisions

### Current-page recovery operation state

- Clicking `Update current-page credentials (N)` immediately creates a
  page-scoped operation record before the first network request. The button is
  disabled while confirmation/submission is active.
- Once the server returns durable task rows, the operation remains active as
  long as any submitted page target is `queued` or `running`.
- The panel renders an adjacent compact status line, for example:
  `Credential update: queued 4 · running 12 · succeeded 76 · failed 8`.
- The button stays disabled while queued/running work exists, including after a
  browser reload: the status is rebuilt from the existing redacted recovery
  task query for visible page rows.
- When all tracked tasks are terminal, the panel retains a final success/fail
  summary and compact, redacted failure reasons. The operator may explicitly
  retry only after the run is terminal.
- No quota endpoint, history endpoint, usage endpoint, inspection run, or CPA
  scheduler call is triggered by this UI state change. The only polling remains
  the existing Manager Server redacted task query.

### Current-page account-banned deletion state

- Keep both existing destructive confirmations.
- From the first click through cancel, second confirmation, and server result,
  store an explicit phase: `awaiting_first_confirmation`,
  `awaiting_second_confirmation`, or `deleting`.
- Disable the delete button in all non-terminal phases and render the matching
  label/state so the page cannot silently launch duplicate confirmation paths.
- After the deletion response, retain a visible result summary:
  `deleted X · skipped Y · failed Z`, plus an expandable redacted detail list
  containing file name and the server's existing code/message.
- Remove only physical files reported in `deleted`; skipped/failed rows remain
  visible by design and their reason explains why.
- Do not alter `MaxConcurrency=4`, the verified inventory snapshot, or
  mutation locking in this change. The new visible result lets later evidence
  distinguish a true CPA-side latency issue from a skipped safety preflight.

### Narrow HTTP 500 EOF demotion

An event is eligible in addition to the existing rules only when all are true:

1. `FailStatusCode == 500`;
2. its local-only `FailBody` contains the normalized exact request marker
   `post "https://chatgpt.com/backend-api/codex/responses":`;
3. that same body contains `unexpected eof`, case-insensitively.

Generic HTTP 500 events, EOF events for a different endpoint, and records
missing either marker remain ineligible. The resulting candidate follows the
existing identity verification, peer-priority rebalance, zero floor, cooldown,
and mutation coordinator path unchanged.

## Implementation Boundaries

### Web

- Extend `CodexInspectionPage.tsx` with typed page-operation state derived from
  the already-present recovery metadata map and batch response.
- Extend `CodexInspectionResultsPanel.tsx` with operation labels and a compact
  persistent result/status display. Keep the actions scoped to the existing
  `reauth` filter and visible result page.
- Reuse `useCredentialRuntimeMetadata`; do not add raw credential metadata
  downloads or a second polling channel.

### Manager Server

- Modify only
  `internal/worker/server_error_priority_demotion.go` to recognize the narrow
  500/EOF event predicate.
- Do not modify CPA Core, `cli-proxy-api`, quota APIs, billing aggregation,
  auth-file mutation routes, or the banned-delete service concurrency.

## Test Strategy

1. Add a focused web regression test proving recovery submission keeps the
   current-page button disabled after the batch POST resolves while its task is
   queued/running, and proves task-terminal updates expose final summary state.
2. Add a focused web regression test proving delete is disabled across both
   confirmations, displays the returned split outcome, and removes only the
   reported `deleted` physical names.
3. Add worker tests that accept the exact HTTP 500 Codex responses EOF body and
   reject generic 500, EOF on another URL, and a matching URL without EOF.
4. Run focused Vitest suites, focused Go worker tests, web type-check and web
   production build. Keep the known Windows-only Unix signal/permission suite
   constraints separate from test assertions.

## Out of Scope

- No automatic retry or cancellation of a submitted manual recovery task.
- No broad HTTP 500 demotion.
- No change to delete concurrency or destructive eligibility rules.
- No quota refresh or account re-inspection after clicking either action.
- No CPA Core update, restart, build, configuration change, or deployment.
