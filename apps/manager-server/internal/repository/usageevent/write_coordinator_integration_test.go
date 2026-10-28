package usageevent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestInsertBatchWaitsForBackgroundLeaseBeforeAtomicWrite(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	event := makeBaseTestEvent(canonicalTestHash("critical-write-priority"), 1_800_000_100_000)
	repo := New(db)
	coordinator := sqliterepo.CoordinatorFor(db)
	background, err := coordinator.Acquire(context.Background(), sqliterepo.WritePriorityBackground)
	if err != nil {
		t.Fatalf("hold background lease: %v", err)
	}

	started := make(chan struct{})
	result := make(chan struct {
		insert model.InsertResult
		err    error
	}, 1)
	go func() {
		close(started)
		insert, err := repo.InsertBatch(context.Background(), []usage.Event{event})
		result <- struct {
			insert model.InsertResult
			err    error
		}{insert: insert, err: err}
	}()
	<-started

	select {
	case got := <-result:
		t.Fatalf("InsertBatch completed while background lease was held: result=%#v err=%v", got.insert, got.err)
	case <-time.After(100 * time.Millisecond):
	}

	background.Release()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("InsertBatch after background release: %v", got.err)
		}
		if got.insert.Inserted != 1 {
			t.Fatalf("InsertBatch result = %#v, want one inserted event", got.insert)
		}
	case <-time.After(time.Second):
		t.Fatal("InsertBatch did not resume after background lease release")
	}

	var rawCount, ledgerCount int
	if err := db.QueryRow(`select count(*) from usage_events where event_hash = ?`, event.EventHash).Scan(&rawCount); err != nil {
		t.Fatalf("count raw event: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(&ledgerCount); err != nil {
		t.Fatalf("count identity ledger row: %v", err)
	}
	if rawCount != 1 || ledgerCount != 1 {
		t.Fatalf("persisted counts = raw:%d ledger:%d, want 1/1", rawCount, ledgerCount)
	}
}
