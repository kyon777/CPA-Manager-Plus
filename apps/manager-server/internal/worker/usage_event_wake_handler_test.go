package worker

import (
	"context"
	"testing"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageEventWakeHandlerWakesOnlyForInsertedEvents(t *testing.T) {
	wakes := 0
	handler := NewUsageEventWakeHandler(func() { wakes++ })
	handler.HandleUsageEvents(context.Background(), collectorpkg.RuntimeConfig{}, nil)
	if wakes != 0 {
		t.Fatalf("empty event batch woke derived worker %d times", wakes)
	}
	handler.HandleUsageEvents(context.Background(), collectorpkg.RuntimeConfig{}, []usage.Event{{EventHash: "event-1"}})
	if wakes != 1 {
		t.Fatalf("inserted event batch woke derived worker %d times, want 1", wakes)
	}
}
