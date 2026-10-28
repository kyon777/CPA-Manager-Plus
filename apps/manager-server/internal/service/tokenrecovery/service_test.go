package tokenrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestServiceRecoversByUniqueEmailWhenStoredAuthIndexIsStale(t *testing.T) {
	core := newRecoveryCore(t, []byte(`{
  "auth_index":"current-index",
  "email":"person@example.com",
  "access_token":"old-access",
  "refresh_token":"old-refresh",
  "id_token":"old-id",
  "note":"keep note",
  "proxy_url":"http://proxy.example:8080"
}`))
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "stale-index", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusSucceeded)
	if acquirer.callCount() != 1 {
		t.Fatalf("token acquisition calls = %d, want 1", acquirer.callCount())
	}
	merged := core.contents()
	for _, want := range []string{
		`"auth_index":"current-index"`, `"access_token":"new-access"`, `"refresh_token":"new-refresh"`, `"id_token":"new-id"`,
		`"note":"keep note"`, `"proxy_url":"http://proxy.example:8080"`,
	} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged auth JSON missing %s: %s", want, merged)
		}
	}
}

func TestServiceRejectsStaleAuthIndexFallbackWhenEmailIsAmbiguous(t *testing.T) {
	core := newRecoveryCore(t, []byte(`[
  {"auth_index":"current-a","email":"person@example.com","access_token":"old-a","refresh_token":"old-a","id_token":"old-a"},
  {"auth_index":"current-b","email":"person@example.com","access_token":"old-b","refresh_token":"old-b","id_token":"old-b"}
]`))
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "stale-index", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if failed.LastErrorCode != "credential_ambiguous" {
		t.Fatalf("failure code = %q, want credential_ambiguous", failed.LastErrorCode)
	}
	if acquirer.callCount() != 0 {
		t.Fatalf("token acquisition calls = %d, want 0", acquirer.callCount())
	}
	if core.uploadCount() != 0 {
		t.Fatalf("core upload count = %d, want 0", core.uploadCount())
	}
}

func TestServiceRecoversUniqueArrayEmailWhenStoredAuthIndexIsStale(t *testing.T) {
	core := newRecoveryCore(t, []byte(`[
  {"auth_index":"current-other","email":"other@example.com","access_token":"other-access","refresh_token":"other-refresh","id_token":"other-id"},
  {"auth_index":"current-target","email":"person@example.com","access_token":"old-access","refresh_token":"old-refresh","id_token":"old-id","note":"keep target note"}
]`))
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "stale-index", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusSucceeded)
	if acquirer.callCount() != 1 || core.uploadCount() != 1 {
		t.Fatalf("acquire/uploads = %d/%d, want 1/1", acquirer.callCount(), core.uploadCount())
	}
	merged := core.contents()
	for _, want := range []string{
		`"auth_index":"current-other"`, `"email":"other@example.com"`, `"access_token":"other-access"`,
		`"auth_index":"current-target"`, `"access_token":"new-access"`, `"refresh_token":"new-refresh"`, `"id_token":"new-id"`, `"note":"keep target note"`,
	} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged array auth JSON missing %s: %s", want, merged)
		}
	}
}

