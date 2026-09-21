package automation

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	SourceStartup = "startup"
	SourceEnv     = "env"
	SourceDB      = "database"
)

// settingsStore is the subset of *store.Store used by the service. It is an
// interface so tests can inject failing loaders to exercise error handling.
type settingsStore interface {
	LoadAutomationSettings(ctx context.Context) (store.AutomationSettings, bool, error)
	SaveAutomationSettings(ctx context.Context, settings store.AutomationSettings) (store.AutomationSettings, error)
}

type Capability struct {
	Enabled       bool   `json:"enabled"`
	Configured    bool   `json:"configured"`
	Source        string `json:"source"`
	Locked        bool   `json:"locked"`
	EnvKey        string `json:"envKey"`
	ConfigFileKey string `json:"configFileKey"`
	DependsOn     string `json:"dependsOn,omitempty"`
}

type Status struct {
	Source                      string     `json:"source"`
	UpdatedAtMS                 int64      `json:"updatedAtMs,omitempty"`
	QuotaCooldown               Capability `json:"codexQuotaCooldown"`
	AccountActions              Capability `json:"authIssueQueue"`
	AccountActionsAutoDisable   Capability `json:"authIssueAutoDisable"`
	ServerErrorPriorityDemotion Capability `json:"serverErrorPriorityDemotion"`
	CodexReauthAutoUpdate       Capability `json:"codexReauthAutoUpdate"`
}

type UpdateRequest struct {
	QuotaCooldownEnabled               *bool `json:"codexQuotaCooldownEnabled,omitempty"`
	AccountActionsEnabled              *bool `json:"authIssueQueueEnabled,omitempty"`
	AccountActionsAutoDisable          *bool `json:"authIssueAutoDisableEnabled,omitempty"`
	ServerErrorPriorityDemotionEnabled *bool `json:"serverErrorPriorityDemotionEnabled,omitempty"`
	CodexReauthAutoUpdateEnabled       *bool `json:"codexReauthAutoUpdateEnabled,omitempty"`
}

type Service struct {
	cfg   config.Config
	store settingsStore

	mu        sync.RWMutex
	lastKnown store.AutomationSettings
	hasKnown  bool

	// updateMu serializes the read-modify-write in Update so concurrent PATCH
	// requests cannot overwrite each other's fields based on a stale snapshot.
	updateMu sync.Mutex
}

func New(cfg config.Config, st ...*store.Store) *Service {
	var storeRef settingsStore
	if len(st) > 0 {
		storeRef = st[0]
	}
	return &Service{cfg: cfg, store: storeRef}
}

// Status returns the effective account-processing policy. Unlike the runtime
// gating path, a read failure is surfaced to the caller so the UI does not
// silently show a stale/default state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	settings, _, err := s.loadSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	return s.statusFromSettings(settings), nil
}

func (s *Service) Update(ctx context.Context, req UpdateRequest) (Status, error) {
	if s.store == nil {
		return Status{}, errors.New("automation settings store is not configured")
	}
	// Hold updateMu across the full read-modify-write so two concurrent PATCH
	// requests (different admins / tabs / fields) cannot interleave and lose a
	// field based on a stale snapshot.
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	current, _, err := s.loadSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	if req.QuotaCooldownEnabled != nil {
		if s.cfg.QuotaCooldownEnvSet {
			return Status{}, errors.New("quotaCooldownEnabled is locked by environment variable")
		}
		current.QuotaCooldownEnabled = boolPtr(*req.QuotaCooldownEnabled)
	}
	if req.AccountActionsEnabled != nil {
		if s.cfg.AccountActionsEnvSet {
			return Status{}, errors.New("accountActionsEnabled is locked by environment variable")
		}
		current.AccountActionsEnabled = boolPtr(*req.AccountActionsEnabled)
	}
	if req.AccountActionsAutoDisable != nil {
		if s.cfg.AccountActionsAutoEnvSet {
			return Status{}, errors.New("accountActionsAutoDisable is locked by environment variable")
		}
		current.AccountActionsAutoDisable = boolPtr(*req.AccountActionsAutoDisable)
	}
	if req.ServerErrorPriorityDemotionEnabled != nil {
		if s.cfg.ServerErrorPriorityDemotionEnvSet {
			return Status{}, errors.New("serverErrorPriorityDemotionEnabled is locked by environment variable")
		}
		current.ServerErrorPriorityDemotionEnabled = boolPtr(*req.ServerErrorPriorityDemotionEnabled)
	}
	if req.CodexReauthAutoUpdateEnabled != nil {
		if s.cfg.CodexReauthAutoUpdateEnvSet {
			return Status{}, errors.New("codexReauthAutoUpdateEnabled is locked by environment variable")
		}
		current.CodexReauthAutoUpdateEnabled = boolPtr(*req.CodexReauthAutoUpdateEnabled)
	}
	// SaveAutomationSettings returns the record it persisted (including the
	// UpdatedAtMS it assigned). We build the response from that record instead
	// of re-reading, so a transient read failure after a successful save cannot
	// cause the persisted state and runtime cache to diverge.
	saved, err := s.store.SaveAutomationSettings(ctx, current)
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	s.lastKnown = saved
	s.hasKnown = true
	s.mu.Unlock()
	return s.statusFromSettings(saved), nil
}

