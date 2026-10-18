# Proxy recheck reconciliation and recovery concurrency plan

Date: 2026-10-18

## Scope

CPAM Manager Server and Web only. CPA Core, cli-proxy-api, usage/billing tables,
and scheduling remain untouched.

## Behavior

1. A proxy-replacement item which writes successfully and whose directed recheck
   succeeds clears only the matching inspection result's `proxy_suspect` flag.
   A failed recheck remains proxy-suspect. This applies to browser-local results
   and to the exact persisted server-inspection run/result pair.
2. Task totals distinguish write failure from recheck failure. A recheck failure
   makes the task partial but is not counted as a write failure.
3. The local inspection UI reconciles every completed task item, including
   tasks larger than the 50-item modal page, so the proxy-abnormal tab/count
   immediately reflects successes without launching a new inspection or quota
   refresh.
4. Manual token recovery has 40 bounded workers in the live app. The file
   mutation coordinator still serializes writes to the same physical auth file;
   unrelated files can acquire tokens concurrently. Startup cleanup runs once.

## Tests first

- proxy task repository: recheck failure does not increment `failed_count` but
  task is partial.
- proxy service/repository: successful recheck clears only the exact server run
  result; a failed recheck does not clear it.
- web helper: only `recheck_succeeded` removes `proxySuspect`.
- recovery service: 40 distinct manual recovery tasks enter the external
  acquisition stage concurrently; no more than configured worker count.

## Verification

Run focused Go tests, relevant Web tests/type-check/build, `git diff --check`.
Do not deploy or push until separately requested.
