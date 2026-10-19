package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	priorityqueue "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/servererrorpriority"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/servererrorpriority"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	serverErrorPriorityDemotionActionTimeout = 30 * time.Second
	serverErrorPriorityInventoryCacheTTL     = 20 * time.Second
	serverErrorPriorityFailureCooldown       = 45 * time.Second

	serverErrorPriorityDemotionDefaultWorkers    = 8
	serverErrorPriorityDemotionDefaultWakeBuffer = 8192
	serverErrorPriorityDemotionDefaultLease      = 2 * time.Minute
	serverErrorPriorityDemotionDefaultReconcile  = time.Second
	serverErrorPriorityDemotionDefaultRecovery   = 10 * time.Second
	serverErrorPriorityDemotionDefaultStats      = time.Minute
	serverErrorPriorityDemotionDefaultCleanup    = time.Hour
	serverErrorPriorityDemotionRetention         = 14 * 24 * time.Hour
	serverErrorPriorityDemotionRetryBaseDelay    = 5 * time.Second
	serverErrorPriorityDemotionRetryMaxDelay     = 5 * time.Minute
	serverErrorPriorityDemotionReconcileBatch    = 1000
)

// ServerErrorPriorityDemotionOptions controls only local CPAM queue execution.
// It never alters CPA usage, billing, quota, or historical data paths.
type ServerErrorPriorityDemotionOptions struct {
	Workers           int
	WakeBuffer        int
	LeaseDuration     time.Duration
	ReconcileInterval time.Duration
	RecoveryInterval  time.Duration
	StatsInterval     time.Duration
	CleanupInterval   time.Duration
}

func (options ServerErrorPriorityDemotionOptions) normalized() ServerErrorPriorityDemotionOptions {
	if options.Workers <= 0 {
		options.Workers = serverErrorPriorityDemotionDefaultWorkers
	}
	if options.Workers > 32 {
		options.Workers = 32
	}
	if options.WakeBuffer <= 0 {
		options.WakeBuffer = serverErrorPriorityDemotionDefaultWakeBuffer
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = serverErrorPriorityDemotionDefaultLease
	}
	if options.ReconcileInterval <= 0 {
		options.ReconcileInterval = serverErrorPriorityDemotionDefaultReconcile
	}
	if options.RecoveryInterval <= 0 {
		options.RecoveryInterval = serverErrorPriorityDemotionDefaultRecovery
	}
	if options.StatsInterval <= 0 {
		options.StatsInterval = serverErrorPriorityDemotionDefaultStats
	}
	if options.CleanupInterval <= 0 {
		options.CleanupInterval = serverErrorPriorityDemotionDefaultCleanup
	}
	return options
}

// ServerErrorPriorityDemotionWorker converts each approved server-error event
// into exactly one durable SQLite job. The in-memory wake channel only wakes
// claim loops after a committed write; saturation can coalesce wakeups but can
// never discard a demotion event.
type ServerErrorPriorityDemotionWorker struct {
	client            *http.Client
	authFileMutations *cpaauthfiles.MutationCoordinator
	setupResolver     serverErrorPrioritySetupResolver
	queue             priorityqueue.Repository
	options           ServerErrorPriorityDemotionOptions
	wake              chan struct{}

	enabledMu sync.RWMutex
	enabled   bool
	startOnce sync.Once

	inventoryMu    sync.Mutex
	inventoryCache map[string]serverErrorPriorityInventorySnapshot
	now            func() time.Time
}

// serverErrorPrioritySetupResolver provides the same already-normalized CPA
// setup that Manager Server uses for its proxy paths. Keeping this dependency
// narrow allows the worker to retain its legacy runtime-config fallback while
// avoiding a public reverse-proxy hop when a local Manager setup is available.
type serverErrorPrioritySetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type serverErrorPriorityInventorySnapshot struct {
	files       []cpaauthfiles.File
	fetchedAt   time.Time
	failureAt   time.Time
	failureText string
	inFlight    chan struct{}
}

type serverErrorPriorityDemotionCandidate struct {
	BaseURL         string
	ManagementKey   string
	FileName        string
	AuthIndex       string
	Provider        string
	AccountID       string
	AccountSnapshot string
	EventHash       string
	StatusCode      int
}

func NewServerErrorPriorityDemotionWorker() *ServerErrorPriorityDemotionWorker {
	return NewServerErrorPriorityDemotionWorkerWithMutationCoordinator(nil)
}

