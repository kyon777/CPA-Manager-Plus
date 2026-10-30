package derivedstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	accountHistoryGenerationPrefix = "account-history-v1:"
	pricingGenerationPrefix        = "pricing-v1:"
)

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

	runGate   chan struct{}
	wake      chan struct{}
	startOnce sync.Once
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
	}
}

func (e *Engine) Start(ctx context.Context) {
	if e == nil {
		return
	}
	e.startOnce.Do(func() {
		go e.loop(ctx)
	})
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
	// Run once at startup, then react to collector/model-price notifications.
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := e.RunCycle(ctx); err != nil && ctx.Err() == nil {
			// The durable stream state records the last successful watermark. A
			// later wake retries from that watermark; no primary mutation occurs.
		}
		select {
		case <-ctx.Done():
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
	snapshot, err := e.source.Snapshot(ctx)
	if err != nil {
		return CycleResult{}, err
	}
	prices, err := e.source.ModelPrices(ctx)
	if err != nil {
		return CycleResult{}, err
	}
	result := CycleResult{TargetEventID: snapshot.TargetEventID}
	streams := []Stream{StreamAccountHistory, StreamPricing}
	for _, stream := range streams {
		if result.Batches >= e.maxBatches {
			break
		}
		batches, events, err := e.processStream(ctx, stream, snapshot, prices, e.maxBatches-result.Batches)
		if err != nil {
			return result, err
		}
		if batches > 0 {
			result.Streams++
		}
		result.Batches += batches
		result.Events += events
	}
	return result, nil
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
	rowsByKey := make(map[string]MaterializedRow)
	if !reset {
		rows, err := e.sink.Rows(ctx, stream, generation)
		if err != nil {
			return 0, 0, err
		}
		for _, row := range rows {
			rowsByKey[row.Key] = row
		}
	} else {
		fromID = 0
	}
	batches, processed := 0, 0
	for batches < budget {
		if err := ctx.Err(); err != nil {
			return batches, processed, err
		}
		events, err := e.source.EventsAfter(ctx, fromID, snapshot.TargetEventID, e.batchSize)
		if err != nil {
			return batches, processed, err
		}
		toID := snapshot.TargetEventID
		if len(events) > 0 {
			toID = events[len(events)-1].ID
			for _, event := range events {
				if err := mergeEvent(rowsByKey, stream, event, prices, e.now().UnixMilli()); err != nil {
					return batches, processed, err
				}
			}
		}
		rows := materializedRows(stream, generation, rowsByKey)
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

func generationFor(stream Stream, snapshot SourceSnapshot) string {
	switch stream {
	case StreamPricing:
		return pricingGenerationPrefix + snapshot.Identity.Epoch + ":" + snapshot.PricingRevision
	default:
		return accountHistoryGenerationPrefix + snapshot.Identity.Epoch
	}
}

func mergeEvent(rows map[string]MaterializedRow, stream Stream, event EventRecord, prices map[string]model.ModelPrice, nowMS int64) error {
	accountKey := AccountKeyForEvent(event)
	if strings.TrimSpace(accountKey) == "" {
		return nil
	}
	modelName, billingModel, pricingModel := eventModels(event, prices)
	serviceTier := strings.TrimSpace(event.ServiceTier)
	threshold := model.ModelPriceBaseContextThreshold
	if stream == StreamPricing {
		if price, ok := prices[pricingModel]; ok {
			_, threshold = model.ModelPriceForContext(price, normalizedInput(event))
		}
	}
	key := aggregateKey(stream, accountKey, modelName, billingModel, pricingModel, serviceTier, threshold)
	row := rows[key]
	var aggregate AccountAggregate
	if row.Key != "" {
		if err := json.Unmarshal([]byte(row.PayloadJSON), &aggregate); err != nil {
			return fmt.Errorf("decode derived aggregate %s: %w", key, err)
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
		return fmt.Errorf("encode derived aggregate %s: %w", key, err)
	}
	if row.Key == "" {
		row = MaterializedRow{Stream: stream, Generation: "", Key: key, FirstEventID: event.ID}
	}
	row.Stream = stream
	row.Key = key
	row.PayloadJSON = string(payload)
	if row.FirstEventID == 0 || event.ID < row.FirstEventID {
		row.FirstEventID = event.ID
	}
	if event.ID > row.LastEventID {
		row.LastEventID = event.ID
	}
	row.UpdatedAtMS = nowMS
	rows[key] = row
	return nil
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
