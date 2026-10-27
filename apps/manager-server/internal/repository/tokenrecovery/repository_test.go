package tokenrecovery

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSignalAutomaticDeduplicatesFailureDuringDailyCooldown(t *testing.T) {
	repo := newTestRepository(t)
	target := model.TokenRecoveryTarget{
		FileName: "Codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 100,
	}
	first, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	second, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("second SignalAutomatic() error = %v", err)
	}
	if first.ID != second.ID || second.Status != model.TokenRecoveryStatusAutoQueued || second.Mode != model.TokenRecoveryModeAuto {
		t.Fatalf("automatic tasks = %#v / %#v", first, second)
	}

	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok || claimed.Status != model.TokenRecoveryStatusAutoRunning {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	failed, err := repo.Fail(context.Background(), claimed.ID, "token_acquisition_failed", "mfa_failed: 二次验证失败")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.Status != model.TokenRecoveryStatusAutoFailedManualOnly ||
		failed.LastErrorCode != "token_acquisition_failed" ||
		failed.LastErrorMessage != "mfa_failed: 二次验证失败" {
		t.Fatalf("failed task = %#v", failed)
	}

	third, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("third SignalAutomatic() error = %v", err)
	}
	if third.ID != first.ID || third.Status != model.TokenRecoveryStatusAutoFailedManualOnly {
		t.Fatalf("automatic retry escaped daily cooldown state: %#v", third)
	}
	manual, err := repo.RequestManual(context.Background(), target)
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}
	if manual.ID != first.ID || manual.Status != model.TokenRecoveryStatusManualQueued || manual.Mode != model.TokenRecoveryModeManual || manual.LastErrorCode != "" || manual.LastErrorMessage != "" {
		t.Fatalf("manual retry = %#v", manual)
	}
}

func TestSignalAutomaticWithoutObservedAtDoesNotRequeueSucceededTask(t *testing.T) {
	repo := newTestRepository(t)
	target := model.TokenRecoveryTarget{FileName: "codex.json", AuthIndex: "7", Provider: "codex", ObservedAtMS: 100}
	queued, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	if _, err := repo.Complete(context.Background(), queued.ID); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	withoutTimestamp := target
	withoutTimestamp.ObservedAtMS = 0
	got, err := repo.SignalAutomatic(context.Background(), withoutTimestamp)
	if err != nil {
		t.Fatalf("SignalAutomatic() without timestamp error = %v", err)
	}
	if got.Status != model.TokenRecoveryStatusSucceeded {
		t.Fatalf("task status = %q, want %q", got.Status, model.TokenRecoveryStatusSucceeded)
	}
	if _, ok, err := repo.ClaimNextQueued(context.Background()); err != nil || ok {
		t.Fatalf("succeeded task was requeued: ok=%t err=%v", ok, err)
	}
}

func TestSignalAutomaticRecordsAttemptAcrossSuccessAndManualRetry(t *testing.T) {
	repo := newTestRepository(t)
	target := model.TokenRecoveryTarget{
		FileName: "codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 100,
	}
	first, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	if first.AutoAttemptedAtMS != 0 {
		t.Fatalf("queued automatic task must not claim an attempt before execution: %#v", first)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok || claimed.ID != first.ID {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	if claimed.AutoAttemptedAtMS <= 0 {
		t.Fatalf("claimed automatic task must retain auto attempt timestamp: %#v", claimed)
	}
	completed, err := repo.Complete(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	repeatedTarget := target
	repeatedTarget.ObservedAtMS = completed.CompletedAtMS + 1
	repeated, err := repo.SignalAutomatic(context.Background(), repeatedTarget)
	if err != nil {
		t.Fatalf("repeated SignalAutomatic() error = %v", err)
	}
	if repeated.ID != first.ID || repeated.Status != model.TokenRecoveryStatusSucceeded || repeated.AutoAttemptedAtMS != claimed.AutoAttemptedAtMS {
		t.Fatalf("completed automatic task was requeued or lost its marker: first=%#v repeated=%#v", first, repeated)
	}

	manual, err := repo.RequestManual(context.Background(), target)
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}
	if manual.Status != model.TokenRecoveryStatusManualQueued || manual.AutoAttemptedAtMS != claimed.AutoAttemptedAtMS {
		t.Fatalf("manual retry must preserve automatic attempt history: %#v", manual)
	}
}

func TestSignalAutomaticDeduplicatesSameAuthIndexWhenOneSourceLacksEmail(t *testing.T) {
	repo := newTestRepository(t)
	withoutEmail, err := repo.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "codex.json", AuthIndex: "7", Provider: "codex", ObservedAtMS: 100,
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() without email error = %v", err)
	}
	withEmail, err := repo.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 101,
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() with email error = %v", err)
	}
	if withoutEmail.ID != withEmail.ID {
		t.Fatalf("same auth index created duplicate tasks: %#v / %#v", withoutEmail, withEmail)
	}
}

