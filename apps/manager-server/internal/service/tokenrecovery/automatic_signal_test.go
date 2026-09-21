package tokenrecovery

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestAutomaticSignalGateOnlyQueuesEnabledCodex401Once(t *testing.T) {
	target := model.TokenRecoveryTarget{
		FileName: "physical.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedStatusCode: http.StatusUnauthorized,
	}
	tests := []struct {
		name         string
		enabled      bool
		target       model.TokenRecoveryTarget
		existing     model.TokenRecoveryTask
		found        bool
		verifyFile   cpaauthfiles.File
		verifyErr    error
		wantSignals  int
		wantVerifies int
		wantTaskID   int64
	}{
		{name: "enabled current 401", enabled: true, target: target, verifyFile: cpaauthfiles.File{Name: "physical.json", AuthIndex: "7", Provider: "codex", AccountSnapshot: "person@example.com"}, wantSignals: 1, wantVerifies: 1, wantTaskID: 41},
		{name: "switch disabled", enabled: false, target: target, wantSignals: 0, wantVerifies: 0},
		{name: "non 401", enabled: true, target: withObservedStatus(target, http.StatusForbidden), wantSignals: 0, wantVerifies: 0},
		{name: "currently disabled", enabled: true, target: target, verifyFile: cpaauthfiles.File{Disabled: true}, wantSignals: 0, wantVerifies: 1},
		{name: "verification unavailable", enabled: true, target: target, verifyErr: errors.New("core unavailable"), wantSignals: 0, wantVerifies: 1},
		{name: "auto attempt already recorded", enabled: true, target: target, existing: model.TokenRecoveryTask{ID: 9, AutoAttemptedAtMS: 123}, found: true, wantSignals: 0, wantVerifies: 0, wantTaskID: 9},
		{name: "automatic queue already present", enabled: true, target: target, existing: model.TokenRecoveryTask{ID: 10, Status: model.TokenRecoveryStatusAutoQueued, Mode: model.TokenRecoveryModeAuto}, found: true, wantSignals: 0, wantVerifies: 0, wantTaskID: 10},
		{name: "manual queue already present", enabled: true, target: target, existing: model.TokenRecoveryTask{ID: 11, Status: model.TokenRecoveryStatusManualQueued, Mode: model.TokenRecoveryModeManual}, found: true, wantSignals: 0, wantVerifies: 0, wantTaskID: 11},
		{name: "legacy terminal auto attempt", enabled: true, target: target, existing: model.TokenRecoveryTask{ID: 12, Status: model.TokenRecoveryStatusSucceeded, Mode: model.TokenRecoveryModeAuto}, found: true, wantSignals: 0, wantVerifies: 0, wantTaskID: 12},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recovery := &automaticSignalRecovery{existing: tc.existing, found: tc.found, next: model.TokenRecoveryTask{ID: 41, Status: model.TokenRecoveryStatusAutoQueued}}
			authFiles := &automaticSignalAuthFiles{file: tc.verifyFile, err: tc.verifyErr}
			gate := NewAutomaticSignalGate(AutomaticSignalGateOptions{
				Recovery:      recovery,
				SetupResolver: automaticSignalSetupResolver{},
				AuthFiles:     authFiles,
				Enabled:       func(context.Context) bool { return tc.enabled },
			})

			got, err := gate.SignalAutomatic(context.Background(), tc.target)
			if err != nil {
				t.Fatalf("SignalAutomatic() error = %v", err)
			}
			if recovery.signalCalls != tc.wantSignals || authFiles.calls != tc.wantVerifies {
				t.Fatalf("signal/verify calls = %d/%d, want %d/%d", recovery.signalCalls, authFiles.calls, tc.wantSignals, tc.wantVerifies)
			}
			if got.ID != tc.wantTaskID {
				t.Fatalf("task = %#v, want id %d", got, tc.wantTaskID)
			}
			if authFiles.calls > 0 && authFiles.identity.AccountSnapshot != "person@example.com" {
				t.Fatalf("verify identity = %#v", authFiles.identity)
			}
		})
	}
}

func withObservedStatus(target model.TokenRecoveryTarget, status int) model.TokenRecoveryTarget {
	target.ObservedStatusCode = status
	return target
}

type automaticSignalRecovery struct {
	existing    model.TokenRecoveryTask
	found       bool
	next        model.TokenRecoveryTask
	signalCalls int
}

func (r *automaticSignalRecovery) Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	return r.existing, r.found, nil
}

func (r *automaticSignalRecovery) SignalAutomatic(_ context.Context, _ model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	r.signalCalls++
	return r.next, nil
}

type automaticSignalSetupResolver struct{}

func (automaticSignalSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "https://core.example.test", ManagementKey: "management-key"}, true, nil
}

type automaticSignalAuthFiles struct {
	file     cpaauthfiles.File
	err      error
	calls    int
	identity cpaauthfiles.Identity
}

func (a *automaticSignalAuthFiles) Verify(_ context.Context, _ string, _ string, identity cpaauthfiles.Identity) (cpaauthfiles.File, error) {
	a.calls++
	a.identity = identity
	return a.file, a.err
}
