package proxyreplacement

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const previewTTL = 15 * time.Minute

const taskWorkerPollInterval = 250 * time.Millisecond

const (
	maxProxyExitIPCheckBatch    = 500
	proxyExitIPCheckConcurrency = 8
)

var (
	ErrUnavailable    = errors.New("proxy replacement service is unavailable")
	ErrInvalidRequest = errors.New("proxy replacement request is invalid")
	ErrTaskExpired    = errors.New("proxy replacement preview has expired")
	proxyURLPattern   = regexp.MustCompile(`(?i)https?://[^\s"'<>}\]]+`)
)

type TaskStore interface {
	CreateDraft(context.Context, model.ProxyReplacementTask, []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error)
	GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error)
	ConfirmDraft(context.Context, int64, int64) (model.ProxyReplacementTask, error)
	ClaimNextQueued(context.Context) (model.ProxyReplacementTask, bool, error)
	RequeueRunningOnStartup(context.Context) (int64, error)
	MarkItem(context.Context, int64, model.ProxyReplacementItemStatus, model.ProxyReplacementItemOutcome) error
	ResetInFlightItems(context.Context) (int64, error)
	CompleteTask(context.Context, int64) (model.ProxyReplacementTask, error)
	ExpireDrafts(context.Context, int64) (int64, error)
}

type SettingsStore interface {
	LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error)
}

type PoolStateStore interface {
	List(context.Context) ([]model.ProxyPoolState, error)
	MarkPending(context.Context, string, string) error
	MarkSuspect(context.Context, string, string) error
}

type poolStateRestorer interface {
	Restore(context.Context, string) (model.ProxyPoolState, error)
}

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

// AuthFilesClient keeps the mutable CPA surface deliberately narrow. There is
// no arbitrary field map; the only write primitive accepts proxy_url.
type AuthFilesClient interface {
	Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
	ResolveProxyURLMutationTarget(context.Context, string, string, string, string) (cpaauthfiles.StatusMutationTarget, error)
	PatchProxyURLTarget(context.Context, string, string, cpaauthfiles.StatusMutationTarget, string) error
	Download(context.Context, string, string, string) ([]byte, error)
	Upload(context.Context, string, string, string, []byte) error
}

type InventoryInvalidator interface {
	InvalidateAuthFileInventory(context.Context)
}

type Rechecker interface {
	RecheckProxyReplacement(context.Context, []model.ProxyReplacementTarget, int) []model.ProxyReplacementProbeResult
}

// InspectionResultStore is intentionally limited to acknowledging a successful
// directed proxy recheck. It has no capability to create, alter, or delete
// inspection evidence beyond clearing the now-resolved transport flag.
type InspectionResultStore interface {
	ClearProxySuspects(context.Context, []model.ProxyReplacementTarget) error
}

type Options struct {
	Tasks               TaskStore
	Settings            SettingsStore
	PoolState           PoolStateStore
	SetupResolver       SetupResolver
	AuthFiles           AuthFilesClient
	MutationCoordinator *cpaauthfiles.MutationCoordinator
	Invalidator         InventoryInvalidator
	Rechecker           Rechecker
	InspectionResults   InspectionResultStore
	ExitIPChecker       ExitIPChecker
	Concurrency         int
}

type Service struct {
	tasks               TaskStore
	settings            SettingsStore
	poolState           PoolStateStore
	setupResolver       SetupResolver
	authFiles           AuthFilesClient
	mutationCoordinator *cpaauthfiles.MutationCoordinator
	invalidator         InventoryInvalidator
	rechecker           Rechecker
	inspectionResults   InspectionResultStore
	exitIPChecker       ExitIPChecker
	concurrency         int
	now                 func() time.Time
	workerMu            sync.Mutex
	workerCancel        context.CancelFunc
	workerDone          chan struct{}
}

// GetTask returns a persisted preview or execution snapshot. URL values are
// decrypted only for the authorized caller by the repository layer.
func (s *Service) GetTask(ctx context.Context, taskID int64, page, pageSize int) (model.ProxyReplacementTaskPage, error) {
	if s == nil || s.tasks == nil || taskID <= 0 || page < 1 || pageSize < 1 || pageSize > 100 {
		return model.ProxyReplacementTaskPage{}, ErrInvalidRequest
	}
	return s.tasks.GetTask(ctx, taskID, page, pageSize)
}

func (s *Service) ListProxyPool(ctx context.Context) ([]model.ProxyPoolState, error) {
	if s == nil || s.poolState == nil {
		return nil, ErrUnavailable
	}
	return s.poolState.List(ctx)
}

func (s *Service) RestoreProxyPoolEntry(ctx context.Context, fingerprint string) (model.ProxyPoolState, error) {
	if s == nil || s.poolState == nil {
		return model.ProxyPoolState{}, ErrUnavailable
	}
	restorer, ok := s.poolState.(poolStateRestorer)
	if !ok {
		return model.ProxyPoolState{}, ErrUnavailable
	}
	if strings.TrimSpace(fingerprint) == "" {
		return model.ProxyPoolState{}, ErrInvalidRequest
	}
	return restorer.Restore(ctx, strings.TrimSpace(fingerprint))
}

