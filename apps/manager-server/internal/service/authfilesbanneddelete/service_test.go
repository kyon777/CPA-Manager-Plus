package authfilesbanneddelete

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestServiceDeletesEligiblePhysicalFilesWithOneInventoryFetch(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{
		{ID: "runtime-a", Name: "a.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com", AccountID: "account-a"},
		{ID: "runtime-b", Name: "b.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "b@example.com", AccountID: "account-b"},
	}}
	invalidator := &recordingInvalidator{}
	service := NewWithOptions(Options{
		SetupResolver: configuredSetupResolver{},
		Recovery: fakeRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
			"a.json:1": {Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned"},
			"b.json:2": {Status: model.TokenRecoveryStatusAutoFailedManualOnly, LastErrorMessage: "account_banned: disabled"},
		}},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		Invalidator:         invalidator,
	})

	result, err := service.Delete(context.Background(), []Target{
		{ClientKey: "a", FileName: "a.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountID: "account-a", AccountSnapshot: "a@example.com"},
		{ClientKey: "b", FileName: "b.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "CODEX", RuntimeID: "runtime-b", AccountID: "account-b", AccountSnapshot: "b@example.com"},
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if client.fetchCalls != 1 {
		t.Fatalf("Fetch() calls = %d, want 1", client.fetchCalls)
	}
	if got := client.deletedSelectors(); len(got) != 2 || got[0] != "runtime-a" || got[1] != "runtime-b" {
		t.Fatalf("deleted selectors = %#v, want runtime-a and runtime-b", got)
	}
	if invalidator.calls != 1 {
		t.Fatalf("invalidator calls = %d, want 1", invalidator.calls)
	}
	if result.Requested != 2 || len(result.Deleted) != 2 || len(result.Skipped) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceSkipsIncompleteSharedFileAndNonBannedRecovery(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{
		{ID: "runtime-a", Name: "shared.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com", AccountID: "account-a"},
		{ID: "runtime-b", Name: "shared.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "b@example.com", AccountID: "account-b"},
	}}
	invalidator := &recordingInvalidator{}
	service := NewWithOptions(Options{
		SetupResolver: configuredSetupResolver{},
		Recovery: fakeRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
			"shared.json:1": {Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned"},
			"shared.json:2": {Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "http_401"},
		}},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		Invalidator:         invalidator,
	})

	result, err := service.Delete(context.Background(), []Target{
		{ClientKey: "a", FileName: "shared.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountID: "account-a", AccountSnapshot: "a@example.com"},
		{ClientKey: "b", FileName: "shared.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "codex", RuntimeID: "runtime-b", AccountID: "account-b", AccountSnapshot: "b@example.com"},
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if client.fetchCalls != 1 {
		t.Fatalf("Fetch() calls = %d, want 1 after one eligible target", client.fetchCalls)
	}
	if got := client.deletedSelectors(); len(got) != 0 {
		t.Fatalf("deleted selectors = %#v, want none", got)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if result.Skipped[0].Code != "shared_file_incomplete" || result.Skipped[1].Code != "not_account_banned" {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
}

func TestServiceBoundsIndependentPhysicalDeletesWhileUsingOneInventoryFetch(t *testing.T) {
	files := make([]cpaauthfiles.File, 0, 5)
	tasks := make(map[string]model.TokenRecoveryTask, 5)
	targets := make([]Target, 0, 5)
	for index := 0; index < 5; index++ {
		name := fmt.Sprintf("credential-%d.json", index)
		authIndex := fmt.Sprintf("%d", index+1)
		email := fmt.Sprintf("credential-%d@example.com", index)
		runtimeID := fmt.Sprintf("runtime-%d", index)
		files = append(files, cpaauthfiles.File{
			ID: runtimeID, Name: name, AuthIndex: authIndex, Provider: "codex", AccountSnapshot: email,
		})
		tasks[name+":"+authIndex] = model.TokenRecoveryTask{
			Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned",
		}
		targets = append(targets, Target{
			ClientKey: name, FileName: name, AuthIndex: authIndex, AccountEmail: email,
			Provider: "codex", RuntimeID: runtimeID,
		})
	}
	client := newBlockingAuthFilesClient(files)
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
		Recovery:            fakeRecoveryLookup{tasks: tasks},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		Concurrency:         2,
	})

	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := service.Delete(context.Background(), targets)
		done <- struct {
			result Result
			err    error
		}{result, err}
	}()

	// Both workers must reach a CPA delete before either is released. A third
	// file may not start until one of these two physical-file mutations finishes.
	<-client.started
	<-client.started
	if active, maximum := client.activeCounts(); active != 2 || maximum != 2 {
		t.Fatalf("active=%d maximum=%d, want exactly configured concurrency 2", active, maximum)
	}
	close(client.release)
	completed := <-done
	if completed.err != nil {
		t.Fatalf("Delete() error = %v", completed.err)
	}
	if client.fetchCalls != 1 {
		t.Fatalf("Fetch() calls = %d, want one inventory fetch", client.fetchCalls)
	}
	if _, maximum := client.activeCounts(); maximum > 2 {
		t.Fatalf("maximum concurrent deletes = %d, want <= 2", maximum)
	}
	if len(completed.result.Deleted) != 5 || len(completed.result.Failed) != 0 {
		t.Fatalf("result = %#v", completed.result)
	}
}

func TestServiceDefaultsToEightIndependentPhysicalDeletes(t *testing.T) {
	files := make([]cpaauthfiles.File, 0, 8)
	tasks := make(map[string]model.TokenRecoveryTask, 8)
	targets := make([]Target, 0, 8)
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf("default-concurrency-%d.json", index)
		authIndex := fmt.Sprintf("%d", index+1)
		email := fmt.Sprintf("default-concurrency-%d@example.com", index)
		runtimeID := fmt.Sprintf("default-runtime-%d", index)
		files = append(files, cpaauthfiles.File{
			ID: runtimeID, Name: name, AuthIndex: authIndex, Provider: "codex", AccountSnapshot: email,
		})
		tasks[name+":"+authIndex] = model.TokenRecoveryTask{
			Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned",
		}
		targets = append(targets, Target{
			ClientKey: name, FileName: name, AuthIndex: authIndex, AccountEmail: email,
			Provider: "codex", RuntimeID: runtimeID,
		})
	}
	client := newBlockingAuthFilesClient(files)
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
		Recovery:            fakeRecoveryLookup{tasks: tasks},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := service.Delete(context.Background(), targets)
		done <- struct {
			result Result
			err    error
		}{result, err}
	}()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(client.release) })
	}
	defer release()

	for started := 0; started < 8; started++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatalf("started deletes = %d, want default concurrency 8", started)
		}
	}
	if active, maximum := client.activeCounts(); active != 8 || maximum != 8 {
		t.Fatalf("active=%d maximum=%d, want default concurrency 8", active, maximum)
	}

	release()
	select {
	case completed := <-done:
		if completed.err != nil {
			t.Fatalf("Delete() error = %v", completed.err)
		}
		if len(completed.result.Deleted) != 8 || len(completed.result.Failed) != 0 {
			t.Fatalf("result = %#v", completed.result)
		}
	case <-time.After(time.Second):
		t.Fatal("Delete() did not complete after releasing all delete workers")
	}
	if client.fetchCalls != 1 {
		t.Fatalf("Fetch() calls = %d, want one inventory fetch", client.fetchCalls)
	}
}

