package authfilesexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	authfilesexportsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesexport"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const testAdminKey = "cpamp_auth_json_export_handler"

func TestHandlerExportsValidBatchWithNoStore(t *testing.T) {
	service := &recordingService{result: authfilesexportsvc.Result{
		Requested: 1,
		Exported:  1,
		Records:   []json.RawMessage{json.RawMessage(`{"email":"person@example.com","refresh_token":"rt-for-copy"}`)},
		Failed:    []authfilesexportsvc.FailedItem{},
	}}
	handler := newTestHandler(t, service)
	request := authorizedRequest(http.MethodPost, exportSelectedPath, `{"targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","accountEmail":"Person@Example.com","provider":"codex","runtimeId":"runtime-a"}]}`)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
	if len(service.targets) != 1 || service.targets[0].AccountEmail != "person@example.com" {
		t.Fatalf("service targets=%#v", service.targets)
	}
}

func TestHandlerRejectsTokenBearingExportPayloadWithoutEchoingSecret(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	secret := "must-not-cross-browser-boundary"
	request := authorizedRequest(http.MethodPost, exportSelectedPath, `{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a","access_token":"`+secret+`"}]}`)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
}

func TestHandlerRequiresAuthorizationAndStrictlyRejectsInvalidRequests(t *testing.T) {
	handler := newTestHandler(t, &recordingService{})
	unauthorized := httptest.NewRequest(http.MethodPost, exportSelectedPath, bytes.NewBufferString(`{"targets":[]}`))
	unauthorizedRecorder := httptest.NewRecorder()
	handler.Handle(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized || unauthorizedRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthorized status=%d headers=%#v", unauthorizedRecorder.Code, unauthorizedRecorder.Header())
	}

	cases := []struct {
		method string
		body   string
	}{
		{method: http.MethodGet, body: ``},
		{method: http.MethodPost, body: `{"targets":[],"unexpected":true}`},
		{method: http.MethodPost, body: `{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex"}]}`},
		{method: http.MethodPost, body: `{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a"},{"clientKey":"row-a","fileName":"b.json","provider":"codex","runtimeId":"runtime-b"}]}`},
		{method: http.MethodPost, body: `{"targets":[]}{"targets":[]}`},
	}
	for _, testCase := range cases {
		request := authorizedRequest(testCase.method, exportSelectedPath, testCase.body)
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		want := http.StatusBadRequest
		if testCase.method == http.MethodGet {
			want = http.StatusMethodNotAllowed
		}
		if recorder.Code != want || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method=%s body=%s status=%d want=%d headers=%#v", testCase.method, testCase.body, recorder.Code, want, recorder.Header())
		}
	}
}

func TestHandlerMapsAggregateExportLimitToSafe413(t *testing.T) {
	handler := newTestHandler(t, &recordingService{err: authfilesexportsvc.ErrExportLimitExceeded})
	request := authorizedRequest(http.MethodPost, exportSelectedPath, `{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a"}]}`)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%#v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["code"] != "export_limit_exceeded" || len(body) != 1 {
		t.Fatalf("body=%s err=%v", recorder.Body.String(), err)
	}
}

type recordingService struct {
	targets []authfilesexportsvc.Target
	result  authfilesexportsvc.Result
	err     error
}

func (s *recordingService) Export(_ context.Context, targets []authfilesexportsvc.Target) (authfilesexportsvc.Result, error) {
	s.targets = append([]authfilesexportsvc.Target(nil), targets...)
	return s.result, s.err
}

func newTestHandler(t *testing.T, service exportService) *Handler {
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

func authorizedRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	return request
}

func TestDecodeTargetsRejectsMoreThanMaximum(t *testing.T) {
	targets := make([]map[string]string, authfilesexportsvc.MaxTargets+1)
	for index := range targets {
		targets[index] = map[string]string{"clientKey": "row-" + string(rune(index)), "fileName": "a.json", "authIndex": "7", "provider": "codex"}
	}
	body, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := decodeTargets(bytes.NewReader(body)); !errors.Is(err, errInvalidRequest) {
		t.Fatalf("decodeTargets error=%v", err)
	}
}
