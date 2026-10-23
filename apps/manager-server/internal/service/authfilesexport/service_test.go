package authfilesexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestServiceExportsSelectedMembersInCallerOrderWithOneDownloadPerPhysicalSource(t *testing.T) {
	client := &recordingExportClient{
		files: []cpaauthfiles.File{
			{ID: "runtime-a", Name: "shared.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com", AccountID: "account-a"},
			{ID: "runtime-b", Name: "shared.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "b@example.com", AccountID: "account-b"},
		},
		downloads: map[string][]byte{
			"shared.json": []byte(`[{"auth_index":"1","email":"a@example.com","token":"a"},{"auth_index":"2","email":"b@example.com","token":"b"}]`),
		},
	}
	service := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client})
	result, err := service.Export(context.Background(), []Target{
		{ClientKey: "b", FileName: "shared.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "codex", RuntimeID: "runtime-b", AccountID: "account-b", AccountSnapshot: "b@example.com"},
		{ClientKey: "a", FileName: "shared.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountID: "account-a", AccountSnapshot: "a@example.com"},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if client.fetchCallCount() != 1 || client.downloadCalls("shared.json") != 1 {
		t.Fatalf("fetch=%d download=%d", client.fetchCallCount(), client.downloadCalls("shared.json"))
	}
	got, err := json.Marshal(result.Records)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	want := `[{"auth_index":"2","email":"b@example.com","token":"b"},{"auth_index":"1","email":"a@example.com","token":"a"}]`
	if string(got) != want {
		t.Fatalf("records=%s want=%s", got, want)
	}
	if result.Requested != 2 || result.Exported != 2 || len(result.Failed) != 0 {
		t.Fatalf("result=%#v", result)
	}
}

func TestServiceKeepsSuccessfulRecordsWhenOneSourceFailsWithoutLeakingSourceData(t *testing.T) {
	client := &recordingExportClient{
		files: []cpaauthfiles.File{
			{ID: "runtime-a", Name: "a.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "a@example.com"},
			{ID: "runtime-b", Name: "b.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "b@example.com"},
		},
		downloads: map[string][]byte{"a.json": []byte(`{"auth_index":"1","email":"a@example.com","refresh_token":"rt-success"}`)},
		errs:      map[string]error{"b.json": errors.New("upstream refused secret-proxy-value")},
	}
	result, err := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client}).Export(context.Background(), []Target{
		{ClientKey: "a", FileName: "a.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-a", AccountSnapshot: "a@example.com"},
		{ClientKey: "b", FileName: "b.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "codex", RuntimeID: "runtime-b", AccountSnapshot: "b@example.com"},
	})
	if err != nil || result.Exported != 1 || len(result.Failed) != 1 || result.Failed[0].Code != "source_unavailable" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	encoded, marshalErr := json.Marshal(result.Failed)
	if marshalErr != nil {
		t.Fatalf("marshal failed items: %v", marshalErr)
	}
	if strings.Contains(string(encoded), "secret-proxy-value") || strings.Contains(string(encoded), "rt-success") {
		t.Fatalf("failure response leaked source error: %s", encoded)
	}
}

func TestServiceClassifiesIdentityAndSourceFailuresPerTarget(t *testing.T) {
	client := &recordingExportClient{
		files: []cpaauthfiles.File{
			{ID: "runtime-good", Name: "good.json", AuthIndex: "1", Provider: "codex", AccountSnapshot: "good@example.com"},
			{ID: "runtime-invalid", Name: "invalid.json", AuthIndex: "2", Provider: "codex", AccountSnapshot: "invalid@example.com"},
			{ID: "runtime-mismatch", Name: "mismatch.json", AuthIndex: "3", Provider: "codex", AccountSnapshot: "actual@example.com"},
		},
		downloads: map[string][]byte{
			"good.json":     []byte(`{"auth_index":"1","email":"good@example.com"}`),
			"invalid.json":  []byte(`[`),
			"mismatch.json": []byte(`{"auth_index":"3","email":"actual@example.com"}`),
		},
	}
	result, err := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client}).Export(context.Background(), []Target{
		{ClientKey: "missing", FileName: "missing.json", AuthIndex: "4", Provider: "codex", RuntimeID: "missing"},
		{ClientKey: "invalid", FileName: "invalid.json", AuthIndex: "2", AccountEmail: "invalid@example.com", Provider: "codex", RuntimeID: "runtime-invalid", AccountSnapshot: "invalid@example.com"},
		{ClientKey: "mismatch", FileName: "mismatch.json", AuthIndex: "3", AccountEmail: "different@example.com", Provider: "codex", RuntimeID: "runtime-mismatch", AccountSnapshot: "different@example.com"},
		{ClientKey: "duplicate", FileName: "good.json", AuthIndex: "1", Provider: "codex", RuntimeID: "runtime-good"},
		{ClientKey: "duplicate", FileName: "good.json", AuthIndex: "1", Provider: "codex", RuntimeID: "runtime-good"},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if result.Exported != 1 || len(result.Failed) != 4 {
		t.Fatalf("result=%#v", result)
	}
	gotCodes := map[string]string{}
	for _, item := range result.Failed {
		gotCodes[item.ClientKey] = item.Code
	}
	if gotCodes["missing"] != "credential_not_found" || gotCodes["invalid"] != "invalid_source_json" || gotCodes["mismatch"] != "identity_mismatch" || gotCodes["duplicate"] != "invalid_target" {
		t.Fatalf("failure codes=%#v", gotCodes)
	}
}

func TestServiceBoundsPhysicalDownloadsToConfiguredConcurrency(t *testing.T) {
	client := newBlockingExportClient(5)
	service := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client, Concurrency: 2})
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := service.Export(context.Background(), client.targets())
		done <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()

	<-client.started
	<-client.started
	if active, maximum := client.activeCounts(); active != 2 || maximum != 2 {
		t.Fatalf("active=%d maximum=%d", active, maximum)
	}
	close(client.release)
	completed := <-done
	if completed.err != nil || completed.result.Exported != 5 {
		t.Fatalf("result=%#v err=%v", completed.result, completed.err)
	}
	if _, maximum := client.activeCounts(); maximum > 2 {
		t.Fatalf("maximum concurrent downloads=%d", maximum)
	}
}

func TestServiceRejectsOverLimitTargetsAndAggregatePayload(t *testing.T) {
	client := &recordingExportClient{}
	service := NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client})
	targets := make([]Target, MaxTargets+1)
	for index := range targets {
		targets[index] = Target{ClientKey: fmt.Sprintf("target-%d", index), FileName: "a.json", AuthIndex: "1", Provider: "codex", RuntimeID: "runtime-a"}
	}
	result, err := service.Export(context.Background(), targets)
	if !errors.Is(err, ErrTooManyTargets) || result.Requested != MaxTargets+1 || client.fetchCallCount() != 0 {
		t.Fatalf("result=%#v err=%v fetch=%d", result, err, client.fetchCallCount())
	}

	client = &recordingExportClient{
		files:     []cpaauthfiles.File{{ID: "runtime-a", Name: "a.json", AuthIndex: "1", Provider: "codex"}},
		downloads: map[string][]byte{"a.json": []byte(`{"auth_index":"1","payload":"too-large"}`)},
	}
	service = NewWithOptions(Options{SetupResolver: configuredExportSetup{}, AuthFiles: client})
	service.maxExportBytes = 16
	result, err = service.Export(context.Background(), []Target{{ClientKey: "a", FileName: "a.json", AuthIndex: "1", Provider: "codex", RuntimeID: "runtime-a"}})
	if !errors.Is(err, ErrExportLimitExceeded) || result.Exported != 0 || len(result.Records) != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type configuredExportSetup struct{}

