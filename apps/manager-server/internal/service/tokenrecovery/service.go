package tokenrecovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	tokenrecoveryrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenacquisition"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultIdlePollInterval            = 500 * time.Millisecond
	defaultWorkerCount                 = 1
	maxWorkerCount                     = 40
	defaultWriteConcurrency            = 4
	maxWriteConcurrency                = maxWorkerCount
	DefaultCPAAuthFileOperationTimeout = 4 * time.Minute
)

var (
	ErrRecoveryNotConfigured       = errors.New("token recovery is not configured")
	ErrRecoveryUnavailable         = errors.New("token recovery dependencies are unavailable")
	ErrCPAAuthFileOperationTimeout = errors.New("CPA auth-file operation timed out")
)

// IsTerminalAccountBanned reports whether the latest redacted recovery task
// proves that the account is permanently banned. It intentionally accepts only
// terminal failure states and the bounded account_banned category emitted by
// Token Acquisition; ordinary re-login failures must remain recoverable.
func IsTerminalAccountBanned(task model.TokenRecoveryTask) bool {
	switch task.Status {
	case model.TokenRecoveryStatusAutoFailedManualOnly, model.TokenRecoveryStatusManualFailedManualOnly:
	default:
		return false
	}
	if strings.EqualFold(strings.TrimSpace(task.LastErrorCode), "account_banned") {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(task.LastErrorMessage))
	const prefix = "account_banned"
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	rest := strings.TrimPrefix(message, prefix)
	if rest == "" || strings.HasPrefix(rest, ":") {
		return true
	}
	return unicode.IsSpace(rune(rest[0]))
}

// TaskStore is the narrow durable state surface used by recovery. It keeps the
// scheduler independent from browser/API wiring and deliberately exposes only
// redacted task records.
type TaskStore interface {
	SignalTokenRecoveryAutomatic(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, error)
	RequestTokenRecoveryManual(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, error)
	GetTokenRecovery(context.Context, store.TokenRecoveryTarget) (store.TokenRecoveryTask, bool, error)
	GetTokenRecoveryByID(context.Context, int64) (store.TokenRecoveryTask, bool, error)
	ClaimNextTokenRecovery(context.Context) (store.TokenRecoveryTask, bool, error)
	ClaimNextEligibleTokenRecovery(context.Context, bool) (store.TokenRecoveryTask, bool, error)
	CompleteTokenRecovery(context.Context, int64) (store.TokenRecoveryTask, error)
	FailTokenRecovery(context.Context, int64, string, string) (store.TokenRecoveryTask, error)
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
	// WorkerCount bounds concurrent external token acquisition. The live app
	// deliberately supplies 40; callers without an explicit setting retain the
	// historical single-worker behavior.
	WorkerCount int
	// AutomaticEnabled is re-evaluated before every claim. A nil callback keeps
	// the historical raw-service behavior (automatic work enabled); the live
	// application supplies the persisted policy callback.
	AutomaticEnabled func(context.Context) bool
	// WriteConcurrency bounds cross-account CPA Core auth-file mutations after
	// token acquisition has completed. Zero retains the production-safe default.
	WriteConcurrency int
}

type Service struct {
	tasks               TaskStore
	setupResolver       SetupResolver
	authFiles           AuthFileClient
	acquirer            tokenacquisition.Acquirer
	mutationCoordinator *cpaauthfiles.MutationCoordinator
	idlePollInterval    time.Duration
	workerCount         int
	writeGate           chan struct{}
	automaticEnabled    func(context.Context) bool
	wake                chan struct{}
	startOnce           sync.Once
}

type cpaAuthFileOperationTimeoutError struct {
	operation string
	err       error
}

func (e *cpaAuthFileOperationTimeoutError) Error() string {
	if e == nil {
		return ErrCPAAuthFileOperationTimeout.Error()
	}
	return fmt.Sprintf("%s: %s", e.operation, ErrCPAAuthFileOperationTimeout)
}

