// Package authfilesselecteddelete safely deletes the current physical CPA
// auth-file sources selected by a Manager user. It is intentionally separate
// from account-banned deletion: ordinary selected credentials do not need a
// token-recovery terminal-state predicate, but still require the same exact
// membership proof before a physical source can be removed.
package authfilesselecteddelete

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	MaxTargets     = 100
	MaxConcurrency = 8
)

var (
	ErrUnavailable    = errors.New("selected auth-file delete is unavailable")
	ErrTooManyTargets = fmt.Errorf("selected auth-file delete accepts at most %d targets", MaxTargets)
)

// SetupResolver resolves the persisted CPA management connection. Browser
// input never supplies the management key or an upstream URL.
type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

// AuthFilesClient is the intentionally small CPA boundary used after one
// verified inventory snapshot has been collected.
type AuthFilesClient interface {
	Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
	Delete(context.Context, string, string, string) error
}

type InventoryInvalidator interface {
	InvalidateAuthFileInventory(context.Context)
}

type Options struct {
	SetupResolver       SetupResolver
	AuthFiles           AuthFilesClient
	MutationCoordinator *cpaauthfiles.MutationCoordinator
	Invalidator         InventoryInvalidator
	Concurrency         int
}

type Service struct {
	setupResolver       SetupResolver
	authFiles           AuthFilesClient
	mutationCoordinator *cpaauthfiles.MutationCoordinator
	invalidator         InventoryInvalidator
	concurrency         int
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
	if concurrency <= 0 || concurrency > MaxConcurrency {
		concurrency = MaxConcurrency
	}
	return &Service{
		setupResolver:       options.SetupResolver,
		authFiles:           client,
		mutationCoordinator: coordinator,
		invalidator:         options.Invalidator,
		concurrency:         concurrency,
	}
}

// Target contains only current, stable identity evidence. It intentionally
// excludes credential JSON, access tokens, management keys, and proxy fields.
type Target struct {
	FileName        string `json:"fileName"`
	AuthIndex       string `json:"authIndex,omitempty"`
	Provider        string `json:"provider"`
	RuntimeID       string `json:"runtimeId,omitempty"`
	AccountID       string `json:"accountId,omitempty"`
	AccountSnapshot string `json:"accountSnapshot,omitempty"`
}