func TestServiceDoesNotFallbackWhenStoredAuthIndexMatchesRecordWithoutEmail(t *testing.T) {
	core := newRecoveryCore(t, []byte(`[
  {"auth_index":"task-index","access_token":"malformed-access","refresh_token":"malformed-refresh","id_token":"malformed-id"},
  {"auth_index":"current-target","email":"person@example.com","access_token":"old-access","refresh_token":"old-refresh","id_token":"old-id"}
]`))
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	service, ctx, cancel := newRecoveryService(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "task-index", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if failed.LastErrorCode != "credential_not_found" {
		t.Fatalf("failure code = %q, want credential_not_found", failed.LastErrorCode)
	}
	if acquirer.callCount() != 0 || core.uploadCount() != 0 {
		t.Fatalf("acquire/uploads = %d/%d, want 0/0", acquirer.callCount(), core.uploadCount())
	}
	if !strings.Contains(core.contents(), `"access_token":"old-access"`) {
		t.Fatalf("target record was unexpectedly changed: %s", core.contents())
	}
}

func TestServiceAutomaticFailureDoesNotPostAgainDuringDailyCooldown(t *testing.T) {
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

func TestServiceStoresRedactedTokenAcquisitionTransportFailure(t *testing.T) {
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	acquirer := &fakeAcquirer{err: fmt.Errorf("dial proxy-user:proxy-password@proxy.example:8080: %w", tokenacquisition.ErrTransportFailed)}
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
	if got, want := failed.LastErrorCode, "token_acquisition_transport_failed"; got != want {
		t.Fatalf("failure code = %q, want %q", got, want)
	}
	if !strings.Contains(failed.LastErrorMessage, "网络请求失败") {
		t.Fatalf("failure message = %q, want actionable transport reason", failed.LastErrorMessage)
	}
	if strings.Contains(failed.LastErrorMessage, "proxy-password") || strings.Contains(failed.LastErrorMessage, "proxy.example") {
		t.Fatalf("failure message leaked transport detail: %q", failed.LastErrorMessage)
	}
}
func TestServiceRunsManualRecoveriesWithConfiguredWorkerConcurrency(t *testing.T) {
	const workerCount = 80
	const acquisitionConcurrency = 40
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-concurrency.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authFiles := &concurrentRecoveryAuthFiles{rawByFile: map[string][]byte{}}
	acquirer := newBlockingRecoveryAcquirer(workerCount)
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		AuthFiles:           authFiles,
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
		WorkerCount:         workerCount,
	})

	taskIDs := make([]int64, 0, workerCount)
	for index := 0; index < workerCount; index++ {
		email := fmt.Sprintf("worker-%02d@example.com", index)
		task, requestErr := service.RequestManual(ctx, model.TokenRecoveryTarget{
			FileName:     fmt.Sprintf("worker-%02d.json", index),
			AuthIndex:    fmt.Sprintf("auth-%02d", index),
			AccountEmail: email,
			Provider:     "codex",
			ObservedAtMS: time.Now().UnixMilli(),
		})
		if requestErr != nil {
			t.Fatalf("queue manual task %d: %v", index, requestErr)
		}
		taskIDs = append(taskIDs, task.ID)
	}

	service.Start(ctx)
	acquirer.waitForEntries(t, acquisitionConcurrency)
	acquirer.assertNoAdditionalEntry(t, 300*time.Millisecond)
	if got := acquirer.maxConcurrent(); got != acquisitionConcurrency {
		t.Fatalf("maximum simultaneous acquisitions = %d, want %d", got, acquisitionConcurrency)
	}
	acquirer.releaseAll()
	for _, taskID := range taskIDs {
		waitForTaskStatus(t, ctx, service, taskID, model.TokenRecoveryStatusSucceeded)
	}
}

func TestServiceRunsAutomaticRecoveriesWithConfiguredWorkerConcurrency(t *testing.T) {
	const workerCount = 40
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-auto-concurrency.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authFiles := &concurrentRecoveryAuthFiles{rawByFile: map[string][]byte{}}
	acquirer := newBlockingRecoveryAcquirer(workerCount)
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		AuthFiles:           authFiles,
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
		WorkerCount:         workerCount,
	})

	taskIDs := make([]int64, 0, workerCount)
	for index := 0; index < workerCount; index++ {
		email := fmt.Sprintf("automatic-%02d@example.com", index)
		task, signalErr := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
			FileName:     fmt.Sprintf("automatic-%02d.json", index),
			AuthIndex:    fmt.Sprintf("auth-%02d", index),
			AccountEmail: email,
			Provider:     "codex",
			ObservedAtMS: time.Now().UnixMilli(),
		})
		if signalErr != nil {
			t.Fatalf("queue automatic task %d: %v", index, signalErr)
		}
		taskIDs = append(taskIDs, task.ID)
	}

	service.Start(ctx)
	acquirer.waitForEntries(t, workerCount)
	if got := acquirer.maxConcurrent(); got != workerCount {
		t.Fatalf("maximum simultaneous automatic acquisitions = %d, want %d", got, workerCount)
	}
	acquirer.releaseAll()
	for _, taskID := range taskIDs {
		waitForTaskStatus(t, ctx, service, taskID, model.TokenRecoveryStatusSucceeded)
	}
}

