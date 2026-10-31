package derivedstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type scriptedSource struct {
	epoch    string
	events   []EventRecord
	archived []EventRecord
	prices   map[string]model.ModelPrice
}

type recordingSink struct {
	Sink
	applied []ApplyBatch
}

func (s *recordingSink) Apply(ctx context.Context, batch ApplyBatch) (StreamState, error) {
	copyBatch := batch
	copyBatch.Events = append([]EventRecord(nil), batch.Events...)
	copyBatch.Rows = append([]MaterializedRow(nil), batch.Rows...)
	s.applied = append(s.applied, copyBatch)
	return s.Sink.Apply(ctx, batch)
}

type boundedReadSink struct {
	Sink
	fullRowsCalls   int
	rowsByKeysCalls int
}

func (s *boundedReadSink) Rows(ctx context.Context, stream Stream, generation string) ([]MaterializedRow, error) {
	s.fullRowsCalls++
	return s.Sink.Rows(ctx, stream, generation)
}

func (s *boundedReadSink) RowsByKeys(ctx context.Context, stream Stream, generation string, keys []string) ([]MaterializedRow, error) {
	s.rowsByKeysCalls++
	return s.Sink.RowsByKeys(ctx, stream, generation, keys)
}

func (s scriptedSource) Identity(context.Context) (SourceIdentity, error) {
	return SourceIdentity{Epoch: s.epoch}, nil
}

func (s scriptedSource) Snapshot(context.Context) (SourceSnapshot, error) {
	var target int64
	for _, event := range s.events {
		if event.ID > target {
			target = event.ID
		}
	}
	for _, event := range s.archived {
		if event.ID > target {
			target = event.ID
		}
	}
	return SourceSnapshot{
		Identity:        SourceIdentity{Epoch: s.epoch},
		TargetEventID:   target,
		PricingRevision: model.ModelPriceValueRevision(s.prices),
	}, nil
}