func NewServerErrorPriorityDemotionWorkerWithMutationCoordinator(
	coordinator *cpaauthfiles.MutationCoordinator,
) *ServerErrorPriorityDemotionWorker {
	return NewServerErrorPriorityDemotionWorkerWithMutationCoordinatorAndSetupResolver(coordinator, nil)
}

func NewServerErrorPriorityDemotionWorkerWithMutationCoordinatorAndSetupResolver(
	coordinator *cpaauthfiles.MutationCoordinator,
	resolver serverErrorPrioritySetupResolver,
) *ServerErrorPriorityDemotionWorker {
	return newServerErrorPriorityDemotionWorker(nil, coordinator, resolver, ServerErrorPriorityDemotionOptions{})
}

// NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver creates the
// production executor. queue must be backed by CPAM's usage.sqlite store.
func NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
	queue priorityqueue.Repository,
	coordinator *cpaauthfiles.MutationCoordinator,
	resolver serverErrorPrioritySetupResolver,
	options ServerErrorPriorityDemotionOptions,
) *ServerErrorPriorityDemotionWorker {
	return newServerErrorPriorityDemotionWorker(queue, coordinator, resolver, options)
}

func newServerErrorPriorityDemotionWorker(
	queue priorityqueue.Repository,
	coordinator *cpaauthfiles.MutationCoordinator,
	resolver serverErrorPrioritySetupResolver,
	options ServerErrorPriorityDemotionOptions,
) *ServerErrorPriorityDemotionWorker {
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	options = options.normalized()
	return &ServerErrorPriorityDemotionWorker{
		client:            &http.Client{Timeout: serverErrorPriorityDemotionActionTimeout},
		authFileMutations: coordinator,
		setupResolver:     resolver,
		queue:             queue,
		options:           options,
		wake:              make(chan struct{}, options.WakeBuffer),
		inventoryCache:    make(map[string]serverErrorPriorityInventorySnapshot),
		now:               time.Now,
	}
}

// Bootstrap records the no-history-replay floor and persists the first enable
// interval before the HTTP listener or collector can accept events.
func (w *ServerErrorPriorityDemotionWorker) Bootstrap(ctx context.Context, enabled bool) error {
	if w == nil {
		return nil
	}
	if w.queue != nil {
		if err := w.queue.Bootstrap(ctx, enabled, w.currentTime().UnixMilli()); err != nil {
			return fmt.Errorf("bootstrap server-error priority demotion queue: %w", err)
		}
	}
	w.setEnabled(enabled)
	return nil
}

// SetEnabled durably marks source-event boundaries before changing claim-loop
// behavior. Existing jobs remain queued while disabled and resume on enable.
func (w *ServerErrorPriorityDemotionWorker) SetEnabled(ctx context.Context, enabled bool) error {
	if w == nil {
		return nil
	}
	if w.queue != nil {
		if err := w.queue.SetEnabled(ctx, enabled, w.currentTime().UnixMilli()); err != nil {
			return fmt.Errorf("set server-error priority demotion enabled=%t: %w", enabled, err)
		}
	}
	w.setEnabled(enabled)
	if enabled {
		w.signalWorkers()
	}
	return nil
}

func (w *ServerErrorPriorityDemotionWorker) Start(ctx context.Context) {
	if w == nil || w.queue == nil {
		return
	}
	w.startOnce.Do(func() {
		for index := 0; index < w.options.Workers; index++ {
			go w.runClaimLoop(ctx)
		}
		go w.runScheduler(ctx)
		w.signalWorkers()
	})
}

func (w *ServerErrorPriorityDemotionWorker) HandleUsageEvents(
	ctx context.Context,
	cfg collectorpkg.RuntimeConfig,
	events []usage.Event,
) {
	if w == nil || len(events) == 0 {
		return
	}
	if w.queue != nil {
		result, err := w.queue.EnqueueEvents(ctx, events, w.currentTime().UnixMilli())
		if err != nil {
			log.Printf("[server-error-priority] durable job admission failed: %v", err)
			return
		}
		if result.Admitted > 0 {
			w.signalWorkers()
		}
		return
	}

	// Compatibility path for old direct callers. Production construction always
	// provides queue, so this path cannot emit a queue-full drop.
	baseURL := strings.TrimSpace(cfg.CPAUpstreamURL)
	managementKey := strings.TrimSpace(cfg.ManagementKey)
	if (baseURL == "" || managementKey == "") && w.setupResolver == nil {
		return
	}
	for _, event := range events {
		candidate, ok := serverErrorPriorityDemotionCandidateFromEvent(event, baseURL, managementKey)
		if !ok {
			continue
		}
		go w.handleCandidate(ctx, candidate)
	}
}

