package worker

import (
	"context"
	"sync"
	"testing"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestTokenRecoverySignalWorkerOnlySignalsCodexReauthEvents(t *testing.T) {
	recorder := &tokenRecoverySignalRecorder{}
	worker := NewTokenRecoverySignalWorker(recorder)
	worker.HandleUsageEvents(context.Background(), collectorpkg.RuntimeConfig{}, []usage.Event{
		{
			Failed: true, FailStatusCode: 401, FailSummary: "invalid_token", TimestampMS: 123,
			AuthFileSnapshot: "physical account.json", AuthIndex: "7", AccountSnapshot: "person@example.com",
			AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "must-not-be-forwarded",
		},
		{
			Failed: true, FailStatusCode: 401, FailSummary: "invalid_token", TimestampMS: 124,
			AuthFileSnapshot: "xai.json", AuthIndex: "8", AccountSnapshot: "xai@example.com", AuthProviderSnapshot: "xai",
		},
		{
			Failed: true, FailStatusCode: 502, FailSummary: "upstream error", TimestampMS: 125,
			AuthFileSnapshot: "server-error.json", AuthIndex: "9", AccountSnapshot: "server@example.com", AuthProviderSnapshot: "codex",
		},
	})

	targets := recorder.targets()
	if len(targets) != 1 {
		t.Fatalf("signal targets = %#v, want one", targets)
	}
	if got := targets[0]; got.FileName != "physical account.json" || got.AuthIndex != "7" || got.AccountEmail != "person@example.com" || got.Provider != "codex" || got.ObservedAtMS != 123 {
		t.Fatalf("signal target = %#v", got)
	}
}

func TestTokenRecoverySignalWorkerDoesNotTreatNonEmailSnapshotAsEmail(t *testing.T) {
	recorder := &tokenRecoverySignalRecorder{}
	worker := NewTokenRecoverySignalWorker(recorder)
	worker.HandleUsageEvents(context.Background(), collectorpkg.RuntimeConfig{}, []usage.Event{{
		Failed: true, FailStatusCode: 401, FailSummary: "invalid_token", TimestampMS: 123,
		AuthFileSnapshot: "physical.json", AuthIndex: "7", AccountSnapshot: "display-label",
		AuthProviderSnapshot: "codex",
	}})
	targets := recorder.targets()
	if len(targets) != 1 || targets[0].AccountEmail != "" {
		t.Fatalf("signal target = %#v, want empty account email", targets)
	}
}

type tokenRecoverySignalRecorder struct {
	mu    sync.Mutex
	items []model.TokenRecoveryTarget
}

func (r *tokenRecoverySignalRecorder) SignalAutomatic(_ context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	r.mu.Lock()
	r.items = append(r.items, target)
	r.mu.Unlock()
	return model.TokenRecoveryTask{}, nil
}

func (r *tokenRecoverySignalRecorder) targets() []model.TokenRecoveryTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.TokenRecoveryTarget(nil), r.items...)
}