// RestoreSuspectProxyPoolEntries deliberately changes only the Manager-owned
// pool eligibility state. It never writes credential files, triggers a CPA
// request, or mutates quota, billing, or inspection history.
func (s *Service) RestoreSuspectProxyPoolEntries(ctx context.Context) (int, error) {
	if s == nil || s.poolState == nil {
		return 0, ErrUnavailable
	}
	restorer, ok := s.poolState.(poolStateRestorer)
	if !ok {
		return 0, ErrUnavailable
	}
	states, err := s.poolState.List(ctx)
	if err != nil {
		return 0, err
	}
	restored := 0
	for _, state := range states {
		fingerprint := strings.TrimSpace(state.Fingerprint)
		if state.Status != model.ProxyPoolStatusSuspect || fingerprint == "" {
			continue
		}
		if _, err := restorer.Restore(ctx, fingerprint); err != nil {
			return restored, err
		}
		restored++
	}
	return restored, nil
}

// ReportInspectionSignal persists only independently classifiable proxy-chain
// evidence from the browser-local inspection mode.  It deliberately ignores
// every other failure class: authentication, rate limits, quota and normal
// upstream errors must never poison a proxy-pool entry.
func (s *Service) ReportInspectionSignal(ctx context.Context, evidence model.ProxyFailureEvidence) (bool, error) {
	if s == nil || s.poolState == nil {
		return false, ErrUnavailable
	}
	if !model.IsSuspectedProxyTransportFailure(evidence) {
		return false, nil
	}
	if err := s.poolState.MarkPending(ctx, evidence.ProxyURL, proxyFailureReason(evidence)); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyProxyPoolExitIPs checks selected saved proxy endpoints through the
// manager's fixed exit-IP Echo target. The input is fingerprint-only: raw
// proxy URLs are resolved only from the Manager-owned saved proxy list and are
// never put into response DTOs, errors, or logs. It does not contact CPA Core,
// read account tokens, mutate credential files, or affect quota/billing data.
func (s *Service) VerifyProxyPoolExitIPs(ctx context.Context, fingerprints []string) (model.ProxyExitIPCheckBatch, error) {
	if s == nil || s.settings == nil || s.poolState == nil || s.exitIPChecker == nil {
		return model.ProxyExitIPCheckBatch{}, ErrUnavailable
	}
	selected, err := normalizeProxyExitIPFingerprints(fingerprints)
	if err != nil {
		return model.ProxyExitIPCheckBatch{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	settings, _, err := s.settings.LoadProxyFilterSettings(ctx)
	if err != nil {
		return model.ProxyExitIPCheckBatch{}, fmt.Errorf("load saved proxy URLs: %w", err)
	}

	// Keep raw URLs within this function only. A fingerprint from the browser
	// can therefore never turn the endpoint into an arbitrary outbound proxy
	// probe.
	savedURLByFingerprint := make(map[string]string, len(settings.URLs))
	for _, rawURL := range settings.URLs {
		normalized := model.NormalizeProxyURL(rawURL)
		fingerprint := model.ProxyFingerprint(normalized)
		if normalized == "" || fingerprint == "" {
			continue
		}
		if _, exists := savedURLByFingerprint[fingerprint]; !exists {
			savedURLByFingerprint[fingerprint] = normalized
		}
	}

	results := make([]model.ProxyExitIPCheckResult, len(selected))
	type verificationJob struct {
		index       int
		fingerprint string
		proxyURL    string
	}
	jobs := make(chan verificationJob)
	workerCount := proxyExitIPCheckConcurrency
	if workerCount > len(selected) {
		workerCount = len(selected)
	}
	var workers sync.WaitGroup
	restorer, canRestore := s.poolState.(poolStateRestorer)
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				result := normalizeProxyExitIPCheckResult(s.exitIPChecker.Check(ctx, job.proxyURL))
				result.Fingerprint = job.fingerprint
				switch result.Outcome {
				case model.ProxyExitIPOutcomeHealthy:
					if !canRestore {
						result = stateUpdateFailure(job.fingerprint)
					} else if _, restoreErr := restorer.Restore(ctx, job.fingerprint); restoreErr != nil {
						result = stateUpdateFailure(job.fingerprint)
					}
				case model.ProxyExitIPOutcomeProxyFailed:
					if markErr := s.poolState.MarkSuspect(ctx, job.proxyURL, result.Reason); markErr != nil {
						result = stateUpdateFailure(job.fingerprint)
					}
				}
				results[job.index] = result
			}
		}()
	}
	for index, fingerprint := range selected {
		proxyURL, saved := savedURLByFingerprint[fingerprint]
		if !saved {
			results[index] = model.ProxyExitIPCheckResult{
				Fingerprint: fingerprint,
				Outcome:     model.ProxyExitIPOutcomeNotSaved,
			}
			continue
		}
		jobs <- verificationJob{index: index, fingerprint: fingerprint, proxyURL: proxyURL}
	}
	close(jobs)
	workers.Wait()
	return model.ProxyExitIPCheckBatch{Results: results}, nil
}