func (w *ServerErrorPriorityDemotionWorker) runClaimLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
		for w.isEnabled() {
			claimed, err := w.claimAndProcess(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[server-error-priority] durable claim failed: %v", err)
				}
				break
			}
			if !claimed {
				break
			}
		}
	}
}

func (w *ServerErrorPriorityDemotionWorker) claimAndProcess(ctx context.Context) (bool, error) {
	if w == nil || w.queue == nil || !w.isEnabled() {
		return false, nil
	}
	leaseToken, err := serverErrorPriorityLeaseToken()
	if err != nil {
		return false, err
	}
	now := w.currentTime()
	job, claimed, err := w.queue.ClaimNext(ctx, now.UnixMilli(), now.Add(w.options.LeaseDuration).UnixMilli(), leaseToken)
	if err != nil || !claimed {
		return claimed, err
	}
	w.processDurableJob(ctx, job)
	return true, nil
}

func (w *ServerErrorPriorityDemotionWorker) runScheduler(ctx context.Context) {
	w.recoverExpiredDurableLeases(ctx)
	w.reconcileDurableEvents(ctx)

	reconcileTicker := time.NewTicker(w.options.ReconcileInterval)
	recoveryTicker := time.NewTicker(w.options.RecoveryInterval)
	statsTicker := time.NewTicker(w.options.StatsInterval)
	cleanupTicker := time.NewTicker(w.options.CleanupInterval)
	defer reconcileTicker.Stop()
	defer recoveryTicker.Stop()
	defer statsTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileTicker.C:
			// Reconcile even while disabled. Interval rows decide whether each
			// source event was enabled; claim loops remain paused until enabled.
			w.reconcileDurableEvents(ctx)
			if w.isEnabled() {
				w.signalWorkers()
			}
		case <-recoveryTicker.C:
			w.recoverExpiredDurableLeases(ctx)
		case <-statsTicker.C:
			w.logDurableQueueStats(ctx)
		case <-cleanupTicker.C:
			w.cleanupDurableQueue(ctx)
		}
	}
}

func (w *ServerErrorPriorityDemotionWorker) reconcileDurableEvents(ctx context.Context) {
	if w == nil || w.queue == nil {
		return
	}
	for {
		result, err := w.queue.Reconcile(ctx, serverErrorPriorityDemotionReconcileBatch, w.currentTime().UnixMilli())
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[server-error-priority] durable reconciliation failed: %v", err)
			}
			return
		}
		if result.Admitted > 0 && w.isEnabled() {
			w.signalWorkers()
		}
		if result.Scanned < serverErrorPriorityDemotionReconcileBatch {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (w *ServerErrorPriorityDemotionWorker) recoverExpiredDurableLeases(ctx context.Context) {
	if w == nil || w.queue == nil {
		return
	}
	recovered, err := w.queue.RecoverExpiredLeases(ctx, w.currentTime().UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[server-error-priority] durable lease recovery failed: %v", err)
		}
		return
	}
	if recovered > 0 && w.isEnabled() {
		w.signalWorkers()
	}
}

func (w *ServerErrorPriorityDemotionWorker) cleanupDurableQueue(ctx context.Context) {
	if w == nil || w.queue == nil {
		return
	}
	deleted, err := w.queue.CleanupTerminal(ctx, w.currentTime().Add(-serverErrorPriorityDemotionRetention).UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[server-error-priority] durable queue cleanup failed: %v", err)
		}
		return
	}
	if deleted > 0 {
		log.Printf("[server-error-priority] durable queue cleanup removed terminal jobs=%d", deleted)
	}
}

func (w *ServerErrorPriorityDemotionWorker) logDurableQueueStats(ctx context.Context) {
	if w == nil || w.queue == nil {
		return
	}
	stats, err := w.queue.Stats(ctx, w.currentTime().UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[server-error-priority] durable queue stats failed: %v", err)
		}
		return
	}
	if stats.Pending+stats.Leased+stats.RetryWait == 0 {
		return
	}
	log.Printf("[server-error-priority] durable queue pending=%d leased=%d retry_wait=%d failed=%d ignored=%d oldest_ready_ms=%d", stats.Pending, stats.Leased, stats.RetryWait, stats.Failed, stats.Ignored, stats.OldestReadyMS)
}