type recoveryCore struct {
	server  *httptest.Server
	mu      sync.Mutex
	raw     []byte
	uploads int
}

type concurrentRecoveryAuthFiles struct {
	mu        sync.Mutex
	rawByFile map[string][]byte
}

func (f *concurrentRecoveryAuthFiles) Download(_ context.Context, _ string, _ string, fileName string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if raw, ok := f.rawByFile[fileName]; ok {
		return append([]byte(nil), raw...), nil
	}
	email := strings.TrimSuffix(fileName, ".json") + "@example.com"
	raw, err := json.Marshal(map[string]string{
		"email":         email,
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"id_token":      "old-id",
	})
	if err != nil {
		return nil, err
	}
	f.rawByFile[fileName] = raw
	return append([]byte(nil), raw...), nil
}

func (f *concurrentRecoveryAuthFiles) Upload(_ context.Context, _ string, _ string, fileName string, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rawByFile[fileName] = append([]byte(nil), raw...)
	return nil
}

type blockingRecoveryAcquirer struct {
	entered chan struct{}
	release chan struct{}

	mu        sync.Mutex
	current   int
	maxActive int
	once      sync.Once
}

func newBlockingRecoveryAcquirer(capacity int) *blockingRecoveryAcquirer {
	return &blockingRecoveryAcquirer{
		entered: make(chan struct{}, capacity),
		release: make(chan struct{}),
	}
}

func (a *blockingRecoveryAcquirer) Acquire(ctx context.Context, request tokenacquisition.Request) (tokenacquisition.Result, error) {
	a.mu.Lock()
	a.current++
	if a.current > a.maxActive {
		a.maxActive = a.current
	}
	a.mu.Unlock()

	select {
	case a.entered <- struct{}{}:
	case <-ctx.Done():
		a.leave()
		return tokenacquisition.Result{}, ctx.Err()
	}

	select {
	case <-a.release:
		a.leave()
		return tokenacquisition.Result{
			Email:        request.Email,
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			IDToken:      "new-id",
		}, nil
	case <-ctx.Done():
		a.leave()
		return tokenacquisition.Result{}, ctx.Err()
	}
}

func (a *blockingRecoveryAcquirer) leave() {
	a.mu.Lock()
	a.current--
	a.mu.Unlock()
}

func (a *blockingRecoveryAcquirer) waitForEntries(t *testing.T, count int) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for index := 0; index < count; index++ {
		select {
		case <-a.entered:
		case <-timeout.C:
			t.Fatalf("only %d/%d recovery acquisitions entered", index, count)
		}
	}
}

func (a *blockingRecoveryAcquirer) assertNoAdditionalEntry(t *testing.T, duration time.Duration) {
	t.Helper()
	select {
	case <-a.entered:
		t.Fatal("an acquisition entered beyond the configured external capacity")
	case <-time.After(duration):
	}
}

func (a *blockingRecoveryAcquirer) maxConcurrent() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxActive
}