func normalizeProxyExitIPFingerprints(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > maxProxyExitIPCheckBatch {
		return nil, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(input))
	selected := make([]string, 0, len(input))
	for _, candidate := range input {
		fingerprint := strings.TrimSpace(candidate)
		if fingerprint == "" {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[fingerprint]; duplicate {
			continue
		}
		seen[fingerprint] = struct{}{}
		selected = append(selected, fingerprint)
	}
	if len(selected) == 0 {
		return nil, ErrInvalidRequest
	}
	return selected, nil
}

func normalizeProxyExitIPCheckResult(result model.ProxyExitIPCheckResult) model.ProxyExitIPCheckResult {
	switch result.Outcome {
	case model.ProxyExitIPOutcomeHealthy:
		if strings.TrimSpace(result.ExitIP) == "" {
			return model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeInconclusive, Reason: "ip_echo_invalid_response"}
		}
		result.Reason = ""
	case model.ProxyExitIPOutcomeProxyFailed:
		if strings.TrimSpace(result.Reason) == "" {
			result.Reason = "proxy_transport_failed"
		}
	case model.ProxyExitIPOutcomeInconclusive:
		if strings.TrimSpace(result.Reason) == "" {
			result.Reason = "ip_echo_unavailable"
		}
	default:
		return model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeInconclusive, Reason: "ip_echo_unavailable"}
	}
	return result
}

func stateUpdateFailure(fingerprint string) model.ProxyExitIPCheckResult {
	return model.ProxyExitIPCheckResult{
		Fingerprint: fingerprint,
		Outcome:     model.ProxyExitIPOutcomeInconclusive,
		Reason:      "state_update_failed",
	}
}

func proxyFailureReason(evidence model.ProxyFailureEvidence) string {
	detail := strings.ToLower(evidence.ErrorDetail)
	if strings.Contains(detail, "context deadline exceeded") || strings.Contains(detail, "timeout") {
		return "transport_timeout"
	}
	return "transport_502_request_failed"
}

// Start owns a single bounded task worker for the process lifetime. Work is
// durable; a restart simply requeues incomplete task items after their old/new
// proxy comparison protects against overwrite.
func (s *Service) Start(parent context.Context) {
	if s == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	s.workerMu.Lock()
	if s.workerDone != nil {
		s.workerMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.workerCancel = cancel
	s.workerDone = done
	s.workerMu.Unlock()

	go func() {
		defer func() {
			s.workerMu.Lock()
			if s.workerDone == done {
				s.workerDone = nil
				s.workerCancel = nil
			}
			close(done)
			s.workerMu.Unlock()
		}()
		if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
			log.Printf("recover proxy replacement tasks: %s", safeTaskError(err))
		}
		ticker := time.NewTicker(taskWorkerPollInterval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			processed, err := s.RunPending(ctx)
			if err != nil && ctx.Err() == nil {
				log.Printf("run proxy replacement task: %s", safeTaskError(err))
			}
			if processed && err == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) StopAndWait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.workerMu.Lock()
	cancel := s.workerCancel
	done := s.workerDone
	s.workerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NewWithOptions(options Options) *Service {
	coordinator := options.MutationCoordinator
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	client := options.AuthFiles
	if client == nil {
		client = cpaauthfiles.New(nil)
	}
	concurrency := options.Concurrency
	if concurrency <= 0 || concurrency > 4 {
		concurrency = 4
	}
	return &Service{
		tasks:               options.Tasks,
		settings:            options.Settings,
		poolState:           options.PoolState,
		setupResolver:       options.SetupResolver,
		authFiles:           client,
		mutationCoordinator: coordinator,
		invalidator:         options.Invalidator,
		rechecker:           options.Rechecker,
		inspectionResults:   options.InspectionResults,
		exitIPChecker:       options.ExitIPChecker,
		concurrency:         concurrency,
		now:                 time.Now,
	}
}

// CreatePreview freezes a validated mapping but never writes CPA. The
// resulting draft expires after fifteen minutes and must be confirmed before
// any account file is touched.
func (s *Service) CreatePreview(ctx context.Context, request model.ProxyReplacementPreviewRequest) (model.ProxyReplacementTaskPage, error) {
	if s == nil || s.tasks == nil || s.settings == nil || s.poolState == nil || s.setupResolver == nil || s.authFiles == nil {
		return model.ProxyReplacementTaskPage{}, ErrUnavailable
	}
	request, err := normalizePreviewRequest(request)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	settings, _, err := s.settings.LoadProxyFilterSettings(ctx)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, fmt.Errorf("load saved proxy URLs: %w", err)
	}
	states, err := s.poolState.List(ctx)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, fmt.Errorf("load proxy pool state: %w", err)
	}
	setup, configured, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil || !configured || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return model.ProxyReplacementTaskPage{}, fmt.Errorf("%w: CPA management connection is unavailable", ErrUnavailable)
	}
	files, err := s.fetchInventory(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, fmt.Errorf("load credential inventory: %w", err)
	}

	items, excludedOld, err := buildPreviewItems(request, files)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	taskOffset := stableTaskOffset(request.Targets)
	task := model.ProxyReplacementTask{
		Source:            request.Source,
		Mode:              request.Mode,
		Status:            model.ProxyReplacementTaskDraft,
		TaskOffset:        taskOffset,
		RecheckAfterWrite: request.Source == model.ProxyReplacementSourceInspection,
		ExpiresAtMS:       s.currentTime().Add(previewTTL).UnixMilli(),
	}
	if request.Mode == model.ProxyReplacementModeUniform {
		for index := range items {
			items[index].NewProxyURL = request.UniformProxyURL
			items[index].NewProxyFingerprint = model.ProxyFingerprint(request.UniformProxyURL)
			items[index].AllocationSource = "uniform"
		}
	} else {
		unused, all, counts := buildPoolCandidates(settings.URLs, states, files, excludedOld)
		task.UnusedNormalCount = counts.unused
		task.FallbackNormalCount = counts.fallback
		task.SuspectExcludedCount = counts.suspect
		assignments := allocatePool(int64(taskOffset), unused, all, len(items))
		if len(assignments) != len(items) {
			task.Status = model.ProxyReplacementTaskUnavailable
			task.ExpiresAtMS = 0
		} else {
			for index, assignment := range assignments {
				items[index].NewProxyURL = assignment.Candidate.URL
				items[index].NewProxyFingerprint = assignment.Candidate.Fingerprint
				items[index].AllocationSource = assignment.Source
			}
		}
	}
	for index := range items {
		items[index].Status = model.ProxyReplacementItemPlanned
	}
	created, err := s.tasks.CreateDraft(ctx, task, items)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	created.CanConfirm = created.Status == model.ProxyReplacementTaskDraft && created.ExpiresAtMS > s.currentTime().UnixMilli()
	return model.ProxyReplacementTaskPage{
		Task:     created,
		Items:    items,
		Page:     1,
		PageSize: len(items),
		Total:    len(items),
	}, nil
}