// RuntimeSettings returns the effective booleans used to gate runtime workers.
// It never blocks the collector loop on a read failure: it logs the error and
// keeps the last known-good configuration (or startup defaults before the first
// successful load).
func (s *Service) RuntimeSettings(ctx context.Context) RuntimeSettings {
	settings, _, err := s.loadSettings(ctx)
	if err != nil {
		log.Printf("[account-processing-policy] failed to load runtime settings: %v; using last known configuration", err)
		s.mu.RLock()
		cached, hasCached := s.lastKnown, s.hasKnown
		s.mu.RUnlock()
		if hasCached {
			return s.runtimeFromSettings(cached)
		}
		return s.runtimeFromSettings(store.AutomationSettings{})
	}
	s.mu.Lock()
	s.lastKnown = settings
	s.hasKnown = true
	s.mu.Unlock()
	return s.runtimeFromSettings(settings)
}

type RuntimeSettings struct {
	QuotaCooldownEnabled               bool
	AccountActionsEnabled              bool
	AccountActionsAutoDisable          bool
	ServerErrorPriorityDemotionEnabled bool
	CodexReauthAutoUpdateEnabled       bool
}

func (s *Service) loadSettings(ctx context.Context) (store.AutomationSettings, bool, error) {
	if s == nil || s.store == nil {
		return store.AutomationSettings{}, false, nil
	}
	return s.store.LoadAutomationSettings(ctx)
}

type resolved struct {
	quotaValue, quotaLocked             bool
	quotaSource                         string
	accountValue, accountLocked         bool
	accountSource                       string
	autoConfigured, autoLocked          bool
	autoSource                          string
	serverErrorValue, serverErrorLocked bool
	serverErrorSource                   string
	codexReauthValue, codexReauthLocked bool
	codexReauthSource                   string
}

func (s *Service) resolve(settings store.AutomationSettings) resolved {
	quotaValue, quotaSource, quotaLocked := s.resolveField(settings.QuotaCooldownEnabled, s.cfg.QuotaCooldownEnabled, s.cfg.QuotaCooldownEnvSet)
	accountValue, accountSource, accountLocked := s.resolveField(settings.AccountActionsEnabled, s.cfg.AccountActionsEnabled, s.cfg.AccountActionsEnvSet)
	autoConfigured, autoSource, autoLocked := s.resolveField(settings.AccountActionsAutoDisable, s.cfg.AccountActionsAutoDisable, s.cfg.AccountActionsAutoEnvSet)
	serverErrorValue, serverErrorSource, serverErrorLocked := s.resolveField(settings.ServerErrorPriorityDemotionEnabled, s.cfg.ServerErrorPriorityDemotionEnabled, s.cfg.ServerErrorPriorityDemotionEnvSet)
	codexReauthValue, codexReauthSource, codexReauthLocked := s.resolveField(settings.CodexReauthAutoUpdateEnabled, s.cfg.CodexReauthAutoUpdateEnabled, s.cfg.CodexReauthAutoUpdateEnvSet)
	return resolved{
		quotaValue:        quotaValue,
		quotaSource:       quotaSource,
		quotaLocked:       quotaLocked,
		accountValue:      accountValue,
		accountSource:     accountSource,
		accountLocked:     accountLocked,
		autoConfigured:    autoConfigured,
		autoSource:        autoSource,
		autoLocked:        autoLocked,
		serverErrorValue:  serverErrorValue,
		serverErrorSource: serverErrorSource,
		serverErrorLocked: serverErrorLocked,
		codexReauthValue:  codexReauthValue,
		codexReauthSource: codexReauthSource,
		codexReauthLocked: codexReauthLocked,
	}
}

