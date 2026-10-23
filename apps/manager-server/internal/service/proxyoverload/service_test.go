package proxyoverload

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestSummaryFetchesCurrentProxyInventoryOnceAndGroupsCurrentURLs(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
		testCodexFile("a.json", "1", "alice@example.test", "workspace-a", " http://proxy.example:8080/ "),
		testCodexFile("b.json", "2", "bob@example.test", "workspace-b", "http://proxy.example:8080////"),
		testCodexFile("c.json", "3", "carol@example.test", "workspace-c", ""),
		testCodexFile("d.json", "4", "dave@example.test", "workspace-d", "http://unused.example:8080"),
	}}
	evidence := &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{
		{RequestIndex: 0, Active: true, ServerOverloaded: true},
		{RequestIndex: 1, Active: true},
		{RequestIndex: 2, Active: true},
	}}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles:     inventory,
		Evidence:      evidence,
		Now:           func() time.Time { return now },
	})

	result, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got, want := inventory.callCount(), 1; got != want {
		t.Fatalf("inventory calls = %d, want %d", got, want)
	}
	if got, want := evidence.callCount(), 1; got != want {
		t.Fatalf("evidence calls = %d, want %d", got, want)
	}
	if got, want := result.ActiveAccountCount, 3; got != want {
		t.Fatalf("active accounts = %d, want %d", got, want)
	}
	if got, want := result.MissingProxyAccountCount, 1; got != want {
		t.Fatalf("missing proxy accounts = %d, want %d", got, want)
	}
	if got, want := result.ProxyCount, 1; got != want {
		t.Fatalf("proxy count = %d, want %d", got, want)
	}
	if got, want := result.OverloadedAccountCount, 1; got != want {
		t.Fatalf("overloaded accounts = %d, want %d", got, want)
	}
	if got, want := len(result.Proxies), 1; got != want {
		t.Fatalf("proxy rows = %#v, want %d", result.Proxies, want)
	}
	row := result.Proxies[0]
	if row.ProxyURL != "http://proxy.example:8080/" ||
		row.ActiveAccountCount != 2 ||
		row.OverloadedAccountCount != 1 ||
		row.NonOverloadedAccountCount != 1 ||
		row.OverloadedRate != 0.5 {
		t.Fatalf("proxy row = %#v", row)
	}
	if evidence.lastLimit != 9 || evidence.lastSinceMS != now.Add(-72*time.Hour).UnixMilli() {
		t.Fatalf("evidence limit/since = %d/%d", evidence.lastLimit, evidence.lastSinceMS)
	}
}

func TestSummaryNormalizesTrailingSlashesOnlyForGrouping(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles: &fakeInventoryReader{files: []cpaauthfiles.File{
			testCodexFile("a.json", "1", "a@example.test", "workspace-a", "http://proxy.example:8080/"),
			testCodexFile("b.json", "2", "b@example.test", "workspace-b", "http://proxy.example:8080////"),
		}},
		Evidence: &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{
			{RequestIndex: 0, Active: true},
			{RequestIndex: 1, Active: true},
		}},
		Now: func() time.Time { return now },
	})

	result, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].ProxyURL != "http://proxy.example:8080/" {
		t.Fatalf("proxy grouping = %#v", result.Proxies)
	}
}

func TestSummaryDoesNotClassifyNoEventCredentialAsActive(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles: &fakeInventoryReader{files: []cpaauthfiles.File{
			testCodexFile("inactive.json", "1", "inactive@example.test", "workspace-a", ""),
		}},
		Evidence: &fakeEvidenceReader{},
		Now:      func() time.Time { return now },
	})

	result, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if result.ActiveAccountCount != 0 || result.MissingProxyAccountCount != 0 || len(result.Proxies) != 0 {
		t.Fatalf("inactive account leaked into aggregate: %#v", result)
	}
}

func TestSummaryIncludesSavedProxyDifferenceWithoutExtraCPAReads(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
		testCodexFile("active.json", "1", "active@example.test", "workspace-active", "http://proxy.example:8080/"),
	}}
	evidence := &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}}}
	settings := &fakeProxySettingsReader{
		settings: store.ProxyFilterSettings{URLs: []string{
			" http://proxy.example:8080/// ",
			"http://saved-without-request.example:8080/",
			"http://proxy.example:8080",
		}},
		present: true,
	}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles:     inventory,
		Evidence:      evidence,
		ProxySettings: settings,
		Now:           func() time.Time { return now },
	})

	result, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !result.SavedProxyListAvailable {
		t.Fatal("saved proxy list should be available")
	}
	if got, want := result.SavedProxyCount, 2; got != want {
		t.Fatalf("saved proxy count = %d, want %d", got, want)
	}
	if got, want := result.ObservedSavedProxyCount, 1; got != want {
		t.Fatalf("observed saved proxy count = %d, want %d", got, want)
	}
	if got, want := result.UnobservedProxyCount, 1; got != want {
		t.Fatalf("unobserved proxy count = %d, want %d", got, want)
	}
	if got, want := result.UnobservedProxies, []string{"http://saved-without-request.example:8080"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unobserved proxies = %#v, want %#v", got, want)
	}
	if got, want := inventory.callCount(), 1; got != want {
		t.Fatalf("inventory calls = %d, want %d", got, want)
	}
	if got, want := evidence.callCount(), 1; got != want {
		t.Fatalf("evidence calls = %d, want %d", got, want)
	}
	if got, want := settings.callCount(), 1; got != want {
		t.Fatalf("settings calls = %d, want %d", got, want)
	}
}