type poolCandidateCounts struct {
	unused   int
	fallback int
	suspect  int
}

func buildPoolCandidates(savedURLs []string, states []model.ProxyPoolState, files []cpaauthfiles.File, excludedOld map[string]struct{}) ([]proxyCandidate, []proxyCandidate, poolCandidateCounts) {
	suspect := make(map[string]struct{}, len(states))
	for _, state := range states {
		if state.Status == model.ProxyPoolStatusSuspect {
			suspect[strings.TrimSpace(state.Fingerprint)] = struct{}{}
		}
	}
	inUse := make(map[string]struct{})
	for _, file := range files {
		if file.Disabled {
			continue
		}
		if fingerprint := model.ProxyFingerprint(proxyURLFromFile(file)); fingerprint != "" {
			inUse[fingerprint] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(savedURLs))
	all := make([]proxyCandidate, 0, len(savedURLs))
	unused := make([]proxyCandidate, 0, len(savedURLs))
	counts := poolCandidateCounts{}
	for _, rawURL := range savedURLs {
		url := model.NormalizeProxyURL(rawURL)
		fingerprint := model.ProxyFingerprint(url)
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		if _, isSuspect := suspect[fingerprint]; isSuspect {
			counts.suspect++
			continue
		}
		if _, blocked := excludedOld[fingerprint]; blocked {
			continue
		}
		_, used := inUse[fingerprint]
		candidate := proxyCandidate{URL: url, Fingerprint: fingerprint, InUse: used}
		all = append(all, candidate)
		if !used {
			unused = append(unused, candidate)
		}
	}
	counts.unused = len(unused)
	counts.fallback = len(all) - len(unused)
	return unused, all, counts
}

func buildPreviewItems(request model.ProxyReplacementPreviewRequest, files []cpaauthfiles.File) ([]model.ProxyReplacementTaskItem, map[string]struct{}, error) {
	items := make([]model.ProxyReplacementTaskItem, 0, len(request.Targets))
	excludedOld := make(map[string]struct{})
	for index, target := range request.Targets {
		file, found := findCurrentFile(files, target)
		if !found {
			return nil, nil, fmt.Errorf("%w: credential target %d is no longer present", ErrInvalidRequest, index+1)
		}
		oldProxyURL := model.NormalizeProxyURL(proxyURLFromFile(file))
		item := model.ProxyReplacementTaskItem{
			ItemOrder:           index,
			Target:              target,
			OldProxyURL:         oldProxyURL,
			OldProxyFingerprint: model.ProxyFingerprint(oldProxyURL),
		}
		items = append(items, item)
		if request.Source == model.ProxyReplacementSourceInspection && item.OldProxyFingerprint != "" {
			excludedOld[item.OldProxyFingerprint] = struct{}{}
		}
	}
	return items, excludedOld, nil
}

func normalizePreviewRequest(request model.ProxyReplacementPreviewRequest) (model.ProxyReplacementPreviewRequest, error) {
	request.UniformProxyURL = model.NormalizeProxyURL(request.UniformProxyURL)
	if len(request.Targets) == 0 || len(request.Targets) > 500 {
		return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: target count must be between 1 and 500", ErrInvalidRequest)
	}
	switch request.Source {
	case model.ProxyReplacementSourceInspection:
		if request.Mode != model.ProxyReplacementModePool {
			return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: inspection replacement requires pool mode", ErrInvalidRequest)
		}
	case model.ProxyReplacementSourceBatchUniform:
		if request.Mode != model.ProxyReplacementModeUniform || request.UniformProxyURL == "" {
			return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: batch uniform replacement requires a proxy URL", ErrInvalidRequest)
		}
	case model.ProxyReplacementSourceBatchPool:
		if request.Mode != model.ProxyReplacementModePool {
			return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: batch pool replacement requires pool mode", ErrInvalidRequest)
		}
	default:
		return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: source is unsupported", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(request.Targets))
	for index := range request.Targets {
		target := request.Targets[index]
		target.ClientKey = strings.TrimSpace(target.ClientKey)
		target.FileName = strings.TrimSpace(target.FileName)
		target.AuthIndex = strings.TrimSpace(target.AuthIndex)
		target.AccountEmail = strings.ToLower(strings.TrimSpace(target.AccountEmail))
		target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
		target.RuntimeID = strings.TrimSpace(target.RuntimeID)
		target.AccountID = strings.TrimSpace(target.AccountID)
		target.AccountSnapshot = strings.TrimSpace(target.AccountSnapshot)
		if target.ClientKey == "" || target.FileName == "" || target.Provider == "" || (target.AuthIndex == "" && target.RuntimeID == "") {
			return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: target %d identity is incomplete", ErrInvalidRequest, index+1)
		}
		if _, duplicate := seen[target.ClientKey]; duplicate {
			return model.ProxyReplacementPreviewRequest{}, fmt.Errorf("%w: duplicate target key", ErrInvalidRequest)
		}
		seen[target.ClientKey] = struct{}{}
		request.Targets[index] = target
	}
	return request, nil
}

func findCurrentFile(files []cpaauthfiles.File, target model.ProxyReplacementTarget) (cpaauthfiles.File, bool) {
	for _, file := range files {
		if target.RuntimeID != "" && strings.TrimSpace(file.ID) != target.RuntimeID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(file.Name), target.FileName) {
			continue
		}
		if target.AuthIndex != "" && strings.TrimSpace(file.AuthIndex) != target.AuthIndex {
			continue
		}
		if target.Provider != "" && !strings.EqualFold(strings.TrimSpace(file.Provider), target.Provider) {
			continue
		}
		if target.AccountEmail != "" && !strings.EqualFold(strings.TrimSpace(file.AccountSnapshot), target.AccountEmail) {
			continue
		}
		return file, true
	}
	return cpaauthfiles.File{}, false
}

func proxyURLFromFile(file cpaauthfiles.File) string {
	for _, key := range []string{"proxy_url", "proxyUrl", "proxy-url"} {
		if raw, found := file.Raw[key]; found {
			if value, ok := raw.(string); ok {
				return model.NormalizeProxyURL(value)
			}
		}
	}
	return ""
}

// fetchInventory uses the CPA extension when available. Older tests and
// deployments retain the base Fetch method while a current CPA can return
// proxy_url with include_proxy_url=1 in the same snapshot.
func (s *Service) fetchInventory(ctx context.Context, baseURL, managementKey string) ([]cpaauthfiles.File, error) {
	if extended, ok := s.authFiles.(interface {
		FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error)
	}); ok {
		return extended.FetchWithProxyURLs(ctx, baseURL, managementKey)
	}
	return s.authFiles.Fetch(ctx, baseURL, managementKey)
}

