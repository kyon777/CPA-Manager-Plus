package authfilesselecteddelete

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestServiceDeletesVerifiedSourcesWithOneInventoryFetch(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{
		selectedDeleteFile("a.json", "runtime-a", "1", "a@example.test", "workspace-a"),
		selectedDeleteFile("b.json", "runtime-b", "2", "b@example.test", "workspace-b"),
	}}
	invalidator := &recordingInvalidator{}
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
		Invalidator:         invalidator,
	})

	result, err := service.Delete(context.Background(), []Target{
		selectedDeleteTarget("a.json", "runtime-a", "1", "a@example.test", "workspace-a"),
		selectedDeleteTarget("b.json", "runtime-b", "2", "b@example.test", "workspace-b"),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := client.fetchCallCount(), 1; got != want {
		t.Fatalf("Fetch() calls = %d, want %d", got, want)
	}
	if got, want := client.deletedSelectors(), []string{"runtime-a", "runtime-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted selectors = %#v, want %#v", got, want)
	}
	if got, want := invalidator.callCount(), 1; got != want {
		t.Fatalf("invalidator calls = %d, want %d", got, want)
	}
	if result.Requested != 2 || len(result.Deleted) != 2 || len(result.Skipped) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceSkipsIncompleteSharedSource(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{
		selectedDeleteFile("shared.json", "runtime-a", "1", "a@example.test", "workspace-a"),
		selectedDeleteFile("shared.json", "runtime-b", "2", "b@example.test", "workspace-b"),
	}}
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	result, err := service.Delete(context.Background(), []Target{
		selectedDeleteTarget("shared.json", "runtime-a", "1", "a@example.test", "workspace-a"),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := client.fetchCallCount(), 1; got != want {
		t.Fatalf("Fetch() calls = %d, want %d", got, want)
	}
	if deleted := client.deletedSelectors(); len(deleted) != 0 {
		t.Fatalf("deleted selectors = %#v, want none", deleted)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Code != "shared_file_incomplete" {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceCapsIndependentPhysicalDeletesAtEightWorkers(t *testing.T) {
	files := make([]cpaauthfiles.File, 0, MaxConcurrency+1)
	targets := make([]Target, 0, MaxConcurrency+1)
	for index := 0; index < MaxConcurrency+1; index++ {
		name := fmt.Sprintf("credential-%d.json", index)
		runtimeID := fmt.Sprintf("runtime-%d", index)
		authIndex := fmt.Sprintf("%d", index+1)
		email := fmt.Sprintf("credential-%d@example.test", index)
		workspace := fmt.Sprintf("workspace-%d", index)
		files = append(files, selectedDeleteFile(name, runtimeID, authIndex, email, workspace))
		targets = append(targets, selectedDeleteTarget(name, runtimeID, authIndex, email, workspace))
	}
	client := newBlockingAuthFilesClient(files)
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
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
		}{result: result, err: err}
	}()

	for count := 0; count < MaxConcurrency; count++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatalf("started worker %d/%d", count+1, MaxConcurrency)
		}
	}
	select {
	case extra := <-client.started:
		t.Fatalf("started a ninth delete %q before one worker completed", extra)
	case <-time.After(40 * time.Millisecond):
	}
	if active, maximum := client.activeCounts(); active != MaxConcurrency || maximum != MaxConcurrency {
		t.Fatalf("active=%d maximum=%d, want %d", active, maximum, MaxConcurrency)
	}
	close(client.release)
	completed := <-done
	if completed.err != nil {
		t.Fatalf("Delete() error = %v", completed.err)
	}
	if got, want := client.fetchCallCount(), 1; got != want {
		t.Fatalf("Fetch() calls = %d, want %d", got, want)
	}
	if got, want := len(completed.result.Deleted), MaxConcurrency+1; got != want {
		t.Fatalf("deleted outcomes = %d, want %d", got, want)
	}
}

func TestServiceSkipsStaleIdentityWithoutDeleting(t *testing.T) {
	client := &recordingAuthFilesClient{files: []cpaauthfiles.File{
		selectedDeleteFile("source.json", "runtime-current", "1", "current@example.test", "workspace-current"),
	}}
	service := NewWithOptions(Options{
		SetupResolver:       configuredSetupResolver{},
		AuthFiles:           client,
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	result, err := service.Delete(context.Background(), []Target{
		selectedDeleteTarget("source.json", "runtime-replaced", "1", "current@example.test", "workspace-current"),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if deleted := client.deletedSelectors(); len(deleted) != 0 {
		t.Fatalf("deleted selectors = %#v, want none", deleted)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Code != "identity_changed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceUsesVerifiedPhysicalSourceAfterPluginVirtualConflict(t *testing.T) {
	var fetchCalls int
	deletedSelectors := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			fetchCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-virtual", "name": "source.json", "auth_index": "auth-1",
				"provider": "codex", "account": "account@example.test",
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
		AuthFiles:           cpaauthfiles.New(server.Client()),
		MutationCoordinator: cpaauthfiles.NewMutationCoordinator(),
	})

	result, err := service.Delete(context.Background(), []Target{
		selectedDeleteTarget("source.json", "runtime-virtual", "auth-1", "account@example.test", ""),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := fetchCalls, 1; got != want {
		t.Fatalf("GET auth-files calls = %d, want %d", got, want)
	}
	if want := []string{"runtime-virtual", "source.json"}; !reflect.DeepEqual(deletedSelectors, want) {
		t.Fatalf("deleted selectors = %#v, want %#v", deletedSelectors, want)
	}
	if len(result.Deleted) != 1 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceRejectsMoreThanMaxTargetsBeforeCPAAccess(t *testing.T) {
	client := &recordingAuthFilesClient{}
	service := NewWithOptions(Options{
		SetupResolver: configuredSetupResolver{},
		AuthFiles:     client,
	})
	targets := make([]Target, MaxTargets+1)
	for index := range targets {
		targets[index] = selectedDeleteTarget(fmt.Sprintf("%d.json", index), fmt.Sprintf("runtime-%d", index), fmt.Sprintf("%d", index), "", "")
	}

	_, err := service.Delete(context.Background(), targets)
	if !errors.Is(err, ErrTooManyTargets) {
		t.Fatalf("Delete() error = %v, want ErrTooManyTargets", err)
	}
	if got := client.fetchCallCount(); got != 0 {
		t.Fatalf("Fetch() calls = %d, want 0", got)
	}
}

type configuredSetupResolver struct{}

func (configuredSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{CPAUpstreamURL: "http://cpa.example.test", ManagementKey: "management-key-sentinel"}, true, nil
}

type fixedSetupResolver struct{ setup store.Setup }

func (r fixedSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, true, nil
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

func (c *recordingAuthFilesClient) fetchCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchCalls
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

func (i *recordingInvalidator) InvalidateAuthFileInventory(context.Context) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
}

func (i *recordingInvalidator) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
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

func (c *blockingAuthFilesClient) fetchCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchCalls
}

func (c *blockingAuthFilesClient) activeCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active, c.maximum
}

func selectedDeleteFile(name, runtimeID, authIndex, account, workspace string) cpaauthfiles.File {
	return cpaauthfiles.File{
		ID:              runtimeID,
		Name:            name,
		AuthIndex:       authIndex,
		Provider:        "codex",
		AccountSnapshot: account,
		AccountID:       workspace,
	}
}

func selectedDeleteTarget(name, runtimeID, authIndex, account, workspace string) Target {
	return Target{
		FileName:        name,
		Provider:        "codex",
		RuntimeID:       runtimeID,
		AuthIndex:       authIndex,
		AccountSnapshot: account,
		AccountID:       workspace,
	}
}
