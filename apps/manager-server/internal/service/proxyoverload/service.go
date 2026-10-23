package proxyoverload

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	summaryWindow             = 72 * time.Hour
	summaryPerCredentialLimit = 9
	summaryCacheTTL           = 15 * time.Second
	defaultSummaryWorkTimeout = 90 * time.Second
)

var (
	// ErrUnavailable is intentionally static: upstream/setup errors may contain
	// credentials or URLs and must never become panel response text.
	ErrUnavailable = errors.New("proxy overload summary is temporarily unavailable")
	// ErrIndexMaintenancePending distinguishes a safe fast failure from a
	// potentially expensive per-account fallback while derived indexes are absent.
	ErrIndexMaintenancePending = errors.New("database index maintenance is incomplete")
)

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type AuthFileInventoryReader interface {
	FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error)
}

type EvidenceReader interface {
	RecentProxyOverloadEvidence(
		context.Context,
		[]store.LatestAccountRequestQuery,
		int64,
		int,
	) ([]store.ProxyOverloadEvidence, error)
}

type Options struct {
	SetupResolver SetupResolver
	AuthFiles     AuthFileInventoryReader
	Evidence      EvidenceReader
	Now           func() time.Time
	WorkTimeout   time.Duration
}

type Summary struct {
	GeneratedAtMS            int64      `json:"generated_at_ms"`
	WindowStartMS            int64      `json:"window_start_ms"`
	ActiveAccountCount       int        `json:"active_account_count"`
	MissingProxyAccountCount int        `json:"missing_proxy_account_count"`
	ProxyCount               int        `json:"proxy_count"`
	OverloadedAccountCount   int        `json:"overloaded_account_count"`
	Proxies                  []ProxyRow `json:"proxies"`
}

type ProxyRow struct {
	ProxyURL                  string  `json:"proxy_url"`
	ActiveAccountCount        int     `json:"active_account_count"`
	OverloadedAccountCount    int     `json:"overloaded_account_count"`
	NonOverloadedAccountCount int     `json:"non_overloaded_account_count"`
	OverloadedRate            float64 `json:"overloaded_rate"`
}

type cachedSummary struct {
	result      Summary
	completedAt time.Time
}

type summaryFlight struct {
	done   chan struct{}
	result Summary
	err    error
}

type Service struct {
	setupResolver SetupResolver
	authFiles     AuthFileInventoryReader
	evidence      EvidenceReader
	now           func() time.Time
	workTimeout   time.Duration

	mu       sync.Mutex
	cache    *cachedSummary
	inflight *summaryFlight
}

func New(options Options) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	workTimeout := options.WorkTimeout
	if workTimeout <= 0 {
		workTimeout = defaultSummaryWorkTimeout
	}
	return &Service{
		setupResolver: options.SetupResolver,
		authFiles:     options.AuthFiles,
		evidence:      options.Evidence,
		now:           now,
		workTimeout:   workTimeout,
	}
}

