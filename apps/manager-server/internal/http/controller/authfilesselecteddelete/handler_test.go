package authfilesselecteddelete

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	authfilesselecteddeletesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesselecteddelete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const testAdminKey = "cpamp_selected_delete_handler"

func TestHandlerDeletesValidBatch(t *testing.T) {
	service := &recordingService{result: authfilesselecteddeletesvc.Result{
		Requested: 1,
		Deleted: []authfilesselecteddeletesvc.Item{{
			FileName: "a.json", PhysicalName: "a.json",
		}},
		Skipped: []authfilesselecteddeletesvc.Item{},
		Failed:  []authfilesselecteddeletesvc.Item{},
	}}
	handler := newTestHandler(t, service)
	request := httptest.NewRequest(http.MethodPost, deleteSelectedBatchPath, bytes.NewBufferString(`{"targets":[{"fileName":"a.json","provider":"codex","authIndex":"7","runtimeId":"runtime-a","accountId":"workspace-a","accountSnapshot":"person@example.com"}]}`))
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(service.targets) != 1 || service.targets[0].FileName != "a.json" || service.targets[0].Provider != "codex" {
		t.Fatalf("service targets = %#v", service.targets)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"deleted"`)) || bytes.Contains(recorder.Body.Bytes(), []byte("management-key")) {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestHandlerRejectsMalformedUnsupportedOrOversizedBatch(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	cases := []string{
		`{"targets":[],"extra":true}`,
		`{"targets":[{"fileName":"a.json","provider":"codex"}]}`,
		`{"targets":[{"fileName":"a.json","provider":"codex","authIndex":"7","accessToken":"must-not-pass"}]}`,
		`{"targets":[{"fileName":"a.json","provider":"" ,"authIndex":"7"}]}`,
	}
	for _, body := range cases {
		request := httptest.NewRequest(http.MethodPost, deleteSelectedBatchPath, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+testAdminKey)
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}

	oversized := `{"targets":[{"fileName":"` + strings.Repeat("a", maxRequestBytes) + `","provider":"codex","authIndex":"1"}]}`
	request := httptest.NewRequest(http.MethodPost, deleteSelectedBatchPath, bytes.NewBufferString(oversized))
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d response=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRequiresPanelAuthorizationAndPost(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	unauthorized := httptest.NewRequest(http.MethodPost, deleteSelectedBatchPath, bytes.NewBufferString(`{"targets":[]}`))
	unauthorizedRecorder := httptest.NewRecorder()
	handler.Handle(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedRecorder.Code)
	}

	getRequest := httptest.NewRequest(http.MethodGet, deleteSelectedBatchPath, nil)
	getRequest.Header.Set("Authorization", "Bearer "+testAdminKey)
	getRecorder := httptest.NewRecorder()
	handler.Handle(getRecorder, getRequest)
	if getRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
}

func TestDecodeTargetsRejectsMoreThanMaximum(t *testing.T) {
	targets := make([]map[string]string, authfilesselecteddeletesvc.MaxTargets+1)
	for index := range targets {
		targets[index] = map[string]string{"fileName": "a.json", "provider": "codex", "authIndex": "7"}
	}
	body, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := decodeTargets(bytes.NewReader(body)); err == nil {
		t.Fatal("decodeTargets() error = nil, want target cap rejection")
	}
}

type recordingService struct {
	targets []authfilesselecteddeletesvc.Target
	result  authfilesselecteddeletesvc.Result
	err     error
}

func (s *recordingService) Delete(_ context.Context, targets []authfilesselecteddeletesvc.Target) (authfilesselecteddeletesvc.Result, error) {
	s.targets = append([]authfilesselecteddeletesvc.Target(nil), targets...)
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
