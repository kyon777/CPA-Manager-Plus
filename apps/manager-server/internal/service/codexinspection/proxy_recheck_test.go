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

type proxyFailureReportCall struct {
	proxyURL string
	reason   string
}

type proxyFailureReporterRecorder struct {
	mu           sync.Mutex
	suspectCalls []proxyFailureReportCall
	pendingCalls []proxyFailureReportCall
}

func (r *proxyFailureReporterRecorder) MarkSuspect(_ context.Context, proxyURL, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspectCalls = append(r.suspectCalls, proxyFailureReportCall{proxyURL: proxyURL, reason: reason})
	return nil
}

func (r *proxyFailureReporterRecorder) MarkPending(_ context.Context, proxyURL, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingCalls = append(r.pendingCalls, proxyFailureReportCall{proxyURL: proxyURL, reason: reason})
	return nil
}

func (r *proxyFailureReporterRecorder) pendingSnapshot() []proxyFailureReportCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proxyFailureReportCall(nil), r.pendingCalls...)
}

func (r *proxyFailureReporterRecorder) suspectSnapshot() []proxyFailureReportCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proxyFailureReportCall(nil), r.suspectCalls...)
}

type proxyInspectionRoundTripper func(*http.Request) (*http.Response, error)

func (fn proxyInspectionRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestInspectSingleAccountMarksOnlyTransportProxyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		proxyURL   string
		statusCode int
		body       string
		transport  error
		want       bool
		wantReason string
	}{
		{
			name:       "management 502 request failed",
			proxyURL:   "http://proxy.example:8080",
			statusCode: http.StatusBadGateway,
			body:       `{"error":"request failed"}`,
			want:       true,
			wantReason: "transport_502_request_failed",
		},
		{
			name:       "transport timeout",
			proxyURL:   "http://proxy.example:8080",
			transport:  context.DeadlineExceeded,
			want:       true,
			wantReason: "transport_timeout",
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
			reporter := &proxyFailureReporterRecorder{}
			client, upstreamURL, cleanup := proxyInspectionTestClient(t, tt.statusCode, tt.body, tt.transport)
			defer cleanup()
			svc := NewWithOptions(nil, nil, ServiceOptions{ProxyFailureReporter: reporter}, client)
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
			if result.ProxySuspect != tt.want {
				t.Fatalf("ProxySuspect = %v, want %v; result=%#v", result.ProxySuspect, tt.want, result)
			}
			calls := reporter.pendingSnapshot()
			if tt.want {
				if len(calls) != 1 || calls[0].proxyURL != tt.proxyURL || calls[0].reason != tt.wantReason {
					t.Fatalf("pending reporter calls = %#v, want URL=%q reason=%q", calls, tt.proxyURL, tt.wantReason)
				}
			} else if len(calls) != 0 {
				t.Fatalf("unexpected pending proxy reports: %#v", calls)
			}
			if calls := reporter.suspectSnapshot(); len(calls) != 0 {
				t.Fatalf("inferred inspection failure prematurely marked suspect: %#v", calls)
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
