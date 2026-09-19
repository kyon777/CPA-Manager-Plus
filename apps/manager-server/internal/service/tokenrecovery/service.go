package tokenrecovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	tokenrecoveryrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenacquisition"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const defaultIdlePollInterval = 500 * time.Millisecond

var (
	ErrRecoveryNotConfigured = errors.New("token recovery is not configured")
	ErrRecoveryUnavailable   = errors.New("token recovery dependencies are unavailable")
)

// TaskStore is the narrow durable state surface used by recovery. It keeps the
// scheduler independent from browser/API wiring and deliberately exposes only
// redacted task records.
type TaskStore interface {
	SignalTokenRecoveryAutomatic(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, error)
	RequestTokenRecoveryManual(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, error)
	GetTokenRecovery(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, bool, error)
	GetTokenRecoveryByID(context.Context, int64) (store.TokenRecoveryTask, bool, error)
	ClaimNextTokenRecovery(context.Context) (store.TokenRecoveryTask, bool, error)
	CompleteTokenRecovery(context.Context, int64) (store.TokenRecoveryTask, error)
	FailTokenRecovery(context.Context, int64, string) (store.TokenRecoveryTask, error)
	FailRunningTokenRecoveriesOnStartup(context.Context) (int64, error)
}

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type AuthFileClient interface {
	Download(context.Context, string, string, string) ([]byte, error)
	Upload(context.Context, string, string, string, []byte) error
}

type Options struct {
	Tasks               TaskStore
	SetupResolver       SetupResolver
	AuthFiles           AuthFileClient
	Acquirer            tokenacquisition.Acquirer
	MutationCoordinator *cpaauthfiles.MutationCoordinator
	IdlePollInterval    time.Duration
}

type Service struct {
	tasks               TaskStore
	setupResolver       SetupResolver
	authFiles           AuthFileClient
	acquirer            tokenacquisition.Acquirer
	mutationCoordinator *cpaauthfiles.MutationCoordinator
	idlePollInterval    time.Duration
	wake                chan struct{}
	startOnce           sync.Once
}

func NewWithOptions(options Options) *Service {
	coordinator := options.MutationCoordinator
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	authFiles := options.AuthFiles
	if authFiles == nil {
		authFiles = cpaauthfiles.New(nil)
	}
	idlePollInterval := options.IdlePollInterval
	if idlePollInterval <= 0 {
		idlePollInterval = defaultIdlePollInterval
	}
	return &Service{
		tasks:               options.Tasks,
		setupResolver:       options.SetupResolver,
		authFiles:           authFiles,
		acquirer:            options.Acquirer,
		mutationCoordinator: coordinator,
		idlePollInterval:    idlePollInterval,
		wake:                make(chan struct{}, 1),
	}
}

// Start resumes queued work and turns interrupted running work into a
// manual-only failure. It intentionally never replays an in-flight external
// acquisition after a process restart.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.run(ctx)
	})
}

func (s *Service) SignalAutomatic(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	if s == nil || s.tasks == nil {
		return model.TokenRecoveryTask{}, ErrRecoveryUnavailable
	}
	task, err := s.tasks.SignalTokenRecoveryAutomatic(ctx, target)
	if err == nil {
		s.notify()
	}
	return task, err
}

func (s *Service) RequestManual(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	if s == nil || s.tasks == nil {
		return model.TokenRecoveryTask{}, ErrRecoveryUnavailable
	}
	task, err := s.tasks.RequestTokenRecoveryManual(ctx, target)
	if err == nil {
		s.notify()
	}
	return task, err
}

func (s *Service) Get(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	if s == nil || s.tasks == nil {
		return model.TokenRecoveryTask{}, false, ErrRecoveryUnavailable
	}
	return s.tasks.GetTokenRecovery(ctx, target)
}

func (s *Service) GetByID(ctx context.Context, id int64) (model.TokenRecoveryTask, bool, error) {
	if s == nil || s.tasks == nil {
		return model.TokenRecoveryTask{}, false, ErrRecoveryUnavailable
	}
	return s.tasks.GetTokenRecoveryByID(ctx, id)
}

func (s *Service) run(ctx context.Context) {
	if s.tasks == nil {
		return
	}
	_, _ = s.tasks.FailRunningTokenRecoveriesOnStartup(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		task, found, err := s.tasks.ClaimNextTokenRecovery(ctx)
		if err != nil {
			s.wait(ctx)
			continue
		}
		if !found {
			s.wait(ctx)
			continue
		}
		s.process(ctx, task)
	}
}

func (s *Service) process(ctx context.Context, task model.TokenRecoveryTask) {
	err := s.recover(ctx, task)
	if err == nil {
		_, _ = s.tasks.CompleteTokenRecovery(ctx, task.ID)
		return
	}
	if ctx.Err() != nil {
		// Startup converts a left-running task to manual-only. This avoids a
		// second external POST if shutdown interrupted a request or upload.
		return
	}
	_, _ = s.tasks.FailTokenRecovery(ctx, task.ID, recoveryFailureCode(err))
}