func (s scriptedSource) EventsAfter(_ context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	result := make([]EventRecord, 0, limit)
	for _, event := range s.events {
		if event.ID > afterID && event.ID <= targetID {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s scriptedSource) EventsForSubjectsAfter(_ context.Context, subjects []string, afterID, targetID int64, limit int) ([]EventRecord, error) {
	allowed := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		allowed[subject] = struct{}{}
	}
	result := make([]EventRecord, 0, limit)
	for _, event := range s.events {
		if event.ID <= afterID || event.ID > targetID {
			continue
		}
		if _, ok := allowed[AccountKeyForEvent(event)]; !ok {
			continue
		}
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s scriptedSource) ArchivedEventsAfter(_ context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	result := make([]EventRecord, 0, limit)
	for _, event := range s.archived {
		if event.ID > afterID && event.ID <= targetID {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s scriptedSource) PricingRevision(context.Context) (string, error) {
	return model.ModelPriceValueRevision(s.prices), nil
}
func (s scriptedSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	return s.prices, nil
}

type pricingRaceSource struct {
	oldPrices     map[string]model.ModelPrice
	newPrices     map[string]model.ModelPrice
	snapshotCalls int
	pricesCalls   int
	events        []EventRecord
}

func (s *pricingRaceSource) Identity(context.Context) (SourceIdentity, error) {
	return SourceIdentity{Epoch: "epoch-race"}, nil
}

func (s *pricingRaceSource) Snapshot(context.Context) (SourceSnapshot, error) {
	s.snapshotCalls++
	prices := s.oldPrices
	if s.snapshotCalls > 1 {
		prices = s.newPrices
	}
	var target int64
	for _, event := range s.events {
		if event.ID > target {
			target = event.ID
		}
	}
	return SourceSnapshot{
		Identity:        SourceIdentity{Epoch: "epoch-race"},
		TargetEventID:   target,
		PricingRevision: model.ModelPriceValueRevision(prices),
	}, nil
}

func (s *pricingRaceSource) EventsAfter(_ context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	result := make([]EventRecord, 0, limit)
	for _, event := range s.events {
		if event.ID > afterID && event.ID <= targetID {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s *pricingRaceSource) EventsForSubjectsAfter(ctx context.Context, subjects []string, afterID, targetID int64, limit int) ([]EventRecord, error) {
	return s.EventsAfter(ctx, afterID, targetID, limit)
}

func (*pricingRaceSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return []EventRecord{}, nil
}

func (s *pricingRaceSource) PricingRevision(context.Context) (string, error) {
	return model.ModelPriceValueRevision(s.newPrices), nil
}

func (s *pricingRaceSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	s.pricesCalls++
	return s.newPrices, nil
}

func TestEngineRetriesWhenPricingSnapshotChangesDuringRead(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	oldPrices := map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}}
	newPrices := map[string]model.ModelPrice{"gpt-test": {Prompt: 2, Completion: 2}}
	source := &pricingRaceSource{
		oldPrices: oldPrices,
		newPrices: newPrices,
		events: []EventRecord{{
			ID: 1, EventHash: "h1", TimestampMS: 100, AuthIndex: "account-a",
			AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test",
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
		}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 1, MaxBatches: 2})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run engine: %v", err)
	}
	wantRevision := model.ModelPriceValueRevision(newPrices)
	if source.snapshotCalls < 2 || source.pricesCalls < 2 {
		t.Fatalf("pricing reads were not retried: snapshots=%d prices=%d", source.snapshotCalls, source.pricesCalls)
	}
	state, err := NewSink(db).State(context.Background(), StreamPricing)
	if err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if state.PricingRevision != wantRevision || state.Generation != GenerationFor(StreamPricing, SourceSnapshot{
		Identity: SourceIdentity{Epoch: "epoch-race"}, PricingRevision: wantRevision,
	}) {
		t.Fatalf("pricing state used inconsistent revision: %#v, want %q", state, wantRevision)
	}
}

func TestEngineProcessesBoundedBatchesAndIsRestartIdempotent(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source := scriptedSource{
		epoch: "epoch-a",
		events: []EventRecord{
			{ID: 1, EventHash: "h1", TimestampMS: 100, AuthFileSnapshot: "a.json", AuthIndex: "a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			{ID: 2, EventHash: "h2", TimestampMS: 200, AuthFileSnapshot: "a.json", AuthIndex: "a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 20, OutputTokens: 3, TotalTokens: 23},
		},
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 1, MaxBatches: 4, Yield: time.Millisecond})
	result, err := engine.RunCycle(context.Background())
	if err != nil {
		t.Fatalf("run first cycle: %v", err)
	}
	if result.Batches < 2 {
		t.Fatalf("cycle batches = %d, want at least 2", result.Batches)
	}
	state, err := NewSink(db).State(context.Background(), StreamPricing)
	if err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if state.CoverageEventID != 2 || state.Status != "ready" {
		t.Fatalf("pricing state = %#v", state)
	}
	rows, err := NewSink(db).Rows(context.Background(), StreamPricing, state.Generation)
	if err != nil {
		t.Fatalf("read pricing rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pricing rows = %#v", rows)
	}
	var aggregate AccountAggregate
	if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &aggregate); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if aggregate.Calls != 2 || aggregate.InputTokens != 30 {
		t.Fatalf("aggregate = %#v", aggregate)
	}

	restarted := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 1, MaxBatches: 4, Yield: time.Millisecond})
	if _, err := restarted.RunCycle(context.Background()); err != nil {
		t.Fatalf("run restart cycle: %v", err)
	}
	rows, err = NewSink(db).Rows(context.Background(), StreamPricing, state.Generation)
	if err != nil {
		t.Fatalf("read rows after restart: %v", err)
	}
	if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &aggregate); err != nil {
		t.Fatalf("decode aggregate after restart: %v", err)
	}
	if aggregate.Calls != 2 || aggregate.InputTokens != 30 {
		t.Fatalf("restart duplicated aggregate = %#v", aggregate)
	}
}

