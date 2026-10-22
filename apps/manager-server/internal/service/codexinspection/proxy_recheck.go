package codexinspection

import (
	"context"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	maxInspectionExitIPCheckConcurrency = 8
)

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
