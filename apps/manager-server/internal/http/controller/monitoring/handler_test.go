package monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxyoverload"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestHandleAccountHistoryRejectsUnknownTargetFields(t *testing.T) {
	st := newHandlerTestStore(t)
	const adminKey = "cpamp_test_key"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &Handler{App: &app.Context{
		AdminAuthService:  adminauthsvc.New(config.Config{}, st),
		MonitoringService: monitoringsvc.New(st),
	}}
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/monitoring/account-history",
		bytes.NewBufferString(`{"accounts":[{"source_hash":"source-only"}]}`),
	)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "source_hash") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestHandleAccountWindowUsageRejectsUnknownTargetFields(t *testing.T) {
	st := newHandlerTestStore(t)
	const adminKey = "cpamp_test_key"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &Handler{App: &app.Context{
		AdminAuthService:  adminauthsvc.New(config.Config{}, st),
		MonitoringService: monitoringsvc.New(st),
	}}
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/monitoring/account-window-usage",
		bytes.NewBufferString(`{"windows":[{"row_key":"row-1","window_key":"5h","from_ms":1,"to_ms":2,"source_hash":"source-only"}]}`),
	)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "source_hash") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestHandleAccountWindowUsageDoesNotWriteServerErrorAfterClientCancellation(t *testing.T) {
	st := newHandlerTestStore(t)
	handler := &Handler{App: &app.Context{MonitoringService: monitoringsvc.New(st)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/monitoring/account-window-usage",
		bytes.NewBufferString(`{"windows":[{"row_key":"row-1","provider_window_id":"monthly","period":"current","from_ms":1,"to_ms":2,"auth_file_snapshot":"credential.json","auth_provider_snapshot":"codex","auth_index":"idx-1"}]}`),
	).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.handleAccountWindowUsage(recorder, req)

	if recorder.Code == http.StatusInternalServerError {
		t.Fatalf("canceled request wrote status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("canceled request wrote body=%s", recorder.Body.String())
	}
}

func TestValidateAccountWindowUsageRequiresCredentialIdentity(t *testing.T) {
	base := monitoringsvc.AccountWindowUsageTarget{
		RowKey:           "row-1",
		ProviderWindowID: "weekly",
		FromMS:           1,
		ToMS:             2,
	}
	weak := base
	weak.AccountSnapshot = "legacy@example.com"
	weak.AuthIndex = "auth-legacy"
	if err := validateAccountWindowUsageRequest(monitoringsvc.AccountWindowUsageRequest{Windows: []monitoringsvc.AccountWindowUsageTarget{weak}}); err == nil || !strings.Contains(err.Error(), "credential identity") {
		t.Fatalf("weak identity error = %v", err)
	}

	for name, mutate := range map[string]func(*monitoringsvc.AccountWindowUsageTarget){
		"auth file": func(target *monitoringsvc.AccountWindowUsageTarget) {
			target.AuthFileSnapshot = "credential.json"
			target.AuthProviderSnapshot = "codex"
		},
		"legacy file source": func(target *monitoringsvc.AccountWindowUsageTarget) {
			target.Source = "credential.json"
			target.AuthProviderSnapshot = "codex"
		},
		"provider auth index": func(target *monitoringsvc.AccountWindowUsageTarget) {
			target.AuthProviderSnapshot = "codex"
			target.AuthIndex = "auth-1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			target := base
			mutate(&target)
			if err := validateAccountWindowUsageRequest(monitoringsvc.AccountWindowUsageRequest{Windows: []monitoringsvc.AccountWindowUsageTarget{target}}); err != nil {
				t.Fatalf("valid credential identity rejected: %v", err)
			}
		})
	}

	for name, mutate := range map[string]func(*monitoringsvc.AccountWindowUsageTarget){
		"auth file without provider": func(target *monitoringsvc.AccountWindowUsageTarget) {
			target.AuthFileSnapshot = "credential.json"
		},
		"legacy file source without provider": func(target *monitoringsvc.AccountWindowUsageTarget) {
			target.Source = "credential.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			target := base
			mutate(&target)
			if err := validateAccountWindowUsageRequest(monitoringsvc.AccountWindowUsageRequest{Windows: []monitoringsvc.AccountWindowUsageTarget{target}}); err == nil || !strings.Contains(err.Error(), "auth_provider_snapshot") {
				t.Fatalf("providerless file identity error = %v", err)
			}
		})
	}
}

func TestHandleAccountHistoryValidatesRowKeysAndTargets(t *testing.T) {
	testCases := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{
			name:        "missing row key",
			body:        `{"accounts":[{"auth_file_snapshot":"credential.json","auth_index":"auth-1"}]}`,
			wantMessage: "row_key is required",
		},
		{
			name:        "duplicate row key",
			body:        `{"accounts":[{"row_key":"row-1","auth_index":"auth-1"},{"row_key":"row-1","auth_index":"auth-2"}]}`,
			wantMessage: "row_key must be unique",
		},
		{
			name:        "missing target",
			body:        `{"accounts":[{"row_key":"row-1"}]}`,
			wantMessage: "at least one account target field is required",
		},
		{
			name:        "file target missing provider",
			body:        `{"accounts":[{"row_key":"row-1","auth_file_snapshot":"credential.json","auth_index":"auth-1"}]}`,
			wantMessage: "auth_provider_snapshot is required for file account targets",
		},
		{
			name:        "legacy file source missing provider",
			body:        `{"accounts":[{"row_key":"row-1","source":"credential.json","auth_index":"auth-1"}]}`,
			wantMessage: "auth_provider_snapshot is required for file account targets",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := executeAuthorizedMonitoringRequest(
				t,
				"/v0/management/monitoring/account-history",
				testCase.body,
			)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), testCase.wantMessage) {
				t.Fatalf("body = %s, want %q", recorder.Body.String(), testCase.wantMessage)
			}
		})
	}
}

