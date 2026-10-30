package derivedstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	accountHistoryGenerationPrefix = "account-history-v1:"
	pricingGenerationPrefix        = "pricing-v1:"
	pricingSnapshotReadAttempts    = 3
)

// ErrSourceCoverageGap means the bounded source did not prove continuity
// from the committed watermark. Advancing across such a gap would silently
// lose a raw fact whose archive reference may still be unpublished.
var ErrSourceCoverageGap = errors.New("derived source coverage gap")

type EngineConfig struct {
	Source     Source
	Sink       Sink
	BatchSize  int
	MaxBatches int
	Yield      time.Duration
	Now        func() time.Time
}

type CycleResult struct {
	Batches       int
	Events        int
	Streams       int
	TargetEventID int64
}

// Engine advances rebuildable projections without opening a write transaction
// on the primary database. It is intentionally single-flight: HTTP reads can
// wake it repeatedly without creating a second derived writer.
type Engine struct {
	source     Source
	sink       Sink
	batchSize  int
	maxBatches int
	yield      time.Duration
	now        func() time.Time

	runGate    chan struct{}
	wake       chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
	cancelMu   sync.Mutex
	cancel     context.CancelFunc
	started    atomic.Bool
	startedCh  chan struct{}
	done       chan struct{}
	stop       chan struct{}
	nextStream int
}

func NewEngine(cfg EngineConfig) *Engine {
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 500
	}
	maxBatches := cfg.MaxBatches
	if maxBatches <= 0 {
		maxBatches = 2
	}
	yield := cfg.Yield
	if yield < 0 {
		yield = 0
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{
		source:     cfg.Source,
		sink:       cfg.Sink,
		batchSize:  batchSize,
		maxBatches: maxBatches,
		yield:      yield,
		now:        now,
		runGate:    make(chan struct{}, 1),
		wake:       make(chan struct{}, 1),
		startedCh:  make(chan struct{}),
		done:       make(chan struct{}),
		stop:       make(chan struct{}),
	}
}

func (e *Engine) Start(ctx context.Context) {
	if e == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		e.cancelMu.Lock()
		e.cancel = cancel
		e.cancelMu.Unlock()
		e.started.Store(true)
		close(e.startedCh)
		select {
		case <-e.stop:
			close(e.done)
			return
		default:
		}
		go e.loop(runCtx)
	})
}

