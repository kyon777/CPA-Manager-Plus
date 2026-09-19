package codexinspection

import (
	"context"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestCompletedCodexInspectionSignalsOnlyCodexReauthLocators(t *testing.T) {
	recorder := &inspectionRecoveryRecorder{}
	service := NewWithOptions(&store.Store{}, nil, ServiceOptions{ReauthRecoveryNotifier: recorder, OwnerID: "test-owner"})
	service.signalCompletedReauthRecoveries(context.Background(), []model.CodexInspectionResult{
		{
			Provider: "codex", Action: "reauth", FileName: "physical account.json", AuthIndex: "7", AccountSnapshot: "person@example.com", AccountID: "must-not-be-forwarded", CreatedAtMS: 123,
		},
		{
			Provider: "xai", Action: "reauth", FileName: "xai.json", AuthIndex: "8", AccountSnapshot: "xai@example.com", CreatedAtMS: 124,
		},
		{
			Provider: "codex", Action: "keep", FileName: "keep.json", AuthIndex: "9", AccountSnapshot: "keep@example.com", CreatedAtMS: 125,
		},
	})

	targets := recorder.targets()
	if len(targets) != 1 {
		t.Fatalf("recovery targets = %#v", targets)
	}
	if got := targets[0]; got.FileName != "physical account.json" || got.AuthIndex != "7" || got.AccountEmail != "person@example.com" || got.Provider != "codex" || got.ObservedAtMS != 123 {
		t.Fatalf("recovery target = %#v", got)
	}
}

type inspectionRecoveryRecorder struct {
	mu    sync.Mutex
	items []model.TokenRecoveryTarget
}

func (r *inspectionRecoveryRecorder) SignalAutomatic(_ context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	r.mu.Lock()
	r.items = append(r.items, target)
	r.mu.Unlock()
	return model.TokenRecoveryTask{}, nil
}

func (r *inspectionRecoveryRecorder) targets() []model.TokenRecoveryTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.TokenRecoveryTarget(nil), r.items...)
}
