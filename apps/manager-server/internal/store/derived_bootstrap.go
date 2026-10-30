package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/derivedstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// bootstrapLegacyDerived imports only already-verified primary materialization
// rows. It is the safe starting floor for a fresh derived database when raw
// event ids have holes because older facts were archived/deleted. It never
// copies raw events, control state, credentials, or pricing secrets.
func (r *separateDerivedRuntime) bootstrapLegacyDerived(ctx context.Context) error {
	if r == nil || r.sourceDB == nil || r.source == nil || r.sink == nil {
		return errors.New("separate derived bootstrap is unavailable")
	}
	snapshot, err := r.source.Snapshot(ctx)
	if err != nil {
		return err
	}
	nowMS := time.Now().UnixMilli()

	historyState, err := r.sink.State(ctx, derivedstore.StreamAccountHistory)
	if err != nil {
		return err
	}
	if strings.TrimSpace(historyState.Generation) == "" {
		coverage, ok, err := r.legacyHistoryCoverage(ctx, snapshot.TargetEventID)
		if err != nil {
			return err
		}
		if ok {
			rows, err := r.readLegacyHistoryRows(ctx)
			if err != nil {
				return err
			}
			if err := r.applyLegacyAggregates(ctx, derivedstore.StreamAccountHistory, snapshot, coverage, rows, nowMS); err != nil {
				return err
			}
		}
	}

	pricingState, err := r.sink.State(ctx, derivedstore.StreamPricing)
	if err != nil {
		return err
	}
	if strings.TrimSpace(pricingState.Generation) == "" {
		coverage, ok, err := r.legacyPricingCoverage(ctx, snapshot.TargetEventID)
		if err != nil {
			return err
		}
		if ok {
			rows, err := r.readLegacyPricingRows(ctx, snapshot.PricingRevision)
			if err != nil {
				return err
			}
			if err := r.applyLegacyAggregates(ctx, derivedstore.StreamPricing, snapshot, coverage, rows, nowMS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *separateDerivedRuntime) legacyHistoryCoverage(ctx context.Context, target int64) (int64, bool, error) {
	if target <= 0 {
		return 0, false, nil
	}
	var version string
	if err := r.sourceDB.QueryRowContext(ctx, `select value from settings
		where key = 'usage_account_history_identity_format_version'`).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if strings.TrimSpace(version) != usageidentity.AccountHistoryStructureRevision() {
		return 0, false, nil
	}
	var coverage, rebuildTarget int64
	var lastError sql.NullString
	err := r.sourceDB.QueryRowContext(ctx, `select c.last_event_id, c.last_error,
		coalesce(r.target_event_id, 0)
		from usage_rollup_checkpoints c
		left join usage_rollup_rebuild_state r on r.name = c.name
		where c.name = ?`, usagerollup.AccountHistoryCheckpointName).Scan(&coverage, &lastError, &rebuildTarget)
	if errors.Is(err, sql.ErrNoRows) || coverage <= 0 {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(lastError.String) != "" || rebuildTarget > coverage {
		return 0, false, nil
	}
	if coverage > target {
		coverage = target
	}
	return coverage, coverage > 0, nil
}

func (r *separateDerivedRuntime) legacyPricingCoverage(ctx context.Context, target int64) (int64, bool, error) {
	if target <= 0 {
		return 0, false, nil
	}
	var schemaVersion int
	var structureRevision, status string
	var lastError sql.NullString
	var coverage int64
	err := r.sourceDB.QueryRowContext(ctx, `select schema_version, structure_revision, status,
		coverage_event_id, last_error from usage_pricing_rollup_state where rollup_name = ?`, usagepricing.RollupName).Scan(
		&schemaVersion, &structureRevision, &status, &coverage, &lastError)
	if errors.Is(err, sql.ErrNoRows) || coverage <= 0 {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if schemaVersion != usagepricing.SchemaVersion || status == "clearing" || strings.TrimSpace(lastError.String) != "" {
		return 0, false, nil
	}
	currentStructure, err := usagepricing.StructureRevision(ctx, r.sourceDB)
	if err != nil {
		return 0, false, err
	}
	if structureRevision != currentStructure {
		return 0, false, nil
	}
	if coverage > target {
		coverage = target
	}
	return coverage, coverage > 0, nil
}

func (r *separateDerivedRuntime) readLegacyHistoryRows(ctx context.Context) ([]derivedstore.AccountAggregate, error) {
	rows, err := r.sourceDB.QueryContext(ctx, `select
		account_key, coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''),
		coalesce(auth_provider_snapshot, ''), coalesce(auth_index, ''), coalesce(source, ''),
		coalesce(source_hash, ''), model, billing_model, service_tier,
		calls, success_calls, failure_calls, input_tokens, output_tokens, reasoning_tokens,
		cached_tokens, cache_read_tokens, cache_creation_tokens, long_input_tokens,
		long_output_tokens, long_cached_tokens, long_cache_read_tokens,
		long_cache_creation_tokens, total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
		from usage_account_model_rollups order by account_key, model, billing_model, service_tier`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]derivedstore.AccountAggregate, 0)
	for rows.Next() {
		var row usagerollup.AccountHistoryRow
		if err := rows.Scan(
			&row.AccountKey, &row.AccountSnapshot, &row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot, &row.AuthIndex, &row.Source, &row.SourceHash,
			&row.Model, &row.BillingModel, &row.ServiceTier, &row.Calls,
			&row.SuccessCalls, &row.FailureCalls, &row.InputTokens, &row.OutputTokens,
			&row.ReasoningTokens, &row.CachedTokens, &row.CacheReadTokens,
			&row.CacheCreationTokens, &row.LongInputTokens, &row.LongOutputTokens,
			&row.LongCachedTokens, &row.LongCacheReadTokens, &row.LongCacheCreationTokens,
			&row.TotalTokens, &row.FirstSeenMS, &row.LastSeenMS, &row.UpdatedAtMS,
		); err != nil {
			return nil, err
		}
		result = append(result, historyAggregate(row))
	}
	return result, rows.Err()
}

func (r *separateDerivedRuntime) readLegacyPricingRows(ctx context.Context, pricingRevision string) ([]derivedstore.AccountAggregate, error) {
	structureRevision, err := usagepricing.StructureRevision(ctx, r.sourceDB)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(structureRevision) == "" {
		return nil, errors.New("primary pricing structure revision is empty")
	}
	rows, err := r.sourceDB.QueryContext(ctx, `select
		account_key, coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''),
		coalesce(auth_provider_snapshot, ''), coalesce(auth_index, ''), coalesce(source, ''),
		coalesce(source_hash, ''), model, billing_model, pricing_model, service_tier,
		context_threshold_tokens, calls, success_calls, failure_calls, input_tokens,
		output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens,
		cache_creation_tokens, long_input_tokens, long_output_tokens, long_cached_tokens,
		long_cache_read_tokens, long_cache_creation_tokens, total_tokens, first_seen_ms,
		last_seen_ms, updated_at_ms
		from usage_pricing_account_rollups_v1 where structure_revision = ?
		order by account_key, model, billing_model, pricing_model, service_tier,
		context_threshold_tokens`, structureRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]derivedstore.AccountAggregate, 0)
	for rows.Next() {
		var row usagepricing.AccountRow
		if err := rows.Scan(
			&row.AccountKey, &row.AccountSnapshot, &row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot, &row.AuthIndex, &row.Source, &row.SourceHash,
			&row.Model, &row.BillingModel, &row.PricingModel, &row.ServiceTier,
			&row.ContextThresholdTokens, &row.Calls, &row.SuccessCalls, &row.FailureCalls,
			&row.InputTokens, &row.OutputTokens, &row.ReasoningTokens, &row.CachedTokens,
			&row.CacheReadTokens, &row.CacheCreationTokens, &row.LongInputTokens,
			&row.LongOutputTokens, &row.LongCachedTokens, &row.LongCacheReadTokens,
			&row.LongCacheCreationTokens, &row.TotalTokens, &row.FirstSeenMS,
			&row.LastSeenMS, &row.UpdatedAtMS,
		); err != nil {
			return nil, err
		}
		result = append(result, pricingAggregate(row))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func historyAggregate(row usagerollup.AccountHistoryRow) derivedstore.AccountAggregate {
	return derivedstore.AccountAggregate{
		AccountKey: row.AccountKey, AccountSnapshot: row.AccountSnapshot,
		AuthLabelSnapshot: row.AuthLabelSnapshot, AuthProviderSnapshot: row.AuthProviderSnapshot,
		AuthIndex: row.AuthIndex, Source: row.Source, SourceHash: row.SourceHash,
		Model: row.Model, BillingModel: row.BillingModel, ServiceTier: row.ServiceTier,
		Calls: row.Calls, SuccessCalls: row.SuccessCalls, FailureCalls: row.FailureCalls,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		ReasoningTokens: row.ReasoningTokens, CachedTokens: row.CachedTokens,
		CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens,
		LongInputTokens: row.LongInputTokens, LongOutputTokens: row.LongOutputTokens,
		LongCachedTokens: row.LongCachedTokens, LongCacheReadTokens: row.LongCacheReadTokens,
		LongCacheCreationTokens: row.LongCacheCreationTokens, TotalTokens: row.TotalTokens,
		FirstSeenMS: row.FirstSeenMS, LastSeenMS: row.LastSeenMS, UpdatedAtMS: row.UpdatedAtMS,
	}
}

func pricingAggregate(row usagepricing.AccountRow) derivedstore.AccountAggregate {
	history := usagerollup.AccountHistoryRow{
		AccountKey: row.AccountKey, AccountSnapshot: row.AccountSnapshot,
		AuthLabelSnapshot: row.AuthLabelSnapshot, AuthProviderSnapshot: row.AuthProviderSnapshot,
		AuthIndex: row.AuthIndex, Source: row.Source, SourceHash: row.SourceHash,
		Model: row.Model, BillingModel: row.BillingModel, ServiceTier: row.ServiceTier,
		Calls: row.Calls, SuccessCalls: row.SuccessCalls, FailureCalls: row.FailureCalls,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		ReasoningTokens: row.ReasoningTokens, CachedTokens: row.CachedTokens,
		CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens,
		TotalTokens: row.TotalTokens,
		FirstSeenMS: row.FirstSeenMS, LastSeenMS: row.LastSeenMS, UpdatedAtMS: row.UpdatedAtMS,
	}
	history.LongInputTokens = row.LongInputTokens
	history.LongOutputTokens = row.LongOutputTokens
	history.LongCachedTokens = row.LongCachedTokens
	history.LongCacheReadTokens = row.LongCacheReadTokens
	history.LongCacheCreationTokens = row.LongCacheCreationTokens
	aggregate := historyAggregate(history)
	aggregate.PricingModel = row.PricingModel
	aggregate.ContextThresholdTokens = row.ContextThresholdTokens
	return aggregate
}
func (r *separateDerivedRuntime) applyLegacyAggregates(ctx context.Context, stream derivedstore.Stream, snapshot derivedstore.SourceSnapshot, coverage int64, aggregates []derivedstore.AccountAggregate, nowMS int64) error {
	generation := derivedstore.GenerationFor(stream, snapshot)
	if coverage <= 0 {
		return nil
	}
	materialized := make([]derivedstore.MaterializedRow, 0, len(aggregates))
	for _, aggregate := range aggregates {
		row, err := derivedstore.MaterializedRowForAggregate(stream, generation, aggregate, 0, coverage, nowMS)
		if err != nil {
			return err
		}
		materialized = append(materialized, row)
	}
	if _, err := r.sink.Apply(ctx, derivedstore.ApplyBatch{
		Stream: stream, SourceEpoch: snapshot.Identity.Epoch, Generation: generation,
		PricingRevision: snapshot.PricingRevision, ArchiveGeneration: snapshot.ArchiveGeneration,
		ToEventID: coverage, Rows: materialized, AppliedAtMS: nowMS,
	}); err != nil {
		return fmt.Errorf("bootstrap %s derived rows: %w", stream, err)
	}
	return nil
}
