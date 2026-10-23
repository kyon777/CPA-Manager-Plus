package proxyfilter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	proxyfiltercontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/proxyfilter"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxyoverload"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestHandlerPersistsNormalizedURLsAndRequiresAuthorizationForWrite(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_proxy_filter_test"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &proxyfiltercontroller.Handler{App: &app.Context{
		Store:            st,
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
	}}

	unauthorized := httptest.NewRecorder()
	handler.Handle(unauthorized, httptest.NewRequest(http.MethodPut, "/usage-service/proxy-filter", bytes.NewBufferString(`{"urls":["http://x"]}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	request := httptest.NewRequest(http.MethodPut, "/usage-service/proxy-filter", bytes.NewBufferString(`{"urls":[" http://one/// ","http://two/","http://one"]}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var saved struct {
		URLs []string `json:"urls"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save response: %v", err)
	}
	if len(saved.URLs) != 2 || saved.URLs[0] != "http://one" || saved.URLs[1] != "http://two" {
		t.Fatalf("save response URLs = %#v", saved.URLs)
	}

	get := httptest.NewRequest(http.MethodGet, "/usage-service/proxy-filter", nil)
	get.Header.Set("Authorization", "Bearer "+adminKey)
	getRecorder := httptest.NewRecorder()
	handler.Handle(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
}

func TestHandlerInvalidatesProxyOverloadSummaryOnlyAfterSuccessfulSave(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_proxy_filter_invalidation_test"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}

	summary := proxyoverload.New(proxyoverload.Options{
		SetupResolver: proxyFilterConfiguredSetup{},
		AuthFiles: proxyFilterInventory{files: []cpaauthfiles.File{{
			Name: "active.json", AuthIndex: "idx-1", Provider: "codex", AccountSnapshot: "active@example.test", AccountID: "workspace-a", ProxyURL: "http://observed.example:8080",
		}}},
		Evidence:      proxyFilterEvidence{},
		ProxySettings: st,
	})
	if _, err := summary.Summary(context.Background(), false); err != nil {
		t.Fatalf("warm summary: %v", err)
	}

	handler := &proxyfiltercontroller.Handler{App: &app.Context{
		Store:                st,
		AdminAuthService:     adminauthsvc.New(config.Config{}, st),
		ProxyOverloadService: summary,
	}}
	request := httptest.NewRequest(http.MethodPut, "/usage-service/proxy-filter", bytes.NewBufferString(`{"urls":["http://saved-without-request.example:8080/"]}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	result, err := summary.Summary(context.Background(), false)
	if err != nil {
		t.Fatalf("fresh summary: %v", err)
	}
	if got, want := result.UnobservedProxies, []string{"http://saved-without-request.example:8080"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unobserved proxies = %#v, want %#v", got, want)
	}
}

type proxyFilterConfiguredSetup struct{}

func (proxyFilterConfiguredSetup) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "http://cpa.example.test", ManagementKey: "manager-key"}, true, nil
}

type proxyFilterInventory struct {
	files []cpaauthfiles.File
}

func (r proxyFilterInventory) FetchWithProxyURLs(context.Context, string, string) ([]cpaauthfiles.File, error) {
	return append([]cpaauthfiles.File(nil), r.files...), nil
}

type proxyFilterEvidence struct{}

func (proxyFilterEvidence) RecentProxyOverloadEvidence(
	context.Context,
	[]store.LatestAccountRequestQuery,
	int64,
	int,
) ([]store.ProxyOverloadEvidence, error) {
	return []store.ProxyOverloadEvidence{{RequestIndex: 0, Active: true}}, nil
}
