# Automatic Recovery Parallelism Design

## Goal

Increase CPAM automatic Codex credential recovery from one serial worker to twelve concurrent workers, while limiting CPA Core auth-file mutation traffic to eight concurrent write flows.

## Scope

- Modify only CPA Manager Plus.
- Do not modify, restart, build, or deploy CPA Core / `cli-proxy-api`.
- Preserve durable one-shot automatic recovery semantics, manual recovery behavior, file-level mutation locks, and terminal failure behavior.

## Current Behavior

The live app configures twelve token-recovery workers, but `Service.Start` gives automatic-task claim permission only to worker zero. Automatic recovery is therefore serial even though manual current-page recovery can use all twelve workers. CPA auth-file write concurrency defaults to four because the live app does not override it.

## Approved Design

1. All twelve workers are eligible to claim automatic tasks when automatic update is enabled.
2. The durable repository claim remains the authority for atomic claims; each task is still claimed once only.
3. Manual tasks retain queue priority because `ClaimNextEligible` selects queued manual tasks before automatic tasks.
4. A per-file mutation coordinator remains in place across initial read and final merge/upload/verify, preventing two workers from changing one physical credential file concurrently.
5. The live app explicitly sets `WriteConcurrency: 8`. Token acquisition may run at up to twelve concurrent tasks; only the final CPA file mutation phase is bounded to eight.
6. The automatic one-shot marker remains unchanged: completed or automatic-failed tasks are not re-enqueued automatically; manual retry remains available.

## Validation

- Add a test proving twelve queued automatic recoveries reach twelve concurrent acquisition calls.
- Update the existing write-gate test to prove eight concurrent CPA auth-file write flows are admitted and a ninth is blocked.
- Run package tests and the manager-server test suite/build before deployment.

## Risks and Boundaries

The change increases outbound Token Acquisition and CPA Core auth-file read traffic during a large 401 burst. The final mutation/upload/verification phase is capped at eight, and all file-specific locks remain intact. It does not change CPA request scheduling, usage accounting, billing, authentication JSON merge fields, or priority rules.
