package credentialruntime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const defaultMaxConcurrentDownloads = 4

var (
	ErrRuntimeNotConfigured = errors.New("credential runtime metadata is not configured")
	ErrRuntimeUnavailable   = errors.New("credential runtime metadata dependencies are unavailable")
)

// Target identifies one visible credential row. ClientKey is panel-local only;
// it is never used as a CPA Core selector.
type Target struct {
	ClientKey    string `json:"clientKey"`
	FileName     string `json:"fileName"`
	AuthIndex    string `json:"authIndex,omitempty"`
	AccountEmail string `json:"accountEmail,omitempty"`
	Provider     string `json:"provider"`
}

// Item is the safe, redacted metadata projection for one visible credential.
// It intentionally contains neither the auth JSON nor any token material.
type Item struct {
	ClientKey    string                   `json:"clientKey"`
	ProxyURL     string                   `json:"proxyUrl,omitempty"`
	RecoveryTask *model.TokenRecoveryTask `json:"recoveryTask,omitempty"`
	ErrorCode    string                   `json:"errorCode,omitempty"`
}

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type AuthFileDownloader interface {
	Download(context.Context, string, string, string) ([]byte, error)
}

type RecoveryLookup interface {
	Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error)
}

type Options struct {
	SetupResolver          SetupResolver
	AuthFiles              AuthFileDownloader
	RecoveryLookup         RecoveryLookup
	MaxConcurrentDownloads int
}

type Service struct {
	setupResolver          SetupResolver
	authFiles              AuthFileDownloader
	recoveryLookup         RecoveryLookup
	maxConcurrentDownloads int
}

func NewWithOptions(options Options) *Service {
	authFiles := options.AuthFiles
	if authFiles == nil {
		authFiles = cpaauthfiles.New(nil)
	}
	maxConcurrentDownloads := options.MaxConcurrentDownloads
	if maxConcurrentDownloads <= 0 {
		maxConcurrentDownloads = defaultMaxConcurrentDownloads
	}
	return &Service{
		setupResolver:          options.SetupResolver,
		authFiles:              authFiles,
		recoveryLookup:         options.RecoveryLookup,
		maxConcurrentDownloads: maxConcurrentDownloads,
	}
}

// Lookup downloads each physical source file at most once, then projects only
// a selected record's proxy URL and redacted Codex recovery task. A malformed
// or absent sibling record is represented by that item's ErrorCode and never
// prevents metadata for another selected record in the same source file.
func (s *Service) Lookup(ctx context.Context, targets []Target) ([]Item, error) {
	items := make([]Item, len(targets))
	if len(targets) == 0 {
		return items, nil
	}

	groups := make(map[string][]int)
	normalizedTargets := make([]Target, len(targets))
	for index, target := range targets {
		target = normalizeTarget(target)
		normalizedTargets[index] = target
		items[index].ClientKey = target.ClientKey
		if target.ClientKey == "" || target.FileName == "" {
			items[index].ErrorCode = "credential_invalid"
			continue
		}
		groups[target.FileName] = append(groups[target.FileName], index)
	}
	if len(groups) == 0 {
		return items, nil
	}

	setup, err := s.resolveSetup(ctx)
	if err != nil {
		return nil, err
	}

	semaphore := make(chan struct{}, s.maxConcurrentDownloads)
	var groupWait sync.WaitGroup
	for fileName, indexes := range groups {
		fileName := fileName
		indexes := append([]int(nil), indexes...)
		groupWait.Add(1)
		go func() {
			defer groupWait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				for _, index := range indexes {
					items[index].ErrorCode = "upstream_unavailable"
				}
				return
			}
			defer func() { <-semaphore }()

			raw, downloadErr := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
			if downloadErr != nil {
				for _, index := range indexes {
					items[index].ErrorCode = metadataErrorCode(downloadErr)
				}
				return
			}
			for _, index := range indexes {
				s.projectItem(ctx, &items[index], normalizedTargets[index], raw)
			}
		}()
	}
	groupWait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) projectItem(ctx context.Context, item *Item, target Target, raw []byte) {
	proxyURL, err := tokenrecovery.ReadProxyURL(raw, tokenrecovery.Locator{
		AuthIndex:    target.AuthIndex,
		AccountEmail: target.AccountEmail,
	})
	if err != nil {
		item.ErrorCode = metadataErrorCode(err)
		return
	}
	item.ProxyURL = proxyURL
	if target.Provider != "codex" {
		return
	}
	if s.recoveryLookup == nil {
		item.ErrorCode = "recovery_unavailable"
		return
	}
	task, found, err := s.recoveryLookup.Get(ctx, model.TokenRecoveryTarget{
		FileName:     target.FileName,
		AuthIndex:    target.AuthIndex,
		AccountEmail: target.AccountEmail,
		Provider:     target.Provider,
	})
	if err != nil {
		item.ErrorCode = "recovery_unavailable"
		return
	}
	if found {
		item.RecoveryTask = &task
	}
}

func (s *Service) resolveSetup(ctx context.Context) (store.Setup, error) {
	if s == nil || s.setupResolver == nil || s.authFiles == nil {
		return store.Setup{}, ErrRuntimeUnavailable
	}
	setup, ok, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil {
		return store.Setup{}, ErrRuntimeUnavailable
	}
	if !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return store.Setup{}, ErrRuntimeNotConfigured
	}
	return setup, nil
}

func normalizeTarget(target Target) Target {
	target.ClientKey = strings.TrimSpace(target.ClientKey)
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.AccountEmail = strings.ToLower(strings.TrimSpace(target.AccountEmail))
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	return target
}

func metadataErrorCode(err error) string {
	switch {
	case errors.Is(err, tokenrecovery.ErrCredentialNotFound),
		errors.Is(err, tokenrecovery.ErrCredentialEmailMismatch),
		errors.Is(err, cpaauthfiles.ErrAuthFileNotFound):
		return "credential_not_found"
	case errors.Is(err, tokenrecovery.ErrCredentialAmbiguous), errors.Is(err, cpaauthfiles.ErrAuthFileAmbiguous):
		return "credential_ambiguous"
	case errors.Is(err, tokenrecovery.ErrCredentialInvalid):
		return "credential_invalid"
	default:
		return "upstream_unavailable"
	}
}