func TestValidateAccountHistoryRequestTreatsRowKeysAsOpaque(t *testing.T) {
	err := validateAccountHistoryRequest(monitoringsvc.AccountHistoryRequest{
		Accounts: []monitoringsvc.AccountHistoryTarget{
			{RowKey: " row", AuthIndex: "auth-1"},
			{RowKey: "row", AuthIndex: "auth-2"},
		},
	})
	if err != nil {
		t.Fatalf("validate opaque row keys: %v", err)
	}
}

func TestValidateAccountRecentIndexRequestRejectsOutOfRangeRecentLimit(t *testing.T) {
	base := monitoringsvc.AccountRecentIndexRequest{Accounts: []monitoringsvc.AccountHistoryTarget{{
		RowKey: "row-1", AuthFileSnapshot: "credential.json", AuthProviderSnapshot: "codex",
	}}}
	for _, limit := range []int{0, 10} {
		req := base
		req.RecentLimit = &limit
		if err := validateAccountRecentIndexRequest(req); err == nil {
			t.Fatalf("recent_limit=%d was accepted", limit)
		}
	}
}

func executeAuthorizedMonitoringRequest(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	st := newHandlerTestStore(t)
	const adminKey = "cpamp_test_key"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &Handler{App: &app.Context{
		AdminAuthService:  adminauthsvc.New(config.Config{}, st),
		MonitoringService: monitoringsvc.New(st),
	}}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, req)
	return recorder
}

func TestProxyOverloadSummaryRequiresPanelAuthorization(t *testing.T) {
	handler, _, _ := newProxyOverloadSummaryHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/monitoring/proxy-overload-summary", nil)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatalf("unauthorized request unexpectedly succeeded: %s", recorder.Body.String())
	}
}

func TestProxyOverloadSummaryRejectsNonGET(t *testing.T) {
	handler, adminKey, _ := newProxyOverloadSummaryHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/monitoring/proxy-overload-summary", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProxyOverloadSummaryRejectsUnknownQuery(t *testing.T) {
	handler, adminKey, inventory := newProxyOverloadSummaryHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/monitoring/proxy-overload-summary?hours=1", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if inventory.calls != 0 {
		t.Fatalf("unknown query invoked summary inventory %d times", inventory.calls)
	}
}

func TestProxyOverloadSummaryReturnsNoStoreAndAggregateOnly(t *testing.T) {
	handler, adminKey, _ := newProxyOverloadSummaryHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/monitoring/proxy-overload-summary", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got, want := recorder.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	body := recorder.Body.String()
	for _, secret := range []string{
		"alice@example.test",
		"secret-file.json",
		"raw_json",
		"fail_body",
		"trace-secret",
		"token-secret",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("aggregate response leaked %q: %s", secret, body)
		}
	}
	var result proxyoverload.Summary
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].OverloadedAccountCount != 1 {
		t.Fatalf("summary = %#v", result)
	}
}

func TestProxyOverloadSummaryForceRefreshBypassesCompletedCache(t *testing.T) {
	handler, adminKey, inventory := newProxyOverloadSummaryHandler(t)

	for _, target := range []string{
		"/v0/management/monitoring/proxy-overload-summary",
		"/v0/management/monitoring/proxy-overload-summary",
		"/v0/management/monitoring/proxy-overload-summary?refresh=1",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer "+adminKey)
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", target, recorder.Code, recorder.Body.String())
		}
	}
	if inventory.calls != 2 {
		t.Fatalf("inventory calls = %d, want cache hit then forced refresh", inventory.calls)
	}
}

type proxyOverloadHandlerSetupResolver struct{}

func (proxyOverloadHandlerSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "http://cpa.example.test", ManagementKey: "manager-key"}, true, nil
}

type proxyOverloadHandlerInventory struct {
	calls int
}

func (inventory *proxyOverloadHandlerInventory) FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error) {
	inventory.calls++
	return []cpaauthfiles.File{{
		Name:            "secret-file.json",
		AuthIndex:       "idx-1",
		Provider:        "codex",
		AccountSnapshot: "alice@example.test",
		AccountID:       "workspace-a",
		ProxyURL:        "http://proxy.example:8080",
	}}, nil
}

type proxyOverloadHandlerEvidence struct{}

func (proxyOverloadHandlerEvidence) RecentProxyOverloadEvidence(
	context.Context,
	[]store.LatestAccountRequestQuery,
	int64,
	int,
) ([]store.ProxyOverloadEvidence, error) {
	return []store.ProxyOverloadEvidence{{
		RequestIndex:     0,
		Active:           true,
		ServerOverloaded: true,
	}}, nil
}

func newProxyOverloadSummaryHandler(t *testing.T) (*Handler, string, *proxyOverloadHandlerInventory) {
	t.Helper()
	st := newHandlerTestStore(t)
	const adminKey = "cpamp_proxy_summary_key"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	inventory := &proxyOverloadHandlerInventory{}
	summaryService := proxyoverload.New(proxyoverload.Options{
		SetupResolver: proxyOverloadHandlerSetupResolver{},
		AuthFiles:     inventory,
		Evidence:      proxyOverloadHandlerEvidence{},
		Now: func() time.Time {
			return time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
		},
	})
	return &Handler{App: &app.Context{
		AdminAuthService:     adminauthsvc.New(config.Config{}, st),
		ProxyOverloadService: summaryService,
	}}, adminKey, inventory
}

func newHandlerTestStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}