func stableTaskOffset(targets []model.ProxyReplacementTarget) int {
	hasher := fnv.New32a()
	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, strings.TrimSpace(target.ClientKey))
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = hasher.Write([]byte(key))
		_, _ = hasher.Write([]byte{0})
	}
	return int(hasher.Sum32() & 0x7fffffff)
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Confirm atomically queues the already frozen preview. It never recomputes a
// mapping, so the operator confirms exactly the URLs shown in the preview.
func (s *Service) Confirm(ctx context.Context, taskID int64) (model.ProxyReplacementTask, error) {
	if s == nil || s.tasks == nil {
		return model.ProxyReplacementTask{}, ErrUnavailable
	}
	if taskID <= 0 {
		return model.ProxyReplacementTask{}, ErrInvalidRequest
	}
	_, _ = s.tasks.ExpireDrafts(ctx, s.currentTime().UnixMilli())
	task, err := s.tasks.ConfirmDraft(ctx, taskID, s.currentTime().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "cannot be confirmed") {
			return model.ProxyReplacementTask{}, ErrTaskExpired
		}
		return model.ProxyReplacementTask{}, err
	}
	return task, nil
}

// Recover makes previously interrupted work eligible for a fresh safe
// execution. It intentionally does not launch uncontrolled background work;
// the application lifecycle calls RunPending in its bounded worker loop.
func (s *Service) Recover(ctx context.Context) error {
	if s == nil || s.tasks == nil {
		return ErrUnavailable
	}
	if _, err := s.tasks.ResetInFlightItems(ctx); err != nil {
		return err
	}
	_, err := s.tasks.RequeueRunningOnStartup(ctx)
	return err
}

// RunPending claims at most one durable task. The caller may invoke it in a
// loop; that keeps shutdown and resource bounds controlled by App lifecycle.
func (s *Service) RunPending(ctx context.Context) (bool, error) {
	if s == nil || s.tasks == nil || s.setupResolver == nil || s.authFiles == nil || s.mutationCoordinator == nil {
		return false, ErrUnavailable
	}
	_, _ = s.tasks.ExpireDrafts(ctx, s.currentTime().UnixMilli())
	task, found, err := s.tasks.ClaimNextQueued(ctx)
	if err != nil || !found {
		return found, err
	}
	if err := s.executeTask(ctx, task); err != nil {
		// The durable task remains running only for a process crash. A normal
		// execution-level failure is represented through individual outcomes.
		return true, err
	}
	return true, nil
}

