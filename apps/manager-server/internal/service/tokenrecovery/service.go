package tokenrecovery

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	// Idle workers are woken immediately when a manual or automatic recovery is
	// queued. Keep the fallback poll deliberately coarse so an empty 80-worker
	// pool does not turn into hundreds of SQLite claims per second.
	defaultIdlePollInterval                = 5 * time.Second
	defaultWorkerCount                     = 1
	maxWorkerCount                         = 80
	defaultAcquisitionConcurrency          = 40
	maxAcquisitionConcurrency              = defaultAcquisitionConcurrency
	defaultWriteConcurrency                = 8
	maxWriteConcurrency                    = defaultWriteConcurrency
	DefaultCPAAuthFileOperationTimeout     = 4 * time.Minute
	RecommendedTokenAcquisitionWorkerCount = 80
	DefaultTokenRecoveryTaskTimeout        = 60 * time.Minute
	defaultStaleTaskReconcileInterval      = time.Minute
	terminalStateWriteAttempts             = 4
	terminalStateWriteInitialBackoff       = 25 * time.Millisecond
	terminalStateWriteTimeout              = 30 * time.Second
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
	FailStaleRunningTokenRecoveries(context.Context, int64) (int64, error)
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
	// supplies RecommendedTokenAcquisitionWorkerCount while the operator may
	// still queue a full current page immediately; callers without an explicit
	// setting retain the historical single-worker behavior. Active external
	// acquisitions are separately bounded by AcquisitionConcurrency.
	WorkerCount int
	// AcquisitionConcurrency bounds active Token Acquisition calls. Zero uses
	// the upstream service's 40-job capacity; a larger request is clamped.
	AcquisitionConcurrency int
	// AutomaticEnabled is re-evaluated before every claim. A nil callback keeps
	// the historical raw-service behavior (automatic work enabled); the live
	// application supplies the persisted policy callback.
	AutomaticEnabled func(context.Context) bool
	// WriteConcurrency bounds cross-account CPA Core auth-file mutations after
	// token acquisition has completed. Zero retains the production-safe default.
	WriteConcurrency int
	// TaskTimeout bounds one complete recovery attempt, including the external
	// Token Acquisition call and CPA Core write/verification. A timeout cancels
	// the in-flight request and records a manual-review terminal state; it never
	// starts a second acquisition.
	TaskTimeout time.Duration
}