func TestServiceFallsBackToPhysicalSourceAfterPluginVirtualConflict(t *testing.T) {
	var getCalls int
	deletedSelectors := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-virtual", "name": "source.json", "auth_index": "auth-1",
				"provider": "codex", "account": "account@example.com",
			}})
		case "DELETE /v0/management/auth-files":
			selector := r.URL.Query().Get("name")
			deletedSelectors = append(deletedSelectors, selector)
			if selector == "runtime-virtual" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "plugin virtual auth cannot be modified directly; edit or delete the source auth file",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewWithOptions(Options{
		SetupResolver: fixedSetupResolver{setup: store.Setup{
			CPAUpstreamURL: server.URL,
			ManagementKey:  "management-key-sentinel",
		}},
		Recovery: fakeRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
			"source.json:auth-1": {
				Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned",
			},
		}},
		AuthFiles:           cpaauthfiles.New(server.Client()),
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	result, err := service.Delete(context.Background(), []Target{{
		ClientKey: "row-a", FileName: "source.json", AuthIndex: "auth-1",
		AccountEmail: "account@example.com", Provider: "codex", RuntimeID: "runtime-virtual",
	}})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if getCalls != 1 {
		t.Fatalf("GET auth-files calls = %d, want one locked inventory fetch", getCalls)
	}
	if want := []string{"runtime-virtual", "source.json"}; !reflect.DeepEqual(deletedSelectors, want) {
		t.Fatalf("deleted selectors = %#v, want %#v", deletedSelectors, want)
	}
	if len(result.Deleted) != 1 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceSkipsRecoveryTaskThatDoesNotExactlyMatchCurrentIdentity(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{{
		ID: "runtime-replacement", Name: "source.json", AuthIndex: "auth-1", Provider: "codex", AccountSnapshot: "replacement@example.com",
	}}}
	service := NewWithOptions(Options{
		SetupResolver: configuredSetupResolver{},
		Recovery: fakeRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
			"source.json:auth-1": {
				FileName: "source.json", AuthIndex: "auth-1", AccountEmail: "old@example.com", Provider: "codex",
				Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned",
			},
		}},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	result, err := service.Delete(context.Background(), []Target{{
		ClientKey: "row-a", FileName: "source.json", AuthIndex: "auth-1", AccountEmail: "replacement@example.com",
		Provider: "codex", RuntimeID: "runtime-replacement", AccountSnapshot: "replacement@example.com",
	}})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if client.fetchCalls != 0 {
		t.Fatalf("Fetch() calls = %d, want 0 when task identity does not match", client.fetchCalls)
	}
	if deleted := client.deletedSelectors(); len(deleted) != 0 {
		t.Fatalf("deleted selectors = %#v, want none", deleted)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Code != "recovery_identity_mismatch" {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceLocksCandidateFilesBeforeInventoryFetch(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{{
		ID: "runtime-a", Name: "a.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com",
	}}}
	coordinator := cpaauthfiles.NewMutationCoordinator()
	releaseHeldLock, err := coordinator.Acquire(context.Background(), "a.json")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	service := NewWithOptions(Options{
		SetupResolver: configuredSetupResolver{},
		Recovery: fakeRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
			"a.json:1": {Status: model.TokenRecoveryStatusManualFailedManualOnly, LastErrorCode: "account_banned"},
		}},
		AuthFiles:           client,
		MutationCoordinator: coordinator,
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.Delete(context.Background(), []Target{{
			ClientKey: "a", FileName: "a.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountSnapshot: "a@example.com",
		}})
	}()
	select {
	case <-done:
		t.Fatal("Delete() completed while candidate physical file remained locked")
	case <-time.After(30 * time.Millisecond):
	}
	if client.fetchCalls != 0 {
		t.Fatalf("Fetch() calls = %d, want 0 before the candidate lock is acquired", client.fetchCalls)
	}
	releaseHeldLock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Delete() did not complete after releasing candidate lock")
	}
	if client.fetchCalls != 1 {
		t.Fatalf("Fetch() calls = %d, want one after candidate lock release", client.fetchCalls)
	}
}

