package authfilesbatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type testSetupResolver struct {
	setup store.Setup
}

func (r testSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, true, nil
}

type testUploadClient struct {
	mu       sync.Mutex
	uploaded []string
	done     chan struct{}
}

func (c *testUploadClient) Upload(
	_ context.Context,
	_ string,
	_ string,
	name string,
	contents []byte,
) error {
	c.mu.Lock()
	c.uploaded = append(c.uploaded, name)
	last := len(c.uploaded) == 2
	c.mu.Unlock()
	if string(contents) == "second-token" {
		if last {
			close(c.done)
		}
		return errors.New("upstream rejected second file")
	}
	if last {
		close(c.done)
	}
	return nil
}

type testInventoryInvalidator struct {
	mu    sync.Mutex
	calls int
}

type immediateUploadClient struct {
	mu       sync.Mutex
	uploaded []string
}

func (c *immediateUploadClient) Upload(
	_ context.Context,
	_ string,
	_ string,
	name string,
	_ []byte,
) error {
	c.mu.Lock()
	c.uploaded = append(c.uploaded, name)
	c.mu.Unlock()
	return nil
}

type blockingUploadClient struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     int
	entered   chan struct{}
	release   <-chan struct{}
}

func (c *blockingUploadClient) Upload(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []byte,
) error {
	c.mu.Lock()
	c.active++
	c.calls++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.mu.Unlock()
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return nil
}

func waitForFinished(t *testing.T, service *Service, id string) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status, found := service.Get(id); found && status.Finished {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, found := service.Get(id)
	if !found {
		t.Fatalf("batch %s disappeared before completion", id)
	}
	t.Fatalf("batch %s did not finish: %#v", id, status)
	return Status{}
}

func (i *testInventoryInvalidator) InvalidateAuthFileInventory(context.Context) {
	i.mu.Lock()
	i.calls++
	i.mu.Unlock()
}

func (i *testInventoryInvalidator) CallCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func TestBatchUploadAcceptsUpTo300Files(t *testing.T) {
	client := &immediateUploadClient{}
	service := New(testSetupResolver{setup: store.Setup{
		CPAUpstreamURL: "http://core.internal:8317",
		ManagementKey:  "management-key",
	}}, client)
	files := make([]UploadFile, 300)
	for index := range files {
		files[index] = UploadFile{
			Name: fmt.Sprintf("account-%03d.json", index),
			Data: []byte("token"),
		}
	}
	started, err := service.Start(context.Background(), files)
	if err != nil {
		t.Fatalf("start 300-file batch: %v", err)
	}
	status := waitForFinished(t, service, started.ID)
	if status.State != "completed" || status.Uploaded != 300 || status.Failed != 0 {
		t.Fatalf("300-file batch status = %#v", status)
	}
}

func TestBatchUploadRejects301Files(t *testing.T) {
	service := New(testSetupResolver{setup: store.Setup{
		CPAUpstreamURL: "http://core.internal:8317",
		ManagementKey:  "management-key",
	}}, &immediateUploadClient{})
	files := make([]UploadFile, 301)
	for index := range files {
		files[index] = UploadFile{
			Name: fmt.Sprintf("account-%03d.json", index),
			Data: []byte("token"),
		}
	}
	if _, err := service.Start(context.Background(), files); err == nil || !strings.Contains(err.Error(), "300") {
		t.Fatalf("301-file batch error = %v, want a 300-file limit error", err)
	}
}

func TestBatchUploadUsesAtMostEightGlobalUploadSlots(t *testing.T) {
	release := make(chan struct{})
	client := &blockingUploadClient{
		entered: make(chan struct{}, 16),
		release: release,
	}
	service := New(testSetupResolver{setup: store.Setup{
		CPAUpstreamURL: "http://core.internal:8317",
		ManagementKey:  "management-key",
	}}, client)
	files := make([]UploadFile, 16)
	for index := range files {
		files[index] = UploadFile{
			Name: fmt.Sprintf("account-%02d.json", index),
			Data: []byte("token"),
		}
	}
	started, err := service.Start(context.Background(), files)
	if err != nil {
		t.Fatalf("start concurrency batch: %v", err)
	}
	enteredEight := true
	for index := 0; index < 8; index++ {
		select {
		case <-client.entered:
		case <-time.After(time.Second):
			enteredEight = false
		}
	}
	close(release)
	status := waitForFinished(t, service, started.ID)
	if status.State != "completed" || status.Uploaded != len(files) {
		t.Fatalf("concurrency batch status = %#v", status)
	}
	client.mu.Lock()
	maxActive := client.maxActive
	client.mu.Unlock()
	if !enteredEight || maxActive < 2 || maxActive > 8 {
		t.Fatalf("maximum concurrent uploads = %d (entered eight=%t), want 2..8", maxActive, enteredEight)
	}
}

func TestBatchUploadInvalidatesCachedInventoryOnceAfterSuccessfulJob(t *testing.T) {
	client := &testUploadClient{done: make(chan struct{})}
	invalidator := &testInventoryInvalidator{}
	service := NewWithInventoryInvalidator(testSetupResolver{setup: store.Setup{
		CPAUpstreamURL: "http://core.internal:8317",
		ManagementKey:  "management-key",
	}}, invalidator, client)

	started, err := service.Start(context.Background(), []UploadFile{
		{Name: "first.json", Data: []byte("first-token")},
		{Name: "second.json", Data: []byte("second-token")},
	})
	if err != nil {
		t.Fatalf("start batch upload: %v", err)
	}
	status := waitForFinished(t, service, started.ID)
	if !status.Finished {
		t.Fatalf("job status after completion = %#v", status)
	}
	if got := invalidator.CallCount(); got != 1 {
		t.Fatalf("cached inventory invalidations = %d, want 1", got)
	}
}

func TestBatchUploadStoresOnlySafeStatus(t *testing.T) {
	client := &testUploadClient{done: make(chan struct{})}
	service := New(testSetupResolver{setup: store.Setup{
		CPAUpstreamURL: "http://core.internal:8317",
		ManagementKey:  "management-key",
	}}, client)

	started, err := service.Start(context.Background(), []UploadFile{
		{Name: "first.json", Data: []byte("first-token")},
		{Name: "second.json", Data: []byte("second-token")},
	})
	if err != nil {
		t.Fatalf("start batch upload: %v", err)
	}
	if started.ID == "" || started.Total != 2 || started.Finished {
		t.Fatalf("initial batch status = %#v", started)
	}
	status := waitForFinished(t, service, started.ID)
	if !status.Finished || status.State != "partial" || status.Uploaded != 1 || status.Failed != 1 {
		t.Fatalf("completed batch status = %#v", status)
	}
	if len(status.Files) != 2 {
		t.Fatalf("result files = %#v", status.Files)
	}
	resultNames := make(map[string]struct{}, len(status.Files))
	for _, result := range status.Files {
		resultNames[result.Name] = struct{}{}
		if result.Name == "second.json" && result.Error == "" {
			t.Fatalf("failure result = %#v", result)
		}
		if result.Error == "first-token" || result.Error == "second-token" {
			t.Fatalf("credential content leaked into status: %#v", result)
		}
	}
	if _, ok := resultNames["first.json"]; !ok {
		t.Fatalf("first result missing: %#v", status.Files)
	}
	if _, ok := resultNames["second.json"]; !ok {
		t.Fatalf("second result missing: %#v", status.Files)
	}
}
