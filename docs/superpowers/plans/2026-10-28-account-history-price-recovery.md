# Account History Price Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore exact per-credential `total_cost` values in the credential list while `pricing_v1` is rebuilding, without changing CPA Core or writing billing data.

**Architecture:** Revert only the account-history read degradation introduced by `9382c610`. The Store will again use the pre-existing exact account pricing reader, which merges current-revision rollups with the compatible retained monitoring projection and post-watermark raw event tail. Existing coverage comparison remains the authority: only accounts whose reconstructed pricing rows exactly match their core history rows receive a cost.

**Tech Stack:** Go, SQLite, existing `usagepricing` projection reader, Manager Server monitoring service, Go tests.

---

## File structure

- Modify: `apps/manager-server/internal/service/monitoring/pricing_coverage_test.go` — black-box regression proving a rebuilding price rollup still returns exact account costs without writes.
- Modify: `apps/manager-server/internal/store/store.go` — restore the pre-degradation exact read path for account-history pricing snapshots.
- Existing, unchanged: `apps/manager-server/internal/repository/usagepricing/repository.go` — supplies `LoadAccountRowsTx` and the retained-projection recovery implementation.
- Existing, unchanged: `apps/web/src/features/accounts/AccountsPage.tsx` — already renders a non-null `total_cost`; no UI behavior change is required.

### Task 1: Add the failing rebuild-price regression test

**Files:**
- Modify: `apps/manager-server/internal/service/monitoring/pricing_coverage_test.go` after `TestAccountHistoryRecoversIncompleteArchivedPricing`
- Test: `apps/manager-server/internal/service/monitoring/pricing_coverage_test.go`

- [ ] **Step 1: Write the failing test for an account-history request during `pricing_v1=rebuilding`**

Add this test. It deliberately removes the current-revision account rollups and marks pricing as rebuilding, while keeping the compatible retained projection produced by `pricingCoverageFixture`.

```go
func TestAccountHistoryRecoversExactPricingDuringRebuild(t *testing.T) {
	db, sqlDB, _, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	request := pricingCoverageAccountRequest()

	baseline, err := New(db).AccountHistory(ctx, request)
	if err != nil || len(baseline.Items) != 2 {
		t.Fatalf("baseline account history = %#v, %v", baseline, err)
	}
	for _, item := range baseline.Items {
		if item.TotalCost == nil {
			t.Fatalf("baseline cost unavailable: %#v", baseline.Items)
		}
	}

	if _, err := sqlDB.ExecContext(ctx, `
		delete from usage_pricing_account_rollups_v1;
		update usage_pricing_rollup_state
		set status = 'rebuilding', coverage_event_id = 0, backfill_last_event_id = 0
		where rollup_name = 'pricing_v1'`); err != nil {
		t.Fatal(err)
	}
	before := pricingCoverageCounts(t, sqlDB)

	got, err := New(db).AccountHistory(ctx, request)
	if err != nil || len(got.Items) != len(baseline.Items) {
		t.Fatalf("rebuilding account history = %#v, %v", got, err)
	}
	for index := range got.Items {
		if got.Items[index].TotalCost == nil {
			t.Fatalf("rebuilding cost unavailable for %q: %#v", got.Items[index].RowKey, got.Items[index])
		}
		if *got.Items[index].TotalCost != *baseline.Items[index].TotalCost {
			t.Fatalf("rebuilding cost for %q = %v, want %v", got.Items[index].RowKey, *got.Items[index].TotalCost, *baseline.Items[index].TotalCost)
		}
	}
	if after := pricingCoverageCounts(t, sqlDB); after != before {
		t.Fatalf("rebuilding history read modified persisted usage data: before=%v after=%v", before, after)
	}
}
```

- [ ] **Step 2: Run the test to prove the present regression**

Run:

```powershell
go test ./internal/service/monitoring -run '^TestAccountHistoryRecoversExactPricingDuringRebuild$' -count=1
```

Expected: FAIL because `TotalCost` is `nil` while `pricing_v1` is `rebuilding`.

- [ ] **Step 3: Commit the red test separately**

```powershell
git add apps/manager-server/internal/service/monitoring/pricing_coverage_test.go
git commit -m "test: cover account pricing during rebuild"
```

### Task 2: Restore exact account pricing recovery

**Files:**
- Modify: `apps/manager-server/internal/store/store.go:814-899`
- Test: `apps/manager-server/internal/service/monitoring/pricing_coverage_test.go`

- [ ] **Step 1: Replace the degraded reader with the existing exact reader**