func TestSummaryKeepsAggregateWhenSavedProxySettingsCannotBeRead(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles: &fakeInventoryReader{files: []cpaauthfiles.File{
			testCodexFile("active.json", "1", "active@example.test", "workspace-active", "http://proxy.example:8080"),
		}},
		Evidence:      &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}}},
		ProxySettings: &fakeProxySettingsReader{err: errors.New("proxy=http://user:pass@private.example:8080")},
		Now:           func() time.Time { return now },
	})

	result, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if result.SavedProxyListAvailable {
		t.Fatal("unreadable saved proxy settings should be marked unavailable")
	}
	if result.SavedProxyCount != 0 || result.ObservedSavedProxyCount != 0 || result.UnobservedProxyCount != 0 || len(result.UnobservedProxies) != 0 {
		t.Fatalf("unavailable settings leaked saved-list data: %#v", result)
	}
	if result.ActiveAccountCount != 1 || len(result.Proxies) != 1 {
		t.Fatalf("valid aggregate was lost when settings read failed: %#v", result)
	}
}

func TestInvalidateDropsCompletedCacheAndPreventsStaleFlightRecache(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	blocked := make(chan struct{})
	inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
		testCodexFile("active.json", "1", "active@example.test", "workspace-active", "http://proxy.example:8080"),
	}}
	evidence := &fakeEvidenceReader{
		items:   []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}},
		block:   blocked,
		started: make(chan struct{}, 2),
	}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles:     inventory,
		Evidence:      evidence,
		Now:           func() time.Time { return now },
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Summary(context.Background(), false)
		firstDone <- err
	}()
	<-evidence.started
	service.Invalidate()
	close(blocked)
	if err := <-firstDone; err != nil {
		t.Fatalf("stale summary: %v", err)
	}

	if _, err := service.Summary(context.Background(), false); err != nil {
		t.Fatalf("fresh summary after invalidation: %v", err)
	}
	if got, want := inventory.callCount(), 2; got != want {
		t.Fatalf("inventory calls after stale flight = %d, want %d", got, want)
	}
	if got, want := evidence.callCount(), 2; got != want {
		t.Fatalf("evidence calls after stale flight = %d, want %d", got, want)
	}
}

func TestSummaryCachesCompletedResultForFifteenSeconds(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	clock := now
	inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
		testCodexFile("a.json", "1", "a@example.test", "workspace-a", "http://proxy.example:8080"),
	}}
	evidence := &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}}}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles:     inventory,
		Evidence:      evidence,
		Now:           func() time.Time { return clock },
	})

	first, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("first summary: %v", err)
	}
	first.Proxies[0].ProxyURL = "mutated-by-caller"
	clock = clock.Add(14 * time.Second)
	second, err := service.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("second summary: %v", err)
	}
	if inventory.callCount() != 1 || evidence.callCount() != 1 {
		t.Fatalf("cache did not suppress repeat work: inventory=%d evidence=%d", inventory.callCount(), evidence.callCount())
	}
	if second.Proxies[0].ProxyURL == "mutated-by-caller" {
		t.Fatal("cached summary exposed mutable proxy rows")
	}
	clock = clock.Add(2 * time.Second)
	if _, err := service.Summary(context.Background(), false); err != nil {
		t.Fatalf("expired cache summary: %v", err)
	}
	if inventory.callCount() != 2 || evidence.callCount() != 2 {
		t.Fatalf("expired cache did not recompute: inventory=%d evidence=%d", inventory.callCount(), evidence.callCount())
	}
}

func TestSummaryForceRefreshBypassesCompletedCacheButJoinsInFlightWork(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	inventory := &fakeInventoryReader{files: []cpaauthfiles.File{
		testCodexFile("a.json", "1", "a@example.test", "workspace-a", "http://proxy.example:8080"),
	}}
	evidence := &fakeEvidenceReader{items: []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}}}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles:     inventory,
		Evidence:      evidence,
		Now:           func() time.Time { return now },
	})

	if _, err := service.Summary(context.Background(), false); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	blocked := make(chan struct{})
	evidence.block = blocked
	evidence.started = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Summary(context.Background(), true)
		firstDone <- err
	}()
	<-evidence.started

	secondDone := make(chan error, 1)
	go func() {
		_, err := service.Summary(context.Background(), false)
		secondDone <- err
	}()
	close(blocked)
	if err := <-firstDone; err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("joined summary: %v", err)
	}
	if inventory.callCount() != 2 || evidence.callCount() != 2 {
		t.Fatalf("force/inflight calls = inventory %d evidence %d, want 2 each", inventory.callCount(), evidence.callCount())
	}
}