func TestGetFindsEmailOnlyTaskAfterAuthIndexBecomesAvailable(t *testing.T) {
	repo := newTestRepository(t)
	emailOnly := model.TokenRecoveryTarget{
		FileName: "codex.json", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 100,
	}
	first, err := repo.SignalAutomatic(context.Background(), emailOnly)
	if err != nil {
		t.Fatalf("SignalAutomatic() email-only = %v", err)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	if _, err := repo.Fail(context.Background(), claimed.ID, "token_acquisition_failed", "mfa_failed: 二次验证失败"); err != nil {
		t.Fatalf("Fail() = %v", err)
	}

	got, found, err := repo.Get(context.Background(), model.TokenRecoveryTarget{
		FileName: "codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex",
	})
	if err != nil || !found {
		t.Fatalf("Get() after auth index becomes available = %#v, %t, %v", got, found, err)
	}
	if got.ID != first.ID || got.LastErrorMessage != "mfa_failed: 二次验证失败" {
		t.Fatalf("compatible task = %#v, want original failed task %#v", got, first)
	}
}

func TestClaimNextQueuedHasExactlyOneConcurrentWinner(t *testing.T) {
	repo := newTestRepository(t)
	_, err := repo.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 100,
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := repo.ClaimNextQueued(context.Background())
			results <- ok
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("ClaimNextQueued() error = %v", err)
		}
	}
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", winners)
	}
}

func TestClaimNextEligibleLeavesAutomaticTaskQueuedWhenAutomaticProcessingIsOff(t *testing.T) {
	repo := newTestRepository(t)
	automatic, err := repo.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "automatic.json", AuthIndex: "1", AccountEmail: "automatic@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	manual, err := repo.RequestManual(context.Background(), model.TokenRecoveryTarget{
		FileName: "manual.json", AuthIndex: "2", AccountEmail: "manual@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}

	claimed, ok, err := repo.ClaimNextEligible(context.Background(), false)
	if err != nil || !ok || claimed.ID != manual.ID || claimed.Status != model.TokenRecoveryStatusManualRunning {
		t.Fatalf("ClaimNextEligible(false) = %#v, %t, %v", claimed, ok, err)
	}
	stillQueued, found, err := repo.GetByID(context.Background(), automatic.ID)
	if err != nil || !found || stillQueued.Status != model.TokenRecoveryStatusAutoQueued || stillQueued.AutoAttemptedAtMS != 0 {
		t.Fatalf("automatic task must remain queued and unattempted: %#v, %t, %v", stillQueued, found, err)
	}
}

func TestClaimNextEligiblePrioritizesManualTaskOverOlderAutomaticTask(t *testing.T) {
	repo := newTestRepository(t)
	automatic, err := repo.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "automatic.json", AuthIndex: "1", AccountEmail: "automatic@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	manual, err := repo.RequestManual(context.Background(), model.TokenRecoveryTarget{
		FileName: "manual.json", AuthIndex: "2", AccountEmail: "manual@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}

	claimed, ok, err := repo.ClaimNextEligible(context.Background(), true)
	if err != nil || !ok || claimed.ID != manual.ID || claimed.Status != model.TokenRecoveryStatusManualRunning {
		t.Fatalf("ClaimNextEligible(true) = %#v, %t, %v; automatic=%#v manual=%#v", claimed, ok, err, automatic, manual)
	}
}