type Item struct {
	FileName     string `json:"fileName"`
	PhysicalName string `json:"physicalName,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
}

type Result struct {
	Requested int    `json:"requested"`
	Deleted   []Item `json:"deleted"`
	Skipped   []Item `json:"skipped"`
	Failed    []Item `json:"failed"`
}

type outcomeKind uint8

const (
	outcomeSkipped outcomeKind = iota + 1
	outcomeDeleted
	outcomeFailed
)

type targetOutcome struct {
	kind outcomeKind
	item Item
}

type preparedTarget struct {
	index  int
	target Target
}

type physicalJob struct {
	physicalName string
	selector     string
	targets      []preparedTarget
}

// Delete removes only physical files whose whole current membership matches
// the submitted stable identities. The one CPA inventory snapshot is taken
// while all candidate source-file locks are held, preventing Manager-side
// upload/recovery/proxy mutation races between verification and deletion.
func (s *Service) Delete(ctx context.Context, targets []Target) (Result, error) {
	result := Result{
		Requested: len(targets),
		Deleted:   []Item{},
		Skipped:   []Item{},
		Failed:    []Item{},
	}
	if s == nil || s.setupResolver == nil || s.authFiles == nil || s.mutationCoordinator == nil {
		return result, ErrUnavailable
	}
	if len(targets) > MaxTargets {
		return result, ErrTooManyTargets
	}
	if len(targets) == 0 {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	outcomes := make([]targetOutcome, len(targets))
	groups := make(map[string][]preparedTarget)
	for index, submitted := range targets {
		target := normalizeTarget(submitted)
		if !validTarget(target) {
			outcomes[index] = skippedOutcome(target, "invalid_target", "凭证标识不完整，已跳过删除")
			continue
		}
		key := normalizedPhysicalName(target.FileName)
		groups[key] = append(groups[key], preparedTarget{index: index, target: target})
	}
	if len(groups) == 0 {
		return composeResult(result, outcomes), nil
	}

	setup, ok, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil || !ok || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		for _, group := range groups {
			for _, candidate := range group {
				outcomes[candidate.index] = failedOutcome(candidate.target, "cpa_not_configured", "CPA 管理连接不可用")
			}
		}
		return composeResult(result, outcomes), nil
	}

	lockedFileNames := make([]string, 0, len(groups))
	for physicalName := range groups {
		lockedFileNames = append(lockedFileNames, physicalName)
	}
	release, lockErr := s.mutationCoordinator.Acquire(ctx, lockedFileNames...)
	if lockErr != nil {
		for _, group := range groups {
			for _, candidate := range group {
				outcomes[candidate.index] = failedOutcome(candidate.target, "delete_unavailable", "删除凭证文件失败")
			}
		}
		return composeResult(result, outcomes), nil
	}
	defer release()

	files, fetchErr := s.authFiles.Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if fetchErr != nil {
		for _, group := range groups {
			for _, candidate := range group {
				outcomes[candidate.index] = failedOutcome(candidate.target, "inventory_unavailable", "无法读取凭证清单")
			}
		}
		return composeResult(result, outcomes), nil
	}

	jobs := make([]physicalJob, 0, len(groups))
	for _, group := range groups {
		identities := make([]cpaauthfiles.Identity, 0, len(group))
		for _, candidate := range group {
			identities = append(identities, cpaauthfiles.Identity{
				AuthFileName:      candidate.target.FileName,
				RuntimeID:         candidate.target.RuntimeID,
				AuthIndex:         candidate.target.AuthIndex,
				Provider:          candidate.target.Provider,
				AccountSnapshot:   candidate.target.AccountSnapshot,
				AccountIDSnapshot: candidate.target.AccountID,
			})
		}
		resolved, resolveErr := cpaauthfiles.ResolveVerifiedPhysicalFileDeleteTargetFromInventory(files, identities)
		if resolveErr != nil {
			code, message := classifyResolveError(resolveErr)
			for _, candidate := range group {
				outcomes[candidate.index] = skippedOutcome(candidate.target, code, message)
			}
			continue
		}
		jobs = append(jobs, physicalJob{
			physicalName: firstPhysicalName(resolved, group[0].target.FileName),
			selector:     resolved.Selector,
			targets:      group,
		})
	}
	if len(jobs) == 0 {
		return composeResult(result, outcomes), nil
	}

	sort.Slice(jobs, func(left, right int) bool {
		return normalizedPhysicalName(jobs[left].physicalName) < normalizedPhysicalName(jobs[right].physicalName)
	})
	workerCount := s.concurrency
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}
	work := make(chan physicalJob)
	var outcomesMu sync.Mutex
	var deletedMu sync.Mutex
	deletedAny := false
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range work {
				deleteErr := s.deleteVerifiedPhysicalJob(ctx, setup, job)
				outcomesMu.Lock()
				if deleteErr != nil {
					for _, candidate := range job.targets {
						outcomes[candidate.index] = failedOutcome(candidate.target, "delete_failed", "删除凭证文件失败")
					}
				} else {
					for _, candidate := range job.targets {
						outcomes[candidate.index] = deletedOutcome(candidate.target, job.physicalName)
					}
					deletedMu.Lock()
					deletedAny = true
					deletedMu.Unlock()
				}
				outcomesMu.Unlock()
			}
		}()
	}
	for _, job := range jobs {
		work <- job
	}
	close(work)
	workers.Wait()

	deletedMu.Lock()
	shouldInvalidate := deletedAny
	deletedMu.Unlock()
	if shouldInvalidate && s.invalidator != nil {
		s.invalidator.InvalidateAuthFileInventory(context.Background())
	}
	return composeResult(result, outcomes), nil
}

func (s *Service) deleteVerifiedPhysicalJob(ctx context.Context, setup store.Setup, job physicalJob) error {
	deleteErr := s.authFiles.Delete(ctx, setup.CPAUpstreamURL, setup.ManagementKey, job.selector)
	if deleteErr == nil || !cpaauthfiles.IsPluginVirtualMutationConflict(deleteErr) {
		return deleteErr
	}
	physicalName := strings.TrimSpace(job.physicalName)
	if physicalName == "" || physicalName == strings.TrimSpace(job.selector) {
		return deleteErr
	}
	return s.authFiles.Delete(ctx, setup.CPAUpstreamURL, setup.ManagementKey, physicalName)
}

func normalizeTarget(target Target) Target {
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	target.RuntimeID = strings.TrimSpace(target.RuntimeID)
	target.AccountID = strings.TrimSpace(target.AccountID)
	target.AccountSnapshot = strings.TrimSpace(target.AccountSnapshot)
	return target
}

func validTarget(target Target) bool {
	return target.FileName != "" && target.Provider != "" && (target.AuthIndex != "" || target.RuntimeID != "")
}

func normalizedPhysicalName(fileName string) string {
	return strings.ToLower(strings.TrimSpace(fileName))
}

func firstPhysicalName(resolved cpaauthfiles.DeleteMutationTarget, fallback string) string {
	if name := strings.TrimSpace(resolved.File.Name); name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

func skippedOutcome(target Target, code string, message string) targetOutcome {
	return targetOutcome{kind: outcomeSkipped, item: resultItem(target, "", code, message)}
}

func failedOutcome(target Target, code string, message string) targetOutcome {
	return targetOutcome{kind: outcomeFailed, item: resultItem(target, "", code, message)}
}

func deletedOutcome(target Target, physicalName string) targetOutcome {
	return targetOutcome{kind: outcomeDeleted, item: resultItem(target, physicalName, "", "")}
}

func resultItem(target Target, physicalName string, code string, message string) Item {
	return Item{
		FileName:     strings.TrimSpace(target.FileName),
		PhysicalName: strings.TrimSpace(physicalName),
		Code:         code,
		Message:      message,
	}
}

func composeResult(result Result, outcomes []targetOutcome) Result {
	for _, outcome := range outcomes {
		switch outcome.kind {
		case outcomeDeleted:
			result.Deleted = append(result.Deleted, outcome.item)
		case outcomeFailed:
			result.Failed = append(result.Failed, outcome.item)
		default:
			result.Skipped = append(result.Skipped, outcome.item)
		}
	}
	return result
}

func classifyResolveError(err error) (string, string) {
	switch {
	case errors.Is(err, cpaauthfiles.ErrDeleteMutationScopeAmbiguous):
		return "shared_file_incomplete", "当前共享凭证文件存在未验证成员，已跳过删除"
	case errors.Is(err, cpaauthfiles.ErrIdentityMismatch):
		return "identity_changed", "凭证标识已变化，已跳过删除"
	case errors.Is(err, cpaauthfiles.ErrAuthFileNotFound):
		return "credential_not_found", "未找到当前凭证文件，已跳过删除"
	default:
		return "identity_unverifiable", "无法核实凭证标识，已跳过删除"
	}
}
