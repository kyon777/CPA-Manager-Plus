package codexinspection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type proxyPoolStateCall struct {
	proxyURL string
	reason   string
}

type proxyInspectionRoundTripper func(*http.Request) (*http.Response, error)

func (fn proxyInspectionRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type exitIPCheckerRecorder struct {
	mu      sync.Mutex
	results map[string]model.ProxyExitIPCheckResult
	calls   []string
}

func (r *exitIPCheckerRecorder) Check(_ context.Context, proxyURL string) model.ProxyExitIPCheckResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, proxyURL)
	return r.results[proxyURL]
}

func (r *exitIPCheckerRecorder) snapshotCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type proxyPoolStateControllerRecorder struct {
	mu           sync.Mutex
	suspectCalls []proxyPoolStateCall
	restoreCalls []string
}

func (r *proxyPoolStateControllerRecorder) MarkSuspect(_ context.Context, proxyURL, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspectCalls = append(r.suspectCalls, proxyPoolStateCall{proxyURL: proxyURL, reason: reason})
	return nil
}

func (r *proxyPoolStateControllerRecorder) Restore(_ context.Context, fingerprint string) (model.ProxyPoolState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restoreCalls = append(r.restoreCalls, fingerprint)
	return model.ProxyPoolState{Fingerprint: fingerprint, Status: model.ProxyPoolStatusNormal}, nil
}

func (r *proxyPoolStateControllerRecorder) suspects() []proxyPoolStateCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proxyPoolStateCall(nil), r.suspectCalls...)
}

func (r *proxyPoolStateControllerRecorder) restores() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.restoreCalls...)
}

func TestApplyExitIPProxyEvidenceRequiresDirectFailure(t *testing.T) {
	proxyURL := "http://proxy.example:8080"
	baseAccount := account{
		Key: "alice.json::7", FileName: "alice.json", DisplayAccount: "alice@example.com",
		AuthIndex: "7", Provider: "codex", File: authFile{"proxy_url": proxyURL},
	}
	timeoutResult := model.CodexInspectionResult{
		AccountKey: baseAccount.Key, FileName: baseAccount.FileName, Provider: baseAccount.Provider,
		ErrorKind: "request_error", ErrorDetail: "context deadline exceeded",
	}

	tests := []struct {
		name         string
		result       model.ProxyExitIPCheckResult
		wantSuspect  bool
		wantChecks   int
		wantSuspects int
		wantRestores int
	}{
		{
			name:       "healthy exit IP clears inferred signal",
			result:     model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeHealthy},
			wantChecks: 1, wantRestores: 1,
		},
		{
			name:        "direct exit IP failure marks proxy abnormal",
			result:      model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeProxyFailed, Reason: "proxy_connect_timeout"},
			wantSuspect: true, wantChecks: 1, wantSuspects: 1,
		},
		{
			name:       "inconclusive exit IP remains non abnormal",
			result:     model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeInconclusive, Reason: "ip_echo_timeout"},
			wantChecks: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &exitIPCheckerRecorder{results: map[string]model.ProxyExitIPCheckResult{proxyURL: tt.result}}
			pool := &proxyPoolStateControllerRecorder{}
			svc := NewWithOptions(nil, nil, ServiceOptions{
				ProxyExitIPChecker: checker,
				ProxyPoolState:     pool,
			})
			got := svc.applyExitIPProxyEvidence(context.Background(), []account{baseAccount}, []model.CodexInspectionResult{timeoutResult}, runLogger{})
			if len(got) != 1 || got[0].ProxySuspect != tt.wantSuspect {
				t.Fatalf("results=%#v, want ProxySuspect=%v", got, tt.wantSuspect)
			}
			if calls := checker.snapshotCalls(); len(calls) != tt.wantChecks {
				t.Fatalf("checker calls=%#v, want count=%d", calls, tt.wantChecks)
			}
			if calls := pool.suspects(); len(calls) != tt.wantSuspects {
				t.Fatalf("suspect calls=%#v, want count=%d", calls, tt.wantSuspects)
			}
			if calls := pool.restores(); len(calls) != tt.wantRestores {
				t.Fatalf("restore calls=%#v, want count=%d", calls, tt.wantRestores)
			}
		})
	}
}

func TestApplyExitIPProxyEvidenceDoesNotProbeHTTP401(t *testing.T) {
	checker := &exitIPCheckerRecorder{}
	svc := NewWithOptions(nil, nil, ServiceOptions{ProxyExitIPChecker: checker})
	entry := account{
		Key: "alice.json::7", FileName: "alice.json", Provider: "codex", AuthIndex: "7",
		File: authFile{"proxy_url": "http://proxy.example:8080"},
	}
	got := svc.applyExitIPProxyEvidence(context.Background(), []account{entry}, []model.CodexInspectionResult{{
		AccountKey: entry.Key, FileName: entry.FileName, Provider: entry.Provider,
		StatusCode: intPtr(http.StatusUnauthorized), ErrorKind: "http_status", ErrorDetail: "expired",
	}}, runLogger{})
	if len(got) != 1 || got[0].ProxySuspect {
		t.Fatalf("HTTP 401 result=%#v", got)
	}
	if calls := checker.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("HTTP 401 must not invoke exit-IP probe: %#v", calls)
	}
}

func TestInspectSingleAccountNeverInfersProxyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		proxyURL   string
		statusCode int
		body       string
		transport  error
	}{
		{
			name:       "management 502 request failed",
			proxyURL:   "http://proxy.example:8080",
			statusCode: http.StatusBadGateway,
			body:       `{"error":"request failed"}`,
		},
		{
			name:      "transport timeout",
			proxyURL:  "http://proxy.example:8080",
			transport: context.DeadlineExceeded,
		},
		{
			name:       "management unauthorized",
			proxyURL:   "http://proxy.example:8080",
			statusCode: http.StatusUnauthorized,
			body:       `{"detail":"invalid token"}`,
		},
		{
			name:       "management rate limited",
			proxyURL:   "http://proxy.example:8080",
			statusCode: http.StatusTooManyRequests,
			body:       `{"detail":"Rate limit exceeded"}`,
		},
		{
			name:     "quota response",
			proxyURL: "http://proxy.example:8080",
			body:     `{"status_code":402,"body":{"detail":"quota exhausted"}}`,
		},
		{
			name:     "business 503 response",
			proxyURL: "http://proxy.example:8080",
			body:     `{"status_code":503,"body":{"error":"Our servers are currently overloaded. Please try again"}}`,
		},
		{
			name:       "no configured proxy",
			statusCode: http.StatusBadGateway,
			body:       `{"error":"request failed"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, upstreamURL, cleanup := proxyInspectionTestClient(t, tt.statusCode, tt.body, tt.transport)
			defer cleanup()
			svc := NewWithOptions(nil, nil, ServiceOptions{}, client)
			settings := model.DefaultCodexInspectionConfig()
			settings.Retries = 0
			result := svc.inspectSingleAccount(
				context.Background(),
				storeSetupForProxyInspection(upstreamURL),
				settings,
				account{
					Key:            "alice.json::7",
					FileName:       "alice.json",
					DisplayAccount: "alice@example.com",
					AuthIndex:      "7",
					Provider:       "codex",
					File:           authFile{"proxy_url": tt.proxyURL},
				},
				runLogger{},
			)
			if result.ProxySuspect {
				t.Fatalf("inspectSingleAccount inferred proxy abnormal without exit-IP proof: %#v", result)
			}
		})
	}
}

func TestReplacementRecheckDoesNotTriggerAutomaticActionsOrUsageCollection(t *testing.T) {
	var actionCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v0/management/auth-files" && request.Method == http.MethodGet:
			if request.URL.Query().Get("include_proxy_url") != "1" {
				t.Fatalf("recheck inventory query = %q, want include_proxy_url=1", request.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-alice","name":"alice.json","auth_index":"7","provider":"codex","account":"alice@example.com","proxy_url":"http://new-proxy.example:8080"}]}`))
		case request.URL.Path == "/v0/management/api-call" && request.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"detail":"expired"}}`))
		case request.URL.Path == "/v0/management/auth-files/status" || request.URL.Path == "/v0/management/auth-files":
			actionCalls++
			http.Error(w, "recheck must not mutate credentials", http.StatusInternalServerError)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	recovery := &inspectionRecoveryRecorder{}
	usageWrites := &proxyRecheckQuotaSnapshotRecorder{}
	svc := newCodexInspectionTestService(t, db)
	svc.client = upstream.Client()
	svc.reauthRecovery = recovery
	svc.quotaSnapshots = usageWrites

	output := svc.RecheckProxyReplacement(context.Background(), []model.ProxyReplacementTarget{{
		ClientKey: "alice", FileName: "alice.json", RuntimeID: "runtime-alice", AuthIndex: "7",
		AccountEmail: "alice@example.com", Provider: "codex",
	}}, 1)
	if len(output) != 1 || output[0].Succeeded || output[0].ErrorCode == "" {
		t.Fatalf("recheck output = %#v, want one failed 401 probe", output)
	}
	if actionCalls != 0 {
		t.Fatalf("recheck performed %d credential mutations", actionCalls)
	}
	if got := len(recovery.targets()); got != 0 {
		t.Fatalf("recheck signaled automatic token recovery: %#v", recovery.targets())
	}
	if got := usageWrites.calls(); got != 0 {
		t.Fatalf("recheck persisted %d usage snapshots", got)
	}
	runs, err := db.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inspection runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("recheck created inspection runs: %#v", runs)
	}
}

func TestSafeProxyRecheckMessageRedactsEmbeddedProxyURL(t *testing.T) {
	message := safeProxyRecheckMessage(`upstream rejected {"proxy_url":"http://user:password@proxy.example:8080/path"}`)
	if strings.Contains(message, "user:password@proxy.example") || strings.Contains(message, "http://") {
		t.Fatalf("proxy URL leaked in recheck message: %q", message)
	}
	if !strings.Contains(message, "[redacted-url]") {
		t.Fatalf("recheck message lost redaction marker: %q", message)
	}
}

type proxyRecheckQuotaSnapshotRecorder struct {
	mu    sync.Mutex
	count int
}

func (r *proxyRecheckQuotaSnapshotRecorder) WriteCodexInspectionResult(context.Context, model.CodexInspectionResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	return nil
}

func (r *proxyRecheckQuotaSnapshotRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func proxyInspectionTestClient(
	t *testing.T,
	statusCode int,
	body string,
	transportErr error,
) (*http.Client, string, func()) {
	t.Helper()
	if transportErr != nil {
		return &http.Client{Transport: proxyInspectionRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})}, "http://proxy-inspection.invalid", func() {}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v0/management/api-call" || request.Method != http.MethodPost {
			http.NotFound(w, request)
			return
		}
		if statusCode != 0 {
			w.WriteHeader(statusCode)
		}
		_, _ = w.Write([]byte(body))
	}))
	return upstream.Client(), upstream.URL, upstream.Close
}

func storeSetupForProxyInspection(upstreamURL string) store.Setup {
	return store.Setup{CPAUpstreamURL: upstreamURL, ManagementKey: "management-key"}
}
