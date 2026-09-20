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