type Service struct {
	tasks                    TaskStore
	setupResolver            SetupResolver
	authFiles                AuthFileClient
	acquirer                 tokenacquisition.Acquirer
	mutationCoordinator      *cpaauthfiles.MutationCoordinator
	idlePollInterval         time.Duration
	workerCount              int
	acquisitionGate          chan struct{}
	writeGate                chan struct{}
	automaticEnabled         func(context.Context) bool
	taskTimeout              time.Duration
	staleTaskReconcilePeriod time.Duration
	wake                     chan struct{}
	startOnce                sync.Once
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

// cpaAuthFileOperationError retains only the recovery phase and an optional
// HTTP status. Its Error method deliberately does not reveal the underlying
// CPA response, credential contents, management key, or proxy URL.
type cpaAuthFileOperationError struct {
	operation  string
	statusCode int
	err        error
}

func (e *cpaAuthFileOperationError) Error() string {
	if e == nil {
		return "CPA auth-file operation failed"
	}
	if e.statusCode >= 300 && e.statusCode <= 999 {
		return fmt.Sprintf("%s: CPA auth-file operation failed: HTTP %d", e.operation, e.statusCode)
	}
	return fmt.Sprintf("%s: CPA auth-file operation failed", e.operation)
}

func (e *cpaAuthFileOperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
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
	acquisitionConcurrency := options.AcquisitionConcurrency
	if acquisitionConcurrency <= 0 {
		acquisitionConcurrency = defaultAcquisitionConcurrency
	}
	if acquisitionConcurrency > maxAcquisitionConcurrency {
		acquisitionConcurrency = maxAcquisitionConcurrency
	}
	writeConcurrency := options.WriteConcurrency
	if writeConcurrency <= 0 {
		writeConcurrency = defaultWriteConcurrency
	}
	if writeConcurrency > maxWriteConcurrency {
		writeConcurrency = maxWriteConcurrency
	}
	taskTimeout := options.TaskTimeout
	if taskTimeout <= 0 {
		taskTimeout = DefaultTokenRecoveryTaskTimeout
	}
	return &Service{
		tasks:                    options.Tasks,
		setupResolver:            options.SetupResolver,
		authFiles:                authFiles,
		acquirer:                 options.Acquirer,
		mutationCoordinator:      coordinator,
		idlePollInterval:         idlePollInterval,
		workerCount:              workerCount,
		acquisitionGate:          make(chan struct{}, acquisitionConcurrency),
		writeGate:                make(chan struct{}, writeConcurrency),
		automaticEnabled:         options.AutomaticEnabled,
		taskTimeout:              taskTimeout,
		staleTaskReconcilePeriod: defaultStaleTaskReconcileInterval,
		wake:                     make(chan struct{}, workerCount),
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
			startupCtx, cancel := terminalStatePersistenceContext(ctx)
			if err := retryTerminalStateWrite(startupCtx, func() error {
				_, err := s.tasks.FailRunningTokenRecoveriesOnStartup(startupCtx)
				return err
			}); err != nil {
				log.Printf("[token-recovery] startup running-task fence failed")
			}
			cancel()
			go s.runStaleTaskReconciler(ctx)
		}
		for worker := 0; worker < s.workerCount; worker++ {
			// Every worker may claim manual or automatic recovery. The repository
			// atomically assigns each task and keeps manual work ahead of automatic
			// work, while the runtime switch still gates automatic task claims.
			go s.run(ctx, true)
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
		taskCtx, cancelTask := context.WithTimeout(ctx, s.taskTimeout)
		s.process(ctx, taskCtx, task)
		cancelTask()
	}
}

func (s *Service) process(lifecycleCtx context.Context, taskCtx context.Context, task model.TokenRecoveryTask) {
	err := s.recover(taskCtx, task)
	if err == nil {
		if lifecycleCtx.Err() != nil {
			return
		}
		persistCtx, cancel := terminalStatePersistenceContext(lifecycleCtx)
		defer cancel()
		if persistErr := s.persistCompletedTask(persistCtx, task.ID); persistErr != nil {
			log.Printf("[token-recovery] task_id=%d completed but terminal state was not persisted", task.ID)
		}
		return
	}
	if lifecycleCtx.Err() != nil {
		// Startup converts a left-running task to manual-only. This avoids a
		// second external POST if shutdown interrupted a request or upload.
		return
	}
	code := recoveryFailureCode(err)
	message := recoveryFailureMessage(err)
	if taskCtx.Err() != nil {
		// The per-task deadline cancels Token Acquisition and every CPA Core
		// operation before the row is terminalized. No automatic retry is made.
		code = "recovery_timeout"
		message = "\u6062\u590d\u4efb\u52a1\u8d85\u8fc7\u6700\u5927\u6267\u884c\u65f6\u95f4\uff0c\u672a\u81ea\u52a8\u91cd\u8bd5\uff0c\u8bf7\u4eba\u5de5\u6838\u67e5"
	}
	// code and message are both deliberately redacted, bounded task fields;
	// never log the raw error because it can contain upstream or proxy details.
	log.Printf("[token-recovery] task_id=%d failed code=%s reason=%q", task.ID, code, message)
	persistCtx, cancel := terminalStatePersistenceContext(lifecycleCtx)
	defer cancel()
	if persistErr := s.persistFailedTask(persistCtx, task.ID, code, message); persistErr != nil {
		log.Printf("[token-recovery] task_id=%d failed but terminal state was not persisted", task.ID)
	}
}

func (s *Service) runStaleTaskReconciler(ctx context.Context) {
	interval := s.staleTaskReconcilePeriod
	if interval <= 0 {
		interval = defaultStaleTaskReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.reconcileStaleRunningTasks(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[token-recovery] stale-task reconciliation failed")
			}
		}
	}
}

func (s *Service) reconcileStaleRunningTasks(ctx context.Context) (int64, error) {
	if s == nil || s.tasks == nil {
		return 0, ErrRecoveryUnavailable
	}
	cutoff := time.Now().Add(-s.taskTimeout).UnixMilli()
	persistCtx, cancel := terminalStatePersistenceContext(ctx)
	defer cancel()
	var changed int64
	err := retryTerminalStateWrite(persistCtx, func() error {
		var err error
		changed, err = s.tasks.FailStaleRunningTokenRecoveries(persistCtx, cutoff)
		return err
	})
	if err == nil && changed > 0 {
		log.Printf("[token-recovery] marked stale running tasks for manual review: count=%d", changed)
	}
	return changed, err
}

// persistCompletedTask retries only the terminal SQLite transition. It never
// repeats token acquisition or CPA auth-file writes, so a transient database
// lock cannot spend another recovery request or mutate a credential twice.
func (s *Service) persistCompletedTask(ctx context.Context, taskID int64) error {
	if s == nil || s.tasks == nil {
		return ErrRecoveryUnavailable
	}
	return retryTerminalStateWrite(ctx, func() error {
		_, err := s.tasks.CompleteTokenRecovery(ctx, taskID)
		return err
	})
}

// persistFailedTask persists the already-classified terminal failure after the
// external recovery attempt has ended. Retrying is restricted to SQLite lock
// contention and preserves the original redacted failure category.
func (s *Service) persistFailedTask(ctx context.Context, taskID int64, code, message string) error {
	if s == nil || s.tasks == nil {
		return ErrRecoveryUnavailable
	}
	return retryTerminalStateWrite(ctx, func() error {
		_, err := s.tasks.FailTokenRecovery(ctx, taskID, code, message)
		return err
	})
}

func terminalStatePersistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), terminalStateWriteTimeout)
}

