# 72-Hour Proxy Enabled Count and Selected Credential Batch Deletion Design

## Goal

Make two bounded changes to the Credentials page without changing CPA Core:

1. Keep proxy-overload statistics scoped to the existing rolling 72-hour request window, but expose how many of those active accounts are currently enabled.
2. Replace the slow browser-side serial permanent-delete path for selected credentials with a Manager-server batch path that preserves all current destructive-delete safety checks and uses bounded eight-way physical-file deletion.

## Confirmed Product Decisions

### Proxy-overload statistics

- The existing definition of an active account remains unchanged: a current credential inventory item with request evidence in the last 72 hours.
- Disabled accounts remain part of active, overload, non-overload, missing-proxy, and success-rate calculations. This keeps historical request evidence comparable with existing results.
- A new subset counter identifies active accounts that are currently enabled according to the current CPA auth-file inventory (`disabled != true`; upstream `status/state` values `disabled` and `inactive` are also disabled).
- The modal displays the subset in both places that help operator decisions:
  - top summary: `活跃账号 N 个（已启用 M 个）`;
  - each observed proxy row: `N（已启用 M）` in the active-account column.
- A proxy row with `活跃账号 N（已启用 0）` therefore means it had 72-hour request evidence, but it currently has no enabled credential assigned to it.
- No proxy selection, copy, overload-rate, success-rate, saved-proxy, or sort semantics change.

### Selected permanent deletion

- The existing first confirmation and second irreversible-delete confirmation remain unchanged.
- Scope remains exactly the selected visible/current-page credentials; no background or filtered-out credentials are added.
- Independent physical source files delete with a maximum concurrency of eight.
- The server must verify the selection against one current CPA auth-file inventory snapshot before deletion. A shared source file is deleted only if all of its current members are selected and match the submitted identities.
- Deletions use the existing shared auth-file mutation coordinator, so recovery, upload, proxy changes, and deletion cannot race on the same physical file.
- The browser removes only physically confirmed deleted files. Failed or skipped rows remain visible and the existing partial-result notification behavior is retained.
- CPA Core / `cli-proxy-api` code, process lifecycle, and configuration are out of scope.

## Root-Cause Evidence

The current generic `useAuthFilesData().batchDelete` path builds safe source-file snapshots, then calls `deleteVerifiedAuthFileSnapshots`.

That helper uses a serial `for ... of` loop. For each selected physical file it:

1. calls `authFilesApi.list()` to read the full auth-file inventory;
2. re-verifies source membership and the selector;
3. issues one delete request; and
4. may make another inventory call for plugin-source fallback.

A current page with 50 distinct physical files can therefore cause up to 50 full inventory reads plus 50 serial deletions. This is why it is visibly much slower than the banned-account action.

`删除当前页已封禁账号` already uses a separate Manager endpoint, `POST /v0/management/auth-files/delete-account-banned-batch`. Its server service takes one inventory snapshot, acquires source-file locks, resolves physical deletion targets, and bounds independent deletes at eight workers. That endpoint has additional `account_banned` recovery-task eligibility rules, so it cannot be reused directly for ordinary selected permanent deletion.

## Architecture

### 1. Proxy-overload response extension

Extend the Manager response types:

```text
ProxyOverloadSummary.enabled_active_account_count
ProxyOverloadSummaryRow.enabled_active_account_count
```

The aggregator already fetches the CPA inventory exactly once through `FetchWithProxyURLs`. While it iterates the existing 72-hour active evidence, it will increment the summary and per-proxy enabled counters when `file.Disabled` is false.

This adds only O(1) work to each existing active-item aggregation. It adds no CPA request, no additional SQLite query, and no per-account API call.

The web API TypeScript types, demo response, Chinese/English/Taiwanese locale strings, and modal renderer are extended accordingly. Existing response consumers remain compatible because older Manager responses may omit the optional field; the renderer treats a missing field as zero until both sides are deployed.

### 2. Generic selected-delete Manager endpoint

Add a dedicated endpoint:

```text
POST /v0/management/auth-files/delete-selected-batch
```

