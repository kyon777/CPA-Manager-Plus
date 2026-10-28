package worker

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	accountPriceCacheBatchSize = 25
	accountPriceCacheQueueCap  = 5000
)

// AccountPriceCacheWorker performs bounded, CPAM-only exact repricing for
// visible accounts. The queue is in memory because the cache is disposable;
// a restart simply lets the next account-history read enqueue stale keys again.
type AccountPriceCacheWorker struct {
	store *store.Store
	wake  chan struct{}

	mu      sync.Mutex
	pending map[string]struct{}
	active  map[string]struct{}
	order   []string
	start   sync.Once
}

func NewAccountPriceCacheWorker(st *store.Store) *AccountPriceCacheWorker {
	return &AccountPriceCacheWorker{
		store:   st,
		wake:    make(chan struct{}, 1),
		pending: make(map[string]struct{}, accountPriceCacheQueueCap),
		active:  make(map[string]struct{}, accountPriceCacheBatchSize),
		order:   make([]string, 0, accountPriceCacheQueueCap),
	}
}

func (w *AccountPriceCacheWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.start.Do(func() {
		go w.loop(ctx)
	})
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// EnqueueAccountPriceRecalculation implements monitoring.AccountPriceCacheEnqueuer.
// Duplicate keys are coalesced and the queue is hard bounded so a large page
// refresh cannot consume unbounded memory.
func (w *AccountPriceCacheWorker) EnqueueAccountPriceRecalculation(accountKeys []string) {
	if w == nil || len(accountKeys) == 0 {
		return
	}
	w.mu.Lock()
	for _, key := range accountKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := w.pending[key]; exists {
			continue
		}
		if _, exists := w.active[key]; exists {
			continue
		}
		if len(w.order) >= accountPriceCacheQueueCap {
			break
		}
		w.pending[key] = struct{}{}
		w.order = append(w.order, key)
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *AccountPriceCacheWorker) PendingCount() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.order)
}

func (w *AccountPriceCacheWorker) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
		for {
			batch := w.dequeue(accountPriceCacheBatchSize)
			if len(batch) == 0 {
				break
			}
			w.processBatch(ctx, batch)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (w *AccountPriceCacheWorker) dequeue(limit int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if limit <= 0 || len(w.order) == 0 {
		return nil
	}
	if limit > len(w.order) {
		limit = len(w.order)
	}
	batch := append([]string(nil), w.order[:limit]...)
	for _, key := range batch {
		delete(w.pending, key)
		w.active[key] = struct{}{}
	}
	w.order = append(w.order[:0], w.order[limit:]...)
	return batch
}

func (w *AccountPriceCacheWorker) processBatch(ctx context.Context, keys []string) {
	defer func() {
		w.mu.Lock()
		for _, key := range keys {
			delete(w.active, key)
		}
		w.mu.Unlock()
	}()
	var err error
	for attempt, delay := range []time.Duration{0, 250 * time.Millisecond, time.Second} {
		if attempt > 0 && !waitFor(ctx, delay) {
			return
		}
		_, err = w.store.RecalculateAccountPriceCache(ctx, keys, time.Now().UnixMilli())
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
	if err != nil {
		if markErr := w.store.MarkAccountPriceCacheFailure(ctx, keys, err.Error(), time.Now().UnixMilli()); markErr != nil && ctx.Err() == nil {
			log.Printf("[account-price-cache] mark failed batch: %v", markErr)
		}
		log.Printf("[account-price-cache] exact repricing failed for %d accounts: %v", len(keys), err)
	}
}