func (s *Service) executeTask(ctx context.Context, task model.ProxyReplacementTask) error {
	setup, configured, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil || !configured || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		return s.failAllTaskItems(ctx, task.ID, "cpa_not_configured", "CPA 管理连接不可用")
	}
	page, err := s.tasks.GetTask(ctx, task.ID, 1, 1000)
	if err != nil {
		return err
	}
	items := page.Items
	if len(items) == 0 {
		_, err := s.tasks.CompleteTask(ctx, task.ID)
		return err
	}

	jobs := groupPhysicalItems(items)
	workerCount := s.concurrency
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}
	if workerCount < 1 {
		workerCount = 1
	}
	work := make(chan []model.ProxyReplacementTaskItem)
	var workers sync.WaitGroup
	var changedMu sync.Mutex
	changed := false
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range work {
				if s.executePhysicalItems(ctx, setup, group) {
					changedMu.Lock()
					changed = true
					changedMu.Unlock()
				}
			}
		}()
	}
	for _, group := range jobs {
		work <- group
	}
	close(work)
	workers.Wait()

	if task.RecheckAfterWrite && s.rechecker != nil {
		s.runRechecks(ctx, task.ID)
	}
	if _, err := s.tasks.CompleteTask(ctx, task.ID); err != nil {
		return err
	}
	changedMu.Lock()
	shouldInvalidate := changed
	changedMu.Unlock()
	if shouldInvalidate && s.invalidator != nil {
		s.invalidator.InvalidateAuthFileInventory(context.Background())
	}
	return nil
}

func groupPhysicalItems(items []model.ProxyReplacementTaskItem) [][]model.ProxyReplacementTaskItem {
	groups := make(map[string][]model.ProxyReplacementTaskItem)
	keys := make([]string, 0)
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Target.FileName))
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], item)
	}
	sort.Strings(keys)
	result := make([][]model.ProxyReplacementTaskItem, 0, len(keys))
	for _, key := range keys {
		result = append(result, groups[key])
	}
	return result
}

func (s *Service) executePhysicalItems(ctx context.Context, setup store.Setup, items []model.ProxyReplacementTaskItem) bool {
	if len(items) == 0 {
		return false
	}
	release, err := s.mutationCoordinator.Acquire(ctx, items[0].Target.FileName)
	if err != nil {
		for _, item := range items {
			_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemWriteFailed, model.ProxyReplacementItemOutcome{
				WriteErrorCode: "lock_failed", WriteErrorMessage: "凭证写入锁不可用",
			})
		}
		return false
	}
	defer release()

	changed := false
	for _, item := range items {
		_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemWriting, model.ProxyReplacementItemOutcome{})
		target, resolveErr := s.resolveCurrentProxyMutationTarget(ctx, setup, item.Target)
		if resolveErr != nil {
			outcome := model.ProxyReplacementItemOutcome{
				WriteErrorCode: "credential_lookup_failed", WriteErrorMessage: "无法读取当前凭证",
			}
			if errors.Is(resolveErr, cpaauthfiles.ErrAuthFileNotFound) || errors.Is(resolveErr, cpaauthfiles.ErrIdentityMismatch) {
				outcome.WriteErrorCode = "credential_not_found"
				outcome.WriteErrorMessage = "未找到当前凭证"
			}
			_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemWriteFailed, outcome)
			continue
		}

		currentProxy := model.NormalizeProxyURL(proxyURLFromFile(target.File))
		if currentProxy == model.NormalizeProxyURL(item.NewProxyURL) {
			_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemUpdated, model.ProxyReplacementItemOutcome{})
			changed = true
			continue
		}
		if currentProxy != model.NormalizeProxyURL(item.OldProxyURL) {
			_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemSkippedStale, model.ProxyReplacementItemOutcome{
				WriteErrorCode: "stale_proxy", WriteErrorMessage: "账号代理已被其他操作修改，未覆盖",
			})
			continue
		}
		if err := s.patchOne(ctx, setup, target, item); err != nil {
			_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemWriteFailed, model.ProxyReplacementItemOutcome{
				WriteErrorCode: "proxy_write_failed", WriteErrorMessage: safeTaskError(err),
			})
			continue
		}
		_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemUpdated, model.ProxyReplacementItemOutcome{})
		changed = true
	}
	return changed
}

// resolveCurrentProxyMutationTarget obtains one live credential under the
// physical-file mutation lock. It deliberately requests include_proxy_url=1 so
// stale-write checks do not need a full credential inventory download.
func (s *Service) resolveCurrentProxyMutationTarget(
	ctx context.Context,
	setup store.Setup,
	replacementTarget model.ProxyReplacementTarget,
) (cpaauthfiles.StatusMutationTarget, error) {
	selector := strings.TrimSpace(replacementTarget.RuntimeID)
	if selector == "" {
		selector = strings.TrimSpace(replacementTarget.FileName)
	}
	target, err := s.authFiles.ResolveProxyURLMutationTarget(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		selector,
		replacementTarget.AuthIndex,
	)
	if err != nil {
		return cpaauthfiles.StatusMutationTarget{}, err
	}
	identitySnapshot := strings.TrimSpace(replacementTarget.AccountSnapshot)
	if identitySnapshot == "" {
		identitySnapshot = strings.TrimSpace(replacementTarget.AccountEmail)
	}
	if err := cpaauthfiles.VerifyResolvedIdentity(target.File, cpaauthfiles.Identity{
		AuthFileName:      replacementTarget.FileName,
		RuntimeID:         replacementTarget.RuntimeID,
		AuthIndex:         replacementTarget.AuthIndex,
		Provider:          replacementTarget.Provider,
		AccountSnapshot:   identitySnapshot,
		AccountIDSnapshot: replacementTarget.AccountID,
	}); err != nil {
		return cpaauthfiles.StatusMutationTarget{}, err
	}
	if expectedEmail := strings.TrimSpace(replacementTarget.AccountEmail); expectedEmail != "" &&
		!strings.EqualFold(strings.TrimSpace(target.File.AccountSnapshot), expectedEmail) {
		return cpaauthfiles.StatusMutationTarget{}, fmt.Errorf("%w: account email mismatch", cpaauthfiles.ErrIdentityMismatch)
	}
	return target, nil
}