The request contains at most 100 selected stable identity snapshots:

```text
physical_file_name
runtime_id
provider
auth_index
account_snapshot
account_id_snapshot
```

It never contains credential JSON, access tokens, raw auth contents, or a CPA management key.

The Manager service resolves its persisted CPA setup rather than trusting browser-supplied upstream connection details. Its execution order is:

1. validate, normalize, and group submitted identities by physical source file;
2. acquire the shared `cpaauthfiles.MutationCoordinator` locks for all target source files;
3. fetch the CPA inventory exactly once;
4. resolve each group with `ResolveVerifiedPhysicalFileDeleteTargetFromInventory`;
5. skip incomplete shared-file groups or changed identities without deleting them;
6. execute independent verified physical deletes in a bounded eight-worker pool;
7. preserve the existing plugin-virtual-to-physical-source fallback; and
8. invalidate the auth-file inventory once if at least one physical source was deleted.

The response contains `requested`, `deleted`, `skipped`, and `failed` entries, with each confirmed deletion carrying the physical source name used to remove rows locally.

The implementation is intentionally separate from `authfilesbanneddelete`: it shares the safe inventory/lock/verified-delete pattern but does **not** require a terminal `account_banned` recovery record and supports ordinary selected credentials across supported providers.

### 3. Browser transport selection

`useAuthFilesData` receives the optional Manager service request scope already available in `AccountsPage`.

- When Manager Server is available, batch deletion uses `delete-selected-batch`; it retains the same confirmation UI and applies only confirmed physical-file names after the response.
- When Manager Server is unavailable, the pre-existing serial safe path remains as a compatibility fallback rather than silently weakening deletion verification.

This makes the high-performance path available on CPAM deployments while preserving standalone behavior.

## Error Handling

- Invalid payload, more than 100 targets, unavailable Manager connection, missing CPA setup, inventory read failure, identity mismatch, incomplete shared source membership, and individual delete errors are reported as structured per-target outcomes.
- A request cancellation must not cancel an already completed mutation or cause browser-side removal without a confirmed response.
- The service must never widen a single virtual runtime deletion into a physical source deletion without full source-membership verification.
- Concurrent mutations on the same physical file wait behind the shared coordinator and retain current correctness over speed.

## Test Strategy

### Manager server

1. Proxy-overload tests:
   - mixed enabled and disabled active accounts aggregate correctly at summary and proxy-row level;
   - inactive/no-72-hour-evidence files do not contribute;
   - existing overload, non-overload, saved-list, cache, and sorting behavior remains unchanged.
2. Selected-delete service tests:
   - one inventory fetch serves a multi-file request;
   - no more than eight independent physical deletes run simultaneously;
   - every current shared-source member must be selected;
   - stale identity, missing credential, and source/runtime collision are skipped or failed without a delete;
   - plugin virtual conflict retries only the previously verified physical source;
   - successful deletion invalidates inventory once.
3. Controller/router tests:
   - panel authorization, method, strict payload validation, target cap, and result serialization.

### Web

1. Proxy-overload model/modal tests cover the new enabled count and preserve success-rate sorting based on all active accounts.
2. Selected-delete tests prove that Manager scope selects the new batch endpoint, only confirmed sources are removed locally, partial outcomes remain visible, and absent Manager scope uses the existing fallback.
3. Locale tests ensure all new strings render in supported locales.

### Verification

Run focused Go service/controller tests, focused web tests, web type-check and production build. Before deployment, verify that only CPAM artifacts changed; after deployment, verify the Manager endpoint, one-page selected deletion, and a proxy row with enabled count. Do not restart or modify CPA Core.

## Out of Scope

- Changing the 72-hour window or redefining active accounts as enabled-only.
- Retrospective mutation of stored request evidence.
- Changing proxy overload scoring, 502 classification, copy selection, or success-rate formula.
- Deleting unselected members of a shared physical credential file.
- Raising deletion concurrency above eight.
- Changes to CPA Core, `cli-proxy-api`, billing, pricing, derived databases, or token recovery behavior.
