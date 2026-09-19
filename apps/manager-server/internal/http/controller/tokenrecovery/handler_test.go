package tokenrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
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

func TestHandlerRejectsOversizedTargetBodyEvenWhenJSONPrefixIsValid(t *testing.T) {
	handler := newTestHandler(t)
	body := `{"fileName":"a.json","accountEmail":"person@example.com","provider":"codex"}` + strings.Repeat(" ", maxTargetRequestBytes)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/signals", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized target status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerBatchManualIsolatesInvalidTarget(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/manual/batch", bytes.NewBufferString(`{
  "targets":[
    {"clientKey":"valid","fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex"},
    {"clientKey":"invalid","fileName":"b.json","accountEmail":"other@example.com","provider":"not-codex"}
  ]
}`))
	request.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			ClientKey string `json:"clientKey"`
			Task      *struct {
				Status string `json:"status"`
			} `json:"task"`
			ErrorCode string `json:"errorCode"`
		} `json:"items"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 2 {
		t.Fatalf("response items = %#v", response.Items)
	}
	if response.Items[0].ClientKey != "valid" || response.Items[0].Task == nil || response.Items[0].Task.Status != "manual_queued" || response.Items[0].ErrorCode != "" {
		t.Fatalf("valid batch item = %#v", response.Items[0])
	}
	if response.Items[1].ClientKey != "invalid" || response.Items[1].Task != nil || response.Items[1].ErrorCode != "invalid_target" {
		t.Fatalf("invalid batch item = %#v", response.Items[1])
	}
}

func TestHandlerBatchQueryReturnsExistingAndNullTask(t *testing.T) {
	handler := newTestHandler(t)
	_, err := handler.App.TokenRecoveryService.SignalAutomatic(context.Background(), model.TokenRecoveryTarget{
		FileName: "a.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex",
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/query", bytes.NewBufferString(`{
  "targets":[
    {"clientKey":"existing","fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex"},
    {"clientKey":"missing","fileName":"b.json","authIndex":"9","accountEmail":"missing@example.com","provider":"codex"}
  ]
}`))
	request.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			ClientKey string          `json:"clientKey"`
			Task      json.RawMessage `json:"task"`
			ErrorCode string          `json:"errorCode"`
		} `json:"items"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 2 {
		t.Fatalf("response items = %#v", response.Items)
	}
	if response.Items[0].ClientKey != "existing" || string(response.Items[0].Task) == "null" || response.Items[0].ErrorCode != "" {
		t.Fatalf("existing query item = %#v", response.Items[0])
	}
	if response.Items[1].ClientKey != "missing" || string(response.Items[1].Task) != "null" || response.Items[1].ErrorCode != "" {
		t.Fatalf("missing query item = %#v", response.Items[1])
	}
}

func TestHandlerBatchRejectsExtraFieldsAndMoreThanOneHundredTargets(t *testing.T) {
	handler := newTestHandler(t)
	extraField := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/query", bytes.NewBufferString(`{"targets":[],"accountId":"must-not-be-accepted"}`))
	extraField.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	extraFieldRecorder := httptest.NewRecorder()
	handler.Handle(extraFieldRecorder, extraField)
	if extraFieldRecorder.Code != http.StatusBadRequest {
		t.Fatalf("extra root field status = %d body=%s", extraFieldRecorder.Code, extraFieldRecorder.Body.String())
	}
	nestedExtraField := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/query", bytes.NewBufferString(`{"targets":[{"clientKey":"row-a","fileName":"a.json","accountEmail":"person@example.com","provider":"codex","accountId":"must-not-be-accepted"}]}`))
	nestedExtraField.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	nestedExtraFieldRecorder := httptest.NewRecorder()
	handler.Handle(nestedExtraFieldRecorder, nestedExtraField)
	if nestedExtraFieldRecorder.Code != http.StatusBadRequest {
		t.Fatalf("extra target field status = %d body=%s", nestedExtraFieldRecorder.Code, nestedExtraFieldRecorder.Body.String())
	}

	targets := make([]map[string]string, 101)
	for index := range targets {
		targets[index] = map[string]string{
			"clientKey":    fmt.Sprintf("row-%d", index),
			"fileName":     "a.json",
			"accountEmail": "person@example.com",
			"provider":     "codex",
		}
	}
	body, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		t.Fatalf("marshal targets: %v", err)
	}
	overLimit := httptest.NewRequest(http.MethodPost, "/v0/management/token-recovery/manual/batch", bytes.NewReader(body))
	overLimit.Header.Set("Authorization", "Bearer "+tokenRecoveryHandlerAdminKey)
	overLimitRecorder := httptest.NewRecorder()
	handler.Handle(overLimitRecorder, overLimit)
	if overLimitRecorder.Code != http.StatusBadRequest {
		t.Fatalf("over-limit status = %d body=%s", overLimitRecorder.Code, overLimitRecorder.Body.String())
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
