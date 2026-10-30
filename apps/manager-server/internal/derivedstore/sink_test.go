package derivedstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSinkAdvancesCoverageOnlyAfterCommit(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := NewSink(db)
	state, err := sink.Apply(context.Background(), ApplyBatch{
		Stream:      StreamAccountHistory,
		SourceEpoch: "epoch-a",
		Generation:  "generation-a",
		FromEventID: 0,
		ToEventID:   2,
		Events: []EventRecord{
			{ID: 1, EventHash: "h1"},
			{ID: 2, EventHash: "h2"},
		},
		Rows: []MaterializedRow{{Stream: StreamAccountHistory, Generation: "generation-a", Key: "account-a", PayloadJSON: `{"calls":2}`, FirstEventID: 1, LastEventID: 2}},
	})
	if err != nil {
		t.Fatalf("apply derived batch: %v", err)
	}
	if state.CoverageEventID != 2 || state.Status != "ready" {
		t.Fatalf("state after apply = %#v", state)
	}
	var applied int
	if err := db.QueryRow(`select count(*) from derived_applied_events where stream_name = ?`, string(StreamAccountHistory)).Scan(&applied); err != nil {
		t.Fatalf("read applied ledger: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied ledger rows = %d, want 2", applied)
	}

	// A failing batch must roll back both materialization and coverage.
	_, err = sink.Apply(context.Background(), ApplyBatch{
		Stream:      StreamAccountHistory,
		SourceEpoch: "epoch-a",
		Generation:  "generation-a",
		ToEventID:   3,
		Rows:        []MaterializedRow{{Stream: StreamAccountHistory, Generation: "generation-a", Key: "bad", PayloadJSON: ""}},
	})
	if err == nil {
		t.Fatal("invalid batch unexpectedly committed")
	}
	var coverage int64
	if err := db.QueryRow(`select coverage_event_id from derived_stream_state where stream_name = ?`, string(StreamAccountHistory)).Scan(&coverage); err != nil {
		t.Fatalf("read coverage after rollback: %v", err)
	}
	if coverage != 2 {
		t.Fatalf("coverage advanced after failed batch: %d", coverage)
	}
}

func TestSinkDoesNotAcceptEmptyPayloadRows(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := NewSink(db).Apply(context.Background(), ApplyBatch{
		Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "generation-a",
		Rows: []MaterializedRow{{Key: "x", PayloadJSON: ""}},
	}); err == nil {
		t.Fatal("empty payload unexpectedly accepted")
	}
}

func TestSinkReadsRowsByStreamAndGeneration(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := NewSink(db)
	if _, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "pricing-v1", ToEventID: 4,
		Rows: []MaterializedRow{
			{Stream: StreamPricing, Generation: "pricing-v1", Key: "b", PayloadJSON: `{"v":2}`, FirstEventID: 3, LastEventID: 4},
			{Stream: StreamPricing, Generation: "pricing-v1", Key: "a", PayloadJSON: `{"v":1}`, FirstEventID: 1, LastEventID: 2},
		},
	}); err != nil {
		t.Fatalf("apply rows: %v", err)
	}
	rows, err := sink.Rows(context.Background(), StreamPricing, "pricing-v1")
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 2 || rows[0].Key != "a" || rows[1].Key != "b" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestSinkFetchesOnlyRequestedSubjects(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := NewSink(db)
	if _, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "pricing-v1", ToEventID: 3,
		Rows: []MaterializedRow{
			{Key: "account-a\x00model-a", SubjectKey: "account-a", PayloadJSON: `{"calls":1}`},
			{Key: "account-b\x00model-a", SubjectKey: "account-b", PayloadJSON: `{"calls":1}`},
			{Key: "account-c\x00model-a", SubjectKey: "account-c", PayloadJSON: `{"calls":1}`},
		},
	}); err != nil {
		t.Fatalf("apply rows: %v", err)
	}
	rows, err := sink.RowsForSubjects(context.Background(), StreamPricing, "pricing-v1", []string{"account-b"})
	if err != nil {
		t.Fatalf("read subject rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "account-b\x00model-a" || rows[0].SubjectKey != "account-b" {
		t.Fatalf("subject rows = %#v", rows)
	}
	rows, err = sink.RowsByKeys(context.Background(), StreamPricing, "pricing-v1", []string{"account-c\x00model-a"})
	if err != nil {
		t.Fatalf("read key rows: %v", err)
	}
	if len(rows) != 1 || rows[0].SubjectKey != "account-c" {
		t.Fatalf("key rows = %#v", rows)
	}
}

func TestSinkResetsCoverageWhenGenerationChanges(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := NewSink(db)
	if _, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "pricing-v1:a", ToEventID: 10,
		Events: []EventRecord{{ID: 10, EventHash: "old"}},
		Rows:   []MaterializedRow{{Key: "old", PayloadJSON: `{"calls":1}`}},
	}); err != nil {
		t.Fatalf("apply first generation: %v", err)
	}
	state, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamPricing, SourceEpoch: "epoch-a", Generation: "pricing-v1:b", ToEventID: 2,
		Events: []EventRecord{{ID: 2, EventHash: "new"}},
		Rows:   []MaterializedRow{{Key: "new", PayloadJSON: `{"calls":1}`}},
	})
	if err != nil {
		t.Fatalf("apply replacement generation: %v", err)
	}
	if state.Generation != "pricing-v1:b" || state.CoverageEventID != 2 || state.ProcessedEvents != 1 {
		t.Fatalf("replacement state = %#v", state)
	}
	rows, err := sink.Rows(context.Background(), StreamPricing, "pricing-v1:a")
	if err != nil {
		t.Fatalf("read old generation: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("old generation rows = %#v", rows)
	}
}

func TestSinkRejectsSourceEpochChange(t *testing.T) {
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sink := NewSink(db)
	if _, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamAccountHistory, SourceEpoch: "epoch-a", Generation: "account-history-v1:a", ToEventID: 1,
		Events: []EventRecord{{ID: 1, EventHash: "a"}},
		Rows:   []MaterializedRow{{Key: "a", PayloadJSON: `{"calls":1}`}},
	}); err != nil {
		t.Fatalf("apply initial epoch: %v", err)
	}
	if _, err := sink.Apply(context.Background(), ApplyBatch{
		Stream: StreamAccountHistory, SourceEpoch: "epoch-b", Generation: "account-history-v1:b", ToEventID: 1,
		Events: []EventRecord{{ID: 1, EventHash: "b"}},
		Rows:   []MaterializedRow{{Key: "b", PayloadJSON: `{"calls":1}`}},
	}); err == nil {
		t.Fatal("source epoch change unexpectedly accepted")
	}
}

var _ *sql.DB