func (a *blockingRecoveryAcquirer) releaseAll() {
	a.once.Do(func() { close(a.release) })
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

func TestIsTerminalAccountBanned(t *testing.T) {
	tests := []struct {
		name string
		task model.TokenRecoveryTask
		want bool
	}{
		{
			name: "automatic terminal code",
			task: model.TokenRecoveryTask{Status: model.TokenRecoveryStatusAutoFailedManualOnly, LastErrorCode: " account_banned "},
			want: true,
		},
		{
			name: "manual terminal message",
			task: model.TokenRecoveryTask{Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorMessage: "ACCOUNT_BANNED: account is disabled"},
			want: true,
		},
		{
			name: "normal reauth error",
			task: model.TokenRecoveryTask{Status: model.TokenRecoveryStatusAutoFailedManualOnly, LastErrorCode: "http_401"},
			want: false,
		},
		{
			name: "queued ban is not terminal",
			task: model.TokenRecoveryTask{Status: model.TokenRecoveryStatusManualQueued, LastErrorCode: "account_banned"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTerminalAccountBanned(tc.task); got != tc.want {
				t.Fatalf("IsTerminalAccountBanned(%#v) = %v, want %v", tc.task, got, tc.want)
			}
		})
	}
}

func TestServiceReportsCPAAuthFileUploadTimeout(t *testing.T) {
	files := &timeoutOnUploadRecoveryAuthFiles{raw: []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old-access","refresh_token":"old-refresh","id_token":"old-id"}`)}
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-timeout.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		AuthFiles:           files,
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
	})
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if failed.LastErrorCode != "cpa_auth_file_timeout" {
		t.Fatalf("failure code = %q, want cpa_auth_file_timeout", failed.LastErrorCode)
	}
	if !strings.Contains(failed.LastErrorMessage, "CPA Core") {
		t.Fatalf("failure message = %q, want actionable CPA Core timeout reason", failed.LastErrorMessage)
	}
}

func TestServiceStoresCPAAuthFileUploadFailureStage(t *testing.T) {
	files := &uploadFailureRecoveryAuthFiles{raw: []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old-access","refresh_token":"old-refresh","id_token":"old-id"}`)}
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{
		Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id",
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-upload-failure.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		AuthFiles:           files,
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
	})
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if got, want := failed.LastErrorCode, "cpa_auth_file_upload_failed"; got != want {
		t.Fatalf("failure code = %q, want %q", got, want)
	}
	if got, want := failed.LastErrorMessage, "CPA Core 凭证写入失败：HTTP 502"; got != want {
		t.Fatalf("failure message = %q, want %q", got, want)
	}
}
func TestDefaultCPAAuthFileOperationTimeout(t *testing.T) {
	if got, want := DefaultCPAAuthFileOperationTimeout, 4*time.Minute; got != want {
		t.Fatalf("DefaultCPAAuthFileOperationTimeout = %s, want %s", got, want)
	}
}

