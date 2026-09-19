package credentialruntime

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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	credentialruntimesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialruntime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const credentialRuntimeHandlerAdminKey = "cpamp_credential_runtime_test"

func TestHandlerRequiresPanelAuthorization(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/credential-runtime-metadata", bytes.NewBufferString(`{"targets":[]}`))
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestHandlerProjectsSafeCredentialRuntimeMetadata(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/credential-runtime-metadata", bytes.NewBufferString(`{
  "targets":[{
    "clientKey":"row-a",
    "fileName":"a.json",
    "authIndex":"1",
    "accountEmail":"person@example.com",
    "provider":"codex"
  }]
}`))
	request.Header.Set("Authorization", "Bearer "+credentialRuntimeHandlerAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []credentialruntimesvc.Item `json:"items"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ClientKey != "row-a" || response.Items[0].ProxyURL != "socks5://proxy.example:1080" || response.Items[0].ErrorCode != "" {
		t.Fatalf("response items = %#v", response.Items)
	}
	for _, forbidden := range []string{"access_token", "hidden-access-token", "core-management-key"} {
		if bytes.Contains(recorder.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("metadata response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestHandlerRejectsUnknownFieldsAndMoreThanOneHundredTargets(t *testing.T) {
	handler := newTestHandler(t)
	unknownField := httptest.NewRequest(http.MethodPost, "/v0/management/credential-runtime-metadata", bytes.NewBufferString(`{"targets":[],"rawJson":"must-not-be-accepted"}`))
	unknownField.Header.Set("Authorization", "Bearer "+credentialRuntimeHandlerAdminKey)
	unknownFieldRecorder := httptest.NewRecorder()
	handler.Handle(unknownFieldRecorder, unknownField)
	if unknownFieldRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d body=%s", unknownFieldRecorder.Code, unknownFieldRecorder.Body.String())
	}

	targets := make([]map[string]string, 101)
	for index := range targets {
		targets[index] = map[string]string{
			"clientKey":    "row-" + string(rune('a'+index)),
			"fileName":     "a.json",
			"accountEmail": "person@example.com",
			"provider":     "codex",
		}
	}
	body, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		t.Fatalf("marshal oversized targets: %v", err)
	}
	overLimit := httptest.NewRequest(http.MethodPost, "/v0/management/credential-runtime-metadata", bytes.NewReader(body))
	overLimit.Header.Set("Authorization", "Bearer "+credentialRuntimeHandlerAdminKey)
	overLimitRecorder := httptest.NewRecorder()
	handler.Handle(overLimitRecorder, overLimit)
	if overLimitRecorder.Code != http.StatusBadRequest {
		t.Fatalf("over-limit status = %d body=%s", overLimitRecorder.Code, overLimitRecorder.Body.String())
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "credential-runtime-handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	credential, err := security.NewAdminCredential(credentialRuntimeHandlerAdminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	service := credentialruntimesvc.NewWithOptions(credentialruntimesvc.Options{
		SetupResolver: handlerSetupResolver{},
		AuthFiles: handlerDownloader{raw: []byte(`{
  "auth_index":"1",
  "email":"person@example.com",
  "proxy_url":"socks5://proxy.example:1080",
  "access_token":"hidden-access-token"
}`)},
		RecoveryLookup: noTaskRecoveryLookup{},
	})
	return &Handler{App: &app.Context{
		Config:                   config.Config{},
		AdminAuthService:         adminauthsvc.New(config.Config{}, st),
		CredentialRuntimeService: service,
	}}
}

type handlerSetupResolver struct{}

func (handlerSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "http://core.example", ManagementKey: "core-management-key"}, true, nil
}

type handlerDownloader struct {
	raw []byte
}

func (d handlerDownloader) Download(context.Context, string, string, string) ([]byte, error) {
	return append([]byte(nil), d.raw...), nil
}

type noTaskRecoveryLookup struct{}

func (noTaskRecoveryLookup) Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	return model.TokenRecoveryTask{}, false, nil
}
