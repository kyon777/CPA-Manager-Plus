package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/servererrorpriority"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	serverErrorPriorityDemotionQueueSize     = 256
	serverErrorPriorityDemotionActionTimeout = 30 * time.Second
	serverErrorPriorityInventoryCacheTTL     = 20 * time.Second
	serverErrorPriorityFailureCooldown       = 45 * time.Second
)

// ServerErrorPriorityDemotionWorker rebalances a credential's priority for
// each newly persisted HTTP 502 or 503 request-monitoring event, plus HTTP 429
// only when its raw response body explicitly reports
// {"detail":"Rate limit exceeded"}, and narrowly-qualified HTTP 500 Codex
// responses transport failures (unexpected EOF or the exact proxy CONNECT 407
// authentication response). The target is moved below the highest
// eligible peer when it is currently above that peer; otherwise it moves down
// one step. The collector only forwards inserted event hashes, so a historical
// event is never replayed by a page refresh or process restart.
type ServerErrorPriorityDemotionWorker struct {
	client            *http.Client
	authFileMutations *cpaauthfiles.MutationCoordinator
	setupResolver     serverErrorPrioritySetupResolver
	jobs              chan serverErrorPriorityDemotionCandidate

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
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	return &ServerErrorPriorityDemotionWorker{
		client:            &http.Client{Timeout: serverErrorPriorityDemotionActionTimeout},
		authFileMutations: coordinator,
		setupResolver:     resolver,
		jobs:              make(chan serverErrorPriorityDemotionCandidate, serverErrorPriorityDemotionQueueSize),
		inventoryCache:    make(map[string]serverErrorPriorityInventorySnapshot),
		now:               time.Now,
	}
}

func (w *ServerErrorPriorityDemotionWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	go w.run(ctx)
}

func (w *ServerErrorPriorityDemotionWorker) HandleUsageEvents(
	ctx context.Context,
	cfg collectorpkg.RuntimeConfig,
	events []usage.Event,
) {
	if w == nil || len(events) == 0 {
		return
	}
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
		select {
		case w.jobs <- candidate:
		case <-ctx.Done():
			return
		default:
			log.Printf("[server-error-priority] job queue full, dropped auth file %q event=%q", candidate.FileName, candidate.EventHash)
		}
	}
}

func (w *ServerErrorPriorityDemotionWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case candidate := <-w.jobs:
			w.handleCandidate(ctx, candidate)
		}
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

// peerInventory caches the complete CPA auth-file snapshot briefly. A single
// worker processes candidates sequentially, so this eliminates the repeated
// expensive full inventory scan generated by bursts of 502/503/qualified-429
// events without weakening the per-credential identity verification above.
// Failed reads use a short cooldown rather than stacking 30-second upstream
// timeouts behind every queued event.
func (w *ServerErrorPriorityDemotionWorker) peerInventory(
	ctx context.Context,
	client *cpaauthfiles.Client,
	candidate serverErrorPriorityDemotionCandidate,
) ([]cpaauthfiles.File, error) {
	if w == nil {
		return client.Fetch(ctx, candidate.BaseURL, candidate.ManagementKey)
	}
	cacheKey := serverErrorPriorityInventoryKey(candidate)
	now := w.currentTime()

	w.inventoryMu.Lock()
	snapshot, ok := w.inventoryCache[cacheKey]
	if ok && len(snapshot.files) > 0 && now.Sub(snapshot.fetchedAt) < serverErrorPriorityInventoryCacheTTL {
		files := cloneServerErrorPriorityFiles(snapshot.files)
		w.inventoryMu.Unlock()
		return files, nil
	}
	if ok && !snapshot.failureAt.IsZero() && now.Sub(snapshot.failureAt) < serverErrorPriorityFailureCooldown {
		err := fmt.Errorf("peer inventory retry is cooling down after: %s", snapshot.failureText)
		w.inventoryMu.Unlock()
		return nil, err
	}
	w.inventoryMu.Unlock()

	files, err := client.Fetch(ctx, candidate.BaseURL, candidate.ManagementKey)
	if err != nil {
		w.inventoryMu.Lock()
		w.inventoryCache[cacheKey] = serverErrorPriorityInventorySnapshot{
			failureAt:   now,
			failureText: serverErrorPrioritySafeFailureText(err),
		}
		w.inventoryMu.Unlock()
		return nil, err
	}

	w.inventoryMu.Lock()
	w.inventoryCache[cacheKey] = serverErrorPriorityInventorySnapshot{
		files:     cloneServerErrorPriorityFiles(files),
		fetchedAt: now,
	}
	w.inventoryMu.Unlock()
	return files, nil
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
	if w == nil || w.setupResolver == nil {
		return candidate, strings.TrimSpace(candidate.BaseURL) != "" && strings.TrimSpace(candidate.ManagementKey) != ""
	}
	setup, configured, err := w.setupResolver.ResolveSetup(ctx)
	if err != nil {
		log.Printf("[server-error-priority] skip auth file %q event=%q: resolve CPA setup failed: %v", candidate.FileName, candidate.EventHash, err)
		return candidate, false
	}
	if !configured || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		log.Printf("[server-error-priority] skip auth file %q event=%q: CPA setup is unavailable", candidate.FileName, candidate.EventHash)
		return candidate, false
	}
	candidate.BaseURL = strings.TrimSpace(setup.CPAUpstreamURL)
	candidate.ManagementKey = strings.TrimSpace(setup.ManagementKey)
	return candidate, true
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
