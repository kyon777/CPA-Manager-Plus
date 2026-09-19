package worker

import (
	"context"
	"strings"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

// TokenRecoverySignalSink is intentionally limited to the redacted locator.
// A monitoring event's account/workspace ID is never used as acceptance
// evidence for recovery.
type TokenRecoverySignalSink interface {
	SignalAutomatic(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, error)
}

type TokenRecoverySignalWorker struct {
	sink TokenRecoverySignalSink
}

func NewTokenRecoverySignalWorker(sink TokenRecoverySignalSink) *TokenRecoverySignalWorker {
	return &TokenRecoverySignalWorker{sink: sink}
}

func (w *TokenRecoverySignalWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || w.sink == nil {
		return
	}
	for _, event := range events {
		target, ok := tokenRecoveryTargetFromUsageEvent(event)
		if !ok {
			continue
		}
		// The call only persists/deduplicates a small task row. It never waits
		// for TokenAcquisition, so collector throughput is not tied to OAuth.
		if ctx.Err() != nil {
			return
		}
		_, _ = w.sink.SignalAutomatic(ctx, target)
	}
}

func tokenRecoveryTargetFromUsageEvent(event usage.Event) (model.TokenRecoveryTarget, bool) {
	decision, ok := classifyAccountActionEvent(event)
	if !ok || decision.Action != credentialpolicy.ActionReauth {
		return model.TokenRecoveryTarget{}, false
	}
	provider := credentialpolicy.NormalizeProvider(firstNonEmpty(event.AuthProviderSnapshot, event.Provider))
	fileName := strings.TrimSpace(event.AuthFileSnapshot)
	if provider != "codex" || fileName == "" {
		return model.TokenRecoveryTarget{}, false
	}
	seenAt := event.TimestampMS
	if seenAt <= 0 {
		seenAt = time.Now().UnixMilli()
	}
	return model.TokenRecoveryTarget{
		FileName:     fileName,
		AuthIndex:    strings.TrimSpace(event.AuthIndex),
		AccountEmail: recoveryEmailFromSnapshot(fileName, event.AccountSnapshot),
		Provider:     "codex",
		ObservedAtMS: seenAt,
	}, true
}

func recoveryEmailFromSnapshot(fileName, value string) string {
	account := stableEventAccountSnapshot(fileName, value)
	if !strings.Contains(account, "@") {
		return ""
	}
	return account
}
