package derivedstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type scriptedSource struct {
	epoch  string
	events []EventRecord
	prices map[string]model.ModelPrice
}

func (s scriptedSource) Identity(context.Context) (SourceIdentity, error) {
	return SourceIdentity{Epoch: s.epoch}, nil
}

func (s scriptedSource) Snapshot(context.Context) (SourceSnapshot, error) {
	return SourceSnapshot{Identity: SourceIdentity{Epoch: s.epoch}, TargetEventID: int64(len(s.events)), PricingRevision: "price-rev"}, nil
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

func (s scriptedSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return nil, nil
}

func (s scriptedSource) PricingRevision(context.Context) (string, error) { return "price-rev", nil }
func (s scriptedSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	return s.prices, nil
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
