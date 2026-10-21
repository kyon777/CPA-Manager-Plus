# Token Acquisition Gate and Inspection Delete Design

## Goal

Eliminate the unintended thirty-second Token Acquisition cutoff observed in
manual recovery, align CPAM's external acquisition load with Token
Acquisition's forty-job capacity, and make the inspection-page banned-account
delete flow visibly and deterministically require both destructive
confirmations before a deletion request is sent.

## Evidence

- On the native CPAM server, thirty-eight manual recovery tasks started within
  two seconds and all failed after 30.086--30.106 seconds with durable code
  `token_acquisition_timeout`.
- `tokenacquisition.New` assigns a default `http.Client{Timeout: 30s}` even
  though `Acquire` already owns a fourteen-minute-thirty-second context
  budget. The client timeout wins first, so these tasks do not reach CPA
  auth-file writes.
- Token Acquisition declares `MAX_CONCURRENCY = 40`; CPAM currently starts up
  to eighty independent acquisition lifecycles.
- The credential-list (`AccountsPage`) deletion flow opens a second dialog
  from the first dialog's `onConfirm`. The generic confirmation modal marks
  itself loading before invoking that callback; `showConfirmation` therefore
  rejects the replacement dialog, then the modal hides the first dialog. This
  exactly produces “first confirmation accepted, no permanent confirmation,
  no deletion request”. The inspection page already uses the generic dialog's
  supported `secondConfirmation` field and is not the broken caller.

## Approved Behavior

1. CPAM retains an eighty-worker recovery pool and eight concurrent CPA
   auth-file write flows.
2. A separate, context-aware gate permits at most forty active calls to
   `Acquirer.Acquire`; queued workers remain durable recovery tasks and do not
   write CPA files until their own acquisition succeeds.
3. The default Token Acquisition HTTP client has no independent whole-request
   deadline. The existing `Acquire` context is the sole default fourteen-
   minute-thirty-second deadline. Explicit caller-supplied HTTP clients remain
   unchanged.
4. `token_acquisition_timeout` has a short, actionable Chinese task message;
   it remains redacted and reveals no proxy or token data.
5. Credential-list banned deletion passes both warnings in one confirmation
   object using the supported `secondConfirmation` field: first warning ->
   permanent-delete warning -> request. The generic modal advances to its
   second step before it enters loading state; cancellation at either warning
   sends no request.

## Boundaries

- CPAM only. No CPA Core / `cli-proxy-api` code, service, build, restart, or
  configuration is changed.
- No accounting, pricing, scheduler, quota, auth JSON merge field, or priority
  policy changes.
- CPA writes stay capped at eight; the external gate does not lower the
  eighty-worker recovery queue capacity.

## Verification

- Regression tests prove the default client no longer has a 30-second cap,
  forty active acquisitions are admitted while an eighty-worker pool is live,
  eight writes remain bounded, and timeout wording is actionable.
- An account-list regression test proves the handler gives the generic modal a
  `secondConfirmation`, emits no nested replacement confirmation, and calls
  the API only through its final `onConfirm` callback. The existing generic
  confirmation modal test proves that the first step advances without calling
  this final callback.
