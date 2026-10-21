# Inspection Recovery Success Prune Design

## Goal

When an operator uses **更新当前页凭证** from the local Codex inspection
`重新登录` view, remove each account from that inspection result as soon as
the Manager's durable manual Token Recovery task reaches `succeeded`. This
prevents the next current-page batch from submitting a second Token
Acquisition request for an account whose new tokens have already been written.

## Observed Problem

- A local inspection result is an immutable-at-creation snapshot. Its
  `reauth` action remains attached to a row after manual recovery writes a
  fresh token.
- The existing page operation only blocks duplicate submits while tasks are
  queued or running. Once a task is terminal, the button is enabled again.
- `RequestManual` intentionally requeues a terminal manual task. Therefore a
  successful row that remains in the current page can be submitted again.
- Banned-account deletion already solves its analogous case by removing only
  Manager-confirmed deleted physical files from the current inspection result.

## Approved Behavior

1. A row is eligible for automatic pruning only when all of the following are
   true:
   - it belongs to the active local inspection result;
   - its matching durable recovery task is `mode=manual` and
     `status=succeeded`;
   - the task's `completedAtMs` is strictly later than the active inspection
     result's `finishedAt`.
2. Eligible rows are removed by their stable inspection account key, not by
   physical auth-file name. A multi-account JSON file therefore retains every
   other account row and the auth JSON itself is never deleted.
3. The in-memory `result` is updated immediately after the normal runtime
   metadata poll observes the durable success. The existing persistence effect
   then saves this reduced result as the latest local inspection snapshot, so
   reloads do not resurrect already-successfully-updated rows.
4. The `重新登录` filter count and pagination are derived from the reduced
   result. The current page reflows with not-yet-successful rows from later
   pages. A later update click targets only rows still present in that view.
5. Queued, running, failed, unavailable, automatic, and pre-inspection
   recovery tasks remain visible and retain the current retry/diagnostic
   behavior. In particular, a new inspection performed after a prior manual
   success is not hidden by that stale success record.

## Data Flow

```text
更新当前页凭证
  -> POST token-recovery/manual/batch
  -> durable task queued/running
  -> useCredentialRuntimeMetadata polls redacted task state
  -> manual + succeeded + completedAtMs > result.finishedAt
  -> remove exact inspection account key from result.results
  -> recompute result summary and persist latest local inspection snapshot
  -> filter count/page slice reflow; no second submission for removed row
```

## Implementation Boundaries

- Change only CPAM's local inspection presentation and its pure result helper.
  `ServerCodexInspectionPage` is out of scope because it does not expose this
  local current-page Token Recovery batch control.
- Do not issue a post-write quota, usage, or health probe. `succeeded` means
  the Manager completed the verified email-matched token merge and CPA write;
  a later explicit health inspection remains the authoritative usability
  check.
- Do not alter CPA Core, `cli-proxy-api`, token merge fields, account enable
  state, priority, billing, usage SQLite, or scheduler behavior.
- Keep the existing banned-account delete behavior unchanged. It removes
  physical files only after the separate delete API confirms deletion.

## Result Summary Semantics

- The source `files` snapshot remains intact for a successful recovery: the
  physical credential file still exists.
- Only the recovered account entry is removed from `results`; the summary is
  recomputed from remaining rows. Thus the action-filter `重新登录` total drops
  immediately while unrelated accounts in the same file remain represented.

## Verification

1. Pure helper tests prove exact-key removal, summary recomputation, and
   preservation of sibling accounts in the same physical JSON file.
2. Presentation tests prove only a newer manual `succeeded` task qualifies;
   automatic, failed, pending, and stale successful tasks do not qualify.
3. Lifecycle coverage proves a polled manual success removes the row, updates
   the persisted inspection snapshot, and leaves failed rows visible.
4. Existing current-page recovery and banned-delete tests remain green.
