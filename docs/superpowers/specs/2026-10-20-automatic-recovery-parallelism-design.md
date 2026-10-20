# Automatic Recovery Parallelism Design

## Goal

Increase CPAM automatic Codex credential recovery from one serial worker to eighty concurrent workers, while limiting CPA Core auth-file mutation traffic to eight concurrent write flows.

## Scope

- Modify only CPA Manager Plus.
- Do not modify, restart, build, or deploy CPA Core / `cli-proxy-api`.
- Preserve durable one-shot automatic recovery semantics, manual recovery behavior, file-level mutation locks, and terminal failure behavior.

## Current Behavior

Before this change, the live app configured twelve token-recovery workers, but `Service.Start` gave automatic-task claim permission only to worker zero. Automatic recovery was therefore serial even though manual current-page recovery could use all twelve workers. CPA auth-file write concurrency defaulted to four.

## Approved Design

1. All eighty workers are eligible to claim automatic tasks when automatic update is enabled.
2. The durable repository claim remains the authority for atomic claims; each task is still claimed once only.
3. Manual tasks retain queue priority because `ClaimNextEligible` selects queued manual tasks before automatic tasks.
4. A per-file mutation coordinator remains in place across initial read and final merge/upload/verify, preventing two workers from changing one physical credential file concurrently.
5. The live app configures an eighty-worker Token Acquisition pool. Token acquisition may run at up to eighty concurrent tasks; the final CPA file mutation phase is hard-capped at eight.
6. The automatic one-shot marker remains unchanged: completed or automatic-failed tasks are not re-enqueued automatically; manual retry remains available.

## Validation

- Add a test proving eighty queued automatic recoveries reach eighty concurrent acquisition calls.
- Update the existing write-gate test to prove eight concurrent CPA auth-file write flows are admitted and a ninth is blocked.
- Run package tests and the manager-server test suite/build before deployment.

## Risks and Boundaries

The change increases outbound Token Acquisition and CPA Core auth-file read traffic during a large 401 burst. The final mutation/upload/verification phase is capped at eight, and all file-specific locks remain intact. It does not change CPA request scheduling, usage accounting, billing, authentication JSON merge fields, or priority rules.