func TestSummaryCallerCancellationDoesNotCancelSharedCalculation(t *testing.T) {
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	blocked := make(chan struct{})
	evidence := &fakeEvidenceReader{
		items:   []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}},
		block:   blocked,
		started: make(chan struct{}),
	}
	service := New(Options{
		SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
		AuthFiles: &fakeInventoryReader{files: []cpaauthfiles.File{
			testCodexFile("a.json", "1", "a@example.test", "workspace-a", "http://proxy.example:8080"),
		}},
		Evidence: evidence,
		Now:      func() time.Time { return now },
	})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Summary(cancelledCtx, false)
		firstDone <- err
	}()
	<-evidence.started
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller error = %v, want context.Canceled", err)
	}

	joinedDone := make(chan error, 1)
	go func() {
		result, err := service.Summary(context.Background(), false)
		if err == nil && result.ActiveAccountCount != 1 {
			err = errors.New("joined caller received an incomplete summary")
		}
		joinedDone <- err
	}()
	close(blocked)
	if err := <-joinedDone; err != nil {
		t.Fatalf("joined caller: %v", err)
	}
	if got := evidence.callCount(); got != 1 {
		t.Fatalf("shared calculation calls = %d, want 1", got)
	}
}

func TestSummaryReturnsSafeUnavailableErrors(t *testing.T) {
	cases := []struct {
		name    string
		options Options
	}{
		{
			name: "setup error",
			options: Options{
				SetupResolver: fakeSetupResolver{err: errors.New("token=secret@example.test")},
			},
		},
		{
			name: "inventory error",
			options: Options{
				SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
				AuthFiles:     &fakeInventoryReader{err: errors.New("proxy=http://user:pass@secret.example:8080")},
				Evidence:      &fakeEvidenceReader{},
			},
		},
		{
			name: "index error",
			options: Options{
				SetupResolver: fakeSetupResolver{setup: configuredSetup(), ok: true},
				AuthFiles: &fakeInventoryReader{files: []cpaauthfiles.File{
					testCodexFile("a.json", "1", "a@example.test", "workspace-a", "http://proxy.example:8080"),
				}},
				Evidence: &fakeEvidenceReader{err: usageevent.ErrLatestRequestIndexesUnavailable},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := New(tc.options)
			_, err := service.Summary(context.Background(), false)
			if err == nil {
				t.Fatal("summary unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), "http://") {
				t.Fatalf("unsafe service error: %q", err)
			}
		})
	}
}

type fakeSetupResolver struct {
	setup store.Setup
	ok    bool
	err   error
}

func (r fakeSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, r.ok, r.err
}

type fakeInventoryReader struct {
	mu    sync.Mutex
	files []cpaauthfiles.File
	err   error
	calls int
}

func (r *fakeInventoryReader) FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return append([]cpaauthfiles.File(nil), r.files...), nil
}

func (r *fakeInventoryReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type fakeEvidenceReader struct {
	mu          sync.Mutex
	items       []store.ProxyOverloadEvidence
	err         error
	calls       int
	lastSinceMS int64
	lastLimit   int
	block       <-chan struct{}
	started     chan struct{}
}

type fakeProxySettingsReader struct {
	mu       sync.Mutex
	settings store.ProxyFilterSettings
	present  bool
	err      error
	calls    int
}

func (r *fakeProxySettingsReader) LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return store.ProxyFilterSettings{}, false, r.err
	}
	return store.ProxyFilterSettings{
		URLs:        append([]string(nil), r.settings.URLs...),
		UpdatedAtMS: r.settings.UpdatedAtMS,
	}, r.present, nil
}

func (r *fakeProxySettingsReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeEvidenceReader) RecentProxyOverloadEvidence(
	_ context.Context,
	_ []store.LatestAccountRequestQuery,
	sinceMS int64,
	limit int,
) ([]store.ProxyOverloadEvidence, error) {
	r.mu.Lock()
	r.calls++
	r.lastSinceMS = sinceMS
	r.lastLimit = limit
	block := r.block
	started := r.started
	err := r.err
	items := append([]store.ProxyOverloadEvidence(nil), r.items...)
	r.mu.Unlock()

	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *fakeEvidenceReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func testCodexFile(name, authIndex, account, workspace, proxyURL string) cpaauthfiles.File {
	return cpaauthfiles.File{
		Name:            name,
		AuthIndex:       authIndex,
		Provider:        "codex",
		AccountSnapshot: account,
		AccountID:       workspace,
		ProxyURL:        proxyURL,
	}
}

func configuredSetup() store.Setup {
	return store.Setup{CPAUpstreamURL: "http://cpa.example.test", ManagementKey: "manager-key"}
}
