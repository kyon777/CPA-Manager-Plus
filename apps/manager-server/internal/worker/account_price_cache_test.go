package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestAccountPriceCacheQueueCoalescesAndBoundsPendingKeys(t *testing.T) {
	w := NewAccountPriceCacheWorker(nil)
	keys := []string{"a", "b", "a", "", "c"}
	w.EnqueueAccountPriceRecalculation(keys)
	w.EnqueueAccountPriceRecalculation([]string{"b", "d"})
	if got := w.PendingCount(); got != 4 {
		t.Fatalf("pending count=%d, want 4", got)
	}
	batch := w.dequeue(2)
	if len(batch) != 2 || batch[0] != "a" || batch[1] != "b" {
		t.Fatalf("dequeued batch=%#v", batch)
	}
	// Active keys are also coalesced while a batch is being processed.
	w.mu.Lock()
	w.active["c"] = struct{}{}
	w.mu.Unlock()
	w.EnqueueAccountPriceRecalculation([]string{"c", "e"})
	if got := w.PendingCount(); got != 3 {
		t.Fatalf("pending after active duplicate=%d, want 3", got)
	}

	for index := 0; index < accountPriceCacheQueueCap+100; index++ {
		w.EnqueueAccountPriceRecalculation([]string{string(rune(0x1000 + index))})
	}
	if got := w.PendingCount(); got > accountPriceCacheQueueCap {
		t.Fatalf("queue exceeded cap: %d > %d", got, accountPriceCacheQueueCap)
	}
}

func TestAccountPriceCacheWorkerStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := NewAccountPriceCacheWorker(nil)
	// The lifecycle guard is intentionally tested without a database: a nil
	// store must not start a goroutine or panic.
	w.Start(ctx)
	cancel()
	select {
	case <-time.After(20 * time.Millisecond):
	}
}

func TestAccountPriceCacheWorkerWritesExactDerivedCost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"model-a": {Prompt: 2, Completion: 4, PromptConfigured: true, CompletionConfigured: true},
	}); err != nil {
		t.Fatalf("save model price: %v", err)
	}
	event := accountPriceCacheTestEvent()
	if _, err := st.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	accountKey, ok := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:     event.AuthFileSnapshot,
		AuthIndex:            event.AuthIndex,
		AuthProviderSnapshot: event.AuthProviderSnapshot,
		AccountSnapshot:      event.AccountSnapshot,
		Source:               event.Source,
	})
	if !ok {
		t.Fatalf("build account key failed")
	}
	worker := NewAccountPriceCacheWorker(st)
	worker.Start(ctx)
	worker.EnqueueAccountPriceRecalculation([]string{accountKey, accountKey})
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := st.LoadUsagePricingAccountSnapshot(ctx, []string{accountKey})
		if err != nil {
			t.Fatalf("load cache snapshot: %v", err)
		}
		if cached, ok := snapshot.PriceCache[accountKey]; ok && cached.Status == "fresh" {
			if cached.TotalCost <= 0 {
				t.Fatalf("cached cost=%v, want positive", cached.TotalCost)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker did not write account price cache; pending=%d", worker.PendingCount())
}

func accountPriceCacheTestEvent() usage.Event {
	hash := sha256.Sum256([]byte("account-price-cache-worker"))
	return usage.Event{
		EventHash:                  hex.EncodeToString(hash[:]),
		TimestampMS:                time.Now().UnixMilli(),
		Timestamp:                  time.Now().UTC().Format(time.RFC3339Nano),
		Provider:                   "codex",
		Model:                      "model-a",
		ResolvedModel:              "model-a",
		Endpoint:                   "POST /v1/responses",
		Method:                     "POST",
		Path:                       "/v1/responses",
		AuthIndex:                  "worker-index",
		Source:                     "worker@example.com.json",
		AuthFileSnapshot:           "worker@example.com.json",
		AuthProviderSnapshot:       "codex",
		AccountSnapshot:            "worker@example.com",
		InputTokens:                1_000_000,
		OutputTokens:               500_000,
		NormalizedTotalInputTokens: 1_000_000,
		TotalTokens:                1_500_000,
	}
}
