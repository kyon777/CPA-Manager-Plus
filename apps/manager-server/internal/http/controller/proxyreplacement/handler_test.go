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

func TestCredentialExitIPChecksRequireAdministratorAuthorization(t *testing.T) {
	handler, _, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/credential-exit-ip-checks", bytes.NewBufferString(`{
		"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a"}]
	}`))
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.credentialExitIPCalls != 0 {
		t.Fatalf("unauthorized request invoked credential exit-IP check %d times", service.credentialExitIPCalls)
	}
}

func TestCredentialExitIPChecksAcceptOnlyStrictCredentialLocators(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	service.credentialExitIPBatch = model.CredentialProxyExitIPCheckBatch{Results: []model.CredentialProxyExitIPCheckResult{{
		ClientKey: "row-a",
		Outcome:   model.ProxyExitIPOutcomeHealthy,
		ExitIP:    "203.0.113.7",
		LatencyMS: 31,
	}}}
	request := httptest.NewRequest(http.MethodPost, basePath+"/credential-exit-ip-checks", bytes.NewBufferString(`{
		"targets":[{
			"clientKey":"row-a","fileName":"a.json","provider":"codex",
			"runtimeId":"runtime-a","authIndex":"7","accountEmail":"a@example.com"
		}]
	}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.credentialExitIPCalls != 1 || len(service.credentialExitIPTargets) != 1 {
		t.Fatalf("credential exit-IP input=%#v calls=%d", service.credentialExitIPTargets, service.credentialExitIPCalls)
	}
	got := service.credentialExitIPTargets[0]
	if got.ClientKey != "row-a" || got.FileName != "a.json" || got.Provider != "codex" || got.RuntimeID != "runtime-a" {
		t.Fatalf("strict locator=%#v", got)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("proxyUrl")) || bytes.Contains(recorder.Body.Bytes(), []byte("proxy.example")) {
		t.Fatalf("response leaked raw proxy URL: %s", recorder.Body.String())
	}
}

func TestCredentialExitIPChecksRejectUnsafeOrAmbiguousInputs(t *testing.T) {
	validTarget := `{"clientKey":"row-a","fileName":"a.json","provider":"codex","runtimeId":"runtime-a"}`
	for name, payload := range map[string]string{
		"raw proxy URL":        `{"targets":[` + validTarget[:len(validTarget)-1] + `,"proxyUrl":"http://user:secret@proxy.example:8080"}]}`,
		"arbitrary target URL": `{"targets":[` + validTarget[:len(validTarget)-1] + `,"targetUrl":"https://attacker.example/"}]}`,
		"inspection identity":  `{"targets":[` + validTarget[:len(validTarget)-1] + `,"inspectionRunId":4}]}`,
		"duplicate client key": `{"targets":[` + validTarget + `,` + validTarget + `]}`,
		"blank client key":     `{"targets":[{"clientKey":" ","fileName":"a.json","provider":"codex"}]}`,
		"no strict locator":    `{"targets":[{"clientKey":"row-a","fileName":"a.json","provider":"codex"}]}`,
		"unknown root field":   `{"targets":[` + validTarget + `],"proxyUrl":"http://user:secret@proxy.example:8080"}`,
	} {
		t.Run(name, func(t *testing.T) {
			handler, adminKey, service := newProxyReplacementHandler(t)
			request := httptest.NewRequest(http.MethodPost, basePath+"/credential-exit-ip-checks", bytes.NewBufferString(payload))
			request.Header.Set("Authorization", "Bearer "+adminKey)
			recorder := httptest.NewRecorder()
			handler.Handle(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if service.credentialExitIPCalls != 0 {
				t.Fatalf("unsafe input invoked credential exit-IP check %d times", service.credentialExitIPCalls)
			}
		})
	}
}

func TestVerifyExitIPRequiresAdministratorAuthorization(t *testing.T) {
	handler, _, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/proxy-pool/verify-exit-ip", bytes.NewBufferString(`{"fingerprints":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`))
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.verifyCalls != 0 {
		t.Fatalf("unauthorized request invoked verification %d times", service.verifyCalls)
	}
}

func TestVerifyExitIPRejectsRawProxyURLWithoutCallingService(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	request := httptest.NewRequest(http.MethodPost, basePath+"/proxy-pool/verify-exit-ip", bytes.NewBufferString(`{"proxyUrl":"http://user:secret@proxy.example:8080"}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.verifyCalls != 0 {
		t.Fatalf("raw proxy URL invoked verification %d times", service.verifyCalls)
	}
}

func TestVerifyExitIPReturnsFingerprintOnlyResults(t *testing.T) {
	handler, adminKey, service := newProxyReplacementHandler(t)
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	service.verifyBatch = model.ProxyExitIPCheckBatch{Results: []model.ProxyExitIPCheckResult{{
		Fingerprint: fingerprint,
		Outcome:     model.ProxyExitIPOutcomeHealthy,
		ExitIP:      "203.0.113.7",
		LatencyMS:   31,
	}}}
	request := httptest.NewRequest(http.MethodPost, basePath+"/proxy-pool/verify-exit-ip", bytes.NewBufferString(`{"fingerprints":["`+fingerprint+`"]}`))
	request.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.verifyCalls != 1 || len(service.verifyInput) != 1 || service.verifyInput[0] != fingerprint {
		t.Fatalf("verification input=%#v calls=%d", service.verifyInput, service.verifyCalls)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("proxy.example")) || bytes.Contains(recorder.Body.Bytes(), []byte("user:secret")) {
		t.Fatalf("response leaked raw proxy URL: %s", recorder.Body.String())
	}
}

func TestVerifyExitIPRejectsDuplicateOrInvalidFingerprintInput(t *testing.T) {
	for name, payload := range map[string]string{
		"duplicate": `{"fingerprints":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`,
		"raw URL":   `{"fingerprints":["http://user:secret@proxy.example:8080"]}`,
		"empty":     `{"fingerprints":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			handler, adminKey, service := newProxyReplacementHandler(t)
			request := httptest.NewRequest(http.MethodPost, basePath+"/proxy-pool/verify-exit-ip", bytes.NewBufferString(payload))
			request.Header.Set("Authorization", "Bearer "+adminKey)
			recorder := httptest.NewRecorder()
			handler.Handle(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if service.verifyCalls != 0 {
				t.Fatalf("invalid input invoked verification %d times", service.verifyCalls)
			}
		})
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
	previewCalls            int
	getCalls                int
	restoreSuspectCalls     int
	verifyCalls             int
	verifyInput             []string
	verifyBatch             model.ProxyExitIPCheckBatch
	credentialExitIPCalls   int
	credentialExitIPTargets []model.ProxyReplacementTarget
	credentialExitIPBatch   model.CredentialProxyExitIPCheckBatch
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
func (s *fakeProxyReplacementService) VerifyProxyPoolExitIPs(_ context.Context, fingerprints []string) (model.ProxyExitIPCheckBatch, error) {
	s.verifyCalls++
	s.verifyInput = append([]string(nil), fingerprints...)
	return s.verifyBatch, nil
}

func (s *fakeProxyReplacementService) VerifyCredentialExitIPs(_ context.Context, targets []model.ProxyReplacementTarget) (model.CredentialProxyExitIPCheckBatch, error) {
	s.credentialExitIPCalls++
	s.credentialExitIPTargets = append([]model.ProxyReplacementTarget(nil), targets...)
	return s.credentialExitIPBatch, nil
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