func TestRequestManualTakesOverPausedAutomaticQueue(t *testing.T) {
	repo := newTestRepository(t)
	target := model.TokenRecoveryTarget{
		FileName: "paused.json", AuthIndex: "3", AccountEmail: "paused@example.com", Provider: "codex",
	}
	automatic, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	manual, err := repo.RequestManual(context.Background(), target)
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}
	if manual.ID != automatic.ID || manual.Status != model.TokenRecoveryStatusManualQueued || manual.Mode != model.TokenRecoveryModeManual {
		t.Fatalf("manual retry did not take over paused automatic queue: %#v", manual)
	}
	claimed, ok, err := repo.ClaimNextEligible(context.Background(), false)
	if err != nil || !ok || claimed.ID != automatic.ID || claimed.Status != model.TokenRecoveryStatusManualRunning {
		t.Fatalf("manual task was not claimable while automatic processing was off: %#v, %t, %v", claimed, ok, err)
	}
}

func TestFailRunningOnStartupNeverRequeuesInterruptedWork(t *testing.T) {
	repo := newTestRepository(t)
	queued, err := repo.RequestManual(context.Background(), model.TokenRecoveryTarget{
		FileName: "codex.json", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: 100,
	})
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	if _, err := repo.FailRunningOnStartup(context.Background()); err != nil {
		t.Fatalf("FailRunningOnStartup() error = %v", err)
	}
	got, ok, err := repo.GetByID(context.Background(), queued.ID)
	if err != nil || !ok {
		t.Fatalf("GetByID() = %#v, %t, %v", got, ok, err)
	}
	if got.Status != model.TokenRecoveryStatusManualFailedManualOnly || got.LastErrorCode != "interrupted" {
		t.Fatalf("interrupted task = %#v", got)
	}
	if _, ok, err := repo.ClaimNextQueued(context.Background()); err != nil || ok {
		t.Fatalf("interrupted task requeued: ok=%t err=%v", ok, err)
	}
}

func TestSignalAutomaticRequeuesSucceededTaskAfterDailyCooldown(t *testing.T) {
	repo, db := newTestRepositoryWithDB(t)
	target := model.TokenRecoveryTarget{FileName: "codex.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex"}

	queued, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() = %v", err)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	completed, err := repo.Complete(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	withinCooldown, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() within cooldown = %v", err)
	}
	if withinCooldown.Status != model.TokenRecoveryStatusSucceeded {
		t.Fatalf("within-cooldown task = %#v, want succeeded", withinCooldown)
	}

	expiredAttemptAt := time.Now().Add(-24*time.Hour - time.Millisecond).UnixMilli()
	if _, err := db.Exec(`update token_recovery_tasks set auto_attempted_at_ms = ? where id = ?`, expiredAttemptAt, completed.ID); err != nil {
		t.Fatalf("expire auto attempt: %v", err)
	}
	requeued, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() after cooldown = %v", err)
	}
	if requeued.ID != queued.ID || requeued.Status != model.TokenRecoveryStatusAutoQueued || requeued.AutoAttemptedAtMS != 0 {
		t.Fatalf("expired automatic task = %#v, want a fresh queued cycle", requeued)
	}
}

func TestSignalAutomaticRequeuesFailedTaskAfterDailyCooldown(t *testing.T) {
	repo, db := newTestRepositoryWithDB(t)
	target := model.TokenRecoveryTarget{FileName: "failed.json", AuthIndex: "8", AccountEmail: "failed@example.com", Provider: "codex"}

	queued, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() = %v", err)
	}
	claimed, ok, err := repo.ClaimNextQueued(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNextQueued() = %#v, %t, %v", claimed, ok, err)
	}
	failed, err := repo.Fail(context.Background(), claimed.ID, "token_acquisition_failed", "external failure")
	if err != nil {
		t.Fatalf("Fail() = %v", err)
	}

	withinCooldown, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() within cooldown = %v", err)
	}
	if withinCooldown.Status != model.TokenRecoveryStatusAutoFailedManualOnly {
		t.Fatalf("within-cooldown task = %#v, want failed terminal state", withinCooldown)
	}

	expiredAttemptAt := time.Now().Add(-24*time.Hour - time.Millisecond).UnixMilli()
	if _, err := db.Exec(`update token_recovery_tasks set auto_attempted_at_ms = ? where id = ?`, expiredAttemptAt, failed.ID); err != nil {
		t.Fatalf("expire auto attempt: %v", err)
	}
	requeued, err := repo.SignalAutomatic(context.Background(), target)
	if err != nil {
		t.Fatalf("SignalAutomatic() after cooldown = %v", err)
	}
	if requeued.ID != queued.ID || requeued.Status != model.TokenRecoveryStatusAutoQueued || requeued.AutoAttemptedAtMS != 0 {
		t.Fatalf("expired failed automatic task = %#v, want a fresh queued cycle", requeued)
	}
}

