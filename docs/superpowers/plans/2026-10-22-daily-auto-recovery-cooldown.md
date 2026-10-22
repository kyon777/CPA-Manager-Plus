# Daily Automatic Recovery Cooldown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace CPAM's permanent one-shot Codex automatic recovery guard with a durable rolling 24-hour cooldown per credential identity.

**Architecture:** Reuse `token_recovery_tasks.auto_attempted_at_ms` as the timestamp of the most recently claimed automatic acquisition. The automatic signal gate suppresses qualifying 401 events during the cooldown before contacting CPA Core; the SQLite repository performs the same check transactionally and requeues only an expired terminal task. The existing redacted task projection drives a small UI distinction between active cooldown and historical auto-update.

**Tech Stack:** Go, SQLite, React, TypeScript, Vitest, i18next.

---

### Task 1: Durable 24-hour cooldown in the Token Recovery repository

**Files:**
- Modify: `apps/manager-server/internal/model/token_recovery.go:1-50`
- Modify: `apps/manager-server/internal/repository/tokenrecovery/repository.go:39-106,210-257,427-443`
- Modify: `apps/manager-server/internal/repository/tokenrecovery/repository_test.go:13-129,313-321`

- [ ] **Step 1: Write failing repository tests for immediate suppression, expiration requeue, failure cooldown, and legacy timestamp backfill.**

```go
func TestSignalAutomaticRequeuesOnlyAfterDailyCooldown(t *testing.T) {
    repo, db := newTestRepositoryWithDB(t)
    target := model.TokenRecoveryTarget{FileName: "codex.json", AuthIndex: "7", Provider: "codex"}
    first, _ := repo.SignalAutomatic(context.Background(), target)
    claimed, _, _ := repo.ClaimNextQueued(context.Background())
    _, _ = repo.Complete(context.Background(), claimed.ID)

    within, _ := repo.SignalAutomatic(context.Background(), target)
    if within.Status != model.TokenRecoveryStatusSucceeded { t.Fatal("within cooldown requeued") }

    _, _ = db.Exec(`update token_recovery_tasks set auto_attempted_at_ms = ? where id = ?`, time.Now().Add(-24*time.Hour-time.Millisecond).UnixMilli(), first.ID)
    queued, _ := repo.SignalAutomatic(context.Background(), target)
    if queued.Status != model.TokenRecoveryStatusAutoQueued || queued.AutoAttemptedAtMS != 0 { t.Fatal("expired cooldown did not queue a fresh auto cycle") }
}
```

- [ ] **Step 2: Run the focused test and verify it fails because terminal automatic tasks remain permanently blocked.**

Run: `go test ./internal/repository/tokenrecovery -run TestSignalAutomaticRequeuesOnlyAfterDailyCooldown -count=1`

Expected: `FAIL`, with the expired task still terminal rather than `auto_queued`.

- [ ] **Step 3: Add the shared, pure cooldown boundary helper to `model.TokenRecoveryTask`.**

```go
const AutomaticRecoveryCooldownMS int64 = 24 * 60 * 60 * 1000

func (task TokenRecoveryTask) AutomaticRecoveryCoolingDown(nowMS int64) bool {
    if task.AutoAttemptedAtMS <= 0 { return false }
    return nowMS <= task.AutoAttemptedAtMS || nowMS-task.AutoAttemptedAtMS < AutomaticRecoveryCooldownMS
}
```

Update the field comment to describe it as the latest automatic attempt time, preserved across manual work rather than immutable forever.

- [ ] **Step 4: Make `SignalAutomatic` transactionally backfill legacy terminal-auto timestamps, suppress unexpired attempts, and requeue only expired terminal work.**

```go
if isQueuedOrRunning(existing.Status) {
    // Keep exactly one in-flight task.
} else {
    attemptedAt := automaticAttemptedAt(existing, now)
    if attemptedAt > 0 && model.TokenRecoveryTask{AutoAttemptedAtMS: attemptedAt}.AutomaticRecoveryCoolingDown(now) {
        // Persist a missing legacy marker, then return unchanged.
    } else {
        // Set status=auto_queued, mode=auto, clear terminal error/timestamps,
        // and set auto_attempted_at_ms=0; ClaimNextEligible writes the new timestamp.
    }
}
```

Keep manual requests unchanged: they retain the latest automatic timestamp and do not alter the automatic cooldown.

- [ ] **Step 5: Run focused repository tests and commit.**

Run: `go test ./internal/repository/tokenrecovery -count=1`

Expected: all repository token-recovery tests pass.

```bash
git add apps/manager-server/internal/model/token_recovery.go apps/manager-server/internal/repository/tokenrecovery/repository.go apps/manager-server/internal/repository/tokenrecovery/repository_test.go
git commit -m "feat: rate limit automatic credential recovery daily"
```