func TestRecoveryFailureMessageDistinguishesCPAAuthFileTimeoutStages(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		want      string
	}{
		{name: "initial read", operation: "initial_read", want: "CPA Core 凭证读取超时：当前读取繁忙，本次未提交更新"},
		{name: "upload", operation: "upload", want: "CPA Core 凭证写入超时：当前写入繁忙，写入结果待确认"},
		{name: "verify", operation: "verify", want: "CPA Core 凭证复核超时：写入可能已完成，请刷新后核实"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &cpaAuthFileOperationTimeoutError{operation: tc.operation, err: context.DeadlineExceeded}
			if got := recoveryFailureMessage(err); got != tc.want {
				t.Fatalf("recoveryFailureMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecoveryFailureClassifiesTokenAcquisitionTransport(t *testing.T) {
	if got, want := recoveryFailureCode(tokenacquisition.ErrTransportFailed), "token_acquisition_transport_failed"; got != want {
		t.Fatalf("recoveryFailureCode() = %q, want %q", got, want)
	}
	if got, want := recoveryFailureMessage(tokenacquisition.ErrTransportFailed), "Token Acquisition 网络请求失败：服务或链路暂时不可用，请稍后手动重试"; got != want {
		t.Fatalf("recoveryFailureMessage() = %q, want %q", got, want)
	}
}

func TestRecoveryFailureClassifiesTokenAcquisitionTimeout(t *testing.T) {
	if got, want := recoveryFailureCode(tokenacquisition.ErrTimedOut), "token_acquisition_timeout"; got != want {
		t.Fatalf("recoveryFailureCode() = %q, want %q", got, want)
	}
	if got, want := recoveryFailureMessage(tokenacquisition.ErrTimedOut), "Token Acquisition 请求超时：外部接口或代理校验繁忙，请稍后手动重试"; got != want {
		t.Fatalf("recoveryFailureMessage() = %q, want %q", got, want)
	}
}

func TestRecoveryFailureClassifiesCPAAuthFileOperationStages(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		status    int
		wantCode  string
		wantText  string
	}{
		{
			name:      "initial read",
			operation: "initial_read",
			status:    http.StatusServiceUnavailable,
			wantCode:  "cpa_auth_file_read_failed",
			wantText:  "CPA Core 凭证读取失败：HTTP 503",
		},
		{
			name:      "upload",
			operation: "upload",
			status:    http.StatusBadGateway,
			wantCode:  "cpa_auth_file_upload_failed",
			wantText:  "CPA Core 凭证写入失败：HTTP 502",
		},
		{
			name:      "verify transport",
			operation: "verify",
			status:    0,
			wantCode:  "cpa_auth_file_verify_failed",
			wantText:  "CPA Core 凭证复核失败：写入可能已完成，请刷新后核实",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyCPAAuthFileOperationError(tc.operation, &cpaauthfiles.OperationError{
				Operation:  cpaauthfiles.OperationDownload,
				StatusCode: tc.status,
			})
			if got := recoveryFailureCode(err); got != tc.wantCode {
				t.Fatalf("recoveryFailureCode() = %q, want %q", got, tc.wantCode)
			}
			if got := recoveryFailureMessage(err); got != tc.wantText {
				t.Fatalf("recoveryFailureMessage() = %q, want %q", got, tc.wantText)
			}
		})
	}
}

func TestRecommendedTokenAcquisitionWorkerCount(t *testing.T) {
	if got, want := RecommendedTokenAcquisitionWorkerCount, 80; got != want {
		t.Fatalf("RecommendedTokenAcquisitionWorkerCount = %d, want %d", got, want)
	}
}

func TestDefaultAcquisitionConcurrency(t *testing.T) {
	if got, want := defaultAcquisitionConcurrency, 40; got != want {
		t.Fatalf("defaultAcquisitionConcurrency = %d, want %d", got, want)
	}
}

func TestDefaultIdlePollIntervalAvoidsSubsecondDatabasePolling(t *testing.T) {
	service := NewWithOptions(Options{})
	if got, want := service.idlePollInterval, 5*time.Second; got != want {
		t.Fatalf("default idle poll interval = %s, want %s", got, want)
	}
}

func TestServiceLimitsConcurrentCPAAuthFileWritesWithoutLimitingAcquisition(t *testing.T) {
	const workers = 12
	const writeConcurrency = 8
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-write-limit.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	files := newBlockingWriteRecoveryAuthFiles(workers)
	acquirer := newBlockingRecoveryAcquirer(workers)
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		AuthFiles:           files,
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
		WorkerCount:         workers,
		WriteConcurrency:    workers,
	})

	taskIDs := make([]int64, 0, workers)
	for index := 0; index < workers; index++ {
		email := fmt.Sprintf("writer-%02d@example.com", index)
		task, requestErr := service.RequestManual(ctx, model.TokenRecoveryTarget{
			FileName: fmt.Sprintf("writer-%02d.json", index), AuthIndex: fmt.Sprintf("auth-%02d", index), AccountEmail: email, Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
		})
		if requestErr != nil {
			t.Fatalf("RequestManual(%d): %v", index, requestErr)
		}
		taskIDs = append(taskIDs, task.ID)
	}
	service.Start(ctx)
	acquirer.waitForEntries(t, workers)
	if got := acquirer.maxConcurrent(); got != workers {
		t.Fatalf("maximum simultaneous acquisitions = %d, want %d", got, workers)
	}
	acquirer.releaseAll()

	files.waitForUploads(t, writeConcurrency)
	select {
	case <-files.entered:
		t.Fatal("a ninth CPA auth-file write started before one of the first eight completed")
	case <-time.After(120 * time.Millisecond):
	}
	files.releaseAll()
	for _, taskID := range taskIDs {
		waitForTaskStatus(t, ctx, service, taskID, model.TokenRecoveryStatusSucceeded)
	}
}

