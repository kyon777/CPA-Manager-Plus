package derivedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// MigrateCodexEmailIdentity folds the prior workspace/file-keyed Codex
// materializations into the current email-keyed generation. It operates only
// on the disposable separate-derived database; raw events, primary rollups,
// credentials, proxy settings, and billing control state are never touched.
//
// The function intentionally accepts Sink so callers retain the existing
// source/sink boundary. Only the built-in SQLite sink has persistent rows to
// migrate; test or alternate sinks simply have no on-disk legacy generation.
func MigrateCodexEmailIdentity(ctx context.Context, sink Sink, snapshot SourceSnapshot) (bool, error) {
	sqliteSink, ok := sink.(*sqlSink)
	if !ok {
		return false, nil
	}
	return sqliteSink.migrateCodexEmailIdentity(ctx, snapshot)
}

func (s *sqlSink) migrateCodexEmailIdentity(ctx context.Context, snapshot SourceSnapshot) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("derived sink is unavailable")
	}
	if strings.TrimSpace(snapshot.Identity.Epoch) == "" {
		return false, errors.New("derived source epoch is required")
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin derived codex identity fold: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSourceEpoch(ctx, tx, snapshot.Identity.Epoch, now); err != nil {
		return false, err
	}

	migrated := false
	for _, stream := range []Stream{StreamAccountHistory, StreamPricing} {
		state, exists, err := derivedStreamStateTx(ctx, tx, stream)
		if err != nil {
			return false, err
		}
		if !exists || !legacyCodexIdentityGeneration(stream, snapshot, state.Generation) {
			continue
		}
		if state.SourceEpoch != "" && state.SourceEpoch != snapshot.Identity.Epoch {
			return false, fmt.Errorf("derived source epoch mismatch for %s: have %s, got %s", stream, state.SourceEpoch, snapshot.Identity.Epoch)
		}
		if err := migrateCodexDerivedStream(ctx, tx, stream, state, snapshot, now); err != nil {
			return false, err
		}
		migrated = true
	}
	if migrated {
		// Exact per-account cost snapshots are keyed by the old account
		// discriminator and are disposable. Clear them only after both streams
		// were atomically folded; the isolated price worker refills the new keys.
		if _, err := tx.ExecContext(ctx, `delete from usage_pricing_account_price_cache_v1`); err != nil {
			return false, fmt.Errorf("clear derived price cache during codex identity fold: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit derived codex identity fold: %w", err)
	}
	return migrated, nil
}

func derivedStreamStateTx(ctx context.Context, tx *sql.Tx, stream Stream) (StreamState, bool, error) {
	var state StreamState
	var streamName string
	err := tx.QueryRowContext(ctx, `select stream_name, source_epoch, generation,
		status, coverage_event_id, target_event_id, pricing_revision,
		archive_generation, processed_events, coalesce(last_success_at_ms, 0),
		last_error_code from derived_stream_state where stream_name = ?`, string(stream)).Scan(
		&streamName, &state.SourceEpoch, &state.Generation, &state.Status,
		&state.CoverageEventID, &state.TargetEventID, &state.PricingRevision,
		&state.ArchiveGeneration, &state.ProcessedEvents, &state.LastSuccessAtMS,
		&state.LastErrorCode,
	)
	switch {
	case err == nil:
		state.Stream = Stream(streamName)
		return state, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return StreamState{Stream: stream, Status: "pending"}, false, nil
	default:
		return StreamState{}, false, fmt.Errorf("read derived %s state for codex identity fold: %w", stream, err)
	}
}

func legacyCodexIdentityGeneration(stream Stream, snapshot SourceSnapshot, generation string) bool {
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return false
	}
	switch stream {
	case StreamPricing:
		return generation == pricingGenerationPrefix+snapshot.Identity.Epoch+":"+snapshot.PricingRevision ||
			generation == pricingGenerationPrefix+snapshot.Identity.Epoch+":codex-2:"+snapshot.PricingRevision
	case StreamAccountHistory:
		return generation == accountHistoryGenerationPrefix+snapshot.Identity.Epoch ||
			generation == accountHistoryGenerationPrefix+snapshot.Identity.Epoch+":codex-2"
	default:
		return false
	}
}