func (e *cpaAuthFileOperationTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *cpaAuthFileOperationTimeoutError) Is(target error) bool {
	return target == ErrCPAAuthFileOperationTimeout
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
	workerCount := options.WorkerCount
	if workerCount <= 0 {
		workerCount = defaultWorkerCount
	}
	if workerCount > maxWorkerCount {
		workerCount = maxWorkerCount
	}
	writeConcurrency := options.WriteConcurrency
	if writeConcurrency <= 0 {
		writeConcurrency = defaultWriteConcurrency
	}
	if writeConcurrency > maxWriteConcurrency {
		writeConcurrency = maxWriteConcurrency
	}
	return &Service{
		tasks:               options.Tasks,
		setupResolver:       options.SetupResolver,
		authFiles:           authFiles,
		acquirer:            options.Acquirer,
		mutationCoordinator: coordinator,
		idlePollInterval:    idlePollInterval,
		workerCount:         workerCount,
		writeGate:           make(chan struct{}, writeConcurrency),
		automaticEnabled:    options.AutomaticEnabled,
		wake:                make(chan struct{}, workerCount),
	}
}

// Start resumes queued work and turns interrupted running work into a
// manual-only failure. It intentionally never replays an in-flight external
// acquisition after a process restart.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.startOnce.Do(func() {
		// This recovery fence is process-wide. Running it in every worker would
		// race the workers that have already claimed new tasks.
		if s.tasks != nil {
			_, _ = s.tasks.FailRunningTokenRecoveriesOnStartup(ctx)
		}
		for worker := 0; worker < s.workerCount; worker++ {
			// Every worker may claim manual recovery. Only worker zero may claim an
			// automatic task when no manual task is waiting, preserving the former
			// single-worker background behavior while giving the current-page
			// operator action up to 40 bounded slots.
			go s.run(ctx, worker == 0)
		}
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

func (s *Service) run(ctx context.Context, mayClaimAutomatic bool) {
	if s.tasks == nil {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		allowAutomatic := mayClaimAutomatic
		if allowAutomatic && s.automaticEnabled != nil {
			allowAutomatic = s.automaticEnabled(ctx)
		}
		task, found, err := s.tasks.ClaimNextEligibleTokenRecovery(ctx, allowAutomatic)
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
	_, _ = s.tasks.FailTokenRecovery(ctx, task.ID, recoveryFailureCode(err), recoveryFailureMessage(err))
}

func (s *Service) recover(ctx context.Context, task model.TokenRecoveryTask) error {
	if s.setupResolver == nil || s.authFiles == nil || s.acquirer == nil || s.mutationCoordinator == nil {
		return ErrRecoveryUnavailable
	}
	locator := Locator{AuthIndex: task.AuthIndex, AccountEmail: task.AccountEmail}
	credential, resolvedLocator, err := s.readCurrentCredential(ctx, task.FileName, locator)
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
	return s.writeCurrentCredential(ctx, task.FileName, resolvedLocator, credential.Email, AcquisitionResult{
		Email:            acquired.Email,
		AccessToken:      acquired.AccessToken,
		RefreshToken:     acquired.RefreshToken,
		IDToken:          acquired.IDToken,
		ChatGPTAccountID: acquired.ChatGPTAccountID,
	})
}

func (s *Service) readCurrentCredential(ctx context.Context, fileName string, locator Locator) (Credential, Locator, error) {
	release, err := s.mutationCoordinator.Acquire(ctx, fileName)
	if err != nil {
		return Credential{}, Locator{}, err
	}
	defer release()
	setup, err := s.resolveSetup(ctx)
	if err != nil {
		return Credential{}, Locator{}, err
	}
	raw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
	if err != nil {
		return Credential{}, Locator{}, classifyCPAAuthFileOperationError("initial_read", err)
	}
	credential, resolvedLocator, err := readCredentialWithStaleAuthIndexFallback(raw, locator)
	if err != nil {
		return Credential{}, Locator{}, err
	}
	return credential, resolvedLocator, nil
}

// readCredentialWithStaleAuthIndexFallback preserves the original auth-index
// locator whenever it still resolves. A durable recovery task can outlive a
// CPA credential import, however, and imports may assign a new auth_index to
// the same physical file and email. In that narrow case, fall back to an
// email-only lookup. ReadCredential accepts it only when exactly one current
// record matches, so an array with duplicate emails remains ambiguous and is
// never mutated.
func readCredentialWithStaleAuthIndexFallback(raw []byte, locator Locator) (Credential, Locator, error) {
	credential, err := ReadCredential(raw, locator)
	if err == nil {
		return credential, locator, nil
	}
	if !errors.Is(err, ErrCredentialNotFound) ||
		strings.TrimSpace(locator.AuthIndex) == "" ||
		normalizeEmail(locator.AccountEmail) == "" ||
		authIndexPresentInAuthDocument(raw, locator.AuthIndex) {
		return Credential{}, Locator{}, err
	}

	fallback := Locator{AccountEmail: locator.AccountEmail}
	credential, fallbackErr := ReadCredential(raw, fallback)
	if fallbackErr != nil {
		return Credential{}, Locator{}, fallbackErr
	}
	return credential, fallback, nil
}

// authIndexPresentInAuthDocument distinguishes a genuinely stale locator from
// an index that still resolves to a malformed record. The latter must fail
// closed: falling back by email could otherwise select a different record in
// the same array and overwrite its tokens.
func authIndexPresentInAuthDocument(raw []byte, authIndex string) bool {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false
	}
	root, err := decodeAuthDocumentRoot(raw)
	if err != nil {
		return false
	}
	switch typed := root.(type) {
	case map[string]any:
		return firstNonEmptyField(typed, authIndexFieldNames...) == authIndex
	case []any:
		for _, value := range typed {
			record, ok := value.(map[string]any)
			if ok && firstNonEmptyField(record, authIndexFieldNames...) == authIndex {
				return true
			}
		}
	}
	return false
}

