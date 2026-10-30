package worker

import (
	"context"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

// UsageEventWakeHandler adapts a non-blocking runtime wake into the
// collector's event fanout. It never writes to the primary usage database.
type UsageEventWakeHandler struct {
	wake func()
}

func NewUsageEventWakeHandler(wake func()) *UsageEventWakeHandler {
	return &UsageEventWakeHandler{wake: wake}
}

func (h *UsageEventWakeHandler) HandleUsageEvents(_ context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if h == nil || h.wake == nil || len(events) == 0 {
		return
	}
	h.wake()
}
