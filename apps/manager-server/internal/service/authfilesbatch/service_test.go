package authfilesbatch

import (
	"context"
	"errors"
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
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("batch upload did not finish")
	}
	if status, found := service.Get(started.ID); !found || !status.Finished {
		t.Fatalf("job status after completion = %#v found=%t", status, found)
	}
	if got := invalidator.CallCount(); got != 1 {
		t.Fatalf("cached inventory invalidations = %d, want 1", got)
	}
}

func TestBatchUploadStoresOnlySafeStatusAndRunsSerially(t *testing.T) {
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
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("batch upload did not finish")
	}

	status, ok := service.Get(started.ID)
	if !ok {
		t.Fatal("completed batch status was not retained")
	}
	if !status.Finished || status.State != "partial" || status.Uploaded != 1 || status.Failed != 1 {
		t.Fatalf("completed batch status = %#v", status)
	}
	if len(status.Files) != 2 || status.Files[0].Name != "first.json" || status.Files[1].Name != "second.json" {
		t.Fatalf("result files = %#v", status.Files)
	}
	for _, result := range status.Files {
		if result.Name == "second.json" && result.Error == "" {
			t.Fatalf("failure result = %#v", result)
		}
		if result.Error == "first-token" || result.Error == "second-token" {
			t.Fatalf("credential content leaked into status: %#v", result)
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if got := client.uploaded; len(got) != 2 || got[0] != "first.json" || got[1] != "second.json" {
		t.Fatalf("upload order = %#v, want serial input order", got)
	}
}
