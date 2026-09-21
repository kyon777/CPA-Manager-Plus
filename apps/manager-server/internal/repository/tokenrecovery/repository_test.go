package tokenrecovery

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSignalAutomaticDeduplicatesAndFailureRequiresManualRetry(t *testing.T) {
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
		t.Fatalf("automatic retry escaped manual-only state: %#v", third)
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

func TestSignalAutomaticRecordsOneShotAttemptAcrossSuccessAndManualRetry(t *testing.T) {
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

func newTestRepository(t *testing.T) Repository {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "token-recovery.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}