func (w *ServerErrorPriorityDemotionWorker) processDurableJob(ctx context.Context, job model.ServerErrorPriorityDemotionJob) {
	if w == nil || w.queue == nil {
		return
	}
	candidate := serverErrorPriorityDemotionCandidateFromJob(job)
	resolvedCandidate, err := w.resolveCandidateSetupWithError(ctx, candidate)
	if err != nil {
		w.retryDurableJob(ctx, job, "setup_unavailable", "CPA setup is unavailable")
		return
	}
	candidate = resolvedCandidate
	identity, err := candidate.identity()
	if err != nil {
		w.terminalDurableJob(ctx, job, model.ServerErrorPriorityDemotionFailed, "identity_invalid", "credential identity is invalid", "identity_invalid")
		return
	}
	if w.authFileMutations == nil {
		w.retryDurableJob(ctx, job, "mutation_coordinator_unavailable", "credential mutation coordinator is unavailable")
		return
	}
	releaseMutation, err := w.authFileMutations.Acquire(ctx, candidate.FileName)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.retryDurableJob(ctx, job, "mutation_lock_unavailable", "credential mutation lock is unavailable")
		return
	}
	defer releaseMutation()

	client := cpaauthfiles.New(w.client, serverErrorPriorityDemotionActionTimeout)
	target, err := client.ResolveVerifiedStatusMutationTarget(ctx, candidate.BaseURL, candidate.ManagementKey, identity)
	if err != nil {
		if state, code, message, reason, terminal := serverErrorPriorityTerminalTargetError(err); terminal {
			w.terminalDurableJob(ctx, job, state, code, message, reason)
			return
		}
		if ctx.Err() != nil {
			return
		}
		w.retryDurableJob(ctx, job, "target_lookup_failed", "CPA credential lookup failed")
		return
	}
	if target.Scope != cpaauthfiles.StatusMutationScopeCredential {
		w.terminalDurableJob(ctx, job, model.ServerErrorPriorityDemotionIgnored, "mutation_scope_unsupported", "priority mutation does not target one credential", "mutation_scope_unsupported")
		return
	}
	currentPriority, valid := serverErrorPriorityFromAuthFile(target.File)
	if !valid {
		w.terminalDurableJob(ctx, job, model.ServerErrorPriorityDemotionFailed, "priority_invalid", "credential priority is not an integer", "priority_invalid")
		return
	}

	if job.PriorityTarget != nil && currentPriority == *job.PriorityTarget {
		w.completeDurableJob(ctx, job, "already_at_target")
		return
	}
	if currentPriority <= 0 {
		w.completeDurableJob(ctx, job, "floor_reached")
		return
	}

	nextPriority := 0
	if job.PriorityBefore != nil && job.PriorityTarget != nil && currentPriority == *job.PriorityBefore {
		nextPriority = *job.PriorityTarget
	} else {
		allFiles, err := w.peerInventory(ctx, client, candidate)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.retryDurableJob(ctx, job, "peer_inventory_failed", "CPA peer-priority inventory is unavailable")
			return
		}
		peerMax, hasPeer := serverErrorPriorityPeerMaximum(allFiles, target.File)
		nextPriority = serverErrorPriorityAfterPeerRebalance(currentPriority, peerMax, hasPeer)
		if nextPriority == currentPriority {
			w.completeDurableJob(ctx, job, "priority_unchanged")
			return
		}
		if err := w.queue.RecordPriorityPlan(ctx, job.ID, job.LeaseToken, currentPriority, nextPriority, w.currentTime().UnixMilli()); err != nil {
			w.logDurableSettlementFailure("record priority plan", job, err)
			return
		}
	}

	if err := client.PatchPriorityTarget(ctx, candidate.BaseURL, candidate.ManagementKey, target, nextPriority); err != nil {
		if ctx.Err() != nil {
			return
		}
		w.retryDurableJob(ctx, job, "priority_patch_failed", "CPA priority update failed")
		return
	}
	w.updateCachedPriority(candidate, target.File, nextPriority)
	w.completeDurableJob(ctx, job, "demoted")
	log.Printf("[server-error-priority] durable demotion applied file=%q priority=%d->%d event=%q status=%d", candidate.FileName, currentPriority, nextPriority, job.EventHash, job.StatusCode)
}

func (w *ServerErrorPriorityDemotionWorker) completeDurableJob(ctx context.Context, job model.ServerErrorPriorityDemotionJob, reason string) {
	if w == nil || w.queue == nil {
		return
	}
	if err := w.queue.Complete(ctx, job.ID, job.LeaseToken, reason, w.currentTime().UnixMilli()); err != nil {
		w.logDurableSettlementFailure("complete", job, err)
	}
}

