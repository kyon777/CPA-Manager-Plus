package codexinspection

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	maxInspectionExitIPCheckConcurrency   = 8
	maxProxyReplacementRecheckConcurrency = 4
)

var proxyURLRedactionPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>}\]]+`)

// applyExitIPProxyEvidence is the sole author of proxy-abnormal evidence for
// an inspection run. An API request timeout or the narrow CPA 502 shape only
// nominates a row for a direct fixed-target exit-IP check; it is never proof by
// itself. Thus an account-level 401/429/quota/server response cannot leak into
// the proxy-abnormal filter or the replacement pool state.
func (s *Service) applyExitIPProxyEvidence(
	ctx context.Context,
	accounts []account,
	results []model.CodexInspectionResult,
	logger runLogger,
) []model.CodexInspectionResult {
	out := append([]model.CodexInspectionResult(nil), results...)
	for index := range out {
		// Results created by this version must not inherit stale in-memory
		// inference flags. The SQLite one-time migration handles persisted rows.
		out[index].ProxySuspect = false
	}
	if s == nil || s.proxyExitIPChecker == nil || len(out) == 0 || len(accounts) == 0 {
		return out
	}

	accountsByKey := make(map[string]account, len(accounts))
	for _, item := range accounts {
		if key := strings.TrimSpace(item.Key); key != "" {
			accountsByKey[key] = item
		}
	}
	type job struct {
		index    int
		proxyURL string
	}
	jobsToRun := make([]job, 0)
	for index, result := range out {
		item, ok := accountsByKey[strings.TrimSpace(result.AccountKey)]
		if !ok {
			continue
		}
		proxyURL := proxyURLFromAccount(item)
		if !isExitIPCheckCandidate(result, proxyURL) {
			continue
		}
		jobsToRun = append(jobsToRun, job{index: index, proxyURL: proxyURL})
	}
	if len(jobsToRun) == 0 {
		return out
	}

	type outcome struct {
		job   job
		check model.ProxyExitIPCheckResult
	}
	workerCount := len(jobsToRun)
	if workerCount > maxInspectionExitIPCheckConcurrency {
		workerCount = maxInspectionExitIPCheckConcurrency
	}
	work := make(chan job)
	completed := make(chan outcome, len(jobsToRun))
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for next := range work {
				completed <- outcome{job: next, check: s.proxyExitIPChecker.Check(ctx, next.proxyURL)}
			}
		}()
	}
	go func() {
		defer close(work)
		for _, next := range jobsToRun {
			select {
			case <-ctx.Done():
				return
			case work <- next:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(completed)
	}()

	for next := range completed {
		switch next.check.Outcome {
		case model.ProxyExitIPOutcomeProxyFailed:
			out[next.job.index].ProxySuspect = true
			if s.proxyPoolState != nil {
				if err := s.proxyPoolState.MarkSuspect(ctx, next.job.proxyURL, strings.TrimSpace(next.check.Reason)); err != nil {
					logger.warning(ctx, "代理出口 IP 异常状态写入失败", map[string]any{
						"fileName": out[next.job.index].FileName,
						"reason":   strings.TrimSpace(next.check.Reason),
					})
				}
			}
		case model.ProxyExitIPOutcomeHealthy:
			out[next.job.index].ProxySuspect = false
			if s.proxyPoolState != nil {
				// Restore is deliberately best effort: most candidate proxies do not
				// have state rows, and an absent row is already effectively normal.
				_, _ = s.proxyPoolState.Restore(ctx, model.ProxyFingerprint(next.job.proxyURL))
			}
		case model.ProxyExitIPOutcomeInconclusive, model.ProxyExitIPOutcomeNotSaved:
			out[next.job.index].ProxySuspect = false
		default:
			out[next.job.index].ProxySuspect = false
		}
	}
	return out
}

func proxyURLFromAccount(item account) string {
	return model.NormalizeProxyURL(firstNonEmpty(
		readString(item.File, "proxy_url", "proxyUrl", "proxy-url"),
		readString(readMap(item.File, "metadata"), "proxy_url", "proxyUrl", "proxy-url"),
	))
}

func isExitIPCheckCandidate(result model.CodexInspectionResult, proxyURL string) bool {
	if model.NormalizeProxyURL(proxyURL) == "" || strings.ToLower(strings.TrimSpace(result.ErrorKind)) != "request_error" {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(result.ErrorDetail))
	if strings.Contains(detail, "context deadline exceeded") || strings.Contains(detail, "timeout") {
		return true
	}
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(detail)
	return strings.Contains(compact, "api-callfailed:502") && strings.Contains(compact, `"error":"requestfailed"`)
}

// RecheckProxyReplacement performs only the CPA usage probe needed to verify
// a successful proxy replacement. It intentionally bypasses the normal Run
// lifecycle: no inspection run/results/logs, quota snapshots, auto actions or
// automatic reauthorization are emitted.
func (s *Service) RecheckProxyReplacement(
	ctx context.Context,
	targets []model.ProxyReplacementTarget,
	concurrency int,
) []model.ProxyReplacementProbeResult {
	if len(targets) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return failedProxyReplacementProbes(targets, "recheck_unavailable", "代理复检服务不可用")
	}

	settings, setup, err := s.resolveRuntime(ctx)
	if err != nil {
		return failedProxyReplacementProbes(targets, "recheck_not_configured", "代理复检未配置")
	}
	// A proxy recheck is evidence collection only. Keep the configured request
	// timeout/user-agent but prevent retry amplification and every automatic
	// action path irrespective of the saved inspection configuration.
	settings.Retries = 0
	settings.AutoActionMode = model.CodexInspectionAutoActionNone
	settings.AutoRecoverEnabled = false

	files, err := s.fetchAuthFiles(ctx, setup)
	if err != nil {
		return failedProxyReplacementProbes(targets, "credential_inventory_failed", "读取当前凭证失败")
	}
	accounts := make([]account, 0, len(files))
	for _, file := range files {
		accounts = append(accounts, toAccount(file))
	}

	workers := concurrency
	if workers <= 0 || workers > maxProxyReplacementRecheckConcurrency {
		workers = maxProxyReplacementRecheckConcurrency
	}
	if workers > len(targets) {
		workers = len(targets)
	}
	if workers < 1 {
		workers = 1
	}

	type recheckJob struct{ index int }
	type recheckOutcome struct {
		index int
		value model.ProxyReplacementProbeResult
	}
	jobs := make(chan recheckJob)
	outcomes := make(chan recheckOutcome, len(targets))
	var workersDone sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for job := range jobs {
				target := targets[job.index]
				outcomes <- recheckOutcome{index: job.index, value: s.recheckProxyReplacementTarget(ctx, setup, settings, accounts, target)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range targets {
			select {
			case <-ctx.Done():
				return
			case jobs <- recheckJob{index: index}:
			}
		}
	}()
	go func() {
		workersDone.Wait()
		close(outcomes)
	}()

	result := make([]model.ProxyReplacementProbeResult, len(targets))
	completed := make([]bool, len(targets))
	for outcome := range outcomes {
		result[outcome.index] = outcome.value
		completed[outcome.index] = true
	}
	for index, target := range targets {
		if completed[index] {
			continue
		}
		result[index] = model.ProxyReplacementProbeResult{
			Target:       target,
			ErrorCode:    "recheck_cancelled",
			ErrorMessage: "代理复检已取消",
		}
	}
	return result
}

func (s *Service) recheckProxyReplacementTarget(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	accounts []account,
	target model.ProxyReplacementTarget,
) model.ProxyReplacementProbeResult {
	item, code, message := findProxyReplacementRecheckAccount(target, accounts)
	if code != "" {
		return model.ProxyReplacementProbeResult{Target: target, ErrorCode: code, ErrorMessage: message}
	}
	probe := s.inspectSingleAccount(ctx, setup, settings, item, runLogger{})
	output := model.ProxyReplacementProbeResult{Target: target, ProxySuspect: probe.ProxySuspect}
	if probe.StatusCode != nil && *probe.StatusCode >= http.StatusOK && *probe.StatusCode < http.StatusMultipleChoices && probe.ErrorKind == "" {
		output.Succeeded = true
		return output
	}
	output.ErrorCode = strings.TrimSpace(probe.ErrorKind)
	if output.ErrorCode == "" && probe.StatusCode != nil {
		output.ErrorCode = fmt.Sprintf("http_%d", *probe.StatusCode)
	}
	if output.ErrorCode == "" {
		output.ErrorCode = "recheck_failed"
	}
	output.ErrorMessage = safeProxyRecheckMessage(firstNonEmpty(probe.ErrorDetail, probe.Error))
	if output.ErrorMessage == "" {
		output.ErrorMessage = "代理复检失败"
	}
	return output
}

func failedProxyReplacementProbes(
	targets []model.ProxyReplacementTarget,
	code string,
	message string,
) []model.ProxyReplacementProbeResult {
	result := make([]model.ProxyReplacementProbeResult, len(targets))
	for index, target := range targets {
		result[index] = model.ProxyReplacementProbeResult{
			Target:       target,
			ErrorCode:    code,
			ErrorMessage: message,
		}
	}
	return result
}

func findProxyReplacementRecheckAccount(
	target model.ProxyReplacementTarget,
	accounts []account,
) (account, string, string) {
	provider := normalizeInspectionProvider(target.Provider)
	if provider != "codex" || strings.TrimSpace(target.FileName) == "" {
		return account{}, "credential_not_found", "当前凭证不存在或已变化"
	}
	matches := make([]account, 0, 1)
	for _, candidate := range accounts {
		if !proxyReplacementTargetMatchesAccount(target, candidate, provider) {
			continue
		}
		matches = append(matches, candidate)
	}
	switch len(matches) {
	case 1:
		return matches[0], "", ""
	case 0:
		return account{}, "credential_not_found", "当前凭证不存在或已变化"
	default:
		return account{}, "credential_ambiguous", "当前凭证匹配到多个记录，已跳过复检"
	}
}

func proxyReplacementTargetMatchesAccount(
	target model.ProxyReplacementTarget,
	candidate account,
	provider string,
) bool {
	if strings.TrimSpace(target.FileName) != strings.TrimSpace(candidate.FileName) ||
		normalizeInspectionProvider(candidate.Provider) != provider {
		return false
	}

	matchedIdentity := false
	if expected := strings.TrimSpace(target.RuntimeID); expected != "" && strings.TrimSpace(candidate.RuntimeID) != "" {
		if expected != strings.TrimSpace(candidate.RuntimeID) {
			return false
		}
		matchedIdentity = true
	}
	if expected := strings.TrimSpace(target.AuthIndex); expected != "" {
		if expected != strings.TrimSpace(candidate.AuthIndex) {
			return false
		}
		matchedIdentity = true
	}
	if expected := strings.TrimSpace(target.AccountEmail); expected != "" && strings.TrimSpace(candidate.AccountSnapshot) != "" {
		if !strings.EqualFold(expected, strings.TrimSpace(candidate.AccountSnapshot)) {
			return false
		}
		matchedIdentity = true
	}
	if expected := strings.TrimSpace(target.AccountSnapshot); expected != "" && strings.TrimSpace(candidate.AccountSnapshot) != "" {
		if !strings.EqualFold(expected, strings.TrimSpace(candidate.AccountSnapshot)) {
			return false
		}
		matchedIdentity = true
	}
	if expected := strings.TrimSpace(target.AccountID); expected != "" && strings.TrimSpace(candidate.AccountID) != "" {
		if expected != strings.TrimSpace(candidate.AccountID) {
			return false
		}
		matchedIdentity = true
	}
	return matchedIdentity
}

func safeProxyRecheckMessage(value string) string {
	message := proxyURLRedactionPattern.ReplaceAllString(strings.TrimSpace(value), "[redacted-url]")
	if len(message) > maxStoredBodyText {
		message = message[:maxStoredBodyText]
	}
	return message
}
