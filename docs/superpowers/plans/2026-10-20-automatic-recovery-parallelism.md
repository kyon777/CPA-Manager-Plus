# Automatic Recovery Parallelism Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run automatic token recovery with all twelve configured workers and cap concurrent CPA auth-file write flows at eight.

**Architecture:** `tokenrecovery.Service.Start` will grant automatic claim eligibility to every configured worker rather than worker zero only. The existing atomic SQLite claim keeps a task single-owned and retains manual-task priority. `app.NewContext` will explicitly configure the existing write gate at eight, so acquisition remains twelve-way but final CPA file mutation is bounded.

**Tech Stack:** Go, SQLite durable task repository, Go `testing`, CPAM manager-server.

---

## File Structure

- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go` — allow every worker to claim automatic tasks and retain the runtime automatic-enable gate.
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go` — add automatic twelve-worker acquisition regression coverage and change the write-gate assertion from four to eight.
- Modify: `apps/manager-server/internal/app/context.go` — explicitly configure the production CPA auth-file write cap as eight.
- Modify: `apps/manager-server/internal/app/app_test.go` only if needed to assert production wiring; otherwise no unrelated test changes.

### Task 1: Prove automatic tasks can use the configured worker pool

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: Write the failing test**

Add a test adjacent to `TestServiceRunsManualRecoveriesWithConfiguredWorkerConcurrency` that queues twelve automatic targets before `Start`, uses `newBlockingRecoveryAcquirer(12)`, then requires all twelve acquisitions to enter concurrently:

```go
func TestServiceRunsAutomaticRecoveriesWithConfiguredWorkerConcurrency(t *testing.T) {
    const workerCount = RecommendedTokenAcquisitionWorkerCount
    // Create store, concurrent auth-file fake, service with WorkerCount workerCount.
    // Queue workerCount unique automatic tasks, start service.
    // waitForEntries(t, workerCount) and assert maxConcurrent() == workerCount.
    // Release and require each task to reach succeeded.
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/tokenrecovery -run '^TestServiceRunsAutomaticRecoveriesWithConfiguredWorkerConcurrency$' -count=1
```

Expected: fail because only worker zero claims automatic tasks and the acquirer never observes twelve entries.

- [ ] **Step 3: Implement the minimal worker claim change**

In `Service.Start`, invoke all workers as automatic-eligible:

```go
for worker := 0; worker < s.workerCount; worker++ {
    go s.run(ctx, true)
}
```

Update the adjacent comment to state that every worker can claim automatic work when the persisted automatic switch is enabled, while the repository maintains manual-task priority.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run the same command from Step 2.

Expected: PASS; the test observes exactly twelve concurrent automatic acquisitions.

- [ ] **Step 5: Commit**

```powershell
git add apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/tokenrecovery/service_test.go
git commit -m "feat: parallelize automatic token recovery"
```

### Task 2: Raise the production CPA write gate to eight

**Files:**
- Modify: `apps/manager-server/internal/app/context.go`
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: Write the failing write-gate test expectation**

Change `TestServiceLimitsConcurrentCPAAuthFileWritesWithoutLimitingAcquisition` so it waits for eight uploads and then asserts the ninth upload does not enter before a slot releases:

```go
files.waitForUploads(t, 8)
select {
case <-files.entered:
    t.Fatal("a ninth CPA auth-file write started before one of the first eight completed")
case <-time.After(120 * time.Millisecond):
}
```

Set `WriteConcurrency: 8` in that test's `Options` so the intended explicit production cap is tested.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```powershell
Set-Location apps/manager-server
go test ./internal/service/tokenrecovery -run '^TestServiceLimitsConcurrentCPAAuthFileWritesWithoutLimitingAcquisition$' -count=1
```

Expected: fail while the service default write gate remains four or production wiring has not been set explicitly.

- [ ] **Step 3: Implement production write-cap wiring**

In `app/context.go`, add this exact option in the live `tokenrecoverysvc.NewWithOptions` call:

```go
WriteConcurrency: 8,
```

No change is made to CPA Core, `cli-proxy-api`, task repository SQL, JSON merge behavior, or automatic task terminal-state rules.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run the command from Step 2.

Expected: PASS; eight writes enter, the ninth remains blocked, then all tasks finish after release.

- [ ] **Step 5: Commit**

```powershell
git add apps/manager-server/internal/app/context.go apps/manager-server/internal/service/tokenrecovery/service_test.go
git commit -m "perf: raise token recovery write concurrency to eight"
```

### Task 3: Regression verification and deployment preparation

**Files:**
- No source changes expected.

- [ ] **Step 1: Run service package tests**

```powershell
Set-Location apps/manager-server
go test ./internal/service/tokenrecovery -count=1
```

Expected: PASS.

- [ ] **Step 2: Run manager-server suite and build**

```powershell
go test ./... -count=1
go build ./cmd/cpa-manager-plus
```

Expected: both exit zero.

- [ ] **Step 3: Review diff and commits**

```powershell
Set-Location ..\..
git status --short
git log --oneline 0fda0726..HEAD
git diff 0fda0726..HEAD -- apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/tokenrecovery/service_test.go apps/manager-server/internal/app/context.go
```

Expected: only the approved CPAM concurrency changes plus the design/plan documentation.

- [ ] **Step 4: Deploy only after explicit user deployment instruction**

Use the CPAM deployment runbook: build candidate from this clean worktree; verify local CPA Core container identity before/after; rebuild only local `cpa-manager-plus`; deploy the same binary to `/home/cpam/app/cpa-manager-plus`; restart only `cpam.service`; preserve remote data/secrets/OpenResty; verify health and confirm `cpa.service` identity did not change.