func TestEngineMergesDeletedArchiveEventsIntoDerivedCoverage(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source := scriptedSource{
		epoch: "epoch-a",
		events: []EventRecord{
			{ID: 1, EventHash: "raw-1", TimestampMS: 100, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 10, TotalTokens: 10},
			{ID: 3, EventHash: "raw-3", TimestampMS: 300, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 30, TotalTokens: 30},
		},
		archived: []EventRecord{
			{ID: 2, EventHash: "archive-2", TimestampMS: 200, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 20, TotalTokens: 20},
		},
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 2, MaxBatches: 4})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run archive-aware engine: %v", err)
	}
	state, err := NewSink(db).State(context.Background(), StreamPricing)
	if err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if state.CoverageEventID != 3 || state.Status != "ready" {
		t.Fatalf("pricing state = %#v", state)
	}
	rows, err := NewSink(db).Rows(context.Background(), StreamPricing, state.Generation)
	if err != nil {
		t.Fatalf("read pricing rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pricing rows = %#v", rows)
	}
	var aggregate AccountAggregate
	if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &aggregate); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if aggregate.Calls != 3 || aggregate.InputTokens != 60 || aggregate.TotalTokens != 60 {
		t.Fatalf("archive event was not materialized: %#v", aggregate)
	}
}

func TestEngineWritesOnlyRowsTouchedByEachBatch(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := &recordingSink{Sink: NewSink(db)}
	source := scriptedSource{
		epoch: "epoch-a",
		events: []EventRecord{
			{ID: 1, EventHash: "h1", TimestampMS: 100, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			{ID: 2, EventHash: "h2", TimestampMS: 200, AuthIndex: "account-b", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 20, OutputTokens: 3, TotalTokens: 23},
		},
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: sink, BatchSize: 1, MaxBatches: 4})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run engine: %v", err)
	}
	for _, batch := range sink.applied {
		if len(batch.Events) == 0 {
			continue
		}
		if got := len(batch.Rows); got != 1 {
			t.Fatalf("%s batch ending at %d wrote %d rows, want only its touched row", batch.Stream, batch.ToEventID, got)
		}
	}
}

func TestEngineAlternatesStreamsWithinCycleBudget(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := &recordingSink{Sink: NewSink(db)}
	source := scriptedSource{
		epoch: "epoch-a",
		events: []EventRecord{
			{ID: 1, EventHash: "h1", TimestampMS: 100, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			{ID: 2, EventHash: "h2", TimestampMS: 200, AuthIndex: "account-b", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 20, OutputTokens: 3, TotalTokens: 23},
		},
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: sink, BatchSize: 1, MaxBatches: 2})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run engine: %v", err)
	}
	if len(sink.applied) != 2 {
		t.Fatalf("applied batches = %d, want 2", len(sink.applied))
	}
	if sink.applied[0].Stream != StreamAccountHistory || sink.applied[1].Stream != StreamPricing {
		t.Fatalf("stream order = %#v, want account-history then pricing", []Stream{sink.applied[0].Stream, sink.applied[1].Stream})
	}
}

