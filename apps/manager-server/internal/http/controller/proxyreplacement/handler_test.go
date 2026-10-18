package proxyreplacement

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestPreviewRejectsUnexpectedFieldsAndDoesNotWriteCPA(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/previews", bytes.NewBufferString(`{
        "source":"inspection","mode":"pool","targets":[{"clientKey":"row-a","fileName":"a.json","authIndex":"7","provider":"codex"}],"priority":1
    }`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.previewCalls != 0 {
		t.Fatalf("unexpected preview CPA/task write calls=%d", service.previewCalls)
	}
}

func TestGetTaskValidatesPaginationBeforeService(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodGet, basePath+"/tasks/17?page=0&page_size=101", nil)
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.getCalls != 0 {
		t.Fatalf("invalid pagination invoked task lookup %d times", service.getCalls)
	}
}

func TestRestoreSuspectPoolEntriesRequiresAdminAndReturnsRestoredCount(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/proxy-pool/restore-suspects", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.restoreSuspectCalls != 1 {
		t.Fatalf("restore suspect calls=%d", service.restoreSuspectCalls)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"restoredCount":3`)) {
		t.Fatalf("response=%s", recorder.Body.String())
	}
}

func TestInspectionSignalForwardsStrictTransportEvidence(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/inspection-signals", bytes.NewBufferString(`{
		"proxyUrl":"http://user:password@proxy.example:8080/",
		"errorKind":"request_error",
		"errorDetail":"context deadline exceeded"
	}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.signalCalls != 1 {
		t.Fatalf("signal calls=%d", service.signalCalls)
	}
	if service.signal.ProxyURL != "http://user:password@proxy.example:8080/" {
		t.Fatalf("unexpected proxy URL forwarding: %#v", service.signal)
	}
}

func TestSafeHandlerErrorRedactsURLEmbeddedInsideStructuredError(t *testing.T) {
	err := safeHandlerError(errors.New(`upstream rejected {"proxy_url":"http://user:password@proxy.example:8080/path"}`))
	if got := err.Error(); bytes.Contains([]byte(got), []byte("user:password")) || bytes.Contains([]byte(got), []byte("proxy.example")) {
		t.Fatalf("proxy URL leaked into handler error: %q", got)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("[redacted-url]")) {
		t.Fatalf("redacted marker missing: %q", err.Error())
	}
}

type fakeProxyReplacementService struct {
	previewCalls        int
	getCalls            int
	signalCalls         int
	signal              model.ProxyFailureEvidence
	restoreSuspectCalls int
}

func (s *fakeProxyReplacementService) CreatePreview(context.Context, model.ProxyReplacementPreviewRequest) (model.ProxyReplacementTaskPage, error) {
	s.previewCalls++
	return model.ProxyReplacementTaskPage{}, nil
}
func (s *fakeProxyReplacementService) Confirm(context.Context, int64) (model.ProxyReplacementTask, error) {
	return model.ProxyReplacementTask{}, nil
}
func (s *fakeProxyReplacementService) GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error) {
	s.getCalls++
	return model.ProxyReplacementTaskPage{}, nil
}
func (s *fakeProxyReplacementService) ListProxyPool(context.Context) ([]model.ProxyPoolState, error) {
	return nil, nil
}
func (s *fakeProxyReplacementService) RestoreProxyPoolEntry(context.Context, string) (model.ProxyPoolState, error) {
	return model.ProxyPoolState{}, nil
}
func (s *fakeProxyReplacementService) RestoreSuspectProxyPoolEntries(context.Context) (int, error) {
	s.restoreSuspectCalls++
	return 3, nil
}
func (s *fakeProxyReplacementService) ReportInspectionSignal(_ context.Context, evidence model.ProxyFailureEvidence) (bool, error) {
	s.signalCalls++
	s.signal = evidence
	return true, nil
}

func newProxyReplacementHandler(t *testing.T) (*Handler, string, *fakeProxyReplacementService) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const adminKey = "proxy-replacement-handler-admin"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	service := &fakeProxyReplacementService{}
	return &Handler{
		App:     &app.Context{Config: config.Config{}, AdminAuthService: adminauthsvc.New(config.Config{}, st)},
		Service: service,
	}, adminKey, service
}
