package authfilesbanneddelete

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	MaxTargets     = 100
	MaxConcurrency = 4
)

var (
	ErrUnavailable    = errors.New("account-banned auth-file delete is unavailable")
	ErrTooManyTargets = fmt.Errorf("account-banned auth-file delete accepts at most %d targets", MaxTargets)
)

// SetupResolver resolves the persisted CPA management connection. Browser input
// never supplies the management key.
type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

// RecoveryLookup exposes only redacted token-recovery task state.
type RecoveryLookup interface {
	Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error)
}

// AuthFilesClient is deliberately small so the batch service can make one
// inventory snapshot and direct verified delete calls without re-entering the
// generic proxy mutation path.
type AuthFilesClient interface {
	Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
	Delete(context.Context, string, string, string) error
}

type InventoryInvalidator interface {
	InvalidateAuthFileInventory(context.Context)
}

type Options struct {
	SetupResolver       SetupResolver
	Recovery            RecoveryLookup
	AuthFiles           AuthFilesClient
	MutationCoordinator *cpaauthfiles.MutationCoordinator
	Invalidator         InventoryInvalidator
	Concurrency         int
}

type Service struct {
	setupResolver       SetupResolver
	recovery            RecoveryLookup
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
		recovery:            options.Recovery,
		authFiles:           client,
		mutationCoordinator: coordinator,
		invalidator:         options.Invalidator,
		concurrency:         concurrency,
	}
}

// Target contains only redacted, stable identity evidence. It intentionally
// has no token, credential JSON, management key, or proxy fields.
type Target struct {
	ClientKey       string `json:"clientKey"`
	FileName        string `json:"fileName"`
	AuthIndex       string `json:"authIndex,omitempty"`
	AccountEmail    string `json:"accountEmail,omitempty"`
	Provider        string `json:"provider"`
	RuntimeID       string `json:"runtimeId,omitempty"`
	AccountID       string `json:"accountId,omitempty"`
	AccountSnapshot string `json:"accountSnapshot,omitempty"`
}

type Item struct {
	ClientKey    string `json:"clientKey"`
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
	indexes      []int
}