type uploadFailureRecoveryAuthFiles struct {
	raw []byte
}

func (f *uploadFailureRecoveryAuthFiles) Download(_ context.Context, _ string, _ string, _ string) ([]byte, error) {
	return append([]byte(nil), f.raw...), nil
}

func (f *uploadFailureRecoveryAuthFiles) Upload(_ context.Context, _ string, _ string, _ string, _ []byte) error {
	return &cpaauthfiles.OperationError{Operation: cpaauthfiles.OperationUpload, StatusCode: http.StatusBadGateway}
}

type timeoutOnUploadRecoveryAuthFiles struct {
	raw []byte
}

func (f *timeoutOnUploadRecoveryAuthFiles) Download(_ context.Context, _ string, _ string, _ string) ([]byte, error) {
	return append([]byte(nil), f.raw...), nil
}

func (f *timeoutOnUploadRecoveryAuthFiles) Upload(_ context.Context, _ string, _ string, _ string, _ []byte) error {
	return fmt.Errorf("POST /v0/management/auth-files: %w", context.DeadlineExceeded)
}

type blockingWriteRecoveryAuthFiles struct {
	mu        sync.Mutex
	rawByFile map[string][]byte
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newBlockingWriteRecoveryAuthFiles(capacity int) *blockingWriteRecoveryAuthFiles {
	return &blockingWriteRecoveryAuthFiles{
		rawByFile: make(map[string][]byte),
		entered:   make(chan struct{}, capacity),
		release:   make(chan struct{}),
	}
}

func (f *blockingWriteRecoveryAuthFiles) Download(_ context.Context, _ string, _ string, fileName string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if raw, ok := f.rawByFile[fileName]; ok {
		return append([]byte(nil), raw...), nil
	}
	email := strings.TrimSuffix(fileName, ".json") + "@example.com"
	raw, err := json.Marshal(map[string]string{
		"email":         email,
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"id_token":      "old-id",
	})
	if err != nil {
		return nil, err
	}
	f.rawByFile[fileName] = raw
	return append([]byte(nil), raw...), nil
}

func (f *blockingWriteRecoveryAuthFiles) Upload(_ context.Context, _ string, _ string, fileName string, raw []byte) error {
	select {
	case f.entered <- struct{}{}:
	default:
	}
	<-f.release
	f.mu.Lock()
	f.rawByFile[fileName] = append([]byte(nil), raw...)
	f.mu.Unlock()
	return nil
}

func (f *blockingWriteRecoveryAuthFiles) waitForUploads(t *testing.T, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for index := 0; index < count; index++ {
		select {
		case <-f.entered:
		case <-deadline.C:
			t.Fatalf("only %d/%d CPA auth-file writes entered", index, count)
		}
	}
}

func (f *blockingWriteRecoveryAuthFiles) releaseAll() {
	f.once.Do(func() { close(f.release) })
}

func TestServiceRetriesBusyFailureTerminalWriteWithoutReacquiringToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-terminal-retry.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tasks := &terminalWriteBusyTaskStore{Store: st, failBusyRemaining: 2}
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	acquirer := &fakeAcquirer{err: errors.New("external failure")}
	service, ctx, cancel := newRecoveryServiceWithTaskStore(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer, tasks)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if got, want := failed.LastErrorCode, "recovery_failed"; got != want {
		t.Fatalf("failure code = %q, want %q", got, want)
	}
	if got, want := tasks.failCallCount(), 3; got != want {
		t.Fatalf("FailTokenRecovery() calls = %d, want %d", got, want)
	}
	if got, want := acquirer.callCount(), 1; got != want {
		t.Fatalf("token acquisition calls = %d, want %d", got, want)
	}
}