func (s *Service) statusFromSettings(settings store.AutomationSettings) Status {
	r := s.resolve(settings)
	autoEffective := r.accountValue && r.autoConfigured

	return Status{
		Source:      overallSource(r.quotaSource, r.accountSource, r.autoSource, r.serverErrorSource, r.codexReauthSource),
		UpdatedAtMS: settings.UpdatedAtMS,
		QuotaCooldown: Capability{
			Enabled:       r.quotaValue,
			Configured:    r.quotaValue,
			Source:        r.quotaSource,
			Locked:        r.quotaLocked,
			EnvKey:        "USAGE_QUOTA_COOLDOWN_ENABLED",
			ConfigFileKey: "quotaCooldownEnabled",
		},
		AccountActions: Capability{
			Enabled:       r.accountValue,
			Configured:    r.accountValue,
			Source:        r.accountSource,
			Locked:        r.accountLocked,
			EnvKey:        "USAGE_ACCOUNT_ACTIONS_ENABLED",
			ConfigFileKey: "accountActionsEnabled",
		},
		AccountActionsAutoDisable: Capability{
			Enabled:       autoEffective,
			Configured:    r.autoConfigured,
			Source:        r.autoSource,
			Locked:        r.autoLocked,
			EnvKey:        "USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE",
			ConfigFileKey: "accountActionsAutoDisable",
			DependsOn:     "authIssueQueue",
		},
		ServerErrorPriorityDemotion: Capability{
			Enabled:       r.serverErrorValue,
			Configured:    r.serverErrorValue,
			Source:        r.serverErrorSource,
			Locked:        r.serverErrorLocked,
			EnvKey:        "USAGE_SERVER_ERROR_PRIORITY_DEMOTION_ENABLED",
			ConfigFileKey: "serverErrorPriorityDemotionEnabled",
		},
		CodexReauthAutoUpdate: Capability{
			Enabled:       r.codexReauthValue,
			Configured:    r.codexReauthValue,
			Source:        r.codexReauthSource,
			Locked:        r.codexReauthLocked,
			EnvKey:        "USAGE_CODEX_REAUTH_AUTO_UPDATE_ENABLED",
			ConfigFileKey: "codexReauthAutoUpdateEnabled",
		},
	}
}

func (s *Service) runtimeFromSettings(settings store.AutomationSettings) RuntimeSettings {
	r := s.resolve(settings)
	return RuntimeSettings{
		QuotaCooldownEnabled:               r.quotaValue,
		AccountActionsEnabled:              r.accountValue,
		AccountActionsAutoDisable:          r.accountValue && r.autoConfigured,
		ServerErrorPriorityDemotionEnabled: r.serverErrorValue,
		CodexReauthAutoUpdateEnabled:       r.codexReauthValue,
	}
}

func (s *Service) resolveField(dbValue *bool, startupValue bool, envLocked bool) (bool, string, bool) {
	if envLocked {
		return startupValue, SourceEnv, true
	}
	if dbValue != nil {
		return *dbValue, SourceDB, false
	}
	return startupValue, SourceStartup, false
}

func overallSource(sources ...string) string {
	hasDB := false
	hasEnv := false
	for _, source := range sources {
		switch source {
		case SourceDB:
			hasDB = true
		case SourceEnv:
			hasEnv = true
		}
	}
	if hasDB {
		return SourceDB
	}
	if hasEnv {
		return SourceEnv
	}
	return SourceStartup
}

func boolPtr(value bool) *bool {
	return &value
}
