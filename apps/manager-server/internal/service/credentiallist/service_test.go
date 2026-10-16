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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type fakeInventoryReader struct {
	mu    sync.Mutex
	pages map[int]string
	reads []url.Values
}

type fakeScopeReader struct {
	*fakeInventoryReader
	scope string
}

func (r *fakeScopeReader) AuthFileInventoryScope(context.Context) (string, error) {
	return r.scope, nil
}

type fakeRecentReader struct{}

type fakeRecoveryReader struct {
	task  model.TokenRecoveryTask
	found bool
	err   error
}

func (r fakeRecoveryReader) Get(_ context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	if target.Provider != "codex" {
		return model.TokenRecoveryTask{}, false, nil
	}
	return r.task, r.found, r.err
}

func (fakeRecentReader) RecentAccountRequests(_ context.Context, targets []store.LatestAccountRequestQuery, _ int) ([]store.LatestAccountRequest, error) {
	result := make([]store.LatestAccountRequest, 0, len(targets))
	for _, target := range targets {
		result = append(result, store.LatestAccountRequest{
			RequestIndex: target.RequestIndex,
			TimestampMS:  2_000 - int64(target.RequestIndex)*1_000,
		})
	}
	return result, nil
}

type countingRecentReader struct {
	mu    sync.Mutex
	calls int
}

type blockingInventoryReader struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	body    []byte
	once    sync.Once
}

func (reader *blockingInventoryReader) ReadAuthFileInventory(_ context.Context, _ url.Values) ([]byte, error) {
	reader.mu.Lock()
	reader.calls++
	reader.mu.Unlock()
	reader.once.Do(func() { close(reader.started) })
	<-reader.release
	return append([]byte(nil), reader.body...), nil
}

func (reader *blockingInventoryReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

func (reader *countingRecentReader) RecentAccountRequests(_ context.Context, targets []store.LatestAccountRequestQuery, _ int) ([]store.LatestAccountRequest, error) {
	reader.mu.Lock()
	reader.calls++
	reader.mu.Unlock()
	result := make([]store.LatestAccountRequest, 0, len(targets))
	for _, target := range targets {
		result = append(result, store.LatestAccountRequest{
			RequestIndex: target.RequestIndex,
			TimestampMS:  10_000 - int64(target.RequestIndex),
		})
	}
	return result, nil
}

func (reader *countingRecentReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
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

func TestListSeparatesSnapshotsByCPASetupScope(t *testing.T) {
	reader := &fakeScopeReader{
		fakeInventoryReader: &fakeInventoryReader{pages: map[int]string{
			1: `{"files":[{"name":"first.json","provider":"codex"}],"total":1,"has_more":false}`,
		}},
		scope: "setup-a",
	}
	service := New(reader, time.Now)

	first, err := service.List(context.Background(), Request{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if got := first.Files[0]["name"]; got != "first.json" {
		t.Fatalf("first snapshot name = %#v", got)
	}

	reader.scope = "setup-b"
	reader.pages[1] = `{"files":[{"name":"second.json","provider":"codex"}],"total":1,"has_more":false}`
	second, err := service.List(context.Background(), Request{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	if got := second.Files[0]["name"]; got != "second.json" {
		t.Fatalf("scope leaked cached snapshot: %#v", got)
	}
}

func TestRecentSortUsesUsageIndexAcrossTheEntireSnapshot(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"older.json","auth_index":"0","provider":"codex"},{"name":"newer.json","auth_index":"1","provider":"codex"}],"total":2,"has_more":false}`,
	}}
	service := NewWithRecentReader(reader, fakeRecentReader{}, time.Now)
	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 2, Sort: "recent_desc"})
	if err != nil {
		t.Fatalf("recent list: %v", err)
	}
	if len(response.Files) != 2 || response.Files[0]["name"] != "older.json" {
		t.Fatalf("recent order = %#v", response.Files)
	}
	if got := response.Files[0]["last_request_ms"]; got != "2000" {
		t.Fatalf("recent projection = %#v", got)
	}
}

func TestRecentSortReusesTheSnapshotRecentIndex(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"a.json","provider":"codex"},{"name":"b.json","provider":"codex"}],"total":2,"has_more":false}`,
	}}
	recent := &countingRecentReader{}
	service := NewWithRecentReader(reader, recent, time.Now)
	request := Request{Page: 1, PageSize: 1, Sort: "recent_desc"}
	if _, err := service.List(context.Background(), request); err != nil {
		t.Fatalf("first recent list: %v", err)
	}
	if _, err := service.List(context.Background(), request); err != nil {
		t.Fatalf("second recent list: %v", err)
	}
	if got := recent.callCount(); got != 1 {
		t.Fatalf("recent index calls = %d, want one cached lookup", got)
	}
}

func TestConcurrentInitialListCoalescesTheCPAFirstPageFetch(t *testing.T) {
	reader := &blockingInventoryReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    []byte(`{"files":[{"name":"a.json"}],"total":1,"has_more":false}`),
	}
	service := New(reader, time.Now)
	results := make(chan error, 2)
	request := Request{Page: 1, PageSize: 1}
	go func() {
		_, err := service.List(context.Background(), request)
		results <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("first inventory request did not start")
	}
	go func() {
		_, err := service.List(context.Background(), request)
		results <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if got := reader.callCount(); got != 1 {
		t.Fatalf("concurrent first-page requests = %d, want one", got)
	}
	close(reader.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("coalesced list: %v", err)
		}
	}
}