func TestEnginePrioritizesPricingBurstWhileHistoryStaysServiced(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := &recordingSink{Sink: NewSink(db)}
	events := make([]EventRecord, 0, 10)
	for id := int64(1); id <= 10; id++ {
		events = append(events, EventRecord{
			ID: id, EventHash: fmt.Sprintf("catchup-%d", id), TimestampMS: id,
			AuthIndex: "account-a", AuthProviderSnapshot: "codex",
			Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 1, TotalTokens: 1,
		})
	}
	source := scriptedSource{
		epoch: "epoch-a", events: events,
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{
		Source: source, Sink: sink, BatchSize: 1, MaxBatches: 2,
		PricingCatchupBatchSize: 2, PricingCatchupMaxBatches: 3, PricingCatchupLagEvents: 2,
	})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run engine: %v", err)
	}
	got := make([]Stream, 0, len(sink.applied))
	for _, batch := range sink.applied {
		got = append(got, batch.Stream)
	}
	want := []Stream{StreamAccountHistory, StreamPricing, StreamPricing, StreamPricing}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stream order = %#v, want %#v", got, want)
	}
	for _, batch := range sink.applied[1:] {
		if batch.Stream == StreamPricing && len(batch.Events) > 2 {
			t.Fatalf("pricing catch-up batch has %d events, want <= 2", len(batch.Events))
		}
	}
}

func TestEngineReadsOnlyMaterializedKeysNeededByBatch(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := &boundedReadSink{Sink: NewSink(db)}
	source := &scriptedSource{
		epoch: "epoch-a",
		events: []EventRecord{
			{ID: 1, EventHash: "h1", TimestampMS: 100, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		},
		prices: map[string]model.ModelPrice{"gpt-test": {Prompt: 1, Completion: 1}},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: sink, BatchSize: 1, MaxBatches: 2})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("materialize first event: %v", err)
	}
	sink.fullRowsCalls = 0
	sink.rowsByKeysCalls = 0
	source.events = append(source.events, EventRecord{ID: 2, EventHash: "h2", TimestampMS: 200, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 20, OutputTokens: 3, TotalTokens: 23})
	if _, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("materialize tail event: %v", err)
	}
	if sink.fullRowsCalls != 0 {
		t.Fatalf("engine performed %d full derived-row scans for one-event tail", sink.fullRowsCalls)
	}
	if sink.rowsByKeysCalls != 2 {
		t.Fatalf("engine bounded-row reads = %d, want 2 (one per derived stream)", sink.rowsByKeysCalls)
	}
}

func TestEngineDoesNotAdvanceCoverageWhenSourceBatchIsEmptyBeforeTarget(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The source advertises a target watermark but cannot return the event in
	// that range. This is the exact shape produced by a raw deletion whose
	// archive reference is not yet visible. Advancing coverage here would make
	// the derived database claim completeness while silently dropping a fact.
	source := scriptedSource{epoch: "epoch-a", prices: map[string]model.ModelPrice{}}
	engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 1, MaxBatches: 1})
	// Replace the scripted source with a source that exposes a non-zero target
	// but an empty live/archive batch.
	engine.source = emptyGapSource{target: 7}
	if result, err := engine.RunCycle(context.Background()); err != nil {
		t.Fatalf("run gap cycle: %v", err)
	} else if result.Batches != 0 {
		t.Fatalf("gap cycle batches = %d, want no committed batch", result.Batches)
	}
	state, err := NewSink(db).State(context.Background(), StreamPricing)
	if err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if state.CoverageEventID != 0 || state.Status != "pending" {
		t.Fatalf("gap cycle advanced state = %#v", state)
	}
}

func TestEngineRejectsUnprovenSourceIDGap(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source := scriptedSource{
		epoch: "epoch-gap",
		events: []EventRecord{
			{ID: 1, EventHash: "h1", TimestampMS: 100, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 1, TotalTokens: 1},
			// Event 2 is absent from both the live source and the published archive.
			{ID: 3, EventHash: "h3", TimestampMS: 300, AuthIndex: "account-a", AuthProviderSnapshot: "codex", Model: "gpt-test", RequestedModel: "gpt-test", InputTokens: 3, TotalTokens: 3},
		},
		prices: map[string]model.ModelPrice{},
	}
	engine := NewEngine(EngineConfig{Source: source, Sink: NewSink(db), BatchSize: 10, MaxBatches: 1})
	if _, err := engine.RunCycle(context.Background()); !errors.Is(err, ErrSourceCoverageGap) {
		t.Fatalf("gap cycle error = %v, want ErrSourceCoverageGap", err)
	}
	state, err := NewSink(db).State(context.Background(), StreamAccountHistory)
	if err != nil {
		t.Fatalf("read account-history state: %v", err)
	}
	if state.CoverageEventID != 0 {
		t.Fatalf("gap cycle advanced coverage to %d", state.CoverageEventID)
	}
}

