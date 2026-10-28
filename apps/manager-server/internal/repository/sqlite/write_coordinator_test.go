package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteCoordinatorAllowsOnlyOneActiveLease(t *testing.T) {
	coordinator := NewWriteCoordinator()
	first, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
	if err != nil {
		t.Fatalf("acquire first background lease: %v", err)
	}
	defer first.Release()

	acquired := make(chan *WriteLease, 1)
	errs := make(chan error, 1)
	go func() {
		lease, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
		if err != nil {
			errs <- err
			return
		}
		acquired <- lease
	}()
	waitForWriteCoordinatorWaiters(t, coordinator, 0, 1)

	select {
	case lease := <-acquired:
		lease.Release()
		t.Fatal("second background lease acquired while first lease is still active")
	case err := <-errs:
		t.Fatalf("wait for second background lease: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	first.Release()
	select {
	case lease := <-acquired:
		lease.Release()
	case err := <-errs:
		t.Fatalf("acquire second background lease after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("second background lease did not acquire after release")
	}
}

func TestWriteCoordinatorPrioritizesCriticalWaiter(t *testing.T) {
	coordinator := NewWriteCoordinator()
	active, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
	if err != nil {
		t.Fatalf("acquire active background lease: %v", err)
	}

	criticalAcquired := make(chan *WriteLease, 1)
	criticalErrs := make(chan error, 1)
	go func() {
		lease, err := coordinator.Acquire(context.Background(), WritePriorityCritical)
		if err != nil {
			criticalErrs <- err
			return
		}
		criticalAcquired <- lease
	}()
	waitForWriteCoordinatorWaiters(t, coordinator, 1, 0)

	backgroundAcquired := make(chan *WriteLease, 1)
	backgroundErrs := make(chan error, 1)
	go func() {
		lease, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
		if err != nil {
			backgroundErrs <- err
			return
		}
		backgroundAcquired <- lease
	}()
	waitForWriteCoordinatorWaiters(t, coordinator, 1, 1)

	active.Release()

	var critical *WriteLease
	select {
	case critical = <-criticalAcquired:
	case err := <-criticalErrs:
		t.Fatalf("critical waiter failed: %v", err)
	case lease := <-backgroundAcquired:
		lease.Release()
		t.Fatal("background waiter acquired before waiting critical lease")
	case err := <-backgroundErrs:
		t.Fatalf("background waiter failed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("critical waiter did not acquire after active lease released")
	}

	select {
	case lease := <-backgroundAcquired:
		lease.Release()
		t.Fatal("background waiter acquired before critical lease released")
	case err := <-backgroundErrs:
		t.Fatalf("background waiter failed while critical lease active: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	critical.Release()
	select {
	case lease := <-backgroundAcquired:
		lease.Release()
	case err := <-backgroundErrs:
		t.Fatalf("background waiter failed after critical release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("background waiter did not acquire after critical lease release")
	}
}

func TestWriteCoordinatorCanceledWaiterDoesNotBlockLaterLease(t *testing.T) {
	coordinator := NewWriteCoordinator()
	active, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
	if err != nil {
		t.Fatalf("acquire active background lease: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() {
		_, err := coordinator.Acquire(ctx, WritePriorityCritical)
		waitErr <- err
	}()
	waitForWriteCoordinatorWaiters(t, coordinator, 1, 0)
	cancel()

	select {
	case err := <-waitErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled critical waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled critical waiter did not return")
	}
	waitForWriteCoordinatorWaiters(t, coordinator, 0, 0)

	active.Release()
	lease, err := coordinator.Acquire(context.Background(), WritePriorityBackground)
	if err != nil {
		t.Fatalf("acquire background lease after canceled waiter: %v", err)
	}
	lease.Release()
}

func TestCoordinatorForReturnsSameCoordinatorForSameDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first := CoordinatorFor(db)
	second := CoordinatorFor(db)
	if first != second {
		t.Fatal("CoordinatorFor returned distinct coordinators for the same database")
	}

	other, err := Open(filepath.Join(t.TempDir(), "other.sqlite"))
	if err != nil {
		t.Fatalf("open other database: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if first == CoordinatorFor(other) {
		t.Fatal("CoordinatorFor returned the same coordinator for distinct databases")
	}
}

func waitForWriteCoordinatorWaiters(t *testing.T, coordinator *WriteCoordinator, wantCritical, wantBackground int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		critical, background := coordinator.waiterCounts()
		if critical == wantCritical && background == wantBackground {
			return
		}
		time.Sleep(time.Millisecond)
	}
	critical, background := coordinator.waiterCounts()
	t.Fatalf("coordinator waiters = critical %d/background %d, want critical %d/background %d", critical, background, wantCritical, wantBackground)
}

var _ *sql.DB
