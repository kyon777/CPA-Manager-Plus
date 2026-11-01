package derivedstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// MigrateLegacyPricingGeneration promotes the current canonical value-keyed
// pricing generation into the structure-keyed generation. It operates only on
// disposable derived materializations and never reads or writes usage facts.
func MigrateLegacyPricingGeneration(ctx context.Context, sink Sink, snapshot SourceSnapshot) (bool, error) {
	sqliteSink, ok := sink.(*sqlSink)
	if !ok {
		return false, nil
	}
	return sqliteSink.migrateLegacyPricingGeneration(ctx, snapshot)
}

func currentLegacyPricingGeneration(snapshot SourceSnapshot) string {
	return legacyPricingGenerationPrefix + snapshot.Identity.Epoch + ":codex-" +
		usageidentity.CodexIdentityRevision + ":" + snapshot.PricingValueRevision
}

func (s *sqlSink) migrateLegacyPricingGeneration(ctx context.Context, snapshot SourceSnapshot) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("derived sink is unavailable")
	}
	if strings.TrimSpace(snapshot.Identity.Epoch) == "" ||
		strings.TrimSpace(snapshot.PricingStructureRevision) == "" ||
		strings.TrimSpace(snapshot.PricingValueRevision) == "" {
		return false, errors.New("derived pricing promotion requires source epoch and pricing revisions")
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin legacy pricing promotion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSourceEpoch(ctx, tx, snapshot.Identity.Epoch, now); err != nil {
		return false, err
	}
	state, exists, err := derivedStreamStateTx(ctx, tx, StreamPricing)
	if err != nil {
		return false, err
	}
	legacyGeneration := currentLegacyPricingGeneration(snapshot)
	if !exists || state.Generation != legacyGeneration ||
		(state.SourceEpoch != "" && state.SourceEpoch != snapshot.Identity.Epoch) ||
		(state.PricingValueRevision != "" && state.PricingValueRevision != snapshot.PricingValueRevision) {
		return false, nil
	}
	expectedGeneration := GenerationFor(StreamPricing, snapshot)
	if expectedGeneration == legacyGeneration {
		return false, nil
	}
	// Retain only the current legacy rows and a possible interrupted v2 attempt.
	// The v2 copy is deleted before the UPDATE so the primary key remains unique.
	if _, err := tx.ExecContext(ctx, `delete from derived_materialized_rows
		where stream_name = ? and generation <> ? and generation <> ?`,
		string(StreamPricing), legacyGeneration, expectedGeneration); err != nil {
		return false, fmt.Errorf("clear orphan pricing materializations before promotion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `delete from derived_materialized_rows
		where stream_name = ? and generation = ?`, string(StreamPricing), expectedGeneration); err != nil {
		return false, fmt.Errorf("clear interrupted v2 pricing materializations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `update derived_materialized_rows set generation = ?
		where stream_name = ? and generation = ?`, expectedGeneration, string(StreamPricing), legacyGeneration); err != nil {
		return false, fmt.Errorf("promote pricing materializations: %w", err)
	}
	// Keep the old applied-event ledger untouched. Coverage is carried by the
	// stream-state checkpoint; rewriting millions of ledger generation keys adds
	// no read correctness and would recreate the very SQLite pressure avoided by
	// separate-derived storage. A future structural rebuild purges it normally.
	if _, err := tx.ExecContext(ctx, `update derived_stream_state set
		source_epoch = ?, generation = ?, target_event_id = max(target_event_id, ?),
		pricing_structure_revision = ?, pricing_revision = ?, archive_generation = ?,
		last_transition = 'legacy_promotion', updated_at_ms = ?
		where stream_name = ?`,
		snapshot.Identity.Epoch, expectedGeneration, snapshot.TargetEventID,
		snapshot.PricingStructureRevision, snapshot.PricingValueRevision,
		snapshot.ArchiveGeneration, now, string(StreamPricing)); err != nil {
		return false, fmt.Errorf("promote pricing stream state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit legacy pricing promotion: %w", err)
	}
	return true, nil
}