func TestServiceRetriesBusySuccessTerminalWriteWithoutReacquiringToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-terminal-retry.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tasks := &terminalWriteBusyTaskStore{Store: st, completeBusyRemaining: 2}
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	acquirer := &fakeAcquirer{result: tokenacquisition.Result{Email: "person@example.com", AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "new-id"}}
	service, ctx, cancel := newRecoveryServiceWithTaskStore(t, core.server.URL, cpaauthfiles.NewMutationCoordinator(), acquirer, tasks)
	defer cancel()
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusSucceeded)
	if got, want := tasks.completeCallCount(), 3; got != want {
		t.Fatalf("CompleteTokenRecovery() calls = %d, want %d", got, want)
	}
	if got, want := acquirer.callCount(), 1; got != want {
		t.Fatalf("token acquisition calls = %d, want %d", got, want)
	}
	if got, want := core.uploadCount(), 1; got != want {
		t.Fatalf("CPA auth-file uploads = %d, want %d", got, want)
	}
}

type terminalWriteBusyTaskStore struct {
	*store.Store

	mu                    sync.Mutex
	completeBusyRemaining int
	failBusyRemaining     int
	completeCalls         int
	failCalls             int
}

func (s *terminalWriteBusyTaskStore) CompleteTokenRecovery(ctx context.Context, id int64) (store.TokenRecoveryTask, error) {
	s.mu.Lock()
	s.completeCalls++
	busy := s.completeBusyRemaining > 0
	if busy {
		s.completeBusyRemaining--
	}
	s.mu.Unlock()
	if busy {
		return store.TokenRecoveryTask{}, errors.New("database is locked (SQLITE_BUSY)")
	}
	return s.Store.CompleteTokenRecovery(ctx, id)
}

func (s *terminalWriteBusyTaskStore) FailTokenRecovery(ctx context.Context, id int64, code, message string) (store.TokenRecoveryTask, error) {
	s.mu.Lock()
	s.failCalls++
	busy := s.failBusyRemaining > 0
	if busy {
		s.failBusyRemaining--
	}
	s.mu.Unlock()
	if busy {
		return store.TokenRecoveryTask{}, errors.New("database is locked (SQLITE_BUSY)")
	}
	return s.Store.FailTokenRecovery(ctx, id, code, message)
}

func (s *terminalWriteBusyTaskStore) completeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeCalls
}

func (s *terminalWriteBusyTaskStore) failCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failCalls
}

func newRecoveryServiceWithTaskStore(t *testing.T, coreURL string, coordinator *cpaauthfiles.MutationCoordinator, acquirer tokenacquisition.Acquirer, tasks TaskStore) (*Service, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	return NewWithOptions(Options{
		Tasks:               tasks,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: coreURL, ManagementKey: "core-management-key"}},
		AuthFiles:           cpaauthfiles.New(http.DefaultClient, time.Second),
		Acquirer:            acquirer,
		MutationCoordinator: coordinator,
		IdlePollInterval:    time.Millisecond,
	}), ctx, cancel
}