func (w *ServerErrorPriorityDemotionWorker) retryDurableJob(ctx context.Context, job model.ServerErrorPriorityDemotionJob, code, message string) {
	if w == nil || w.queue == nil || ctx.Err() != nil {
		return
	}
	now := w.currentTime()
	availableAt := now.Add(serverErrorPriorityRetryDelay(job.AttemptCount)).UnixMilli()
	if err := w.queue.Retry(ctx, job.ID, job.LeaseToken, code, message, availableAt, now.UnixMilli()); err != nil {
		w.logDurableSettlementFailure("retry", job, err)
	}
}

func (w *ServerErrorPriorityDemotionWorker) terminalDurableJob(ctx context.Context, job model.ServerErrorPriorityDemotionJob, state, code, message, reason string) {
	if w == nil || w.queue == nil || ctx.Err() != nil {
		return
	}
	if err := w.queue.Terminal(ctx, job.ID, job.LeaseToken, state, code, message, reason, w.currentTime().UnixMilli()); err != nil {
		w.logDurableSettlementFailure("terminal", job, err)
	}
}

func (w *ServerErrorPriorityDemotionWorker) logDurableSettlementFailure(operation string, job model.ServerErrorPriorityDemotionJob, err error) {
	if err == nil {
		return
	}
	log.Printf("[server-error-priority] durable %s settlement failed job=%d event=%q: %v", operation, job.ID, job.EventHash, err)
}

func serverErrorPriorityTerminalTargetError(err error) (state, code, message, reason string, terminal bool) {
	switch {
	case errors.Is(err, cpaauthfiles.ErrAuthFileNotFound):
		return model.ServerErrorPriorityDemotionFailed, "credential_not_found", "credential no longer exists in CPA", "credential_not_found", true
	case errors.Is(err, cpaauthfiles.ErrAuthFileAmbiguous),
		errors.Is(err, cpaauthfiles.ErrIdentityMismatch):
		return model.ServerErrorPriorityDemotionFailed, "credential_identity_mismatch", "credential identity no longer resolves uniquely", "credential_identity_mismatch", true
	case errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous):
		return model.ServerErrorPriorityDemotionIgnored, "mutation_scope_unsupported", "priority mutation does not target one credential", "mutation_scope_unsupported", true
	default:
		return "", "", "", "", false
	}
}

func serverErrorPriorityRetryDelay(attemptCount int) time.Duration {
	if attemptCount <= 1 {
		return serverErrorPriorityDemotionRetryBaseDelay
	}
	delay := serverErrorPriorityDemotionRetryBaseDelay
	for attempt := 1; attempt < attemptCount && delay < serverErrorPriorityDemotionRetryMaxDelay; attempt++ {
		delay *= 2
	}
	if delay > serverErrorPriorityDemotionRetryMaxDelay {
		return serverErrorPriorityDemotionRetryMaxDelay
	}
	return delay
}

func serverErrorPriorityLeaseToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate priority demotion lease token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func (w *ServerErrorPriorityDemotionWorker) setEnabled(enabled bool) {
	if w == nil {
		return
	}
	w.enabledMu.Lock()
	w.enabled = enabled
	w.enabledMu.Unlock()
}

func (w *ServerErrorPriorityDemotionWorker) isEnabled() bool {
	if w == nil {
		return false
	}
	w.enabledMu.RLock()
	defer w.enabledMu.RUnlock()
	return w.enabled
}

func (w *ServerErrorPriorityDemotionWorker) signalWorkers() {
	if w == nil || w.wake == nil {
		return
	}
	for index := 0; index < w.options.Workers; index++ {
		select {
		case w.wake <- struct{}{}:
		default:
			return
		}
	}
}

func serverErrorPriorityDemotionCandidateFromJob(job model.ServerErrorPriorityDemotionJob) serverErrorPriorityDemotionCandidate {
	return serverErrorPriorityDemotionCandidate{
		FileName:        strings.TrimSpace(job.FileName),
		AuthIndex:       strings.TrimSpace(job.AuthIndex),
		Provider:        strings.TrimSpace(job.Provider),
		AccountID:       strings.TrimSpace(job.AccountIDSnapshot),
		AccountSnapshot: strings.TrimSpace(job.AccountSnapshot),
		EventHash:       strings.TrimSpace(job.EventHash),
		StatusCode:      job.StatusCode,
	}
}