### Task 2: Gate repeated 401 signals and show cooldown state accurately

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/automatic_signal.go:50-120`
- Modify: `apps/manager-server/internal/service/tokenrecovery/automatic_signal_test.go:1-90`
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts:222-248`
- Modify: `apps/web/src/features/accounts/model/credentialRuntimeMetadata.test.ts:238-242`
- Modify: `apps/web/src/features/accounts/AccountsPage.tsx:9890-9941`
- Modify: `apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts:203-224`
- Modify: `apps/web/src/i18n/locales/en.json`, `apps/web/src/i18n/locales/ru.json`, `apps/web/src/i18n/locales/zh-CN.json`, `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: Write failing gate and web-model tests for the 24-hour boundary.**

```go
{name: "auto attempt within daily cooldown", existing: model.TokenRecoveryTask{ID: 9, AutoAttemptedAtMS: time.Now().Add(-time.Hour).UnixMilli()}, found: true, wantSignals: 0, wantVerifies: 0},
{name: "auto attempt after daily cooldown", existing: model.TokenRecoveryTask{ID: 9, AutoAttemptedAtMS: time.Now().Add(-24*time.Hour-time.Millisecond).UnixMilli()}, found: true, verifyFile: enabledFile, wantSignals: 1, wantVerifies: 1},
```

```ts
expect(isAutomaticRecoveryCoolingDown({ autoAttemptedAtMs: now - 1 } as never, now)).toBe(true);
expect(isAutomaticRecoveryCoolingDown({ autoAttemptedAtMs: now - 24 * 60 * 60 * 1000 } as never, now)).toBe(false);
```

- [ ] **Step 2: Run focused tests and verify the after-24-hour gate case fails under the old permanent guard.**

Run: `go test ./internal/service/tokenrecovery -run TestAutomaticSignalGateOnlyQueuesEnabledCodex401Once -count=1 && npm --workspace apps/web run test -- src/features/accounts/model/credentialRuntimeMetadata.test.ts`

Expected: the Go test fails specifically for the expired auto attempt being suppressed.

- [ ] **Step 3: Replace the one-shot gate predicate with a current-time cooldown predicate.**

```go
func automaticSignalAlreadyResolved(task model.TokenRecoveryTask, nowMS int64) bool {
    if task.AutomaticRecoveryCoolingDown(nowMS) { return true }
    return isQueuedOrRunning(task.Status)
}
```

Use `time.Now().UnixMilli()` only when evaluating the gate. Legacy terminal auto tasks with no explicit marker must flow to the repository so it can backfill a durable timestamp.

- [ ] **Step 4: Add the client-only cooldown helper and update the status chip.**

```ts
export const automaticRecoveryCooldownMs = 24 * 60 * 60 * 1000;
export const isAutomaticRecoveryCoolingDown = (task: TokenRecoveryTask | null | undefined, nowMs = Date.now()): boolean => {
  const attemptedAtMs = task?.autoAttemptedAtMs ?? 0;
  return attemptedAtMs > 0 && (nowMs <= attemptedAtMs || nowMs - attemptedAtMs < automaticRecoveryCooldownMs);
};
```

Render `accounts.recovery_auto_cooling_down` while active and `accounts.recovery_auto_attempted` once the cooldown has expired. Use concise translations in all four locales; preserve error and queue/running presentations.

- [ ] **Step 5: Run focused Go/Web tests, type-check, and commit.**

Run: `go test ./internal/service/tokenrecovery -count=1 && npm --workspace apps/web run test -- src/features/accounts/model/credentialRuntimeMetadata.test.ts src/features/accounts/accountsWorkspaceWiring.test.ts && npm --workspace apps/web run type-check`

Expected: all commands exit 0.

```bash
git add apps/manager-server/internal/service/tokenrecovery/automatic_signal.go apps/manager-server/internal/service/tokenrecovery/automatic_signal_test.go apps/web/src/features/accounts/model/credentialRuntimeMetadata.ts apps/web/src/features/accounts/model/credentialRuntimeMetadata.test.ts apps/web/src/features/accounts/AccountsPage.tsx apps/web/src/features/accounts/accountsWorkspaceWiring.test.ts apps/web/src/i18n/locales
git commit -m "feat: display automatic recovery daily cooldown"
```

### Task 3: Preserve the custom behavior across future CPAM updates

**Files:**
- Modify: `docs/LO-CPAM-CUSTOM-MEMORY.md:75-85,370-381,486-492`
- Modify: `docs/superpowers/specs/2026-09-21-codex-401-one-shot-auto-recovery-design.md:1-46`

- [ ] **Step 1: Replace all permanent one-shot statements with the confirmed rolling 24-hour policy.**

Document: enabled Codex + new HTTP 401 only; 24 hours measured from the automatic worker claim; success and failure both cool down; manual retry does not reset or consume the cooldown; a new qualifying 401 is required after expiry; no billing, CPA Core, scheduler, quota, or priority policy changes.

- [ ] **Step 2: Verify no stale one-shot behavior remains in active custom-memory or source comments.**

Run: `rg -n "permanent one-shot|永久.*一次|strict one-shot|一次性自动尝试" apps/manager-server docs/LO-CPAM-CUSTOM-MEMORY.md`

Expected: no active implementation/docs claim permanent automatic blocking; historical design may explicitly state it was superseded.

- [ ] **Step 3: Run full scoped verification and commit.**

Run: `go test ./internal/repository/tokenrecovery ./internal/service/tokenrecovery && npm --workspace apps/web run test -- src/features/accounts/model/credentialRuntimeMetadata.test.ts src/features/accounts/accountsWorkspaceWiring.test.ts && npm --workspace apps/web run type-check`

Expected: all commands exit 0.

```bash
git add docs/LO-CPAM-CUSTOM-MEMORY.md docs/superpowers/specs/2026-09-21-codex-401-one-shot-auto-recovery-design.md
git commit -m "docs: record daily automatic recovery policy"
```