func TestServiceMarksExceededRecoveryRuntimeTerminalWithoutSecondAcquisition(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-runtime-timeout.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	core := newRecoveryCore(t, []byte(`{"auth_index":"7","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`))
	acquirer := &deadlineBlockingRecoveryAcquirer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewWithOptions(Options{
		Tasks:               st,
		SetupResolver:       staticSetupResolver{setup: store.Setup{CPAUpstreamURL: core.server.URL, ManagementKey: "core-management-key"}},
		AuthFiles:           cpaauthfiles.New(http.DefaultClient, time.Second),
		Acquirer:            acquirer,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		IdlePollInterval:    time.Millisecond,
		TaskTimeout:         25 * time.Millisecond,
	})
	service.Start(ctx)

	task, err := service.SignalAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "physical account.json", AuthIndex: "7", AccountEmail: "person@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalAutomatic() error = %v", err)
	}
	failed := waitForTaskStatus(t, ctx, service, task.ID, model.TokenRecoveryStatusAutoFailedManualOnly)
	if got, want := failed.LastErrorCode, "recovery_timeout"; got != want {
		t.Fatalf("failure code = %q, want %q", got, want)
	}
	if got, want := failed.LastErrorMessage, "\u6062\u590d\u4efb\u52a1\u8d85\u8fc7\u6700\u5927\u6267\u884c\u65f6\u95f4\uff0c\u672a\u81ea\u52a8\u91cd\u8bd5\uff0c\u8bf7\u4eba\u5de5\u6838\u67e5"; got != want {
		t.Fatalf("failure message = %q, want %q", got, want)
	}
	if got, want := acquirer.callCount(), 1; got != want {
		t.Fatalf("token acquisition calls = %d, want %d", got, want)
	}
	if got := core.uploadCount(); got != 0 {
		t.Fatalf("CPA auth-file uploads = %d, want 0", got)
	}
}

type deadlineBlockingRecoveryAcquirer struct {
	mu    sync.Mutex
	calls int
}

func (a *deadlineBlockingRecoveryAcquirer) Acquire(ctx context.Context, _ tokenacquisition.Request) (tokenacquisition.Result, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	<-ctx.Done()
	return tokenacquisition.Result{}, ctx.Err()
}

func (a *deadlineBlockingRecoveryAcquirer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func TestServiceReconcilesExpiredRunningRecoveryTasks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "token-recovery-stale-reconcile.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	stale, err := st.SignalTokenRecoveryAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "stale.json", AuthIndex: "1", AccountEmail: "stale@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalTokenRecoveryAutomatic(stale) = %v", err)
	}
	if _, found, err := st.ClaimNextEligibleTokenRecovery(ctx, true); err != nil || !found {
		t.Fatalf("ClaimNextEligibleTokenRecovery(stale) found=%t err=%v", found, err)
	}
	service := NewWithOptions(Options{Tasks: st, TaskTimeout: 200 * time.Millisecond})
	time.Sleep(220 * time.Millisecond)
	fresh, err := st.SignalTokenRecoveryAutomatic(ctx, model.TokenRecoveryTarget{
		FileName: "fresh.json", AuthIndex: "2", AccountEmail: "fresh@example.com", Provider: "codex", ObservedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SignalTokenRecoveryAutomatic(fresh) = %v", err)
	}
	if _, found, err := st.ClaimNextEligibleTokenRecovery(ctx, true); err != nil || !found {
		t.Fatalf("ClaimNextEligibleTokenRecovery(fresh) found=%t err=%v", found, err)
	}
	changed, err := service.reconcileStaleRunningTasks(ctx)
	if err != nil {
		t.Fatalf("reconcileStaleRunningTasks() = %v", err)
	}
	if got, want := changed, int64(1); got != want {
		t.Fatalf("reconcileStaleRunningTasks() changed %d tasks, want %d", got, want)
	}
	staleTask, found, err := st.GetTokenRecoveryByID(ctx, stale.ID)
	if err != nil || !found {
		t.Fatalf("GetTokenRecoveryByID(stale) found=%t err=%v", found, err)
	}
	if staleTask.Status != model.TokenRecoveryStatusAutoFailedManualOnly || staleTask.LastErrorCode != "recovery_timeout" {
		t.Fatalf("stale task = %#v", staleTask)
	}
	freshTask, found, err := st.GetTokenRecoveryByID(ctx, fresh.ID)
	if err != nil || !found {
		t.Fatalf("GetTokenRecoveryByID(fresh) found=%t err=%v", found, err)
	}
	if freshTask.Status != model.TokenRecoveryStatusAutoRunning || freshTask.LastErrorCode != "" {
		t.Fatalf("fresh task = %#v", freshTask)
	}
}