func migrateCodexDerivedStream(ctx context.Context, tx *sql.Tx, stream Stream, state StreamState, snapshot SourceSnapshot, now int64) error {
	expectedGeneration := GenerationFor(stream, snapshot)
	rows, err := materializedRowsForIdentityFold(ctx, tx, stream, state.Generation, expectedGeneration)
	if err != nil {
		return err
	}
	folded, err := foldCodexDerivedRows(stream, expectedGeneration, rows)
	if err != nil {
		return err
	}
	// The current generation is the only reader-visible generation. Removing
	// all older rows/ledger entries after successful decode prevents an orphaned
	// workspace bucket from resurfacing on a later restart.
	if _, err := tx.ExecContext(ctx, `delete from derived_materialized_rows where stream_name = ?`, string(stream)); err != nil {
		return fmt.Errorf("clear stale %s materializations: %w", stream, err)
	}
	if _, err := tx.ExecContext(ctx, `delete from derived_applied_events where stream_name = ?`, string(stream)); err != nil {
		return fmt.Errorf("clear stale %s applied ledger: %w", stream, err)
	}
	if err := insertFoldedDerivedRows(ctx, tx, folded); err != nil {
		return err
	}
	target := state.TargetEventID
	if snapshot.TargetEventID > target {
		target = snapshot.TargetEventID
	}
	if _, err := tx.ExecContext(ctx, `update derived_stream_state set
		source_epoch = ?, generation = ?, target_event_id = ?, pricing_revision = ?,
		archive_generation = ?, updated_at_ms = ?
		where stream_name = ?`,
		snapshot.Identity.Epoch, expectedGeneration, target, snapshot.PricingRevision,
		snapshot.ArchiveGeneration, now, string(stream)); err != nil {
		return fmt.Errorf("advance %s codex identity generation: %w", stream, err)
	}
	return nil
}

func materializedRowsForIdentityFold(ctx context.Context, tx *sql.Tx, stream Stream, currentGeneration, expectedGeneration string) ([]MaterializedRow, error) {
	rows, err := tx.QueryContext(ctx, `select stream_name, generation, row_key, subject_key,
		payload_json, first_event_id, last_event_id, updated_at_ms
		from derived_materialized_rows
		where stream_name = ? and generation in (?, ?)
		order by generation, row_key`, string(stream), currentGeneration, expectedGeneration)
	if err != nil {
		return nil, fmt.Errorf("read %s legacy materializations: %w", stream, err)
	}
	return scanMaterializedRows(rows)
}

func foldCodexDerivedRows(stream Stream, generation string, rows []MaterializedRow) ([]MaterializedRow, error) {
	byKey := make(map[string]MaterializedRow, len(rows))
	for _, row := range rows {
		var aggregate AccountAggregate
		if err := json.Unmarshal([]byte(row.PayloadJSON), &aggregate); err != nil {
			return nil, fmt.Errorf("decode %s legacy materialization %q: %w", stream, row.Key, err)
		}
		canonicalizeCodexDerivedAggregate(&aggregate)
		key := aggregateKey(stream, aggregate.AccountKey, aggregate.Model, aggregate.BillingModel,
			aggregate.PricingModel, aggregate.ServiceTier, aggregate.ContextThresholdTokens)
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s legacy materialization %q has no aggregate key", stream, row.Key)
		}
		if existing, exists := byKey[key]; exists {
			var current AccountAggregate
			if err := json.Unmarshal([]byte(existing.PayloadJSON), &current); err != nil {
				return nil, fmt.Errorf("decode folded %s materialization %q: %w", stream, key, err)
			}
			current = mergeCodexDerivedAggregate(current, aggregate)
			updated, err := MaterializedRowForAggregate(stream, generation, current,
				earliestDerivedEventID(existing.FirstEventID, row.FirstEventID),
				maxDerivedEventID(existing.LastEventID, row.LastEventID),
				maxDerivedEventID(existing.UpdatedAtMS, row.UpdatedAtMS),
			)
			if err != nil {
				return nil, err
			}
			byKey[key] = updated
			continue
		}
		updated, err := MaterializedRowForAggregate(stream, generation, aggregate,
			row.FirstEventID, row.LastEventID, row.UpdatedAtMS)
		if err != nil {
			return nil, err
		}
		byKey[key] = updated
	}
	result := make([]MaterializedRow, 0, len(byKey))
	for _, row := range byKey {
		result = append(result, row)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

func canonicalizeCodexDerivedAggregate(aggregate *AccountAggregate) {
	if aggregate == nil {
		return
	}
	provider := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(aggregate.AuthProviderSnapshot), "_", "-"))
	if provider != "codex" {
		return
	}
	email, ok := usageidentity.NormalizeCodexMemberSnapshot(aggregate.AccountSnapshot)
	if !ok {
		return
	}
	key, ok := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot: "codex",
		AccountSnapshot:      email,
	})
	if !ok {
		return
	}
	aggregate.AccountKey = key
	aggregate.AccountSnapshot = email
	aggregate.AuthProviderSnapshot = "codex"
}