func (configuredExportSetup) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return store.Setup{
		CPAUpstreamURL: "http://cpa.test:8317",
		ManagementKey:  "test-management-key",
	}, true, nil
}

type recordingExportClient struct {
	mu                  sync.Mutex
	files               []cpaauthfiles.File
	downloads           map[string][]byte
	errs                map[string]error
	fetchErr            error
	fetchCalls          int
	downloadCallsByName map[string]int
}

func (c *recordingExportClient) Fetch(context.Context, string, string) ([]cpaauthfiles.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchCalls++
	if c.fetchErr != nil {
		return nil, c.fetchErr
	}
	return append([]cpaauthfiles.File(nil), c.files...), nil
}

func (c *recordingExportClient) Download(_ context.Context, _ string, _ string, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadCallsByName == nil {
		c.downloadCallsByName = map[string]int{}
	}
	c.downloadCallsByName[name]++
	if err := c.errs[name]; err != nil {
		return nil, err
	}
	return append([]byte(nil), c.downloads[name]...), nil
}

func (c *recordingExportClient) downloadCalls(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.downloadCallsByName[name]
}

func (c *recordingExportClient) fetchCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchCalls
}

type blockingExportClient struct {
	*recordingExportClient
	started chan struct{}
	release chan struct{}
	active  int
	maximum int
}

func newBlockingExportClient(count int) *blockingExportClient {
	files := make([]cpaauthfiles.File, 0, count)
	downloads := make(map[string][]byte, count)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("credential-%d.json", index)
		authIndex := fmt.Sprintf("%d", index+1)
		email := fmt.Sprintf("credential-%d@example.com", index)
		files = append(files, cpaauthfiles.File{ID: fmt.Sprintf("runtime-%d", index), Name: name, AuthIndex: authIndex, Provider: "codex", AccountSnapshot: email})
		downloads[name] = []byte(fmt.Sprintf(`{"auth_index":%q,"email":%q}`, authIndex, email))
	}
	return &blockingExportClient{
		recordingExportClient: &recordingExportClient{files: files, downloads: downloads},
		started:               make(chan struct{}, count),
		release:               make(chan struct{}),
	}
}

func (c *blockingExportClient) Download(ctx context.Context, baseURL string, managementKey string, name string) ([]byte, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maximum {
		c.maximum = c.active
	}
	c.mu.Unlock()
	c.started <- struct{}{}
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return c.recordingExportClient.Download(ctx, baseURL, managementKey, name)
}

func (c *blockingExportClient) activeCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active, c.maximum
}

func (c *blockingExportClient) targets() []Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	targets := make([]Target, 0, len(c.files))
	for index, file := range c.files {
		targets = append(targets, Target{
			ClientKey:       fmt.Sprintf("target-%d", index),
			FileName:        file.Name,
			AuthIndex:       file.AuthIndex,
			AccountEmail:    file.AccountSnapshot,
			Provider:        file.Provider,
			RuntimeID:       file.ID,
			AccountSnapshot: file.AccountSnapshot,
		})
	}
	return targets
}