func newTestRepository(t *testing.T) Repository {
	t.Helper()
	repository, _ := newTestRepositoryWithDB(t)
	return repository
}

func newTestRepositoryWithDB(t *testing.T) (Repository, *sql.DB) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "token-recovery.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), db
}

func TestFailStaleRunningMarksOnlyExpiredTasksAsRecoveryTimeout(t *testing.T) {
	repo, db := newTestRepositoryWithDB(t)
	ctx := context.Background()

	automatic, err := repo.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "stale-auto.json", AuthIndex: "1", AccountEmail: "stale-auto@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("SignalAutomatic(stale automatic) = %v", err)
	}
	claimedAutomatic, ok, err := repo.ClaimNextQueued(ctx)
	if err != nil || !ok || claimedAutomatic.ID != automatic.ID {
		t.Fatalf("ClaimNextQueued(stale automatic) = %#v, %t, %v", claimedAutomatic, ok, err)
	}

	manual, err := repo.RequestManual(ctx, model.TokenRecoveryTarget{
		FileName: "stale-manual.json", AuthIndex: "2", AccountEmail: "stale-manual@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("RequestManual(stale manual) = %v", err)
	}
	claimedManual, ok, err := repo.ClaimNextQueued(ctx)
	if err != nil || !ok || claimedManual.ID != manual.ID {
		t.Fatalf("ClaimNextQueued(stale manual) = %#v, %t, %v", claimedManual, ok, err)
	}

	fresh, err := repo.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "fresh.json", AuthIndex: "3", AccountEmail: "fresh@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("SignalAutomatic(fresh) = %v", err)
	}
	claimedFresh, ok, err := repo.ClaimNextQueued(ctx)
	if err != nil || !ok || claimedFresh.ID != fresh.ID {
		t.Fatalf("ClaimNextQueued(fresh) = %#v, %t, %v", claimedFresh, ok, err)
	}

	now := time.Now().UnixMilli()
	staleStartedAt := now - int64((2 * time.Hour).Milliseconds())
	freshStartedAt := now - int64((10 * time.Minute).Milliseconds())
	if _, err := db.Exec(`update token_recovery_tasks set started_at_ms = ?, updated_at_ms = ? where id in (?, ?)`, staleStartedAt, staleStartedAt, claimedAutomatic.ID, claimedManual.ID); err != nil {
		t.Fatalf("age stale running tasks: %v", err)
	}
	if _, err := db.Exec(`update token_recovery_tasks set started_at_ms = ?, updated_at_ms = ? where id = ?`, freshStartedAt, freshStartedAt, claimedFresh.ID); err != nil {
		t.Fatalf("age fresh running task: %v", err)
	}

	changed, err := repo.FailStaleRunning(ctx, now-int64(time.Hour.Milliseconds()))
	if err != nil {
		t.Fatalf("FailStaleRunning() = %v", err)
	}
	if got, want := changed, int64(2); got != want {
		t.Fatalf("FailStaleRunning() changed %d tasks, want %d", got, want)
	}

	gotAutomatic, _, err := repo.GetByID(ctx, automatic.ID)
	if err != nil {
		t.Fatalf("GetByID(stale automatic) = %v", err)
	}
	if gotAutomatic.Status != model.TokenRecoveryStatusAutoFailedManualOnly || gotAutomatic.LastErrorCode != "recovery_timeout" {
		t.Fatalf("stale automatic task = %#v", gotAutomatic)
	}
	gotManual, _, err := repo.GetByID(ctx, manual.ID)
	if err != nil {
		t.Fatalf("GetByID(stale manual) = %v", err)
	}
	if gotManual.Status != model.TokenRecoveryStatusManualFailedManualOnly || gotManual.LastErrorCode != "recovery_timeout" {
		t.Fatalf("stale manual task = %#v", gotManual)
	}
	gotFresh, _, err := repo.GetByID(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("GetByID(fresh) = %v", err)
	}
	if gotFresh.Status != model.TokenRecoveryStatusAutoRunning || gotFresh.LastErrorCode != "" {
		t.Fatalf("fresh running task = %#v", gotFresh)
	}
}
