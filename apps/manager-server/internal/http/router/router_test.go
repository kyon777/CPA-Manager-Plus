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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
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
	appContext := &app.Context{
		Config:               config.Config{CORSOrigins: []string{"*"}},
		AdminAuthService:     adminauthsvc.New(config.Config{}, st),
		TokenRecoveryService: tokenrecoverysvc.NewWithOptions(tokenrecoverysvc.Options{Tasks: st}),
	}
	handler := New(appContext)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/signals", bytes.NewBufferString(`{"fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex"}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("route status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