func retryTerminalStateWrite(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < terminalStateWriteAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation()
		if lastErr == nil || !tokenrecoveryrepo.IsSQLiteBusyError(lastErr) {
			return lastErr
		}
		if attempt+1 == terminalStateWriteAttempts {
			break
		}
		timer := time.NewTimer(terminalStateWriteInitialBackoff << attempt)
		select {
		case <-ctx.Done():
			stopTerminalStateRetryTimer(timer)
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func stopTerminalStateRetryTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
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
	if err := s.acquireAcquisitionSlot(ctx); err != nil {
		return err
	}
	acquired, err := s.acquirer.Acquire(ctx, tokenacquisition.Request{
		Email:     credential.Email,
		HTTPProxy: credential.HTTPProxy,
	})
	s.releaseAcquisitionSlot()
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

func (s *Service) acquireAcquisitionSlot(ctx context.Context) error {
	if s == nil || s.acquisitionGate == nil {
		return nil
	}
	select {
	case s.acquisitionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseAcquisitionSlot() {
	if s == nil || s.acquisitionGate == nil {
		return
	}
	<-s.acquisitionGate
}

func (s *Service) releaseWriteSlot() {
	if s == nil || s.writeGate == nil {
		return
	}
	<-s.writeGate
}

func classifyCPAAuthFileOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &cpaAuthFileOperationTimeoutError{operation: operation, err: err}
	}
	statusCode := 0
	var operationErr *cpaauthfiles.OperationError
	if errors.As(err, &operationErr) && operationErr != nil {
		statusCode = operationErr.StatusCode
	}
	return &cpaAuthFileOperationError{operation: operation, statusCode: statusCode, err: err}
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
	case errors.Is(err, tokenacquisition.ErrTransportFailed):
		return "token_acquisition_transport_failed"
	case errors.Is(err, tokenacquisition.ErrJobFailed):
		return "token_acquisition_failed"
	case errors.Is(err, tokenacquisition.ErrRequestFailed):
		return "token_acquisition_request_failed"
	case errors.Is(err, ErrCPAAuthFileOperationTimeout):
		return "cpa_auth_file_timeout"
	case errors.Is(err, cpaauthfiles.ErrMutationCoordinatorUnavailable):
		return "mutation_unavailable"
	}
	if operationErr := recoveryCPAAuthFileOperation(err); operationErr != nil {
		switch operationErr.operation {
		case "initial_read", "write_read":
			return "cpa_auth_file_read_failed"
		case "upload":
			return "cpa_auth_file_upload_failed"
		case "verify":
			return "cpa_auth_file_verify_failed"
		default:
			return "cpa_auth_file_operation_failed"
		}
	}
	return "recovery_failed"
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
	if operationErr := recoveryCPAAuthFileOperation(err); operationErr != nil {
		return recoveryCPAAuthFileOperationMessage(operationErr)
	}
	if reason := tokenacquisition.FailureReason(err); reason != "" {
		return reason
	}
	switch {
	case errors.Is(err, tokenacquisition.ErrTimedOut):
		return "Token Acquisition 请求超时：外部接口或代理校验繁忙，请稍后手动重试"
	case errors.Is(err, tokenacquisition.ErrTransportFailed):
		return "Token Acquisition 网络请求失败：服务或链路暂时不可用，请稍后手动重试"
	case errors.Is(err, tokenacquisition.ErrRequestFailed):
		return "Token Acquisition 请求失败：外部接口未接受请求，请稍后手动重试"
	case errors.Is(err, tokenacquisition.ErrJobFailed):
		return "Token Acquisition 返回失败：外部接口未提供可用凭证，请稍后手动重试"
	case errors.Is(err, tokenacquisition.ErrInvalidResult):
		return "Token Acquisition 返回数据无效：请稍后手动重试"
	case errors.Is(err, tokenacquisition.ErrConflict):
		return "Token Acquisition 当前已有处理中任务：请稍后手动重试"
	default:
		return "恢复过程未返回可识别原因，请手动重试"
	}
}

func recoveryCPAAuthFileOperation(err error) *cpaAuthFileOperationError {
	var operationErr *cpaAuthFileOperationError
	if errors.As(err, &operationErr) {
		return operationErr
	}
	return nil
}

func recoveryCPAAuthFileOperationMessage(err *cpaAuthFileOperationError) string {
	if err == nil {
		return "CPA Core 凭证文件操作失败：请稍后手动重试"
	}
	if err.statusCode >= 300 && err.statusCode <= 999 {
		switch err.operation {
		case "initial_read", "write_read":
			return fmt.Sprintf("CPA Core 凭证读取失败：HTTP %d", err.statusCode)
		case "upload":
			return fmt.Sprintf("CPA Core 凭证写入失败：HTTP %d", err.statusCode)
		case "verify":
			return fmt.Sprintf("CPA Core 凭证复核失败：HTTP %d，写入可能已完成，请刷新后核实", err.statusCode)
		}
	}
	switch err.operation {
	case "initial_read", "write_read":
		return "CPA Core 凭证读取失败：请稍后手动重试"
	case "upload":
		return "CPA Core 凭证写入失败：写入结果未确认，请刷新后核实"
	case "verify":
		return "CPA Core 凭证复核失败：写入可能已完成，请刷新后核实"
	default:
		return "CPA Core 凭证文件操作失败：请稍后手动重试"
	}
}