func TestEngineStopAndWaitIsBoundedAndIdempotent(t *testing.T) {
	engine := NewEngine(EngineConfig{})
	if err := engine.StopAndWait(context.Background()); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
	engine.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.StopAndWait(ctx); err != nil {
		t.Fatalf("stop running engine: %v", err)
	}
	if err := engine.StopAndWait(context.Background()); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestEngineStopAndWaitCancelsInFlightSourceRead(t *testing.T) {
	source := blockingSource{started: make(chan struct{})}
	engine := NewEngine(EngineConfig{
		Source: source,
		Sink:   &blockingSink{},
	})
	engine.Start(context.Background())
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("isolated engine did not start its source read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.StopAndWait(ctx); err != nil {
		t.Fatalf("stop in-flight engine: %v", err)
	}
}

type blockingSource struct {
	started chan struct{}
}

func (s blockingSource) Identity(ctx context.Context) (SourceIdentity, error) {
	return SourceIdentity{Epoch: "epoch-a"}, nil
}

func (s blockingSource) Snapshot(ctx context.Context) (SourceSnapshot, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-ctx.Done()
	return SourceSnapshot{}, ctx.Err()
}

func (blockingSource) EventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return nil, nil
}

func (blockingSource) EventsForSubjectsAfter(context.Context, []string, int64, int64, int) ([]EventRecord, error) {
	return nil, nil
}

func (blockingSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return nil, nil
}

func (blockingSource) PricingRevision(context.Context) (string, error) { return "", nil }

func (blockingSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	return map[string]model.ModelPrice{}, nil
}

type blockingSink struct{}

func (*blockingSink) State(context.Context, Stream) (StreamState, error) {
	return StreamState{}, nil
}

func (*blockingSink) Rows(context.Context, Stream, string) ([]MaterializedRow, error) {
	return nil, nil
}

func (*blockingSink) RowsByKeys(context.Context, Stream, string, []string) ([]MaterializedRow, error) {
	return nil, nil
}

func (*blockingSink) RowsForSubjects(context.Context, Stream, string, []string) ([]MaterializedRow, error) {
	return nil, nil
}

func (*blockingSink) Apply(context.Context, ApplyBatch) (StreamState, error) {
	return StreamState{}, nil
}

func (*blockingSink) Status(context.Context) (Status, error) {
	return Status{}, nil
}

type emptyGapSource struct{ target int64 }

func (s emptyGapSource) Identity(context.Context) (SourceIdentity, error) {
	return SourceIdentity{Epoch: "epoch-a"}, nil
}

func (s emptyGapSource) Snapshot(context.Context) (SourceSnapshot, error) {
	return SourceSnapshot{
		Identity:        SourceIdentity{Epoch: "epoch-a"},
		TargetEventID:   s.target,
		PricingRevision: model.ModelPriceValueRevision(map[string]model.ModelPrice{}),
	}, nil
}

func (emptyGapSource) EventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return []EventRecord{}, nil
}

func (emptyGapSource) EventsForSubjectsAfter(context.Context, []string, int64, int64, int) ([]EventRecord, error) {
	return []EventRecord{}, nil
}

func (emptyGapSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return []EventRecord{}, nil
}

func (emptyGapSource) PricingRevision(context.Context) (string, error) {
	return model.ModelPriceValueRevision(map[string]model.ModelPrice{}), nil
}

func (emptyGapSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	return map[string]model.ModelPrice{}, nil
}