// StopAndWait prevents the isolated worker from using either database while
// the Store is closing. It is safe to call before Start and repeatedly.
func (e *Engine) StopAndWait(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.stopOnce.Do(func() { close(e.stop) })
	e.cancelMu.Lock()
	cancel := e.cancel
	e.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !e.started.Load() {
		return nil
	}
	select {
	case <-e.startedCh:
		select {
		case <-e.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) Wake() {
	if e == nil {
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) loop(ctx context.Context) {
	defer close(e.done)
	// Run once at startup, then react to collector/model-price notifications.
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-e.stop:
			return
		default:
		}
		if _, err := e.RunCycle(ctx); err != nil && ctx.Err() == nil {
			// The durable stream state records the last successful watermark. A
			// later wake retries from that watermark; no primary mutation occurs.
		}
		select {
		case <-ctx.Done():
			return
		case <-e.stop:
			return
		case <-e.wake:
		case <-time.After(time.Second):
		}
	}
}

func (e *Engine) RunCycle(ctx context.Context) (CycleResult, error) {
	if e == nil || e.source == nil || e.sink == nil {
		return CycleResult{}, errors.New("derived engine is not configured")
	}
	select {
	case e.runGate <- struct{}{}:
		defer func() { <-e.runGate }()
	case <-ctx.Done():
		return CycleResult{}, ctx.Err()
	}
	snapshot, prices, err := e.snapshotAndPrices(ctx)
	if err != nil {
		return CycleResult{}, err
	}
	result := CycleResult{TargetEventID: snapshot.TargetEventID}
	streams := []Stream{StreamAccountHistory, StreamPricing}
	streamsRun := make(map[Stream]struct{}, len(streams))
	for attempt := 0; attempt < e.maxBatches; attempt++ {
		stream := streams[e.nextStream%len(streams)]
		e.nextStream = (e.nextStream + 1) % len(streams)
		batches, events, err := e.processStream(ctx, stream, snapshot, prices, 1)
		if err != nil {
			return result, err
		}
		if batches > 0 {
			streamsRun[stream] = struct{}{}
		}
		result.Batches += batches
		result.Events += events
	}
	result.Streams = len(streamsRun)
	return result, nil
}

// snapshotAndPrices reads the source watermark and the model-rate map as one
// logical pricing snapshot. SQLite gives each individual read a consistent
// view, but the two reads can still straddle a model-price write. In that
// case the engine must retry instead of materializing rows with rates from a
// newer revision under an older generation.
func (e *Engine) snapshotAndPrices(ctx context.Context) (SourceSnapshot, map[string]model.ModelPrice, error) {
	var lastSnapshot SourceSnapshot
	var lastRevision string
	for attempt := 0; attempt < pricingSnapshotReadAttempts; attempt++ {
		snapshot, err := e.source.Snapshot(ctx)
		if err != nil {
			return SourceSnapshot{}, nil, err
		}
		prices, err := e.source.ModelPrices(ctx)
		if err != nil {
			return SourceSnapshot{}, nil, err
		}
		pricesRevision := model.ModelPriceValueRevision(prices)
		if snapshot.PricingRevision == pricesRevision {
			return snapshot, prices, nil
		}
		lastSnapshot = snapshot
		lastRevision = pricesRevision
		if err := ctx.Err(); err != nil {
			return SourceSnapshot{}, nil, err
		}
	}
	return SourceSnapshot{}, nil, fmt.Errorf(
		"pricing source changed during snapshot after %d attempts: snapshot revision %q, prices revision %q",
		pricingSnapshotReadAttempts, lastSnapshot.PricingRevision, lastRevision,
	)
}

func (e *Engine) processStream(ctx context.Context, stream Stream, snapshot SourceSnapshot, prices map[string]model.ModelPrice, budget int) (int, int, error) {
	if budget <= 0 {
		return 0, 0, nil
	}
	state, err := e.sink.State(ctx, stream)
	if err != nil {
		return 0, 0, err
	}
	generation := generationFor(stream, snapshot)
	reset := state.Generation != generation || state.SourceEpoch != snapshot.Identity.Epoch
	fromID := state.CoverageEventID
	if reset {
		fromID = 0
	}
	batches, processed := 0, 0
	for batches < budget {
		if err := ctx.Err(); err != nil {
			return batches, processed, err
		}
		events, err := e.eventsAfter(ctx, fromID, snapshot.TargetEventID, e.batchSize)
		if err != nil {
			return batches, processed, err
		}
		if len(events) == 0 && snapshot.TargetEventID > fromID {
			// An empty bounded batch cannot prove that the source range is
			// complete: raw rows may have been deleted while their archive
			// reference is still unpublished, or the archive file may be
			// temporarily unavailable. Never advance coverage across that gap.
			// The next wake/cycle retries from the same committed watermark.
			return batches, processed, nil
		}
		toID := snapshot.TargetEventID
		touchedRows := make(map[string]MaterializedRow)
		rowsByKey := make(map[string]MaterializedRow)
		if len(events) > 0 {
			toID = events[len(events)-1].ID
			if !reset {
				keys := aggregateKeysForEvents(stream, events, prices)
				if len(keys) > 0 {
					rows, err := e.sink.RowsByKeys(ctx, stream, generation, keys)
					if err != nil {
						return batches, processed, err
					}
					for _, row := range rows {
						rowsByKey[row.Key] = row
					}
				}
			}
			for _, event := range events {
				key, err := mergeEvent(rowsByKey, stream, event, prices, e.now().UnixMilli())
				if err != nil {
					return batches, processed, err
				}
				if key == "" {
					continue
				}
				if row, ok := rowsByKey[key]; ok {
					touchedRows[key] = row
				}
			}
		}
		rows := materializedRows(stream, generation, touchedRows)
		if _, err := e.sink.Apply(ctx, ApplyBatch{
			Stream:            stream,
			SourceEpoch:       snapshot.Identity.Epoch,
			Generation:        generation,
			PricingRevision:   snapshot.PricingRevision,
			ArchiveGeneration: snapshot.ArchiveGeneration,
			FromEventID:       fromID,
			ToEventID:         toID,
			Events:            events,
			Rows:              rows,
			AppliedAtMS:       e.now().UnixMilli(),
		}); err != nil {
			return batches, processed, err
		}
		batches++
		processed += len(events)
		reset = false
		if len(events) == 0 || toID >= snapshot.TargetEventID {
			break
		}
		fromID = toID
		if e.yield > 0 {
			timer := time.NewTimer(e.yield)
			select {
			case <-ctx.Done():
				timer.Stop()
				return batches, processed, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return batches, processed, nil
}

func (e *Engine) eventsAfter(ctx context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	if limit <= 0 || targetID <= afterID {
		return []EventRecord{}, nil
	}
	raw, err := e.source.EventsAfter(ctx, afterID, targetID, limit)
	if err != nil {
		return nil, err
	}
	archived, err := e.source.ArchivedEventsAfter(ctx, afterID, targetID, limit)
	if err != nil {
		return nil, err
	}
	merged, err := mergeSourceEvents(raw, archived, limit)
	if err != nil {
		return nil, err
	}
	if len(merged) > 0 {
		wantID := afterID + 1
		for _, event := range merged {
			if event.ID != wantID {
				return nil, fmt.Errorf("%w: expected event %d, got %d", ErrSourceCoverageGap, wantID, event.ID)
			}
			wantID++
		}
	}
	return merged, nil
}

func mergeSourceEvents(raw, archived []EventRecord, limit int) ([]EventRecord, error) {
	result := make([]EventRecord, 0, minInt(limit, len(raw)+len(archived)))
	i, j := 0, 0
	for len(result) < limit && (i < len(raw) || j < len(archived)) {
		var next EventRecord
		switch {
		case j >= len(archived) || (i < len(raw) && raw[i].ID < archived[j].ID):
			next, i = raw[i], i+1
		case i >= len(raw) || archived[j].ID < raw[i].ID:
			next, j = archived[j], j+1
		default:
			if raw[i].EventHash != archived[j].EventHash {
				return nil, fmt.Errorf("source event id %d has conflicting hashes", raw[i].ID)
			}
			next = raw[i]
			i++
			j++
		}
		result = append(result, next)
	}
	return result, nil
}

// GenerationFor returns the immutable materialization generation expected for
// a source snapshot. A pricing generation includes the canonical price
// revision; an account-history generation does not, because its counters are
// independent of model rates.
func GenerationFor(stream Stream, snapshot SourceSnapshot) string {
	switch stream {
	case StreamPricing:
		return pricingGenerationPrefix + snapshot.Identity.Epoch + ":" + snapshot.PricingRevision
	default:
		return accountHistoryGenerationPrefix + snapshot.Identity.Epoch
	}
}

func generationFor(stream Stream, snapshot SourceSnapshot) string {
	return GenerationFor(stream, snapshot)
}

func mergeEvent(rows map[string]MaterializedRow, stream Stream, event EventRecord, prices map[string]model.ModelPrice, nowMS int64) (string, error) {
	key, accountKey := aggregateKeyForEvent(stream, event, prices)
	if key == "" {
		return "", nil
	}
	modelName, billingModel, pricingModel := eventModels(event, prices)
	serviceTier := strings.TrimSpace(event.ServiceTier)
	threshold := model.ModelPriceBaseContextThreshold
	if stream == StreamPricing {
		if price, ok := prices[pricingModel]; ok {
			_, threshold = model.ModelPriceForContext(price, normalizedInput(event))
		}
	}
	row := rows[key]
	var aggregate AccountAggregate
	if row.Key != "" {
		if err := json.Unmarshal([]byte(row.PayloadJSON), &aggregate); err != nil {
			return "", fmt.Errorf("decode derived aggregate %s: %w", key, err)
		}
	} else {
		aggregate = AccountAggregate{
			AccountKey:             accountKey,
			AccountSnapshot:        event.AccountSnapshot,
			AuthLabelSnapshot:      event.AuthLabelSnapshot,
			AuthProviderSnapshot:   event.AuthProviderSnapshot,
			AuthIndex:              event.AuthIndex,
			Source:                 event.Source,
			SourceHash:             event.SourceHash,
			Model:                  modelName,
			BillingModel:           billingModel,
			PricingModel:           pricingModel,
			ServiceTier:            serviceTier,
			ContextThresholdTokens: threshold,
			FirstSeenMS:            event.TimestampMS,
			LastSeenMS:             event.TimestampMS,
		}
	}
	fillAggregateSnapshots(&aggregate, event)
	aggregate.Calls++
	if event.Failed {
		aggregate.FailureCalls++
	} else {
		aggregate.SuccessCalls++
	}
	aggregate.InputTokens += event.InputTokens
	aggregate.OutputTokens += event.OutputTokens
	aggregate.ReasoningTokens += event.ReasoningTokens
	aggregate.CachedTokens += usage.CompatibleCachedTokens(event.CachedTokens, event.CacheTokens, event.CacheReadTokens, event.CacheCreationTokens)
	aggregate.CacheReadTokens += event.CacheReadTokens
	aggregate.CacheCreationTokens += event.CacheCreationTokens
	aggregate.TotalTokens += event.TotalTokens
	inputForLong := event.InputTokens
	if stream == StreamPricing {
		inputForLong = normalizedInput(event)
	}
	compatibleCached := usage.CompatibleCachedTokens(event.CachedTokens, event.CacheTokens, event.CacheReadTokens, event.CacheCreationTokens)
	if usage.IsLongContextInput(inputForLong) {
		aggregate.LongInputTokens += inputForLong
		aggregate.LongOutputTokens += event.OutputTokens
		aggregate.LongCachedTokens += compatibleCached
		aggregate.LongCacheReadTokens += event.CacheReadTokens
		aggregate.LongCacheCreationTokens += event.CacheCreationTokens
	}
	if aggregate.FirstSeenMS == 0 || (event.TimestampMS > 0 && event.TimestampMS < aggregate.FirstSeenMS) {
		aggregate.FirstSeenMS = event.TimestampMS
	}
	if event.TimestampMS > aggregate.LastSeenMS {
		aggregate.LastSeenMS = event.TimestampMS
	}
	aggregate.UpdatedAtMS = nowMS
	payload, err := json.Marshal(aggregate)
	if err != nil {
		return "", fmt.Errorf("encode derived aggregate %s: %w", key, err)
	}
	if row.Key == "" {
		row = MaterializedRow{Stream: stream, Generation: "", Key: key, SubjectKey: accountKey, FirstEventID: event.ID}
	}
	row.Stream = stream
	row.Key = key
	row.SubjectKey = accountKey
	row.PayloadJSON = string(payload)
	if row.FirstEventID == 0 || event.ID < row.FirstEventID {
		row.FirstEventID = event.ID
	}
	if event.ID > row.LastEventID {
		row.LastEventID = event.ID
	}
	row.UpdatedAtMS = nowMS
	rows[key] = row
	return key, nil
}

func aggregateKeysForEvents(stream Stream, events []EventRecord, prices map[string]model.ModelPrice) []string {
	keys := make([]string, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		key, _ := aggregateKeyForEvent(stream, event, prices)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func aggregateKeyForEvent(stream Stream, event EventRecord, prices map[string]model.ModelPrice) (string, string) {
	accountKey := AccountKeyForEvent(event)
	if strings.TrimSpace(accountKey) == "" {
		return "", ""
	}
	modelName, billingModel, pricingModel := eventModels(event, prices)
	threshold := model.ModelPriceBaseContextThreshold
	if stream == StreamPricing {
		if price, ok := prices[pricingModel]; ok {
			_, threshold = model.ModelPriceForContext(price, normalizedInput(event))
		}
	}
	return aggregateKey(stream, accountKey, modelName, billingModel, pricingModel, strings.TrimSpace(event.ServiceTier), threshold), accountKey
}

func materializedRows(stream Stream, generation string, rows map[string]MaterializedRow) []MaterializedRow {
	result := make([]MaterializedRow, 0, len(rows))
	for _, row := range rows {
		row.Stream = stream
		row.Generation = generation
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

// MaterializedRowForAggregate converts a verified legacy aggregate into the
// same opaque row-key/payload representation produced by the event engine.
// It is intentionally pure: bootstrap callers can build a derived snapshot
// without opening a cross-database transaction or replaying raw facts.
func MaterializedRowForAggregate(stream Stream, generation string, aggregate AccountAggregate, firstEventID, lastEventID, nowMS int64) (MaterializedRow, error) {
	if strings.TrimSpace(generation) == "" {
		return MaterializedRow{}, errors.New("derived aggregate generation is required")
	}
	if strings.TrimSpace(aggregate.AccountKey) == "" {
		return MaterializedRow{}, errors.New("derived aggregate account key is required")
	}
	key := aggregateKey(stream, aggregate.AccountKey, aggregate.Model, aggregate.BillingModel,
		aggregate.PricingModel, aggregate.ServiceTier, aggregate.ContextThresholdTokens)
	payload, err := json.Marshal(aggregate)
	if err != nil {
		return MaterializedRow{}, fmt.Errorf("encode derived bootstrap aggregate %s: %w", key, err)
	}
	return MaterializedRow{
		Stream:       stream,
		Generation:   generation,
		Key:          key,
		SubjectKey:   aggregate.AccountKey,
		PayloadJSON:  string(payload),
		FirstEventID: firstEventID,
		LastEventID:  lastEventID,
		UpdatedAtMS:  nowMS,
	}, nil
}

// MergeMaterializedRows overlays a bounded source tail on the last verified
// derived rows without changing the derived database or its coverage state.
// It is used by the current-page read path while the isolated worker catches
// up asynchronously.
func MergeMaterializedRows(stream Stream, generation string, base []MaterializedRow, events []EventRecord, prices map[string]model.ModelPrice, nowMS int64) ([]MaterializedRow, error) {
	rowsByKey := make(map[string]MaterializedRow, len(base)+len(events))
	for _, row := range base {
		rowsByKey[row.Key] = row
	}
	for _, event := range events {
		if _, err := mergeEvent(rowsByKey, stream, event, prices, nowMS); err != nil {
			return nil, err
		}
	}
	return materializedRows(stream, generation, rowsByKey), nil
}

func aggregateKey(stream Stream, accountKey, modelName, billingModel, pricingModel, serviceTier string, threshold int64) string {
	if stream == StreamAccountHistory {
		return strings.Join([]string{accountKey, modelName, billingModel, serviceTier}, "\x00")
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d", accountKey, modelName, billingModel, pricingModel, serviceTier, threshold)
}

func eventModels(event EventRecord, prices map[string]model.ModelPrice) (string, string, string) {
	requested := usageidentity.EffectiveRequestedModel(event.Model, event.RequestedModel)
	analytics := usageidentity.AnalyticsModel(requested)
	if analytics == "" {
		analytics = strings.TrimSpace(event.Model)
	}
	billing := strings.TrimSpace(event.ResolvedModel)
	if billing == "" {
		billing = analytics
	}
	if billing == "" {
		billing = "-"
	}
	modelName := analytics
	if modelName == "" {
		modelName = billing
	}
	pricing := billing
	if _, ok := prices[pricing]; !ok {
		if _, ok := prices[analytics]; ok {
			pricing = analytics
		} else if _, ok := prices[requested]; ok {
			pricing = requested
		}
	}
	return modelName, billing, pricing
}

func normalizedInput(event EventRecord) int64 {
	if event.NormalizedInputTokens > 0 {
		return event.NormalizedInputTokens
	}
	return event.InputTokens
}

func fillAggregateSnapshots(target *AccountAggregate, event EventRecord) {
	if target.AccountSnapshot == "" {
		target.AccountSnapshot = event.AccountSnapshot
	}
	if target.AuthLabelSnapshot == "" {
		target.AuthLabelSnapshot = event.AuthLabelSnapshot
	}
	if target.AuthProviderSnapshot == "" {
		target.AuthProviderSnapshot = event.AuthProviderSnapshot
	}
	if target.AuthIndex == "" {
		target.AuthIndex = event.AuthIndex
	}
	if target.Source == "" {
		target.Source = event.Source
	}
	if target.SourceHash == "" {
		target.SourceHash = event.SourceHash
	}
}