func (w *ServerErrorPriorityDemotionWorker) handleCandidate(
	ctx context.Context,
	candidate serverErrorPriorityDemotionCandidate,
) {
	if w == nil || candidate.FileName == "" {
		return
	}
	resolvedCandidate, ok := w.resolveCandidateSetup(ctx, candidate)
	if !ok {
		return
	}
	candidate = resolvedCandidate
	identity, err := candidate.identity()
	if err != nil {
		log.Printf("[server-error-priority] skip auth file %q event=%q: %v", candidate.FileName, candidate.EventHash, err)
		return
	}
	if w.authFileMutations == nil {
		log.Printf("[server-error-priority] mutation coordinator unavailable, skip auth file %q", candidate.FileName)
		return
	}
	releaseMutation, err := w.authFileMutations.Acquire(ctx, candidate.FileName)
	if err != nil {
		log.Printf("[server-error-priority] failed to coordinate auth file %q: %v", candidate.FileName, err)
		return
	}
	defer releaseMutation()

	client := cpaauthfiles.New(w.client, serverErrorPriorityDemotionActionTimeout)
	target, err := client.ResolveVerifiedStatusMutationTarget(ctx, candidate.BaseURL, candidate.ManagementKey, identity)
	if err != nil {
		log.Printf("[server-error-priority] identity verification failed for auth file %q event=%q: %v", candidate.FileName, candidate.EventHash, err)
		return
	}
	if target.Scope != cpaauthfiles.StatusMutationScopeCredential {
		log.Printf("[server-error-priority] skip auth file %q event=%q: priority mutation scope is %q", candidate.FileName, candidate.EventHash, target.Scope)
		return
	}
	currentPriority, valid := serverErrorPriorityFromAuthFile(target.File)
	if !valid {
		log.Printf("[server-error-priority] skip auth file %q event=%q: current priority is not an integer", candidate.FileName, candidate.EventHash)
		return
	}
	if currentPriority <= 0 {
		log.Printf("[server-error-priority] priority already zero for auth file %q event=%q", candidate.FileName, candidate.EventHash)
		return
	}
	allFiles, err := w.peerInventory(ctx, client, candidate)
	if err != nil {
		log.Printf("[server-error-priority] failed to read peer priorities for auth file %q event=%q: %v", candidate.FileName, candidate.EventHash, err)
		return
	}
	peerMax, hasPeer := serverErrorPriorityPeerMaximum(allFiles, target.File)
	nextPriority := serverErrorPriorityAfterPeerRebalance(currentPriority, peerMax, hasPeer)
	if nextPriority == currentPriority {
		log.Printf("[server-error-priority] priority unchanged for auth file %q event=%q: current=%d peer_max=%d has_peer=%t", candidate.FileName, candidate.EventHash, currentPriority, peerMax, hasPeer)
		return
	}
	if err := client.PatchPriorityTarget(ctx, candidate.BaseURL, candidate.ManagementKey, target, nextPriority); err != nil {
		log.Printf("[server-error-priority] failed to lower priority for auth file %q event=%q: %v", candidate.FileName, candidate.EventHash, err)
		return
	}
	w.updateCachedPriority(candidate, target.File, nextPriority)
	log.Printf("[server-error-priority] lowered auth file %q priority %d -> %d after HTTP %d event=%q peer_max=%d has_peer=%t", candidate.FileName, currentPriority, nextPriority, candidate.StatusCode, candidate.EventHash, peerMax, hasPeer)
}

