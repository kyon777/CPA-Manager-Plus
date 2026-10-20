# Automatic Recovery Parallelism Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run automatic token recovery with all eighty configured workers and cap concurrent CPA auth-file write flows at eight.

**Architecture:** `tokenrecovery.Service.Start` grants automatic claim eligibility to every configured worker rather than worker zero only. The existing atomic SQLite claim keeps a task single-owned and retains manual-task priority. The service configures an eighty-worker acquisition pool and a hard eight-slot CPA auth-file write gate.

**Tech Stack:** Go, SQLite durable task repository, Go `testing`, CPAM manager-server.

---

## File Structure

- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go` — allow every worker to claim automatic tasks and retain the runtime automatic-enable gate.
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go` — add automatic eighty-worker acquisition regression coverage and change the write-gate assertion from four to eight.

### Task 1: Prove automatic tasks can use the configured worker pool

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service_test.go`

- [ ] **Step 1: Write the failing test**

Add a test adjacent to `TestServiceRunsManualRecoveriesWithConfiguredWorkerConcurrency` that queues eighty automatic targets before `Start`, uses `newBlockingRecoveryAcquirer(80)`, then requires all eighty acquisitions to enter concurrently:

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

Expected: fail because the pre-change worker ceiling is forty and the acquirer cannot observe eighty entries.

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

Expected: PASS; the test observes exactly eighty concurrent automatic acquisitions.

- [ ] **Step 5: Commit**

```powershell
git add apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/tokenrecovery/service_test.go
git commit -m "feat: parallelize automatic token recovery"
```

### Task 2: Raise the production CPA write gate to eight

**Files:**
- Modify: `apps/manager-server/internal/service/tokenrecovery/service.go`
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

- [ ] **Step 3: Implement the hard write-cap**

In `internal/service/tokenrecovery/service.go`, make the write cap independent from the acquisition worker cap:

```go
maxWorkerCount          = 80
defaultWriteConcurrency = 8
maxWriteConcurrency     = defaultWriteConcurrency
```

No change is made to CPA Core, `cli-proxy-api`, task repository SQL, JSON merge behavior, or automatic task terminal-state rules. The production context continues to use the service default, which is now a hard maximum of eight write flows.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run the command from Step 2.

Expected: PASS; eight writes enter, the ninth remains blocked, then all tasks finish after release.

- [ ] **Step 5: Commit**

```powershell
git add apps/manager-server/internal/service/tokenrecovery/service.go apps/manager-server/internal/service/tokenrecovery/service_test.go
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
