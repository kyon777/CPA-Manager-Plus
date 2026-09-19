package tokenrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenacquisition"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestServiceAcquiresAndMergesExactPhysicalAuthFile(t *testing.T) {
	core := newRecoveryCore(t, []byte(`{
  "auth_index":"7",
  "email":"person@example.com",
  "access_token":"old-access",
  "refresh_token":"old-refresh",
  "id_token":"old-id",
  "account_id":"old-account",
  "note":"keep note",
  "priority":20000,
  "proxy_url":"http://proxy-user:proxy-pass@proxy.example:8080",
  "unknown":{"nested":true}
}`))
	coordinator := cpaauthfiles.NewMutationCoordinator()
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "PERSON@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id", ChatGPTAccountID: "new-account",
	}}
	acquirer.onAcquire = func(ctx context.Context, _ tokenacquisition.Request) error {
		release, err := coordinator.Acquire(ctx, "physical account.json")
		if err != nil {
			return err
		}
		release()
		return nil
	}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, coordinator, acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	completed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusSucceeded)
	if completed.LastErrorCode != "" {
		t.Fatalf("completed task = %#v", completed)
	}
	request := acquirer.lastRequest()
	if request.Email != "person@example.com" || request.HTTPProxy != "http://proxy-user:proxy-pass@proxy.example:8080" {
		t.Fatalf("token acquisition request = %#v", request)
	}
	if !acquirer.lockWasReleased() {
		t.Fatal("auth file lock was held while TokenAcquisition ran")
	}
	if core.uploadCount() != 1 {
		t.Fatalf("core upload count = %d, want 1", core.uploadCount())
	}
	merged := core.contents()
	for _, want := range []string{
		`"note":"keep note"`, `"priority":20000`, `"proxy_url":"http://proxy-user:proxy-pass@proxy.example:8080"`, `"unknown":{"nested":true}`,
		`"access_token":"new-access"`, `"refresh_token":"new-refresh"`, `"id_token":"new-id"`,
		`"chatgpt_account_id":"new-account"`, `"account_id":"new-account"`,
	} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged auth JSON missing %s: %s", want, merged)
		}
	}
}

func TestServiceAutomaticFailureDoesNotPostAgainUntilManualRequest(t *testing.T) {
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	acquirer := &fakeAcquirer{err: errors.New("external failure")}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)
	target := model.TokenRecoveryTarget{FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli()}

	task, err := service.SignalAutomatic(ctx, target)
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if acquirer.callCount() != 1 {
		t.Fatalf("automatic calls = %d, want 1", acquirer.callCount())
	}
	if _, err := service.SignalAutomatic(ctx, target); err != nil {
		t.Fatalf("repeated SignalAutomatic() error = %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if acquirer.callCount() != 1 {
		t.Fatalf("automatic retry calls = %d, want 1", acquirer.callCount())
	}

	acquirer.setResult(tokenacquisition.Result{Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id"})
	manual, err := service.RequestManual(ctx, target)
	if err != nil {
		t.Fatalf("RequestManual() error = %v", err)
	}
	waitForTaskStatus(t, ctx, service, manual.ID, model.TokenRecoveryStatusSucceeded)
	if acquirer.callCount() != 2 {
		t.Fatalf("manual retry calls = %d, want 2", acquirer.callCount())
	}
}

func TestServiceStoresStructuredTokenAcquisitionFailureReason(t *testing.T) {
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	tokenService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-admin-key" {
			t.Fatalf("TokenAcquisition API key = %q", r.Header.Get("X-Api-Key"))
		}
		switch r.URL.Path {
		case "/v1/tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"batch_id": "batch-failed"})
		case "/v1/batches/batch-failed":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"done": true,
				"jobs": []any{map[string]any{
					"email":  "person@example.com",
					"status": "error",
					"error": map[string]any{
						"code":    "mfa_failed",
						"message": "二次验证失败",
					},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer tokenService.Close()

	acquirer := tokenacquisition.New(tokenacquisition.Config{
		BaseURL:      tokenService.URL,
		APIKey:       "test-admin-key",
		HTTPClient:   tokenService.Client(),
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
	})
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if failed.LastErrorCode != "token_acquisition_failed" || failed.LastErrorMessage != "mfa_failed: 二次验证失败" {
		t.Fatalf("failed task = %#v", failed)
	}
}

type recoveryCore struct {
	server  *httptest.Server
	mu      sync.Mutex
	raw     []byte
	uploads int
}

func newRecoveryCore(t *testing.T, raw []byte) *recoveryCore {
	t.Helper()
	core := &recoveryCore{raw: append([]byte(nil), raw...)}
	core.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer core-management-key" {
			t.Fatalf("core authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			if r.URL.Query().Get("name") != "physical account.json" {
				t.Fatalf("download name = %q", r.URL.Query().Get("name"))
			}
			core.mu.Lock()
			_, _ = w.Write(core.raw)
			core.mu.Unlock()
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/auth-files":
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("upload file: %v", err)
			}
			defer file.Close()
			if header.Filename != "physical account.json" {
				t.Fatalf("upload filename = %q", header.Filename)
			}
			body, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("read upload: %v", err)
			}
			core.mu.Lock()
			core.raw = body
			core.uploads++
			core.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(core.server.Close)
	return core
}

func (c *recoveryCore) contents() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.raw)
}

func (c *recoveryCore) uploadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uploads
}

type fakeAcquirer struct {
	mu           sync.Mutex
	result       tokenacquisition.Result
	err          error
	calls        int
	last         tokenacquisition.Request
	onAcquire    func(context.Context, tokenacquisition.Request) error
	lockReleased bool
}

func (a *fakeAcquirer) Acquire(ctx context.Context, request tokenacquisition.Request) (tokenacquisition.Result, error) {
	a.mu.Lock()
	a.calls++
	a.last = request
	hook := a.onAcquire
	a.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, request); err != nil {
			return tokenacquisition.Result{}, err
		}
		a.mu.Lock()
		a.lockReleased = true
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result, a.err
}

func (a *fakeAcquirer) setResult(result tokenacquisition.Result) {
	a.mu.Lock()
	a.result = result
	a.err = nil
	a.mu.Unlock()
}

func (a *fakeAcquirer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *fakeAcquirer) lastRequest() tokenacquisition.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func (a *fakeAcquirer) lockWasReleased() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lockReleased
}

func newRecoveryService(t *testing.T, coreURL string, coordinator *cpaauthfiles.MutationCoordinator, acquirer tokenacquisition.Acquirer) (*Service, context.Context, context.CancelFunc) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	return NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: coreURL, ManagementKey: "core-management-key"}},
		AuthFiles:           cpaauthfiles.New(http.DefaultClient, time.Second),
		Acquirer:            acquirer,
		MutationCoordinator: coordinator,
		IdlePollInterval:    time.Millisecond,
	}), ctx, cancel
}

type staticSetupResolver struct {
	setup store.Setup
}

func (s staticSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return s.setup, true, nil
}

func waitForTaskStatus(t *testing.T, ctx context.Context, service *Service, id int64, want string) model.TokenRecoveryTask {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		task, ok, err := service.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if ok && task.Status == want {
			return task
		}
		select {
		case <-deadline.C:
			t.Fatalf("task %d status = %#v, want %q", id, task, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