// Delete deletes physical CPA auth files only if every current member is
// supplied and independently proven terminal account_banned. It snapshots the
// CPA inventory once per call, then bounds independent physical-file deletion
// to MaxConcurrency workers. No usage, quota, inspection, or scheduler state
// is changed here.
func (s *Service) Delete(ctx context.Context, targets []Target) (Result, error) {
	result := Result{Requested: len(targets), Deleted: []Item{}, Skipped: []Item{}, Failed: []Item{}}
	if s == nil || s.setupResolver == nil || s.recovery == nil || s.authFiles == nil || s.mutationCoordinator == nil {
		return result, ErrUnavailable
	}
	if len(targets) > MaxTargets {
		return result, ErrTooManyTargets
	}
	if len(targets) == 0 {
		return result, nil
	}

	outcomes := make([]targetOutcome, len(targets))
	groups := make(map[string][]preparedTarget)
	allGroups := make(map[string][]int)
	for index, submitted := range targets {
		target := normalizeTarget(submitted)
		if !validTarget(target) {
			outcomes[index] = skippedOutcome(target, "invalid_target", "凭证标识不完整，已跳过删除")
			continue
		}
		physicalKey := normalizedPhysicalName(target.FileName)
		allGroups[physicalKey] = append(allGroups[physicalKey], index)
		task, found, err := s.recovery.Get(ctx, model.TokenRecoveryTarget{
			FileName:     target.FileName,
			AuthIndex:    target.AuthIndex,
			AccountEmail: target.AccountEmail,
			Provider:     target.Provider,
		})
		switch {
		case err != nil:
			outcomes[index] = skippedOutcome(target, "recovery_lookup_failed", "无法核实凭证恢复状态，已跳过删除")
		case !found:
			outcomes[index] = skippedOutcome(target, "recovery_record_missing", "未找到凭证恢复记录，已跳过删除")
		case !recoveryTaskMatchesTarget(task, target):
			// Get intentionally supports legacy recovery-task compatibility
			// fallbacks. A destructive delete is stricter: it must never let an
			// older task authorize a replacement credential that happens to reuse
			// a file name or auth index.
			outcomes[index] = skippedOutcome(target, "recovery_identity_mismatch", "凭证恢复记录与当前账号不匹配，已跳过删除")
		case !tokenrecovery.IsTerminalAccountBanned(task):
			outcomes[index] = skippedOutcome(target, "not_account_banned", "账号未确认封禁，已跳过删除")
		default:
			groups[physicalKey] = append(groups[physicalKey], preparedTarget{index: index, target: target})
		}
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

	// Hold every candidate physical-file lock before taking the single CPA
	// inventory snapshot. The coordinator is shared by Manager's upload,
	// recovery, proxy, and automated auth-file mutations, so this prevents a
	// concurrent Manager mutation from replacing a member between verification
	// and the source-file delete below.
	lockedFileNames := make([]string, 0, len(groups))
	for physicalKey := range groups {
		lockedFileNames = append(lockedFileNames, physicalKey)
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

	files, err := s.authFiles.Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		for _, group := range groups {
			for _, candidate := range group {
				outcomes[candidate.index] = failedOutcome(candidate.target, "inventory_unavailable", "无法读取凭证清单")
			}
		}
		return composeResult(result, outcomes), nil
	}

	jobs := make([]physicalJob, 0, len(groups))
	for physicalKey, group := range groups {
		if len(group) != len(allGroups[physicalKey]) {
			for _, candidate := range group {
				outcomes[candidate.index] = skippedOutcome(candidate.target, "shared_file_incomplete", "当前共享凭证文件存在未验证成员，已跳过删除")
			}
			continue
		}
		identities := make([]cpaauthfiles.Identity, 0, len(group))
		indexes := make([]int, 0, len(group))
		for _, candidate := range group {
			identities = append(identities, cpaauthfiles.Identity{
				AuthFileName:      candidate.target.FileName,
				RuntimeID:         candidate.target.RuntimeID,
				AuthIndex:         candidate.target.AuthIndex,
				Provider:          candidate.target.Provider,
				AccountSnapshot:   candidate.target.AccountSnapshot,
				AccountIDSnapshot: candidate.target.AccountID,
			})
			indexes = append(indexes, candidate.index)
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
			indexes:      indexes,
		})
	}

	if len(jobs) == 0 {
		return composeResult(result, outcomes), nil
	}
	sort.Slice(jobs, func(left, right int) bool {
		return strings.ToLower(jobs[left].physicalName) < strings.ToLower(jobs[right].physicalName)
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
					for _, index := range job.indexes {
						outcomes[index] = failedOutcome(targets[index], "delete_failed", "删除凭证文件失败")
					}
				} else {
					for _, index := range job.indexes {
						outcomes[index] = deletedOutcome(targets[index], job.physicalName)
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

// deleteVerifiedPhysicalJob uses the one inventory snapshot collected while
// the matching physical-file lock is held. A plugin-backed single runtime
// identity can reject its synthetic runtime ID; CPA then accepts the verified
// physical source name. Retrying that source name does not require another
// inventory fetch and therefore keeps batch latency bounded.
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
	target.ClientKey = strings.TrimSpace(target.ClientKey)
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.AccountEmail = strings.ToLower(strings.TrimSpace(target.AccountEmail))
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	target.RuntimeID = strings.TrimSpace(target.RuntimeID)
	target.AccountID = strings.TrimSpace(target.AccountID)
	target.AccountSnapshot = strings.TrimSpace(target.AccountSnapshot)
	return target
}

func validTarget(target Target) bool {
	return target.ClientKey != "" && target.FileName != "" && target.Provider == "codex" && target.AccountEmail != "" &&
		(target.AuthIndex != "" || target.RuntimeID != "")
}

// recoveryTaskMatchesTarget is intentionally stricter than RecoveryLookup.Get.
// The latter has compatibility fallbacks used for recovery workflows; a
// destructive file delete requires the durable task to prove the same
// file+provider+email identity and the exact locator flavor supplied by the
// current UI snapshot.
func recoveryTaskMatchesTarget(task model.TokenRecoveryTask, target Target) bool {
	if !strings.EqualFold(strings.TrimSpace(task.FileName), strings.TrimSpace(target.FileName)) ||
		strings.ToLower(strings.TrimSpace(task.Provider)) != strings.ToLower(strings.TrimSpace(target.Provider)) ||
		!strings.EqualFold(strings.TrimSpace(task.AccountEmail), strings.TrimSpace(target.AccountEmail)) {
		return false
	}
	return strings.TrimSpace(task.AuthIndex) == strings.TrimSpace(target.AuthIndex)
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
		ClientKey:    strings.TrimSpace(target.ClientKey),
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
