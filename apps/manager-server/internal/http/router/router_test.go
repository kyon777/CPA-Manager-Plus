package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	authfilesbanneddeletesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesbanneddelete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	credentialruntimesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialruntime"
	proxyreplacementsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxyreplacement"
	tokenrecoverysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestRouterRoutesTokenRecoveryBeforeCoreManagementProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "router.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_router_token_recovery"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	recovery := tokenrecoverysvc.NewWithOptions(tokenrecoverysvc.Options{Tasks: st})
	automaticSignal := tokenrecoverysvc.NewAutomaticSignalGate(tokenrecoverysvc.AutomaticSignalGateOptions{
		Recovery:      recovery,
		SetupResolver: routerTokenRecoverySetupResolver{},
		AuthFiles:     routerTokenRecoveryAuthFiles{},
		Enabled:       func(context.Context) bool { return true },
	})
	appContext := &app.Context{
		Config:                       config.Config{CORSOrigins: []string{"*"}},
		AdminAuthService:             adminauthsvc.New(config.Config{}, st),
		TokenRecoveryService:         recovery,
		TokenRecoveryAutomaticSignal: automaticSignal,
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/signals", bytes.NewBufferString(`{"fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex","observedStatusCode":401}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("route status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRouterRoutesCredentialRuntimeMetadataBeforeCoreManagementProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "router-credential-runtime.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_router_credential_runtime"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files/download" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer core-management-key" {
			t.Fatalf("core authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"auth_index":"7","email":"person@example.com","proxy_url":"http://proxy.example:8080"}`))
	}))
	t.Cleanup(core.Close)
	recovery := tokenrecoverysvc.NewWithOptions(tokenrecoverysvc.Options{Tasks: st})
	runtime := credentialruntimesvc.NewWithOptions(credentialruntimesvc.Options{
		SetupResolver:  routerCredentialRuntimeSetupResolver{baseURL: core.URL},
		AuthFiles:      cpaauthfiles.New(core.Client()),
		RecoveryLookup: recovery,
	})
	appContext := &app.Context{
		Config:                   config.Config{CORSOrigins: []string{"*"}},
		AdminAuthService:         adminauthsvc.New(config.Config{}, st),
		CredentialRuntimeService: runtime,
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/credential-runtime-metadata", bytes.NewBufferString(`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex"}]}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"proxyUrl":"http://proxy.example:8080"`)) {
		t.Fatalf("route status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

type routerCredentialRuntimeSetupResolver struct {
	baseURL string
}

func (r routerCredentialRuntimeSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: r.baseURL, ManagementKey: "core-management-key"}, true, nil
}

type routerTokenRecoverySetupResolver struct{}

func (routerTokenRecoverySetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "https://core.example.test", ManagementKey: "management-key"}, true, nil
}

type routerTokenRecoveryAuthFiles struct{}

func (routerTokenRecoveryAuthFiles) Verify(
	_ context.Context,
	_ string,
	_ string,
	identity cpaauthfiles.Identity,
) (cpaauthfiles.File, error) {
	return cpaauthfiles.File{
		Name:            identity.AuthFileName,
		AuthIndex:       identity.AuthIndex,
		Provider:        identity.Provider,
		AccountSnapshot: identity.AccountSnapshot,
	}, nil
}

func TestRouterRoutesAccountBannedDeleteBeforeCoreManagementProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "router-banned-delete.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_router_banned_delete"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	appContext := &app.Context{
		Config:           config.Config{CORSOrigins: []string{"*"}},
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
		AccountBannedAuthFileDeleteService: authfilesbanneddeletesvc.NewWithOptions(authfilesbanneddeletesvc.Options{
			SetupResolver: routerCredentialRuntimeSetupResolver{baseURL: "http://cpa.example.test"},
			Recovery:      routerBannedRecoveryLookup{},
			AuthFiles:     routerBannedAuthFiles{},
		}),
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/delete-account-banned-batch", bytes.NewBufferString(`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex","runtimeId":"runtime-a"}]}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"deleted"`)) {
		t.Fatalf("route status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRouterRoutesProxyReplacementBeforeCoreManagementProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "router-proxy-replacement.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_router_proxy_replacement"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	appContext := &app.Context{
		Config:           config.Config{CORSOrigins: []string{"*"}},
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
		ProxyReplacementService: proxyreplacementsvc.NewWithOptions(proxyreplacementsvc.Options{
			PoolState: routerProxyReplacementPool{},
		}),
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/proxy-replacements/proxy-pool", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"items"`)) {
		t.Fatalf("route status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type routerProxyReplacementPool struct{}

func (routerProxyReplacementPool) List(context.Context) ([]model.ProxyPoolState, error) {
	return []model.ProxyPoolState{}, nil
}
func (routerProxyReplacementPool) MarkSuspect(context.Context, string, string) error { return nil }

type routerBannedRecoveryLookup struct{}

func (routerBannedRecoveryLookup) Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	return model.TokenRecoveryTask{Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned"}, true, nil
}

type routerBannedAuthFiles struct{}

func (routerBannedAuthFiles) Fetch(context.Context, string, string) ([]cpaauthfiles.File, error) {
	return []cpaauthfiles.File{{ID: "runtime-a", Name: "a.json", AuthIndex: "7", Provider: "codex", AccountSnapshot: "person@example.com"}}, nil
}

func (routerBannedAuthFiles) Delete(context.Context, string, string, string) error { return nil }
func TestRouterRoutesPriorityDemotionUpdatesBeforeCoreManagementProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "router-server-error-priority.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "cpamp_router_server_error_priority"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	appContext := &app.Context{
		Config:           config.Config{CORSOrigins: []string{"*"}},
		Store:            st,
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/server-error-priority-demotions/updates", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"items"`)) {
		t.Fatalf("route status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
