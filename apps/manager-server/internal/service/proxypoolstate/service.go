package proxypoolstate

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// Store is intentionally narrow so inspection and replacement orchestration
// share pool state without importing one another.
type Store interface {
	UpsertProxyPoolState(context.Context, model.ProxyPoolState) (model.ProxyPoolState, error)
	ListProxyPoolStates(context.Context) ([]model.ProxyPoolState, error)
	RestoreProxyPoolState(context.Context, string) (model.ProxyPoolState, error)
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

// MarkSuspect receives a full URL only transiently. It converts that URL to a
// fingerprint before persistence, and records a finite reason code rather
// than potentially credential-bearing transport text.
func (s *Service) MarkSuspect(ctx context.Context, proxyURL string, reason string) error {
	if s == nil || s.store == nil {
		return errors.New("proxy pool state store is unavailable")
	}
	fingerprint := model.ProxyFingerprint(proxyURL)
	if fingerprint == "" {
		return errors.New("proxy URL is required")
	}
	_, err := s.store.UpsertProxyPoolState(ctx, model.ProxyPoolState{
		Fingerprint:       fingerprint,
		Status:            model.ProxyPoolStatusSuspect,
		LastFailureAtMS:   time.Now().UnixMilli(),
		LastFailureReason: normalizeReason(reason),
		UpdatedAtMS:       time.Now().UnixMilli(),
	})
	return err
}

func (s *Service) List(ctx context.Context) ([]model.ProxyPoolState, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("proxy pool state store is unavailable")
	}
	return s.store.ListProxyPoolStates(ctx)
}

func (s *Service) Restore(ctx context.Context, fingerprint string) (model.ProxyPoolState, error) {
	if s == nil || s.store == nil {
		return model.ProxyPoolState{}, errors.New("proxy pool state store is unavailable")
	}
	return s.store.RestoreProxyPoolState(ctx, strings.TrimSpace(fingerprint))
}

func normalizeReason(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(normalized)
	if strings.Contains(compact, "api-callfailed:502") && strings.Contains(compact, `"error":"requestfailed"`) {
		return "transport_502_request_failed"
	}
	if strings.Contains(normalized, "context deadline exceeded") || strings.Contains(normalized, "timeout") {
		return "transport_timeout"
	}
	return "transport_unknown"
}
