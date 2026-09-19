# Account-Banned Current-Page Deletion and Localization Design

## Goal

Repair the account-list strings that render as literal question marks, restore the proxy URL label, and add a guarded action that deletes only current-page credentials whose terminal token-recovery error code is `account_banned`.

## Confirmed Product Decisions

- The action is a visible danger button in the Accounts page batch bar: `Delete current-page banned credentials (N)`.
- `N` is computed from the currently visible paginated `pageRows`, after all existing filters and sorting have been applied.
- The button is disabled when no eligible row exists, so the scope is always legible without making a destructive action available for an empty set.
- A credential is eligible only when all of the following are true:
  - it is a non-runtime-only Codex credential;
  - Manager runtime metadata contains a terminal failed recovery task for that row;
  - the task error code, trimmed and compared case-insensitively, is exactly `account_banned`.
- The Chinese message is display evidence only. Selection must use the stable error code, never the translated or upstream human-readable error text.

## Existing Safe Deletion Path

The new button will not invent a separate deletion API. It will pass the eligible rows' existing `AuthFileItem` values to the established `useAuthFilesData().batchDelete` path.

That path already provides the required safety boundaries:

1. a first confirmation dialog with an explicit current-page banned count and affected file preview;
2. its existing second confirmation for destructive batch deletion;
3. current-runtime identity snapshots and CPA-side verified selectors;
4. rejection of partial membership selection for shared physical source files;
5. reload and visible notification after completion.

This means the button does not delete a row merely because stale browser metadata says it is banned. It remains subject to the same server-side preflight and identity protections as ordinary account deletion.

## Localization Repair

The issue is persisted source content, not browser decoding: the newly added `accounts` recovery strings in `zh-CN.json`, `zh-TW.json`, and `ru.json` literally contain question-mark characters.

The change will restore the eight recovery and batch-update messages in those locale files using the existing English keys as the semantic source. English remains unchanged.

Both grid and table identity renderers currently call a nonexistent `auth_files.proxy_url` key. They will use the existing `auth_files.proxy_url_label` key instead, so a rendered proxy reads `Proxy URL: ...` / `代理 URL: ...` rather than the unresolved translation key.

## UI and Data Flow

1. The Accounts page receives page-scoped runtime metadata from Manager Server.
2. A pure account model helper derives eligible banned rows from `pageRows` plus the metadata map.
3. The batch bar renders both the existing update button and the new red delete button, each with its own count and disabled state.
4. On click, the page supplies the derived `row.raw` values to `batchDelete` with localized title, body, and confirm labels.
5. Existing deletion code performs confirmation, verification, CPA mutation, refresh, and error reporting.

No raw tokens, management keys, or recovery payloads are added to the browser/API surface.

## Test Strategy

1. Add model tests for exact `account_banned` selection, case normalization, current-page-only scope, terminal-failure requirement, runtime-only exclusion, and non-Codex exclusion.
2. Add Accounts page tests proving the button count is page-scoped, the disabled state is correct at zero candidates, and clicking passes only eligible rows to the established batch-delete function.
3. Update proxy label render assertions for grid/table rendering to expect `auth_files.proxy_url_label`.
4. Add locale regression assertions that the repaired locale keys contain no literal question marks and retain required interpolation variables such as `{{count}}` and `{{reason}}`.
5. Run focused Accounts/model tests, the full web test suite under `TZ=Asia/Shanghai`, type-check, and production build before merge/deployment.

## Out of Scope

- No deletion of rows outside the current page.
- No matching on the Chinese error description.
- No new Manager Server deletion endpoint.
- No changes to CPA Core deletion behavior or shared-source safety rules.
