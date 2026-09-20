package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	serverErrorPriorityDemotionQueueSize     = 256
	serverErrorPriorityDemotionActionTimeout = 30 * time.Second
)

// ServerErrorPriorityDemotionWorker rebalances a credential's priority for
// each newly persisted HTTP 502 or 503 request-monitoring event, plus HTTP 429
// only when its raw response body explicitly reports
// {"detail":"Rate limit exceeded"}. The target is moved below the highest
// eligible peer when it is currently above that peer; otherwise it moves down
// one step. The collector only forwards inserted event hashes, so a historical
// event is never replayed by a page refresh or process restart.
type ServerErrorPriorityDemotionWorker struct {
	client            *http.Client
	authFileMutations *cpaauthfiles.MutationCoordinator
	jobs              chan serverErrorPriorityDemotionCandidate
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
	if coordinator == nil {
		coordinator = cpaauthfiles.NewMutationCoordinator()
	}
	return &ServerErrorPriorityDemotionWorker{
		client:            &http.Client{Timeout: serverErrorPriorityDemotionActionTimeout},
		authFileMutations: coordinator,
		jobs:              make(chan serverErrorPriorityDemotionCandidate, serverErrorPriorityDemotionQueueSize),
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
	if baseURL == "" || managementKey == "" {
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
	if w == nil || candidate.FileName == "" || candidate.BaseURL == "" || candidate.ManagementKey == "" {
		return
	}
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
	allFiles, err := client.Fetch(ctx, candidate.BaseURL, candidate.ManagementKey)
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
	log.Printf("[server-error-priority] lowered auth file %q priority %d -> %d after HTTP %d event=%q peer_max=%d has_peer=%t", candidate.FileName, currentPriority, nextPriority, candidate.StatusCode, candidate.EventHash, peerMax, hasPeer)
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
	if !isServerErrorPriorityDemotionEvent(event) {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	fileName := strings.TrimSpace(event.AuthFileSnapshot)
	provider := credentialpolicy.NormalizeProvider(firstNonEmpty(event.AuthProviderSnapshot, event.Provider))
	if fileName == "" || provider == "" {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	candidate := serverErrorPriorityDemotionCandidate{
		BaseURL:         strings.TrimSpace(baseURL),
		ManagementKey:   strings.TrimSpace(managementKey),
		FileName:        fileName,
		AuthIndex:       strings.TrimSpace(event.AuthIndex),
		Provider:        provider,
		AccountID:       strings.TrimSpace(event.AuthAccountIDSnapshot),
		AccountSnapshot: stableEventAccountSnapshot(fileName, event.AccountSnapshot),
		EventHash:       strings.TrimSpace(event.EventHash),
		StatusCode:      event.FailStatusCode,
	}
	if candidate.BaseURL == "" || candidate.ManagementKey == "" {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	if candidate.Provider == "codex" && candidate.AuthIndex == "" {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	if candidate.AuthIndex == "" && candidate.AccountID == "" && candidate.AccountSnapshot == "" {
		return serverErrorPriorityDemotionCandidate{}, false
	}
	return candidate, true
}

func isServerErrorPriorityDemotionEvent(event usage.Event) bool {
	if !event.Failed {
		return false
	}
	switch event.FailStatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	case http.StatusTooManyRequests:
		return isQualifiedRateLimit429(event.FailBody)
	default:
		return false
	}
}

// isQualifiedRateLimit429 deliberately requires the raw local-only response
// body. A generic 429 can mean many different things; only the precise
// TokenAcquisition rate-limit response is eligible for priority demotion.
func isQualifiedRateLimit429(failBody string) bool {
	var response struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(failBody)), &response); err != nil {
		return false
	}
	return response.Detail == "Rate limit exceeded"
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
