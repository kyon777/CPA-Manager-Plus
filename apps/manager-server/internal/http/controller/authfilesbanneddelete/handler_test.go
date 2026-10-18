package authfilesbanneddelete

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
	authfilesbanneddeletesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesbanneddelete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const testAdminKey = "cpamp_banned_delete_handler"

func TestHandlerDeletesValidBatchAndReturnsSafeSummary(t *testing.T) {
	service := &recordingService{result: authfilesbanneddeletesvc.Result{
		Requested: 1,
		Deleted:   []authfilesbanneddeletesvc.Item{{ClientKey: "row-a", FileName: "a.json", PhysicalName: "a.json"}},
		Skipped:   []authfilesbanneddeletesvc.Item{},
		Failed:    []authfilesbanneddeletesvc.Item{},
	}}
	handler := newTestHandler(t, service)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/delete-account-banned-batch", bytes.NewBufferString(`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","accountEmail":"person@example.com","provider":"codex","runtimeId":"runtime-a","accountId":"account-a","accountSnapshot":"person@example.com"}]}`))
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(service.targets) != 1 || service.targets[0].ClientKey != "row-a" {
		t.Fatalf("service targets = %#v", service.targets)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("management-key")) || bytes.Contains(recorder.Body.Bytes(), []byte("access_token")) {
		t.Fatalf("response leaked sensitive content: %s", recorder.Body.String())
	}
}

func TestHandlerRejectsMalformedOrUnsupportedBatch(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	cases := []string{
		`{"targets":[],"extra":true}`,
		`{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex"}]}`,
		`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","provider":"codex"}]}`,
		`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","provider":"xai"}]}`,
		`{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","provider":"codex","accessToken":"must-not-pass"}]}`,
	}
	for _, body := range cases {
		request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/delete-account-banned-batch", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+testAdminKey)
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHandlerRequiresPanelAuthorization(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/delete-account-banned-batch", bytes.NewBufferString(`{"targets":[]}`))
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

type recordingService struct {
	targets []authfilesbanneddeletesvc.Target
	result  authfilesbanneddeletesvc.Result
	err     error
}

func (s *recordingService) Delete(_ context.Context, targets []authfilesbanneddeletesvc.Target) (authfilesbanneddeletesvc.Result, error) {
	s.targets = append([]authfilesbanneddeletesvc.Target(nil), targets...)
	return s.result, s.err
}

func newTestHandler(t *testing.T, service deleteService) *Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	credential, err := security.NewAdminCredential(testAdminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	return &Handler{
		App:     &app.Context{AdminAuthService: adminauthsvc.New(config.Config{}, st)},
		Service: service,
	}
}

func TestDecodeTargetsRejectsMoreThanMaximum(t *testing.T) {
	targets := make([]map[string]string, authfilesbanneddeletesvc.MaxTargets+1)
	for index := range targets {
		targets[index] = map[string]string{"clientKey": "row", "fileName": "a.json", "authIndex": "7", "provider": "codex"}
	}
	body, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := decodeTargets(bytes.NewReader(body)); err == nil {
		t.Fatal("decodeTargets() error = nil, want limit rejection")
	}
}