func mergeCodexDerivedAggregate(current, incoming AccountAggregate) AccountAggregate {
	useIncomingMetadata := incoming.LastSeenMS > current.LastSeenMS ||
		(incoming.LastSeenMS == current.LastSeenMS && incoming.UpdatedAtMS > current.UpdatedAtMS) ||
		(incoming.LastSeenMS == current.LastSeenMS && incoming.UpdatedAtMS == current.UpdatedAtMS &&
			incoming.Source+"\x00"+incoming.AuthIndex > current.Source+"\x00"+current.AuthIndex)
	if useIncomingMetadata {
		current.AccountSnapshot = incoming.AccountSnapshot
		current.AuthLabelSnapshot = incoming.AuthLabelSnapshot
		current.AuthProviderSnapshot = incoming.AuthProviderSnapshot
		current.AuthIndex = incoming.AuthIndex
		current.Source = incoming.Source
		current.SourceHash = incoming.SourceHash
	}
	current.Calls += incoming.Calls
	current.SuccessCalls += incoming.SuccessCalls
	current.FailureCalls += incoming.FailureCalls
	current.InputTokens += incoming.InputTokens
	current.OutputTokens += incoming.OutputTokens
	current.ReasoningTokens += incoming.ReasoningTokens
	current.CachedTokens += incoming.CachedTokens
	current.CacheReadTokens += incoming.CacheReadTokens
	current.CacheCreationTokens += incoming.CacheCreationTokens
	current.LongInputTokens += incoming.LongInputTokens
	current.LongOutputTokens += incoming.LongOutputTokens
	current.LongCachedTokens += incoming.LongCachedTokens
	current.LongCacheReadTokens += incoming.LongCacheReadTokens
	current.LongCacheCreationTokens += incoming.LongCacheCreationTokens
	current.TotalTokens += incoming.TotalTokens
	current.FirstSeenMS = earliestDerivedEventID(current.FirstSeenMS, incoming.FirstSeenMS)
	current.LastSeenMS = maxDerivedEventID(current.LastSeenMS, incoming.LastSeenMS)
	current.UpdatedAtMS = maxDerivedEventID(current.UpdatedAtMS, incoming.UpdatedAtMS)
	return current
}

func insertFoldedDerivedRows(ctx context.Context, tx *sql.Tx, rows []MaterializedRow) error {
	statement, err := tx.PrepareContext(ctx, `insert into derived_materialized_rows(
		stream_name, generation, row_key, subject_key, payload_json,
		first_event_id, last_event_id, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare folded derived materialization insert: %w", err)
	}
	defer statement.Close()
	for _, row := range rows {
		if _, err := statement.ExecContext(ctx, string(row.Stream), row.Generation, row.Key, row.SubjectKey,
			row.PayloadJSON, row.FirstEventID, row.LastEventID, row.UpdatedAtMS); err != nil {
			return fmt.Errorf("insert folded derived materialization %s: %w", row.Key, err)
		}
	}
	return nil
}

func earliestDerivedEventID(current, incoming int64) int64 {
	switch {
	case current <= 0:
		return incoming
	case incoming <= 0:
		return current
	case incoming < current:
		return incoming
	default:
		return current
	}
}

func maxDerivedEventID(current, incoming int64) int64 {
	if incoming > current {
		return incoming
	}
	return current
}