func TestListReturnsAllEnabledProxyProjectionOnlyWhenSnapshotIsComplete(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"enabled.json","disabled":false,"proxy_url":"http://used:1"},{"name":"disabled.json","disabled":true,"proxy_url":"http://ignored:1"}],"total":2,"has_more":false,"proxy_url_included":true}`,
	}}
	service := New(reader, time.Now)
	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 1, IncludeProxyURL: true})
	if err != nil {
		t.Fatalf("proxy projection list: %v", err)
	}
	if !response.ProxyProjectionComplete || response.EnabledCredentialCount != 1 {
		t.Fatalf("projection metadata = %#v", response)
	}
	if len(response.ProxyURLs) != 1 || response.ProxyURLs[0] != "http://used:1" {
		t.Fatalf("proxy URLs = %#v", response.ProxyURLs)
	}
}

func TestListKeepsZeroEnabledCredentialCountExplicit(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"disabled.json","disabled":true,"proxy_url":"http://ignored:1"}],"total":1,"has_more":false,"proxy_url_included":true}`,
	}}
	service := New(reader, time.Now)
	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 1, IncludeProxyURL: true})
	if err != nil {
		t.Fatalf("proxy projection list: %v", err)
	}
	if response.EnabledCredentialCount != 0 || !response.ProxyProjectionComplete {
		t.Fatalf("projection metadata = %#v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(encoded), `"enabled_credential_count":0`) {
		t.Fatalf("zero enabled count omitted: %s", encoded)
	}
}

func TestWarmingDoesNotStopAtTheFirstPageWhenCPAOmitsTotal(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"a.json"}],"has_more":true}`,
		2: `{"files":[{"name":"b.json"}],"has_more":false}`,
	}}
	service := New(reader, time.Now)
	first, err := service.List(context.Background(), Request{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.SyncStatus != "warming" {
		t.Fatalf("first sync status = %q", first.SyncStatus)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		current := service.cache["default\x00plain"]
		service.mu.Unlock()
		if current != nil && current.complete {
			if len(current.items) != 2 {
				t.Fatalf("warmed items = %d, want 2", len(current.items))
			}
			return
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("snapshot did not finish warming")
}

func TestListEmbedsVisibleCodexRecoveryTaskWithoutCredentialDownload(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[
			{"name":"codex.json","auth_index":"entry-1","email":"codex@example.com","provider":"codex","access_token":"must-not-leak"},
			{"name":"xai.json","email":"xai@example.com","provider":"xai"}
		],"total":2,"has_more":false}`,
	}}
	service := NewWithRecentAndRecoveryReader(
		reader,
		nil,
		fakeRecoveryReader{
			found: true,
			task: model.TokenRecoveryTask{
				ID:               9,
				FileName:         "codex.json",
				AuthIndex:        "entry-1",
				AccountEmail:     "codex@example.com",
				Provider:         "codex",
				Status:           model.TokenRecoveryStatusAutoFailedManualOnly,
				Mode:             model.TokenRecoveryModeAuto,
				LastErrorCode:    "account_banned",
				LastErrorMessage: "account_banned: blocked",
			},
		},
		time.Now,
	)

	response, err := service.List(context.Background(), Request{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(response.Files) != 2 {
		t.Fatalf("visible files = %#v", response.Files)
	}
	task, ok := response.Files[0]["recovery_task"].(model.TokenRecoveryTask)
	if !ok || task.ID != 9 || task.LastErrorCode != "account_banned" {
		t.Fatalf("recovery task = %#v", response.Files[0]["recovery_task"])
	}
	if _, present := response.Files[1]["recovery_task"]; present {
		t.Fatalf("non-Codex row unexpectedly has recovery task: %#v", response.Files[1])
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(encoded), "access_token") || strings.Contains(string(encoded), "must-not-leak") {
		t.Fatalf("credential token leaked in response: %s", encoded)
	}
}

func TestParseInventoryHonorsExplicitHasMoreFalseOnFinalPage(t *testing.T) {
	parsed, err := parseInventory([]byte(`{"files":[{"name":"last.json"}],"total":426,"page":9,"page_size":50,"has_more":false}`))
	if err != nil {
		t.Fatalf("parse final page: %v", err)
	}
	if parsed.warming || !parsed.complete {
		t.Fatalf("final page with explicit has_more=false was treated as warming: %#v", parsed)
	}
}
