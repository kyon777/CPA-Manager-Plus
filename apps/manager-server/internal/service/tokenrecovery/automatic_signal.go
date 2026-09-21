package tokenrecovery

import (
	"context"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// AutomaticSignalRecovery is deliberately smaller than Service. The gate only
// needs to inspect durable recovery history and enqueue a fresh automatic
// cycle after it has verified all current CPA-side conditions.
type AutomaticSignalRecovery interface {
	Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error)
	SignalAutomatic(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, error)
}

type AutomaticSignalSetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type AutomaticSignalAuthFiles interface {
	Verify(context.Context, string, string, cpaauthfiles.Identity) (cpaauthfiles.File, error)
}

type AutomaticSignalGateOptions struct {
	Recovery      AutomaticSignalRecovery
	SetupResolver AutomaticSignalSetupResolver
	AuthFiles     AutomaticSignalAuthFiles
	// Enabled is evaluated for every signal rather than captured at startup,
	// allowing the persisted manager switch to take effect immediately.
	Enabled func(context.Context) bool
}

// AutomaticSignalGate is the single admission point for background recovery.
// It deliberately fails closed: a missing switch, stale/ambiguous CPA state,
// a disabled credential, or any status other than HTTP 401 produces no task.
// Manual recovery bypasses this gate and remains available to the operator.
type AutomaticSignalGate struct {
	recovery      AutomaticSignalRecovery
	setupResolver AutomaticSignalSetupResolver
	authFiles     AutomaticSignalAuthFiles
	enabled       func(context.Context) bool
}

func NewAutomaticSignalGate(options AutomaticSignalGateOptions) *AutomaticSignalGate {
	return &AutomaticSignalGate{
		recovery:      options.Recovery,
		setupResolver: options.SetupResolver,
		authFiles:     options.AuthFiles,
		enabled:       options.Enabled,
	}
}

func (g *AutomaticSignalGate) SignalAutomatic(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	if g == nil || g.recovery == nil {
		return model.TokenRecoveryTask{}, ErrRecoveryUnavailable
	}
	if !isEligibleAutomaticSignal(target) || g.enabled == nil || !g.enabled(ctx) {
		return model.TokenRecoveryTask{}, nil
	}

	// Avoid hitting CPA Core after a queue/running state or a previous automatic
	// attempt. This is both a strict one-shot guard and a low-noise path for the
	// stream of repeated 401 usage events.
	existing, found, err := g.recovery.Get(ctx, target)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if found && automaticSignalAlreadyResolved(existing) {
		return existing, nil
	}
	if g.setupResolver == nil || g.authFiles == nil {
		return model.TokenRecoveryTask{}, ErrRecoveryUnavailable
	}
	setup, ok, err := g.setupResolver.ResolveSetup(ctx)
	if err != nil || !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		// Automatic recovery must never turn uncertain infrastructure state into
		// a durable task that later runs unexpectedly. The next manual operation
		// gives the operator a concrete error through the normal service path.
		return model.TokenRecoveryTask{}, nil
	}
	file, err := g.authFiles.Verify(ctx, setup.CPAUpstreamURL, setup.ManagementKey, cpaauthfiles.Identity{
		AuthFileName:    strings.TrimSpace(target.FileName),
		AuthIndex:       strings.TrimSpace(target.AuthIndex),
		Provider:        "codex",
		AccountSnapshot: strings.TrimSpace(target.AccountEmail),
	})
	if err != nil || file.Disabled {
		// Verification errors are intentionally non-actionable automatic signals;
		// neither case is proof that this currently enabled credential may be
		// changed. Preserve it for manual operator review instead.
		return model.TokenRecoveryTask{}, nil
	}
	return g.recovery.SignalAutomatic(ctx, target)
}

func isEligibleAutomaticSignal(target model.TokenRecoveryTarget) bool {
	return strings.EqualFold(strings.TrimSpace(target.Provider), "codex") &&
		target.ObservedStatusCode == 401 &&
		strings.TrimSpace(target.FileName) != "" &&
		(strings.TrimSpace(target.AuthIndex) != "" || strings.TrimSpace(target.AccountEmail) != "")
}

func automaticSignalAlreadyResolved(task model.TokenRecoveryTask) bool {
	if task.AutoAttemptedAtMS > 0 {
		return true
	}
	switch task.Status {
	case model.TokenRecoveryStatusAutoQueued,
		model.TokenRecoveryStatusAutoRunning,
		model.TokenRecoveryStatusManualQueued,
		model.TokenRecoveryStatusManualRunning:
		return true
	}
	// A legacy terminal auto task predates the explicit audit marker but still
	// proves that its one automatic acquisition cycle has already happened.
	return task.Mode == model.TokenRecoveryModeAuto
}