type configuredSetupResolver struct{}

func (configuredSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "http://cpa.test", ManagementKey: "management-key-sentinel"}, true, nil
}

type fixedSetupResolver struct{ setup store.Setup }

func (r fixedSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, true, nil
}

type fakeRecoveryLookup struct {
	tasks map[string]model.TokenRecoveryTask
}

func (r fakeRecoveryLookup) Get(_ context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	task, ok := r.tasks[target.FileName+":"+target.AuthIndex]
	if !ok {
		return task, false, nil
	}
	// Existing fixtures focus on recovery terminal-state behavior. Fill identity
	// fields exactly as a real repository row would carry them unless a test is
	// deliberately asserting a mismatch.
	if task.FileName == "" {
		task.FileName = target.FileName
	}
	if task.AuthIndex == "" {
		task.AuthIndex = target.AuthIndex
	}
	if task.AccountEmail == "" {
		task.AccountEmail = target.AccountEmail
	}
	if task.Provider == "" {
		task.Provider = target.Provider
	}
	return task, ok, nil
}

type recordingAuthFilesClient struct {
	files      []cpaauthfiles.File
	fetchCalls int
	deleted    []string
	mu         sync.Mutex
}

func (c *recordingAuthFilesClient) Fetch(context.Context, string, string) ([]cpaauthfiles.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchCalls++
	return append([]cpaauthfiles.File(nil), c.files...), nil
}

func (c *recordingAuthFilesClient) Delete(_ context.Context, _ string, _ string, selector string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, selector)
	return nil
}

func (c *recordingAuthFilesClient) deletedSelectors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.deleted...)
}

type recordingInvalidator struct {
	calls int
	mu    sync.Mutex
}

type blockingAuthFilesClient struct {
	files      []cpaauthfiles.File
	fetchCalls int
	started    chan string
	release    chan struct{}
	active     int
	maximum    int
	mu         sync.Mutex
}

func newBlockingAuthFilesClient(files []cpaauthfiles.File) *blockingAuthFilesClient {
	return &blockingAuthFilesClient{
		files:   append([]cpaauthfiles.File(nil), files...),
		started: make(chan string, len(files)),
		release: make(chan struct{}),
	}
}

func (c *blockingAuthFilesClient) Fetch(context.Context, string, string) ([]cpaauthfiles.File, error) {
	c.mu.Lock()
	c.fetchCalls++
	c.mu.Unlock()
	return append([]cpaauthfiles.File(nil), c.files...), nil
}

func (c *blockingAuthFilesClient) Delete(_ context.Context, _ string, _ string, selector string) error {
	c.mu.Lock()
	c.active++
	if c.active > c.maximum {
		c.maximum = c.active
	}
	c.mu.Unlock()
	c.started <- selector
	<-c.release
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return nil
}

func (c *blockingAuthFilesClient) activeCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active, c.maximum
}

func (i *recordingInvalidator) InvalidateAuthFileInventory(context.Context) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
}