// peerInventory caches the complete CPA auth-file snapshot briefly. Its
// in-flight barrier prevents concurrent workers from multiplying the same full
// inventory request. Failed reads retain the existing cooldown rather than
// stacking 30-second upstream timeouts behind every queued event.
func (w *ServerErrorPriorityDemotionWorker) peerInventory(
	ctx context.Context,
	client *cpaauthfiles.Client,
	candidate serverErrorPriorityDemotionCandidate,
) ([]cpaauthfiles.File, error) {
	if w == nil {
		return client.Fetch(ctx, candidate.BaseURL, candidate.ManagementKey)
	}
	cacheKey := serverErrorPriorityInventoryKey(candidate)
	for {
		now := w.currentTime()
		w.inventoryMu.Lock()
		snapshot := w.inventoryCache[cacheKey]
		if len(snapshot.files) > 0 && now.Sub(snapshot.fetchedAt) < serverErrorPriorityInventoryCacheTTL {
			files := cloneServerErrorPriorityFiles(snapshot.files)
			w.inventoryMu.Unlock()
			return files, nil
		}
		if !snapshot.failureAt.IsZero() && now.Sub(snapshot.failureAt) < serverErrorPriorityFailureCooldown {
			err := fmt.Errorf("peer inventory retry is cooling down after: %s", snapshot.failureText)
			w.inventoryMu.Unlock()
			return nil, err
		}
		if snapshot.inFlight != nil {
			done := snapshot.inFlight
			w.inventoryMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		snapshot.inFlight = done
		w.inventoryCache[cacheKey] = snapshot
		w.inventoryMu.Unlock()

		files, err := client.Fetch(ctx, candidate.BaseURL, candidate.ManagementKey)

		w.inventoryMu.Lock()
		updated := w.inventoryCache[cacheKey]
		updated.inFlight = nil
		if err != nil {
			updated.files = nil
			updated.fetchedAt = time.Time{}
			updated.failureAt = now
			updated.failureText = serverErrorPrioritySafeFailureText(err)
		} else {
			updated.files = cloneServerErrorPriorityFiles(files)
			updated.fetchedAt = now
			updated.failureAt = time.Time{}
			updated.failureText = ""
		}
		w.inventoryCache[cacheKey] = updated
		close(done)
		w.inventoryMu.Unlock()
		if err != nil {
			return nil, err
		}
		return files, nil
	}
}

func (w *ServerErrorPriorityDemotionWorker) updateCachedPriority(
	candidate serverErrorPriorityDemotionCandidate,
	target cpaauthfiles.File,
	priority int,
) {
	if w == nil {
		return
	}
	cacheKey := serverErrorPriorityInventoryKey(candidate)
	w.inventoryMu.Lock()
	defer w.inventoryMu.Unlock()
	snapshot, ok := w.inventoryCache[cacheKey]
	if !ok {
		return
	}
	for index := range snapshot.files {
		if !sameServerErrorPriorityCredential(snapshot.files[index], target) {
			continue
		}
		if snapshot.files[index].Raw == nil {
			snapshot.files[index].Raw = map[string]any{}
		}
		snapshot.files[index].Raw["priority"] = priority
		break
	}
	w.inventoryCache[cacheKey] = snapshot
}

func (w *ServerErrorPriorityDemotionWorker) currentTime() time.Time {
	if w != nil && w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *ServerErrorPriorityDemotionWorker) resolveCandidateSetup(
	ctx context.Context,
	candidate serverErrorPriorityDemotionCandidate,
) (serverErrorPriorityDemotionCandidate, bool) {
	resolved, err := w.resolveCandidateSetupWithError(ctx, candidate)
	if err != nil {
		log.Printf("[server-error-priority] skip auth file %q event=%q: resolve CPA setup failed: %v", candidate.FileName, candidate.EventHash, err)
		return candidate, false
	}
	return resolved, true
}

func (w *ServerErrorPriorityDemotionWorker) resolveCandidateSetupWithError(
	ctx context.Context,
	candidate serverErrorPriorityDemotionCandidate,
) (serverErrorPriorityDemotionCandidate, error) {
	if w == nil {
		return candidate, errors.New("priority demotion worker is unavailable")
	}
	if w.setupResolver == nil {
		if strings.TrimSpace(candidate.BaseURL) == "" || strings.TrimSpace(candidate.ManagementKey) == "" {
			return candidate, errors.New("CPA setup is unavailable")
		}
		return candidate, nil
	}
	setup, configured, err := w.setupResolver.ResolveSetup(ctx)
	if err != nil {
		return candidate, err
	}
	if !configured || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return candidate, errors.New("CPA setup is unavailable")
	}
	candidate.BaseURL = strings.TrimSpace(setup.CPAUpstreamURL)
	candidate.ManagementKey = strings.TrimSpace(setup.ManagementKey)
	return candidate, nil
}

