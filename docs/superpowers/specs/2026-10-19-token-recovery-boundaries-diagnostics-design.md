# Token Recovery Boundaries and Diagnostics Design

**Date:** 2026-10-19  
**Scope:** CPA Manager Plus (CPAM) only

## Problem

A current-page/manual recovery burst can queue forty recovery tasks immediately.  The
live Token Acquisition phase also starts forty independent outbound flows at once,
although the acquisition service itself documents a lower Node/VM concurrency target.
When one of those outbound requests fails before Token Acquisition returns its
structured result, CPAM persists the ambiguous `recovery_failed` code with no useful
redacted reason.  CPA auth-file failures other than a context deadline have the same
loss of stage information.

## Decisions

1. Keep the browser/operator experience unchanged: a current page may enqueue up to
   forty manual recovery tasks immediately.
2. Change the live CPAM Token Recovery worker pool from forty to twelve.  This caps
   active external Token Acquisition flows at twelve while tasks remain durable in the
   existing SQLite queue.  The existing CPA auth-file mutation gate remains four.
3. Add a typed `tokenacquisition.ErrTransportFailed` category for request transport
   errors (DNS/TLS/socket/reset/connection failures).  Context deadlines remain
   `ErrTimedOut`; structured HTTP/API responses retain their existing categories.
4. Add a typed, redacted CPA auth-file operation error containing only operation and
   optional HTTP status.  It must unwrap the existing cause so callers retain
   `errors.Is` behavior, and it must never include response bodies, management keys,
   token values, or proxy credentials.
5. At the recovery boundary, map auth-file failures by phase:
   `initial_read`/`write_read` -> `cpa_auth_file_read_failed`, `upload` ->
   `cpa_auth_file_upload_failed`, and `verify` -> `cpa_auth_file_verify_failed`.
   Existing `credential_not_found`, timeout, ambiguity, and validation mappings keep
   their more-specific current meanings.
6. Persist only safe, bounded operator text.  New tasks must never fall through to a
   blank generic reason for known transport or auth-file operations.  Historical rows
   cannot be retroactively diagnosed because their raw errors were deliberately not
   stored; an operator can manually retry them after this deployment.
7. Emit a compact CPAM server log record with task ID and safe failure code/message
   only.  Do not log raw error strings.

## Non-goals

- No CPA Core / `cli-proxy-api` changes, rebuilds, restarts, or configuration edits.
- No automatic retry of failed manual/automatic recovery tasks.
- No change to the automatic one-shot recovery rule.
- No quota, history, billing, scheduler, or priority-demotion request.
- No exposure of token, API-key, management-key, or proxy password data.

## Verification

Targeted Go tests prove transport categorization, redaction, phase categorization,
existing timeout behavior, and twelve-worker live wiring.  Then run the relevant
manager-server package tests, formatting, `go vet`, frontend build/regression tests if
source wiring changes, and production health checks after CPAM-only deployment.