func (s *Service) patchOne(
	ctx context.Context,
	setup store.Setup,
	target cpaauthfiles.StatusMutationTarget,
	item model.ProxyReplacementTaskItem,
) error {
	err := s.authFiles.PatchProxyURLTarget(ctx, setup.CPAUpstreamURL, setup.ManagementKey, target, item.NewProxyURL)
	if err == nil || !cpaauthfiles.IsPluginVirtualMutationConflict(err) {
		return err
	}
	physicalName := strings.TrimSpace(item.Target.FileName)
	if physicalName == "" {
		return err
	}
	raw, downloadErr := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, physicalName)
	if downloadErr != nil {
		return downloadErr
	}
	next, rewriteErr := cpaauthfiles.RewriteProxyURL(raw, cpaauthfiles.ProxyRecordLocator{
		AuthIndex: item.Target.AuthIndex, AccountEmail: item.Target.AccountEmail,
	}, item.OldProxyURL, item.NewProxyURL)
	if rewriteErr != nil {
		return rewriteErr
	}
	return s.authFiles.Upload(ctx, setup.CPAUpstreamURL, setup.ManagementKey, physicalName, next)
}

func (s *Service) failAllTaskItems(ctx context.Context, taskID int64, code, message string) error {
	page, err := s.tasks.GetTask(ctx, taskID, 1, 1000)
	if err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemWriteFailed, model.ProxyReplacementItemOutcome{
			WriteErrorCode: code, WriteErrorMessage: message,
		}); err != nil {
			return err
		}
	}
	_, err = s.tasks.CompleteTask(ctx, taskID)
	return err
}

func (s *Service) runRechecks(ctx context.Context, taskID int64) {
	page, err := s.tasks.GetTask(ctx, taskID, 1, 1000)
	if err != nil {
		return
	}
	targets := make([]model.ProxyReplacementTarget, 0)
	itemsByKey := make(map[string]model.ProxyReplacementTaskItem)
	itemIDs := make([]int64, 0)
	for _, item := range page.Items {
		if item.Status != model.ProxyReplacementItemUpdated {
			continue
		}
		_ = s.tasks.MarkItem(ctx, item.ID, model.ProxyReplacementItemRecheckRunning, model.ProxyReplacementItemOutcome{})
		targets = append(targets, item.Target)
		itemsByKey[item.Target.ClientKey] = item
		itemIDs = append(itemIDs, item.ID)
	}
	if len(targets) == 0 {
		return
	}
	results := s.rechecker.RecheckProxyReplacement(ctx, targets, s.concurrency)
	byKey := make(map[string]model.ProxyReplacementProbeResult, len(results))
	for _, result := range results {
		byKey[result.Target.ClientKey] = result
	}

	// A pre-existing suspect entry is restored only after a directed recheck
	// proves that the replacement proxy reached the upstream.  This lookup also
	// avoids issuing a no-op Restore call for ordinary pool URLs that have never
	// been quarantined.
	preexistingSuspects := make(map[string]struct{})
	if s.poolState != nil {
		states, stateErr := s.poolState.List(ctx)
		if stateErr != nil {
			log.Printf("load proxy pool state for recheck reconciliation: %s", safeTaskError(stateErr))
		} else {
			for _, state := range states {
				if state.Status != model.ProxyPoolStatusSuspect {
					continue
				}
				if fingerprint := strings.TrimSpace(state.Fingerprint); fingerprint != "" {
					preexistingSuspects[fingerprint] = struct{}{}
				}
			}
		}
	}
	type poolRecheckEvidence struct {
		verifiedTransport bool
		proxySuspect      bool
	}
	evidenceByFingerprint := make(map[string]poolRecheckEvidence)
	succeededTargets := make([]model.ProxyReplacementTarget, 0, len(targets))
	for index, target := range targets {
		result, found := byKey[target.ClientKey]
		item, hasItem := itemsByKey[target.ClientKey]
		fingerprint := ""
		if hasItem {
			fingerprint = strings.TrimSpace(item.NewProxyFingerprint)
			if fingerprint == "" {
				fingerprint = model.ProxyFingerprint(item.NewProxyURL)
			}
		}
		if found && fingerprint != "" {
			evidence := evidenceByFingerprint[fingerprint]
			if result.ProxySuspect {
				evidence.proxySuspect = true
			} else if recheckTransportVerified(result) {
				// HTTP authentication/quota failures are still proof that the
				// proxy transport connected. Internal lookup/configuration
				// failures are not, and must leave a quarantine intact.
				evidence.verifiedTransport = true
			}
			evidenceByFingerprint[fingerprint] = evidence
		}
		if !found || !result.Succeeded {
			message := "复检失败"
			code := "recheck_failed"
			if found && strings.TrimSpace(result.ErrorMessage) != "" {
				message = result.ErrorMessage
			}
			if found && strings.TrimSpace(result.ErrorCode) != "" {
				code = result.ErrorCode
			}
			if found && result.ProxySuspect && s.poolState != nil && hasItem {
				// Store only the new proxy fingerprint and a finite reason code.
				// Recheck failure never rolls this value back or triggers another
				// automatic pool allocation.
				_ = s.poolState.MarkPending(ctx, item.NewProxyURL, proxySuspectReasonFromRecheck(result))
			}
			_ = s.tasks.MarkItem(ctx, itemIDs[index], model.ProxyReplacementItemRecheckFailed, model.ProxyReplacementItemOutcome{
				RecheckErrorCode: code, RecheckErrorMessage: message,
			})
			continue
		}
		_ = s.tasks.MarkItem(ctx, itemIDs[index], model.ProxyReplacementItemRecheckSucceeded, model.ProxyReplacementItemOutcome{})
		succeededTargets = append(succeededTargets, target)
	}

	// A single explicit transport failure wins for a shared proxy in this batch.
	// That prevents result order from resurrecting a genuinely bad pool entry.
	if restorer, ok := s.poolState.(poolStateRestorer); ok {
		for fingerprint, evidence := range evidenceByFingerprint {
			if !evidence.verifiedTransport || evidence.proxySuspect {
				continue
			}
			if _, wasSuspect := preexistingSuspects[fingerprint]; !wasSuspect {
				continue
			}
			if _, restoreErr := restorer.Restore(ctx, fingerprint); restoreErr != nil {
				// The task result remains authoritative. A later explicit
				// recheck can reconcile a transient pool-state write failure.
				log.Printf("restore proxy pool state after directed recheck: %s", safeTaskError(restoreErr))
			}
		}
	}
	if len(succeededTargets) > 0 && s.inspectionResults != nil {
		if err := s.inspectionResults.ClearProxySuspects(ctx, succeededTargets); err != nil {
			// The credential write and recheck are already authoritative. Leave
			// task success intact and retry reconciliation on a later explicit
			// inspection rather than falsely reporting a write failure.
			log.Printf("reconcile successful proxy recheck state: %s", safeTaskError(err))
		}
	}
}

