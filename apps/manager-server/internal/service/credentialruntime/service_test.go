package credentialruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestLookupCoalescesSourceAndProjectsSafeFields(t *testing.T) {
	downloader := &recordingDownloader{files: map[string][]byte{
		"shared.json": []byte(`[
  {"auth_index":"1","email":"a@example.com","proxy_url":"http://a.example:8080","access_token":"never-expose"},
  {"auth_index":"2","email":"b@example.com","proxy-url":"socks5://b.example:1080","refresh_token":"also-never-expose"}
]`),
	}}
	recovery := &recordingRecoveryLookup{tasks: map[string]model.TokenRecoveryTask{
		recoveryTargetKey("shared.json", "2", "b@example.com"): {
			ID: 27, FileName: "shared.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "codex", Status: model.TokenRecoveryStatusAutoRunning,
		},
	}}
	service := NewWithOptions(Options{
		SetupResolver:  staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://core.example", ManagementKey: "core-management-key"}, ok: true},
		AuthFiles:      downloader,
		RecoveryLookup: recovery,
	})

	items, err := service.Lookup(context.Background(), []Target{
		{ClientKey: "row-a", FileName: "shared.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex"},
		{ClientKey: "row-b", FileName: "shared.json", AuthIndex: "2", AccountEmail: "b@example.com", Provider: "CODEX"},
	})
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if downloader.callsFor("shared.json") != 1 {
		t.Fatalf("shared source downloads = %d, want 1", downloader.callsFor("shared.json"))
	}
	if len(items) != 2 {
		t.Fatalf("Lookup() item count = %d, want 2", len(items))
	}
	if items[0].ClientKey != "row-a" || items[0].ProxyURL != "http://a.example:8080" || items[0].RecoveryTask != nil || items[0].ErrorCode != "" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].ClientKey != "row-b" || items[1].ProxyURL != "socks5://b.example:1080" || items[1].RecoveryTask == nil || items[1].RecoveryTask.ID != 27 || items[1].ErrorCode != "" {
		t.Fatalf("second item = %#v", items[1])
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	for _, forbidden := range []string{"never-expose", "also-never-expose", "core-management-key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestLookupKeepsBrokenSiblingFailureLocal(t *testing.T) {
	downloader := &recordingDownloader{files: map[string][]byte{
		"shared.json": []byte(`[
  {"auth_index":"1","email":"a@example.com","proxy_url":"http://a.example:8080"}
]`),
	}}
	service := NewWithOptions(Options{
		SetupResolver:  staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://core.example", ManagementKey: "core-management-key"}, ok: true},
		AuthFiles:      downloader,
		RecoveryLookup: &recordingRecoveryLookup{},
	})

	items, err := service.Lookup(context.Background(), []Target{
		{ClientKey: "valid", FileName: "shared.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex"},
		{ClientKey: "missing", FileName: "shared.json", AuthIndex: "missing", AccountEmail: "missing@example.com", Provider: "codex"},
	})
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if downloader.callsFor("shared.json") != 1 {
		t.Fatalf("shared source downloads = %d, want 1", downloader.callsFor("shared.json"))
	}
	if items[0].ProxyURL != "http://a.example:8080" || items[0].ErrorCode != "" {
		t.Fatalf("valid item = %#v", items[0])
	}
	if items[1].ProxyURL != "" || items[1].ErrorCode != "credential_not_found" {
		t.Fatalf("broken sibling item = %#v", items[1])
	}
}

func TestLookupLimitsConcurrentPhysicalDownloads(t *testing.T) {
	downloader := newBlockingDownloader([]byte(`{"email":"person@example.com","proxy_url":"http://proxy.example:8080"}`))
	service := NewWithOptions(Options{
		SetupResolver:  staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://core.example", ManagementKey: "core-management-key"}, ok: true},
		AuthFiles:      downloader,
		RecoveryLookup: &recordingRecoveryLookup{},
	})
	targets := make([]Target, 0, 5)
	for index := 0; index < 5; index++ {
		targets = append(targets, Target{ClientKey: string(rune('a' + index)), FileName: string(rune('a'+index)) + ".json", AccountEmail: "person@example.com", Provider: "codex"})
	}

	done := make(chan error, 1)
	go func() {
		_, err := service.Lookup(context.Background(), targets)
		done <- err
	}()
	for index := 0; index < 4; index++ {
		select {
		case <-downloader.started:
		case <-time.After(time.Second):
			t.Fatalf("download %d did not start", index+1)
		}
	}
	select {
	case fileName := <-downloader.started:
		t.Fatalf("fifth download %q started before a slot was released", fileName)
	default:
	}
	close(downloader.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Lookup() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Lookup() did not complete after downloads were released")
	}
	if downloader.maxActive() > 4 {
		t.Fatalf("maximum concurrent downloads = %d, want <= 4", downloader.maxActive())
	}
}

type staticSetupResolver struct {
	setup store.Setup
	ok    bool
	err   error
}

func (r staticSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, r.ok, r.err
}

type recordingDownloader struct {
	mu    sync.Mutex
	files map[string][]byte
	calls map[string]int
	err   error
}

func (d *recordingDownloader) Download(_ context.Context, _ string, _ string, fileName string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = make(map[string]int)
	}
	d.calls[fileName]++
	if d.err != nil {
		return nil, d.err
	}
	return append([]byte(nil), d.files[fileName]...), nil
}

func (d *recordingDownloader) callsFor(fileName string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[fileName]
}

type blockingDownloader struct {
	raw     []byte
	started chan string
	release chan struct{}

	mu     sync.Mutex
	active int
	max    int
}

func newBlockingDownloader(raw []byte) *blockingDownloader {
	return &blockingDownloader{
		raw:     append([]byte(nil), raw...),
		started: make(chan string, 5),
		release: make(chan struct{}),
	}
}

func (d *blockingDownloader) Download(ctx context.Context, _ string, _ string, fileName string) ([]byte, error) {
	d.mu.Lock()
	d.active++
	if d.active > d.max {
		d.max = d.active
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.active--
		d.mu.Unlock()
	}()

	d.started <- fileName
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.release:
		return append([]byte(nil), d.raw...), nil
	}
}

func (d *blockingDownloader) maxActive() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.max
}

type recordingRecoveryLookup struct {
	mu    sync.Mutex
	tasks map[string]model.TokenRecoveryTask
	err   error
	calls int
}

func (r *recordingRecoveryLookup) Get(_ context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return model.TokenRecoveryTask{}, false, r.err
	}
	task, found := r.tasks[recoveryTargetKey(target.FileName, target.AuthIndex, target.AccountEmail)]
	return task, found, nil
}

func recoveryTargetKey(fileName, authIndex, accountEmail string) string {
	return strings.ToLower(strings.TrimSpace(fileName)) + "\x1f" + strings.TrimSpace(authIndex) + "\x1f" + strings.ToLower(strings.TrimSpace(accountEmail))
}
