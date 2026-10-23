// Package authfilesexport provides a read-only, identity-verified export of
// selected CPA auth JSON records. It intentionally has no mutation, cache, or
// persistence hooks because raw credential JSON must remain request-scoped.
package authfilesexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	// MaxTargets keeps browser and Manager memory bounded for a single copy
	// action while still covering the user's intended 300-account batch.
	MaxTargets = 300
	// MaxConcurrency limits only independent physical CPA source downloads.
	MaxConcurrency = 8
	// MaxExportBytes caps the final top-level JSON array sent to the browser.
	MaxExportBytes = 48 * 1024 * 1024
)

var (
	ErrUnavailable         = errors.New("selected auth JSON export is unavailable")
	ErrTooManyTargets      = fmt.Errorf("selected auth JSON export accepts at most %d targets", MaxTargets)
	ErrExportLimitExceeded = fmt.Errorf("selected auth JSON export exceeds %d MiB", MaxExportBytes/(1024*1024))
)

// SetupResolver resolves the persisted CPA management connection. Browser
// requests never carry the management key.
type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

// AuthFilesClient is narrowed to one inventory snapshot and raw source reads.
// There are deliberately no mutation methods in this dependency.
type AuthFilesClient interface {
	Fetch(context.Context, string, string) ([]cpaauthfiles.File, error)
	Download(context.Context, string, string, string) ([]byte, error)
}

type Options struct {
	SetupResolver SetupResolver
	AuthFiles     AuthFilesClient
	Concurrency   int
}

type Service struct {
	setupResolver  SetupResolver
	authFiles      AuthFilesClient
	concurrency    int
	maxExportBytes int
}

func NewWithOptions(options Options) *Service {
	client := options.AuthFiles
	if client == nil {
		client = cpaauthfiles.New(nil)
	}
	concurrency := options.Concurrency
	if concurrency <= 0 || concurrency > MaxConcurrency {
		concurrency = MaxConcurrency
	}
	return &Service{
		setupResolver:  options.SetupResolver,
		authFiles:      client,
		concurrency:    concurrency,
		maxExportBytes: MaxExportBytes,
	}
}

// Target contains identity evidence only. It intentionally has no token, raw
// credential JSON, management key, proxy URL, quota, or history fields.
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

// FailedItem deliberately exposes only client correlation, source file name,
// and a fixed safe code. It never includes upstream errors or source data.
type FailedItem struct {
	ClientKey string `json:"clientKey"`
	FileName  string `json:"fileName"`
	Code      string `json:"code"`
}

type Result struct {
	Requested int               `json:"requested"`
	Exported  int               `json:"exported"`
	Records   []json.RawMessage `json:"records"`
	Failed    []FailedItem      `json:"failed"`
}

type preparedTarget struct {
	index  int
	target Target
}

type physicalJob struct {
	physicalName string
	targets      []preparedTarget
}

type targetOutcome struct {
	record json.RawMessage
	failed *FailedItem
}

