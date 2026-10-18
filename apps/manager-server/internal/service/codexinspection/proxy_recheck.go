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

const maxProxyReplacementRecheckConcurrency = 4

var proxyURLRedactionPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>}\]]+`)

// markProxySuspect attaches narrow proxy-chain evidence to an ordinary
// inspection result. It deliberately does not alter the action decision: a
// failed proxy is an operational issue that requires an administrator's
// explicit replacement confirmation, not an automatic credential mutation.
func (s *Service) markProxySuspect(
	ctx context.Context,
	item account,
	result model.CodexInspectionResult,
) model.CodexInspectionResult {
	proxyURL := proxyURLFromAccount(item)
	evidence := model.ProxyFailureEvidence{
		ProxyURL:    proxyURL,
		ErrorKind:   result.ErrorKind,
		ErrorDetail: result.ErrorDetail,
	}
	if result.StatusCode != nil {
		evidence.StatusCode = *result.StatusCode
	}
	if !model.IsSuspectedProxyTransportFailure(evidence) {
		return result
	}

	result.ProxySuspect = true
	if s != nil && s.proxyFailureReporter != nil {
		// Pool-state persistence is best effort. The persisted inspection result
		// remains the source of evidence even if a transient SQLite write fails.
		_ = s.proxyFailureReporter.MarkSuspect(ctx, proxyURL, proxyFailureReason(evidence))
	}
	return result
}

func proxyURLFromAccount(item account) string {
	return model.NormalizeProxyURL(firstNonEmpty(
		readString(item.File, "proxy_url", "proxyUrl", "proxy-url"),
		readString(readMap(item.File, "metadata"), "proxy_url", "proxyUrl", "proxy-url"),
	))
}

func proxyFailureReason(evidence model.ProxyFailureEvidence) string {
	detail := strings.ToLower(strings.TrimSpace(evidence.ErrorDetail))
	if strings.Contains(detail, "context deadline exceeded") || strings.Contains(detail, "timeout") {
		return "transport_timeout"
	}
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(detail)
	if strings.Contains(compact, "api-callfailed:502") &&
		strings.Contains(compact, `"error":"requestfailed"`) {
		return "transport_502_request_failed"
	}
	return "transport_unknown"
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