In `LoadUsagePricingAccountSnapshot`, replace:

```go
rows, state, available, err := s.UsagePricing.LoadAccountRowsForHistoryTx(ctx, tx, accountKeys)
```

with:

```go
rows, state, available, err := s.UsagePricing.LoadAccountRowsTx(ctx, tx, accountKeys)
```

This reader keeps current revision rows, merges the post-coverage tail, and lets the existing coverage check identify the accounts that need retained-projection recovery.

- [ ] **Step 2: Restore recovery for incomplete accounts regardless of rebuild state**

Replace:

```go
if len(incomplete) > 0 && pricingHistoryRecoveryAllowed(state) {
```

with:

```go
if len(incomplete) > 0 {
```

Then delete the now-unused `pricingHistoryRecoveryAllowed` function. `LoadAccountRowsFromEventsTx` remains the only recovery mechanism and validates compatible projection coverage before returning rows; incomplete accounts remain fail-closed through the existing `stillIncomplete` filtering.

- [ ] **Step 3: Run the focused regression test to verify green**

Run:

```powershell
go test ./internal/service/monitoring -run '^TestAccountHistoryRecoversExactPricingDuringRebuild$' -count=1
```

Expected: PASS. The exact baseline cost is present during rebuild, and row counts are unchanged after the read.

- [ ] **Step 4: Run the surrounding pricing and Store tests**

Run:

```powershell
go test ./internal/service/monitoring ./internal/store ./internal/repository/usagepricing -count=1 -timeout=10m
```

Expected: PASS with no data-mutation or coverage failures.

- [ ] **Step 5: Commit the minimal behavior restoration**

```powershell
git add apps/manager-server/internal/store/store.go apps/manager-server/internal/service/monitoring/pricing_coverage_test.go
git commit -m "fix: restore account pricing during rebuild"
```

### Task 3: Build and verify deployment candidate

**Files:**
- No source changes.

- [ ] **Step 1: Build the Manager Server binary**

Run from `apps/manager-server`:

```powershell
go build ./cmd/cpa-manager-plus
```

Expected: exit code 0 and a candidate `cpa-manager-plus` binary. Remove only the generated local candidate after deployment packaging; do not touch CPA Core binaries.

- [ ] **Step 2: Run the full Manager Server regression suite**

Run:

```powershell
go test ./... -count=1 -timeout=20m
```

Expected: exit code 0.

- [ ] **Step 3: Perform a read-only server preflight before deployment**

Record, without changing services:

```bash
systemctl show cpam.service -p MainPID -p ActiveEnterTimestamp --no-pager
systemctl show cpa.service -p MainPID -p ActiveEnterTimestamp --no-pager
sha256sum /home/cpam/app/cpa-manager-plus
```

Expected: both services are active; CPA identity is recorded as an invariant for post-deploy comparison.

- [ ] **Step 4: Deploy only CPAM to both targets**

1. Build/restart only local Docker service `cpa-manager-plus` using the verified candidate.
2. Upload only the Manager Server candidate binary to `/home/cpam/app/cpa-manager-plus` with an atomic replacement and a timestamped backup.
3. Restart only `cpam.service`.
4. Do not touch `/home/cpa`, `cpa.service`, `cli-proxy-api`, `/home/cpam/config/cpam.env`, `/home/cpam/secrets/*`, or `/home/cpam/data/*`.

- [ ] **Step 5: Post-deploy verification**

Run:

```bash
systemctl is-active cpam.service
systemctl show cpa.service -p MainPID -p ActiveEnterTimestamp --no-pager
curl -fsS --max-time 10 http://127.0.0.1:18317/health
```

Then issue one authenticated `POST /v0/management/monitoring/account-history` request for a small visible account set and verify the response contains non-null `total_cost` values while the service reports normal operation. Compare the recorded `cpa.service` identity with preflight; it must be unchanged.

- [ ] **Step 6: Commit delivery documentation and push only private branch**

```powershell
git status --short
git push origin HEAD:lo/custom
```

Expected: only the private `origin/lo/custom` branch is updated. Do not push upstream.

## Plan self-review

- Spec coverage: Task 1 proves the exact rebuild regression; Task 2 restores only the old exact price path; Task 3 verifies the read-only accounting invariant and CPAM-only deployment.
- Placeholder scan: no deferred implementation steps or unspecified APIs remain.
- Type consistency: the plan uses existing `LoadAccountRowsTx`, `LoadAccountRowsFromEventsTx`, `pricingCoverageFixture`, `pricingCoverageCounts`, and `AccountHistory` interfaces without adding new types.