// Export reads the CPA inventory exactly once, verifies every browser target,
// deduplicates physical source downloads, and returns records in caller order.
// It does not mutate CPA, acquire mutation locks, invalidate caches, write a
// database, or emit raw credential data to logs.
func (s *Service) Export(ctx context.Context, submitted []Target) (Result, error) {
	result := emptyResult(len(submitted))
	if s == nil || s.setupResolver == nil || s.authFiles == nil {
		return result, ErrUnavailable
	}
	if len(submitted) > MaxTargets {
		return result, ErrTooManyTargets
	}
	if len(submitted) == 0 {
		return result, nil
	}

	outcomes := make([]targetOutcome, len(submitted))
	prepared := make([]preparedTarget, 0, len(submitted))
	seenClientKeys := make(map[string]struct{}, len(submitted))
	for index, submittedTarget := range submitted {
		target := normalizeTarget(submittedTarget)
		if !validTarget(target) {
			outcomes[index] = failedOutcome(target, "invalid_target")
			continue
		}
		if _, duplicate := seenClientKeys[target.ClientKey]; duplicate {
			outcomes[index] = failedOutcome(target, "invalid_target")
			continue
		}
		seenClientKeys[target.ClientKey] = struct{}{}
		prepared = append(prepared, preparedTarget{index: index, target: target})
	}
	if len(prepared) == 0 {
		return composeResult(result, outcomes), nil
	}

	setup, configured, setupErr := s.setupResolver.ResolveSetup(ctx)
	if setupErr != nil || !configured || strings.TrimSpace(setup.CPAUpstreamURL) == "" || strings.TrimSpace(setup.ManagementKey) == "" {
		for _, candidate := range prepared {
			outcomes[candidate.index] = failedOutcome(candidate.target, "source_unavailable")
		}
		return composeResult(result, outcomes), nil
	}

	files, fetchErr := s.authFiles.Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if fetchErr != nil {
		for _, candidate := range prepared {
			outcomes[candidate.index] = failedOutcome(candidate.target, "source_unavailable")
		}
		return composeResult(result, outcomes), nil
	}

	jobsByPhysicalName := make(map[string]*physicalJob, len(prepared))
	for _, candidate := range prepared {
		file, identityErr := cpaauthfiles.VerifyIdentity(files, identityFor(candidate.target))
		if identityErr != nil {
			outcomes[candidate.index] = failedOutcome(candidate.target, classifyIdentityError(identityErr))
			continue
		}
		physicalName := strings.TrimSpace(file.Name)
		if physicalName == "" {
			outcomes[candidate.index] = failedOutcome(candidate.target, "runtime_only")
			continue
		}
		physicalKey := strings.ToLower(physicalName)
		job := jobsByPhysicalName[physicalKey]
		if job == nil {
			job = &physicalJob{physicalName: physicalName}
			jobsByPhysicalName[physicalKey] = job
		}
		job.targets = append(job.targets, candidate)
	}

	jobs := sortedPhysicalJobs(jobsByPhysicalName)
	if len(jobs) == 0 {
		return composeResult(result, outcomes), nil
	}

	workerCount := s.concurrency
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}
	work := make(chan physicalJob)
	var outcomesMu sync.Mutex
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range work {
				raw, downloadErr := s.authFiles.Download(ctx, setup.CPAUpstreamURL, setup.ManagementKey, job.physicalName)
				if downloadErr != nil {
					outcomesMu.Lock()
					for _, candidate := range job.targets {
						outcomes[candidate.index] = failedOutcome(candidate.target, classifyDownloadError(downloadErr))
					}
					outcomesMu.Unlock()
					continue
				}

				for _, candidate := range job.targets {
					record, extractErr := cpaauthfiles.ExtractAuthJSONRecord(raw, cpaauthfiles.AuthJSONRecordLocator{
						AuthIndex:    candidate.target.AuthIndex,
						AccountEmail: candidate.target.AccountEmail,
					})
					outcomesMu.Lock()
					if extractErr != nil {
						outcomes[candidate.index] = failedOutcome(candidate.target, classifyExtractError(extractErr))
					} else {
						outcomes[candidate.index] = targetOutcome{record: record}
					}
					outcomesMu.Unlock()
				}
			}
		}()
	}
	for _, job := range jobs {
		work <- job
	}
	close(work)
	workers.Wait()

	result = composeResult(result, outcomes)
	if encodedRecordsSize(result.Records) > s.maxExportBytes {
		return emptyResult(len(submitted)), ErrExportLimitExceeded
	}
	return result, nil
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
	return target.ClientKey != "" && target.FileName != "" && target.Provider != "" &&
		(target.AuthIndex != "" || target.RuntimeID != "")
}

func identityFor(target Target) cpaauthfiles.Identity {
	return cpaauthfiles.Identity{
		AuthFileName:      target.FileName,
		RuntimeID:         target.RuntimeID,
		AuthIndex:         target.AuthIndex,
		Provider:          target.Provider,
		AccountSnapshot:   target.AccountSnapshot,
		AccountIDSnapshot: target.AccountID,
	}
}

func classifyIdentityError(err error) string {
	if errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) {
		return "credential_not_found"
	}
	return "identity_mismatch"
}

func classifyDownloadError(err error) string {
	if errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) {
		return "credential_not_found"
	}
	return "source_unavailable"
}

func classifyExtractError(err error) string {
	if errors.Is(err, cpaauthfiles.ErrAuthJSONInvalid) {
		return "invalid_source_json"
	}
	return "identity_mismatch"
}

func failedOutcome(target Target, code string) targetOutcome {
	return targetOutcome{failed: &FailedItem{
		ClientKey: target.ClientKey,
		FileName:  target.FileName,
		Code:      code,
	}}
}

func emptyResult(requested int) Result {
	return Result{
		Requested: requested,
		Records:   make([]json.RawMessage, 0),
		Failed:    make([]FailedItem, 0),
	}
}

func composeResult(result Result, outcomes []targetOutcome) Result {
	for _, outcome := range outcomes {
		if len(outcome.record) > 0 {
			result.Records = append(result.Records, outcome.record)
			result.Exported++
			continue
		}
		if outcome.failed != nil {
			result.Failed = append(result.Failed, *outcome.failed)
		}
	}
	return result
}

func encodedRecordsSize(records []json.RawMessage) int {
	encoded, err := json.Marshal(records)
	if err != nil {
		// json.RawMessage returned by ExtractAuthJSONRecord is always valid JSON.
		// If a future caller violates that invariant, fail closed at the same
		// boundary as an oversized export rather than send malformed auth data.
		return MaxExportBytes + 1
	}
	return len(encoded)
}

func sortedPhysicalJobs(jobsByPhysicalName map[string]*physicalJob) []physicalJob {
	jobs := make([]physicalJob, 0, len(jobsByPhysicalName))
	for _, job := range jobsByPhysicalName {
		jobs = append(jobs, *job)
	}
	sort.Slice(jobs, func(left, right int) bool {
		return strings.ToLower(jobs[left].physicalName) < strings.ToLower(jobs[right].physicalName)
	})
	return jobs
}