func serverErrorPriorityInventoryKey(candidate serverErrorPriorityDemotionCandidate) string {
	// The key is never logged or persisted. A digest additionally ensures that
	// a memory dump of map keys cannot directly reveal the management key.
	material := strings.TrimRight(strings.TrimSpace(candidate.BaseURL), "/") + "\x00" + candidate.ManagementKey
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func cloneServerErrorPriorityFiles(files []cpaauthfiles.File) []cpaauthfiles.File {
	cloned := make([]cpaauthfiles.File, len(files))
	for index, file := range files {
		cloned[index] = file
		if file.Raw == nil {
			continue
		}
		cloned[index].Raw = make(map[string]any, len(file.Raw))
		for key, value := range file.Raw {
			cloned[index].Raw[key] = value
		}
	}
	return cloned
}

func serverErrorPrioritySafeFailureText(err error) string {
	if err == nil {
		return "upstream request failed"
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 240 {
		return text[:240]
	}
	if text == "" {
		return "upstream request failed"
	}
	return text
}

func serverErrorPriorityAfterPeerRebalance(currentPriority int, peerMax int, hasPeer bool) int {
	if currentPriority <= 0 {
		return 0
	}
	nextPriority := currentPriority - 1
	if hasPeer && peerMax < currentPriority {
		nextPriority = peerMax - 1
	}
	if nextPriority < 0 {
		return 0
	}
	return nextPriority
}

func serverErrorPriorityPeerMaximum(files []cpaauthfiles.File, target cpaauthfiles.File) (int, bool) {
	targetProvider := credentialpolicy.NormalizeProvider(target.Provider)
	maxPriority := 0
	found := false
	for _, file := range files {
		if file.Disabled || credentialpolicy.NormalizeProvider(file.Provider) != targetProvider || sameServerErrorPriorityCredential(file, target) {
			continue
		}
		priority, valid := serverErrorPriorityFromAuthFile(file)
		if !valid {
			continue
		}
		if !found || priority > maxPriority {
			maxPriority = priority
			found = true
		}
	}
	return maxPriority, found
}

func sameServerErrorPriorityCredential(left cpaauthfiles.File, right cpaauthfiles.File) bool {
	leftID := strings.TrimSpace(left.ID)
	rightID := strings.TrimSpace(right.ID)
	if leftID != "" && rightID != "" {
		return leftID == rightID
	}
	leftName := strings.TrimSpace(left.Name)
	rightName := strings.TrimSpace(right.Name)
	leftAuthIndex := strings.TrimSpace(left.AuthIndex)
	rightAuthIndex := strings.TrimSpace(right.AuthIndex)
	return leftName != "" && leftName == rightName && leftAuthIndex != "" && leftAuthIndex == rightAuthIndex
}

func serverErrorPriorityDemotionCandidateFromEvent(
	event usage.Event,
	baseURL string,
	managementKey string,
) (serverErrorPriorityDemotionCandidate, bool) {
	input, ok := servererrorpriority.Classify(event)
	if !ok || strings.TrimSpace(input.IgnoredReason) != "" {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	return serverErrorPriorityDemotionCandidate{
		BaseURL:         strings.TrimSpace(baseURL),
		ManagementKey:   strings.TrimSpace(managementKey),
		FileName:        input.FileName,
		AuthIndex:       input.AuthIndex,
		Provider:        input.Provider,
		AccountID:       input.AccountIDSnapshot,
		AccountSnapshot: input.AccountSnapshot,
		EventHash:       input.EventHash,
		StatusCode:      input.StatusCode,
	}, true
}

func isServerErrorPriorityDemotionEvent(event usage.Event) bool {
	return servererrorpriority.IsEligible(event)
}

func (candidate serverErrorPriorityDemotionCandidate) identity() (cpaauthfiles.Identity, error) {
	provider := credentialpolicy.NormalizeProvider(candidate.Provider)
	if provider == "" {
		return cpaauthfiles.Identity{}, fmt.Errorf("credential provider is missing")
	}
	authIndex := strings.TrimSpace(candidate.AuthIndex)
	accountID := strings.TrimSpace(candidate.AccountID)
	accountSnapshot := strings.TrimSpace(candidate.AccountSnapshot)
	if provider == "codex" {
		if authIndex == "" {
			return cpaauthfiles.Identity{}, fmt.Errorf("Codex priority mutation requires auth_index")
		}
		if accountID != "" {
			workspace, ok := usageidentity.NormalizeCodexWorkspaceSnapshot(accountID)
			if !ok {
				return cpaauthfiles.Identity{}, fmt.Errorf("Codex workspace identity is invalid")
			}
			accountID = workspace
		}
		if accountSnapshot != "" {
			member, ok := usageidentity.NormalizeCodexMemberSnapshot(accountSnapshot)
			if ok {
				accountSnapshot = member
			} else {
				accountSnapshot = ""
			}
		}
	}
	if authIndex == "" && accountID == "" && accountSnapshot == "" {
		return cpaauthfiles.Identity{}, fmt.Errorf("credential has no stable auth index, account ID, or account snapshot")
	}
	return cpaauthfiles.Identity{
		AuthFileName:      strings.TrimSpace(candidate.FileName),
		AuthIndex:         authIndex,
		Provider:          provider,
		AccountSnapshot:   accountSnapshot,
		AccountIDSnapshot: accountID,
	}, nil
}

func serverErrorPriorityFromAuthFile(file cpaauthfiles.File) (int, bool) {
	if file.Raw == nil {
		return 0, true
	}
	raw, exists := file.Raw["priority"]
	if !exists || raw == nil {
		return 0, true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(raw)), 10, 0)
	if err != nil {
		return 0, false
	}
	if value <= 0 {
		return 0, true
	}
	return int(value), true
}
