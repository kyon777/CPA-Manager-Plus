package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// WritePriority determines which database writes are allowed to acquire the
// process-local writer lease first. It does not alter SQLite transaction
// behavior; it only prevents CPAM-owned write transactions from needlessly
// competing with each other.
type WritePriority uint8

const (
	// WritePriorityCritical is reserved for irrecoverable source-of-truth writes,
	// such as raw usage-event persistence.
	WritePriorityCritical WritePriority = iota + 1
	// WritePriorityBackground is used by rebuildable projections and maintenance.
	WritePriorityBackground
)

var errInvalidWritePriority = errors.New("invalid sqlite write priority")

// WriteCoordinator serializes CPAM-owned write transactions for one SQLite
// database. Critical waiters are selected before Background waiters whenever a
// lease is released. SQLite remains the final lock authority for any external
// writer or legacy path not yet routed through this coordinator.
type WriteCoordinator struct {
	mu sync.Mutex

	active            bool
	criticalWaiters   int
	backgroundWaiters int
	notification      chan struct{}
}

// WriteLease owns one coordinator slot until Release is called. Release is
// idempotent so callers can safely defer it alongside explicit early returns.
type WriteLease struct {
	coordinator *WriteCoordinator
	releaseOnce sync.Once
}

var writeCoordinatorRegistry sync.Map // map[*sql.DB]*WriteCoordinator

// NewWriteCoordinator creates an independent coordinator. Production callers
// should use CoordinatorFor so repositories sharing a database share a lease.
func NewWriteCoordinator() *WriteCoordinator {
	return &WriteCoordinator{notification: make(chan struct{})}
}

// CoordinatorFor returns the process-local coordinator associated with db.
// A nil database cannot safely share a lease, so it receives an independent
// coordinator rather than poisoning the registry with a nil key.
func CoordinatorFor(db *sql.DB) *WriteCoordinator {
	if db == nil {
		return NewWriteCoordinator()
	}
	candidate := NewWriteCoordinator()
	actual, _ := writeCoordinatorRegistry.LoadOrStore(db, candidate)
	return actual.(*WriteCoordinator)
}

// Acquire waits for an exclusive lease. A Critical waiter is always preferred
// over Background waiters at the next lease boundary. It never opens a SQL
// transaction while waiting, so cancellation cannot leave a database lock.
func (c *WriteCoordinator) Acquire(ctx context.Context, priority WritePriority) (*WriteLease, error) {
	if c == nil {
		return nil, errors.New("nil sqlite write coordinator")
	}
	if !priority.valid() {
		return nil, errInvalidWritePriority
	}
	if ctx == nil {
		ctx = context.Background()
	}

	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			if waiting {
				c.mu.Lock()
				c.removeWaiterLocked(priority)
				c.signalLocked()
				c.mu.Unlock()
			}
			return nil, err
		}

		c.mu.Lock()
		if !c.active && (priority == WritePriorityCritical || c.criticalWaiters == 0) {
			if waiting {
				c.removeWaiterLocked(priority)
			}
			c.active = true
			c.mu.Unlock()
			return &WriteLease{coordinator: c}, nil
		}
		if !waiting {
			c.addWaiterLocked(priority)
			waiting = true
		}
		notification := c.notification
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			c.mu.Lock()
			if waiting {
				c.removeWaiterLocked(priority)
				waiting = false
				c.signalLocked()
			}
			c.mu.Unlock()
			return nil, ctx.Err()
		case <-notification:
		}
	}
}

// Release returns the lease to the next queued writer. It is safe to invoke
// more than once and safe to invoke on a nil lease.
func (l *WriteLease) Release() {
	if l == nil || l.coordinator == nil {
		return
	}
	l.releaseOnce.Do(func() {
		l.coordinator.mu.Lock()
		l.coordinator.active = false
		l.coordinator.signalLocked()
		l.coordinator.mu.Unlock()
	})
}

func (priority WritePriority) valid() bool {
	return priority == WritePriorityCritical || priority == WritePriorityBackground
}

func (c *WriteCoordinator) addWaiterLocked(priority WritePriority) {
	if priority == WritePriorityCritical {
		c.criticalWaiters++
		return
	}
	c.backgroundWaiters++
}

func (c *WriteCoordinator) removeWaiterLocked(priority WritePriority) {
	if priority == WritePriorityCritical {
		if c.criticalWaiters > 0 {
			c.criticalWaiters--
		}
		return
	}
	if c.backgroundWaiters > 0 {
		c.backgroundWaiters--
	}
}

func (c *WriteCoordinator) signalLocked() {
	close(c.notification)
	c.notification = make(chan struct{})
}

func (c *WriteCoordinator) waiterCounts() (critical, background int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.criticalWaiters, c.backgroundWaiters
}
