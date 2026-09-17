package worker

import (
	"context"
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

// ServerErrorPriorityDemotionWorker lowers a credential's current priority by
// one for each newly persisted 5xx request-monitoring event. The collector only
// forwards inserted event hashes, so a historical event is never replayed by a
// page refresh or process restart.
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
	nextPriority := currentPriority - 1
	if err := client.PatchPriorityTarget(ctx, candidate.BaseURL, candidate.ManagementKey, target, nextPriority); err != nil {
		log.Printf("[server-error-priority] failed to lower priority for auth file %q event=%q: %v", candidate.FileName, candidate.EventHash, err)
		return
	}
	log.Printf("[server-error-priority] lowered auth file %q priority %d -> %d after HTTP %d event=%q", candidate.FileName, currentPriority, nextPriority, candidate.StatusCode, candidate.EventHash)
}

func serverErrorPriorityDemotionCandidateFromEvent(
	event usage.Event,
	baseURL string,
	managementKey string,
) (serverErrorPriorityDemotionCandidate, bool) {
	if !event.Failed || event.FailStatusCode < http.StatusInternalServerError || event.FailStatusCode > 599 {
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