// recheckTransportVerified distinguishes an actual CPA/ChatGPT HTTP response
// from local bookkeeping failures such as missing credentials or cancelled
// work. The latter cannot prove that a proxy has recovered.
func recheckTransportVerified(result model.ProxyReplacementProbeResult) bool {
	if result.ProxySuspect {
		return false
	}
	if result.Succeeded {
		return true
	}
	code := strings.ToLower(strings.TrimSpace(result.ErrorCode))
	return code == "http_status" || strings.HasPrefix(code, "http_")
}

func proxySuspectReasonFromRecheck(result model.ProxyReplacementProbeResult) string {
	code := strings.ToLower(strings.TrimSpace(result.ErrorCode))
	message := strings.ToLower(strings.TrimSpace(result.ErrorMessage))
	if strings.Contains(code, "timeout") || strings.Contains(message, "timeout") || strings.Contains(message, "deadline") {
		return "transport_timeout"
	}
	if strings.Contains(code, "502") || strings.Contains(message, "request failed") {
		return "transport_502_request_failed"
	}
	return "transport_unknown"
}

func safeTaskError(err error) string {
	if err == nil {
		return ""
	}
	// CPA errors may include credential-adjacent response bodies. Preserve a
	// useful but bounded reason for the authenticated task UI without URLs.
	message := redactProxyURL(strings.TrimSpace(err.Error()))
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func redactProxyURL(message string) string {
	return proxyURLPattern.ReplaceAllString(message, "[redacted-url]")
}

// proxyCandidate is intentionally internal: it holds raw URLs only while a
// preview is being composed or a confirmed task is executing. Persistent pool
// state stores the fingerprint instead.
type proxyCandidate struct {
	URL         string
	Fingerprint string
	InUse       bool
}

type proxyAssignment struct {
	Candidate proxyCandidate
	Source    string
}

// allocatePool first grants every currently unoccupied candidate once, then
// rotates the complete normal candidate set with a stable task offset. This
// prevents one high-priority broken account from repeatedly receiving itself
// while preserving predictable, evenly distributed fallback reuse.
func allocatePool(taskID int64, unused, all []proxyCandidate, targetCount int) []proxyAssignment {
	if targetCount <= 0 || len(all) == 0 {
		return []proxyAssignment{}
	}
	result := make([]proxyAssignment, 0, targetCount)
	if len(unused) > 0 {
		unusedStart := int(taskID % int64(len(unused)))
		for step := 0; step < len(unused) && len(result) < targetCount; step++ {
			candidate := unused[(unusedStart+step)%len(unused)]
			result = append(result, proxyAssignment{Candidate: candidate, Source: "unused"})
		}
	}
	allStart := int(taskID % int64(len(all)))
	for cycle := 0; len(result) < targetCount; cycle++ {
		candidate := all[(allStart+cycle)%len(all)]
		source := "fallback"
		if !candidate.InUse {
			source = "unused"
		}
		result = append(result, proxyAssignment{Candidate: candidate, Source: source})
	}
	return result
}
