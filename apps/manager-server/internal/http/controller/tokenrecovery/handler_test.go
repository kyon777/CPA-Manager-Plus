package tokenrecovery

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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	tokenrecoverysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenrecovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const tokenRecoveryHandlerAdminKey = "cpamp_token_recovery_test"

func TestHandlerSignalsAndReadsRedactedTokenRecoveryTask(t *testing.T) {
	handler := newTestHandler(t)
	body := []byte(`{"fileName":"physical account.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex","observedAtMs":123}`)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/signals", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("signal status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var signal struct {
		Task struct {
			ID            int64  `json:"id"`
			Status        string `json:"status"`
			AccountEmail  string `json:"accountEmail"`
			LastErrorCode string `json:"lastErrorCode"`
		} `json:"task"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&signal); err != nil {
		t.Fatalf("decode signal response: %v", err)
	}
	if signal.Task.ID == 0 || signal.Task.Status != "auto_queued" || signal.Task.AccountEmail != "person@example.com" || signal.Task.LastErrorCode != "" {
		t.Fatalf("signal response = %#v", signal)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("access_token")) || bytes.Contains(recorder.Body.Bytes(), []byte("proxy")) {
		t.Fatalf("signal response leaked sensitive field: %s", recorder.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/v0/management/token-recovery?fileName=physical+account.json&authIndex=7&accountEmail=person@example.com&provider=codex", nil)
	statusRequest.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	statusRecorder := httptest.NewRecorder()
	handler.Handle(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK || !bytes.Contains(statusRecorder.Body.Bytes(), []byte(`"auto_queued"`)) {
		t.Fatalf("status response = %d %s", statusRecorder.Code, statusRecorder.Body.String())
	}
}

func TestHandlerRejectsAccountIDAndRequiresPanelAuthorization(t *testing.T) {
	handler := newTestHandler(t)
	unauthorized := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/signals", bytes.NewReader([]byte(`{}`)))
	unauthorizedRecorder := httptest.NewRecorder()
	handler.Handle(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedRecorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/manual", bytes.NewReader([]byte(`{"fileName":"a.json","accountEmail":"person@example.com","provider":"codex","accountId":"must-not-be-used"}`)))
	request.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("accountId request status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	credential, err := security.NewAdminCredential(tokenRecoveryHandlerAdminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	recovery := tokenrecoverysvc.NewWithOptions(tokenrecoverysvc.Options{Tasks: st})
	return &Handler{App: &app.Context{
		Config:               config.Config{},
		AdminAuthService:     adminauthsvc.New(config.Config{}, st),
		TokenRecoveryService: recovery,
	}}
}