func (s *Service) recover(ctx context.Context, task model.TokenRecoveryTask) error {
	if s.setupResolver == nil || s.authFiles == nil || s.acquirer == nil || s.mutationCoordinator == nil {
		return ErrRecoveryUnavailable
	}
	locator := Locator{AuthIndex: task.AuthIndex, AccountEmail: task.AccountEmail}
	credential, _, err := s.readCurrentCredential(ctx, task.FileName, locator)
	if err != nil {
		return err
	}
	acquired, err := s.acquirer.Acquire(ctx, tokenacquisition.Request{
		Email:     credential.Email,
		HTTPProxy: credential.HTTPProxy,
	})
	if err != nil {
		return err
	}
	return s.writeCurrentCredential(ctx, task.FileName, locator, credential.Email, AcquisitionResult{
		Email:            acquired.Email,
		AccessToken:      acquired.AccessToken,
		RefreshToken:     acquired.RefreshToken,
		IDToken:          acquired.IDToken,
		ChatGPTAccountID: acquired.ChatGPTAccountID,
	})
}

func (s *Service) readCurrentCredential(ctx context.Context, fileName string, locator Locator) (Credential, store.Setup, error) {
	release, err := s.mutationCoordinator.Acquire(ctx, fileName)
	if err != nil {
		return Credential{}, store.Setup{}, err
	}
	defer release()
	setup, err := s.resolveSetup(ctx)
	if err != nil {
		return Credential{}, store.Setup{}, err
	}
	raw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
	if err != nil {
		return Credential{}, store.Setup{}, err
	}
	credential, err := ReadCredential(raw, locator)
	if err != nil {
		return Credential{}, store.Setup{}, err
	}
	return credential, setup, nil
}

func (s *Service) writeCurrentCredential(ctx context.Context, fileName string, locator Locator, originalEmail string, result AcquisitionResult) error {
	release, err := s.mutationCoordinator.Acquire(ctx, fileName)
	if err != nil {
		return err
	}
	defer release()
	setup, err := s.resolveSetup(ctx)
	if err != nil {
		return err
	}
	raw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
	if err != nil {
		return err
	}
	current, err := ReadCredential(raw, locator)
	if err != nil {
		return err
	}
	if normalizeEmail(current.Email) != normalizeEmail(originalEmail) {
		return ErrCredentialEmailMismatch
	}
	mergeLocator := Locator{AuthIndex: locator.AuthIndex, AccountEmail: originalEmail}
	merged, err := MergeAuthJSON(raw, mergeLocator, result)
	if err != nil {
		return err
	}
	if err := s.authFiles.Upload(ctx, setup.CPAUpstreamURL, setup.ManagementKey, strings.TrimSpace(fileName), merged); err != nil {
		return err
	}
	verifiedRaw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
	if err != nil {
		return err
	}
	return VerifyMergedAuthJSON(verifiedRaw, mergeLocator, result)
}

func (s *Service) resolveSetup(ctx context.Context) (store.Setup, error) {
	setup, ok, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil {
		return store.Setup{}, err
	}
	if !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return store.Setup{}, ErrRecoveryNotConfigured
	}
	return setup, nil
}

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) wait(ctx context.Context) {
	timer := time.NewTimer(s.idlePollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-timer.C:
	}
}

func recoveryFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrRecoveryNotConfigured), errors.Is(err, tokenacquisition.ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, tokenrecoveryrepo.ErrInvalidTarget):
		return "invalid_target"
	case errors.Is(err, ErrCredentialNotFound), errors.Is(err, cpaauthfiles.ErrAuthFileNotFound):
		return "credential_not_found"
	case errors.Is(err, ErrCredentialAmbiguous), errors.Is(err, cpaauthfiles.ErrAuthFileAmbiguous):
		return "credential_ambiguous"
	case errors.Is(err, ErrCredentialEmailMismatch):
		return "email_mismatch"
	case errors.Is(err, ErrIncompleteTokenResult), errors.Is(err, tokenacquisition.ErrInvalidResult):
		return "invalid_token_result"
	case errors.Is(err, tokenacquisition.ErrConflict):
		return "token_acquisition_conflict"
	case errors.Is(err, tokenacquisition.ErrTimedOut):
		return "token_acquisition_timeout"
	case errors.Is(err, tokenacquisition.ErrJobFailed):
		return "token_acquisition_failed"
	case errors.Is(err, tokenacquisition.ErrRequestFailed):
		return "token_acquisition_request_failed"
	case errors.Is(err, cpaauthfiles.ErrMutationCoordinatorUnavailable):
		return "mutation_unavailable"
	default:
		return "recovery_failed"
	}
}
