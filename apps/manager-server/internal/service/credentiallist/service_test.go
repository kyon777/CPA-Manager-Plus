package credentiallist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeInventoryReader struct {
	mu    sync.Mutex
	pages map[int]string
	reads []url.Values
}

func (r *fakeInventoryReader) ReadAuthFileInventory(_ context.Context, query url.Values) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, query)
	page := 1
	if value := query.Get("page"); value != "" {
		_, _ = fmt.Sscanf(value, "%d", &page)
	}
	return []byte(r.pages[page]), nil
}

func TestListReturnsSanitizedPagedProjectionAndPreservesProxy(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"a.json","email":"a@example.com","note":"today","proxy_url":"http://proxy:1","access_token":"must-not-leak","refresh_token":"must-not-leak"}],"total":2,"page":1,"page_size":1,"has_more":true,"proxy_url_included":true}`,
	}}
	service := New(reader, time.Now)
	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if response.Total != 2 || response.Page != 1 || response.PageSize != 1 || !response.HasMore {
		t.Fatalf("page metadata = %#v", response)
	}
	if len(response.Files) != 1 || response.Files[0]["proxy_url"] != "http://proxy:1" {
		t.Fatalf("files = %#v", response.Files)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "access_token") || strings.Contains(string(encoded), "must-not-leak") {
		t.Fatalf("credential token leaked in response: %s", encoded)
	}
}

func TestListFiltersLoadedSnapshotAndReportsWarmingForUnavailablePage(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"a.json","email":"a@example.com","provider":"codex","disabled":false},{"name":"b.json","email":"b@example.com","provider":"xai","disabled":true}],"total":2,"page":1,"page_size":2,"has_more":false}`,
	}}
	service := New(reader, time.Now)
	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 1, Provider: "codex", Status: "enabled"})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(response.Files) != 1 || response.Files[0]["name"] != "a.json" {
		t.Fatalf("filtered files = %#v", response.Files)
	}
	if response.SyncStatus != "ready" {
		t.Fatalf("sync status = %q, want ready", response.SyncStatus)
	}
}