// Summary returns a complete, current-proxy aggregate. A client cancellation
// only stops that caller's wait: the shared calculation uses a bounded service
// context so other dashboard sessions can join the same work safely.
func (s *Service) Summary(ctx context.Context, force bool) (Summary, error) {
	if s == nil || s.setupResolver == nil || s.authFiles == nil || s.evidence == nil {
		return Summary{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := s.now()
	s.mu.Lock()
	if !force && s.cache != nil && now.Sub(s.cache.completedAt) >= 0 && now.Sub(s.cache.completedAt) < summaryCacheTTL {
		result := cloneSummary(s.cache.result)
		s.mu.Unlock()
		return result, nil
	}
	if flight := s.inflight; flight != nil {
		s.mu.Unlock()
		return waitForSummary(ctx, flight)
	}
	flight := &summaryFlight{done: make(chan struct{})}
	s.inflight = flight
	s.mu.Unlock()

	workCtx, cancel := context.WithTimeout(context.Background(), s.workTimeout)
	result, err := s.compute(workCtx)
	cancel()

	s.mu.Lock()
	flight.result = cloneSummary(result)
	flight.err = err
	if err == nil {
		s.cache = &cachedSummary{
			result:      cloneSummary(result),
			completedAt: s.now(),
		}
	}
	if s.inflight == flight {
		s.inflight = nil
	}
	close(flight.done)
	s.mu.Unlock()

	return cloneSummary(result), err
}

func waitForSummary(ctx context.Context, flight *summaryFlight) (Summary, error) {
	select {
	case <-flight.done:
		return cloneSummary(flight.result), flight.err
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	}
}

func (s *Service) compute(ctx context.Context) (Summary, error) {
	setup, ok, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil || !ok ||
		strings.TrimSpace(setup.CPAUpstreamURL) == "" ||
		strings.TrimSpace(setup.ManagementKey) == "" {
		return Summary{}, ErrUnavailable
	}

	files, err := s.authFiles.FetchWithProxyURLs(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return Summary{}, ErrUnavailable
	}

	targets := make([]store.LatestAccountRequestQuery, 0, len(files))
	filesByTarget := make([]cpaauthfiles.File, 0, len(files))
	for _, file := range files {
		target, eligible := latestRequestTarget(len(targets), file)
		if !eligible {
			continue
		}
		targets = append(targets, target)
		filesByTarget = append(filesByTarget, file)
	}
	now := s.now()
	result := Summary{
		GeneratedAtMS: now.UnixMilli(),
		WindowStartMS: now.Add(-summaryWindow).UnixMilli(),
		Proxies:       []ProxyRow{},
	}
	if len(targets) == 0 {
		return result, nil
	}

	evidence, err := s.evidence.RecentProxyOverloadEvidence(
		ctx,
		targets,
		result.WindowStartMS,
		summaryPerCredentialLimit,
	)
	if err != nil {
		if errors.Is(err, usageevent.ErrLatestRequestIndexesUnavailable) {
			return Summary{}, ErrIndexMaintenancePending
		}
		return Summary{}, ErrUnavailable
	}

	activeByIndex := make(map[int]store.ProxyOverloadEvidence, len(evidence))
	for _, item := range evidence {
		if !item.Active || item.RequestIndex < 0 || item.RequestIndex >= len(filesByTarget) {
			continue
		}
		current := activeByIndex[item.RequestIndex]
		current.RequestIndex = item.RequestIndex
		current.Active = true
		current.ServerOverloaded = current.ServerOverloaded || item.ServerOverloaded
		activeByIndex[item.RequestIndex] = current
	}

	groups := make(map[string]*proxyAggregate)
	for index, item := range activeByIndex {
		if index < 0 || index >= len(filesByTarget) || !item.Active {
			continue
		}
		result.ActiveAccountCount++
		key, displayURL := normalizeProxyURL(filesByTarget[index].ProxyURL)
		if key == "" {
			result.MissingProxyAccountCount++
			continue
		}
		group := groups[key]
		if group == nil {
			group = &proxyAggregate{key: key, proxyURL: displayURL}
			groups[key] = group
		}
		group.activeCount++
		if item.ServerOverloaded {
			group.overloadedCount++
			result.OverloadedAccountCount++
		}
	}
	result.Proxies = make([]ProxyRow, 0, len(groups))
	for _, group := range groups {
		row := ProxyRow{
			ProxyURL:                  group.proxyURL,
			ActiveAccountCount:        group.activeCount,
			OverloadedAccountCount:    group.overloadedCount,
			NonOverloadedAccountCount: group.activeCount - group.overloadedCount,
		}
		if row.ActiveAccountCount > 0 {
			row.OverloadedRate = float64(row.OverloadedAccountCount) / float64(row.ActiveAccountCount)
		}
		result.Proxies = append(result.Proxies, row)
	}
	sort.Slice(result.Proxies, func(i, j int) bool {
		left, right := result.Proxies[i], result.Proxies[j]
		leftHasOverload := left.OverloadedAccountCount > 0
		rightHasOverload := right.OverloadedAccountCount > 0
		if leftHasOverload != rightHasOverload {
			return !leftHasOverload
		}
		if left.ActiveAccountCount != right.ActiveAccountCount {
			return left.ActiveAccountCount > right.ActiveAccountCount
		}
		leftKey, _ := normalizeProxyURL(left.ProxyURL)
		rightKey, _ := normalizeProxyURL(right.ProxyURL)
		return leftKey < rightKey
	})
	result.ProxyCount = len(result.Proxies)
	return result, nil
}

type proxyAggregate struct {
	key             string
	proxyURL        string
	activeCount     int
	overloadedCount int
}

func latestRequestTarget(index int, file cpaauthfiles.File) (store.LatestAccountRequestQuery, bool) {
	name := strings.TrimSpace(file.Name)
	if name == "" {
		return store.LatestAccountRequestQuery{}, false
	}
	provider := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(file.Provider), "_", "-"))
	if provider == "codex" && (file.AccountIDInvalid || file.AccountSnapshotInvalid) {
		// An explicitly malformed or conflicting Codex identity must not
		// silently fall back to a physical selector in this new aggregate.
		return store.LatestAccountRequestQuery{}, false
	}
	return store.LatestAccountRequestQuery{
		RequestIndex:          index,
		AuthFileSnapshot:      name,
		AuthIndex:             strings.TrimSpace(file.AuthIndex),
		Provider:              provider,
		AuthAccountIDSnapshot: strings.TrimSpace(file.AccountID),
		AuthProjectIDSnapshot: strings.TrimSpace(file.ProjectID),
		AccountSnapshot:       strings.TrimSpace(file.AccountSnapshot),
	}, true
}

func normalizeProxyURL(value string) (key, displayURL string) {
	displayURL = strings.TrimSpace(value)
	key = strings.TrimRight(displayURL, "/")
	if key == "" {
		return "", ""
	}
	return key, displayURL
}

func cloneSummary(value Summary) Summary {
	value.Proxies = append([]ProxyRow(nil), value.Proxies...)
	if value.Proxies == nil {
		value.Proxies = []ProxyRow{}
	}
	return value
}
