# Inspection Recovery and Stale Delete Prune Design

## Goal

Keep the local Codex inspection `重新登录` result actionable and free of rows
that no longer exist in CPA:

1. When an operator uses **更新当前页凭证**, remove an account from that
   inspection result as soon as the Manager's durable manual Token Recovery
   task reaches `succeeded`. This prevents the next current-page batch from
   submitting a second Token Acquisition request for an account whose new
   tokens have already been written.
2. When **删除当前页已封禁账号** receives the terminal
   `credential_not_found` outcome, remove the missing physical credential
   source from the inspection result and the credential-list cache. This
   prevents a stale, already-absent account from remaining in `重新登录` and
   being selected again.

## Observed Problem

- A local inspection result is an immutable-at-creation snapshot. Its
  `reauth` action remains attached to a row after manual recovery writes a
  fresh token.
- The existing page operation only blocks duplicate submits while tasks are
  queued or running. Once a task is terminal, the button is enabled again.
  `RequestManual` intentionally requeues a terminal manual task, so a
  successful row that remains in the current page can be submitted again.
- Banned-account deletion currently prunes only `response.deleted`. A
  `response.skipped` item with `code=credential_not_found` remains in the
  saved inspection even though the Manager's fresh CPA inventory has already
  shown that its physical source is absent.

## Root-Cause Evidence for `credential_not_found`

The batch deletion service first locks the candidate source and fetches a
fresh CPA auth-file inventory. It then calls
`ResolveVerifiedPhysicalFileDeleteTargetFromInventory`. For a valid delete
target, its `ErrAuthFileNotFound` branch is reached only when that inventory
has zero members for the target physical file name. The service maps this to
the returned skipped item `credential_not_found`.

No `AuthFilesClient.Delete` job is constructed in that branch. Therefore the
current delete click did **not** delete that file; it established that the
file was already absent from CPA's current inventory (for example after an
earlier delete, replacement, or rename). It is safe to prune this stale
physical source from the local UI, but it must be reported as **already
absent**, never as a newly deleted credential.

## Approved Behavior

### A. Manual recovery success

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
3. The normal runtime-metadata poll updates the in-memory `result`; the
   existing persistence effect saves the reduced result as the latest local
   inspection snapshot. Reloading does not resurrect the completed account.

### B. Stale physical source reported during banned deletion

1. From the delete response, form an `absentPhysicalNames` set from only
   `response.skipped` items whose code is exactly `credential_not_found`.
   Use `physicalName` when present, otherwise the returned `fileName`.
2. Form a separate `confirmedRemovedPhysicalNames` union of:
   - `response.deleted` physical names — removed by this request; and
   - `absentPhysicalNames` — already absent before this request.
3. For every source in that union, remove the physical file from
   `currentAuthFiles`, all matching rows from `result.results`, and the
   matching item from `result.files`. Recompute the inspection summary and
   persist the reduced local snapshot. Because `credential_not_found` means
   the entire physical source has zero current CPA members, all stale sibling
   rows for that source are also removed safely.
4. Notify the parent credential-list cache with the same union so its stale
   file card disappears too. This is cache reconciliation, not a claim that
   this request deleted the `credential_not_found` file.
5. Keep `credential_not_found` separate in operation feedback as
   **CPA 已不存在**. It must not inflate the actual `已删除` count or be shown
   as a failed deletion. Other skipped and failed outcomes remain visible,
   are not pruned, and retain their existing diagnostic detail.

## Data Flow

```text
更新当前页凭证
  -> POST token-recovery/manual/batch
  -> durable task queued/running
  -> useCredentialRuntimeMetadata polls redacted task state
  -> manual + succeeded + completedAtMs > result.finishedAt
  -> remove exact inspection account key from result.results
  -> recompute/persist result; no second submission for removed row

删除当前页已封禁账号
  -> POST auth-files/delete-account-banned-batch
  -> Manager locks sources and fetches fresh CPA auth-file inventory
  -> response.deleted: deletion was confirmed
  -> response.skipped[code=credential_not_found]: source absent before delete
  -> union both physical source names for local-cache/result pruning
  -> report deleted and already-absent counts separately
```

## Implementation Boundaries

- Change only CPAM's local inspection presentation and pure result/operation
  helpers. `ServerCodexInspectionPage` is out of scope because it does not
  expose this local current-page Token Recovery batch control.
- Do not issue a post-write quota, usage, or health probe. `succeeded` means
  the Manager completed the verified email-matched token merge and CPA write;
  a later explicit health inspection remains the authoritative usability
  check.
- Do not alter CPA Core, `cli-proxy-api`, token merge fields, account enable
  state, priority, billing, usage SQLite, or scheduler behavior.
- Do not turn every skipped deletion into a prune. Only the backend's exact
  `credential_not_found` signal has the required fresh-inventory meaning.

## Result Summary Semantics

- A successful token recovery leaves the physical source in place and removes
  only that one recovered account result.
- A `credential_not_found` deletion outcome removes an absent physical source
  from both the local inspection snapshot and the credential-list cache. The
  source is not counted as newly deleted.
- Filter counts and pagination derive from the reduced result immediately.
  Current pages reflow with the next still-actionable rows, so subsequent
  batches cannot target pruned rows.

## Verification

1. Pure helper tests prove exact-key recovery pruning, summary recomputation,
   and preservation of sibling accounts in the same physical JSON file.
2. Presentation tests prove only a newer manual `succeeded` task qualifies;
   automatic, failed, pending, and stale successful tasks do not qualify.
3. Banned-delete helper tests prove that a returned
   `skipped/credential_not_found` source joins the physical prune set without
   increasing the confirmed-delete count; unrelated skipped/failed codes stay
   visible and are not pruned.
4. Lifecycle coverage proves a `credential_not_found` response removes the
   stale card(s), updates the persisted inspection snapshot and parent list
   cache, and renders an explicit already-absent count.
5. Existing current-page recovery and confirmed-delete tests remain green.