func (s *Service) writeCurrentCredential(ctx context.Context, fileName string, locator Locator, originalEmail string, result AcquisitionResult) error {
	if err := s.acquireWriteSlot(ctx); err != nil {
		return err
	}
	defer s.releaseWriteSlot()

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
		return classifyCPAAuthFileOperationError("write_read", err)
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
	if err := s.authFiles.Upload(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName, merged); err != nil {
		return classifyCPAAuthFileOperationError("upload", err)
	}
	verifiedRaw, err := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, fileName)
	if err != nil {
		return classifyCPAAuthFileOperationError("verify", err)
	}
	return VerifyMergedAuthJSON(verifiedRaw, mergeLocator, result)
}

func (s *Service) acquireWriteSlot(ctx context.Context) error {
	if s == nil || s.writeGate == nil {
		return nil
	}
	select {
	case s.writeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseWriteSlot() {
	if s == nil || s.writeGate == nil {
		return
	}
	<-s.writeGate
}

func classifyCPAAuthFileOperationError(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &cpaAuthFileOperationTimeoutError{operation: operation, err: err}
	}
	return err
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
	for worker := 0; worker < s.workerCount; worker++ {
		select {
		case s.wake <- struct{}{}:
		default:
			return
		}
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
	case errors.Is(err, ErrCPAAuthFileOperationTimeout):
		return "cpa_auth_file_timeout"
	case errors.Is(err, cpaauthfiles.ErrMutationCoordinatorUnavailable):
		return "mutation_unavailable"
	default:
		return "recovery_failed"
	}
}

func recoveryFailureMessage(err error) string {
	if errors.Is(err, ErrCPAAuthFileOperationTimeout) {
		var timeoutErr *cpaAuthFileOperationTimeoutError
		if errors.As(err, &timeoutErr) {
			switch timeoutErr.operation {
			case "initial_read", "write_read":
				return "CPA Core 凭证读取超时：当前读取繁忙，本次未提交更新"
			case "upload":
				return "CPA Core 凭证写入超时：当前写入繁忙，写入结果待确认"
			case "verify":
				return "CPA Core 凭证复核超时：写入可能已完成，请刷新后核实"
			}
		}
		return "CPA Core 凭证文件操作超时：请稍后重试"
	}
	return tokenacquisition.FailureReason(err)
}
