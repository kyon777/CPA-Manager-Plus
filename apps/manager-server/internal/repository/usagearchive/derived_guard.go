package usagearchive

import (
	"context"
	"errors"
)

// ErrDerivedCoverageIncomplete means an optional, external derived-storage
// guard could not prove that an archive delete is safe. It is intentionally a
// separate sentinel so callers can preserve the existing archive error while
// exposing a concise, actionable maintenance state.
var ErrDerivedCoverageIncomplete = errors.New("separate derived coverage is incomplete")

// DerivedCoverageGuard is evaluated before StatusDeleting is entered. It must
// never write to the primary database. A false or error result blocks raw
// deletion; it never weakens the repository's built-in retention proofs.
type DerivedCoverageGuard interface {
	ValidateArchiveDelete(context.Context, Run) error
}

type derivedCoverageGuardFunc func(context.Context, Run) error

func (f derivedCoverageGuardFunc) ValidateArchiveDelete(ctx context.Context, run Run) error {
	return f(ctx, run)
}

// SetDerivedCoverageGuard installs an optional process-local safety guard.
// Supplying nil removes it, which preserves the legacy primary-store mode.
// The guard is intentionally checked by BeginDelete rather than inside the
// delete transaction: it owns a different SQLite connection and must not be
// made part of a cross-database transaction.
func (r *Repository) SetDerivedCoverageGuard(guard DerivedCoverageGuard) {
	if r == nil {
		return
	}
	r.guardMu.Lock()
	r.derivedCoverageGuard = guard
	r.guardMu.Unlock()
}

func (r *Repository) validateDerivedCoverage(ctx context.Context, run Run) error {
	if r == nil {
		return ErrDerivedCoverageIncomplete
	}
	r.guardMu.RLock()
	guard := r.derivedCoverageGuard
	r.guardMu.RUnlock()
	if guard == nil {
		return nil
	}
	if err := guard.ValidateArchiveDelete(ctx, run); err != nil {
		return errors.Join(ErrDerivedCoverageIncomplete, err)
	}
	return nil
}
