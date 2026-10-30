package derivedstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type sqlSource struct{ db *sql.DB }

// NewSource wraps a query-only primary connection. It accepts *sql.DB only so
// tests can inject a read-only connection; production callers must pass the
// result of sqlite.OpenReadOnly.
func NewSource(db *sql.DB) Source {
	if db == nil {
		return &sqlSource{}
	}
	return &sqlSource{db: db}
}

func (s *sqlSource) Identity(ctx context.Context) (SourceIdentity, error) {
	if s == nil || s.db == nil {
		return SourceIdentity{}, errors.New("derived source is unavailable")
	}
	var epoch string
	if err := s.db.QueryRowContext(ctx, `select source_epoch from usage_source_identity where singleton = 1`).Scan(&epoch); err != nil {
		return SourceIdentity{}, fmt.Errorf("read usage source identity: %w", err)
	}
	if strings.TrimSpace(epoch) == "" {
		return SourceIdentity{}, errors.New("usage source identity is empty")
	}
	return SourceIdentity{Epoch: epoch}, nil
}

func (s *sqlSource) Snapshot(ctx context.Context) (SourceSnapshot, error) {
	identity, err := s.Identity(ctx)
	if err != nil {
		return SourceSnapshot{}, err
	}
	var target int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&target); err != nil {
		return SourceSnapshot{}, fmt.Errorf("read usage source target: %w", err)
	}
	pricingRevision, err := s.PricingRevision(ctx)
	if err != nil {
		return SourceSnapshot{}, err
	}
	archiveGeneration, err := latestArchiveGeneration(ctx, s.db)
	if err != nil {
		return SourceSnapshot{}, err
	}
	return SourceSnapshot{
		Identity:          identity,
		TargetEventID:     target,
		ArchiveGeneration: archiveGeneration,
		PricingRevision:   pricingRevision,
	}, nil
}

func (s *sqlSource) PricingRevision(ctx context.Context) (string, error) {
	prices, err := s.ModelPrices(ctx)
	if err != nil {
		return "", fmt.Errorf("read model price revision: %w", err)
	}
	return model.ModelPriceValueRevision(prices), nil
}

// ModelPrices takes one read-only transaction so a pricing revision and its
// rates always describe the same source snapshot. The model-price repository
// is read-only here; it never receives the primary Store writer connection.
func (s *sqlSource) ModelPrices(ctx context.Context) (map[string]model.ModelPrice, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived source is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin model price source read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prices, err := modelprice.New(s.db).LoadAllTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read model prices: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model price source read: %w", err)
	}
	return prices, nil
}

func (s *sqlSource) EventsAfter(ctx context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		return []EventRecord{}, nil
	}
	if targetID <= afterID {
		return []EventRecord{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `select
		id, coalesce(event_hash, ''), timestamp_ms, coalesce(provider, ''), coalesce(executor_type, ''), coalesce(model, ''),
		coalesce(requested_model, ''), coalesce(resolved_model, ''), coalesce(auth_index, ''), coalesce(source, ''), coalesce(source_hash, ''),
		coalesce(api_key_hash, ''), coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''), coalesce(auth_file_snapshot, ''),
		coalesce(auth_provider_snapshot, ''), coalesce(auth_account_id_snapshot, ''), coalesce(auth_project_id_snapshot, ''),
		coalesce(service_tier, ''), coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(cached_tokens, 0),
		coalesce(cache_tokens, 0), coalesce(cache_read_tokens, 0), coalesce(cache_creation_tokens, 0),
		coalesce(normalized_total_input_tokens, input_tokens, 0), coalesce(total_tokens, 0), latency_ms, coalesce(failed, 0),
		coalesce(fail_status_code, 0), coalesce(fail_summary, ''), coalesce(header_quota_recover_at_ms, 0),
		header_quota_used_percent, coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''),
		coalesce(header_error_code, ''), coalesce(header_trace_id, '')
		from usage_events
		where id > ? and id <= ?
		order by id asc limit ?`, afterID, targetID, limit)
	if err != nil {
		return nil, fmt.Errorf("read bounded usage source batch: %w", err)
	}
	defer rows.Close()
	result := make([]EventRecord, 0, limit)
	for rows.Next() {
		event, err := scanEventRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ArchivedEventsAfter is deliberately fail-closed until an archive reader is
// attached. A worker must not pretend that deleted raw events are covered by a
// partial archive scan.
func (s *sqlSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error) {
	return nil, errors.New("archived derived source is not configured")
}

type scanner interface{ Scan(...any) error }

func scanEventRecord(row scanner) (EventRecord, error) {
	var event EventRecord
	var failed int
	var latency sql.NullInt64
	var quotaUsed sql.NullFloat64
	if err := row.Scan(
		&event.ID, &event.EventHash, &event.TimestampMS, &event.Provider, &event.ExecutorType,
		&event.Model, &event.RequestedModel, &event.ResolvedModel, &event.AuthIndex, &event.Source,
		&event.SourceHash, &event.APIKeyHash, &event.AccountSnapshot, &event.AuthLabelSnapshot,
		&event.AuthFileSnapshot, &event.AuthProviderSnapshot, &event.AuthAccountIDSnapshot,
		&event.AuthProjectIDSnapshot, &event.ServiceTier, &event.InputTokens, &event.OutputTokens,
		&event.ReasoningTokens, &event.CachedTokens, &event.CacheTokens, &event.CacheReadTokens,
		&event.CacheCreationTokens, &event.NormalizedInputTokens, &event.TotalTokens, &latency,
		&failed, &event.FailStatusCode, &event.FailSummary, &event.HeaderQuotaRecoverAt,
		&quotaUsed, &event.HeaderQuotaPlanType, &event.HeaderErrorKind, &event.HeaderErrorCode,
		&event.HeaderTraceID,
	); err != nil {
		return EventRecord{}, err
	}
	event.Failed = failed != 0
	if latency.Valid {
		value := latency.Int64
		event.LatencyMS = &value
	}
	if quotaUsed.Valid {
		value := quotaUsed.Float64
		event.HeaderQuotaUsed = &value
	}
	if event.NormalizedInputTokens == 0 {
		event.NormalizedInputTokens = event.InputTokens
	}
	return event, nil
}

func accountKey(event EventRecord) string {
	key, _ := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:      event.AuthFileSnapshot,
		AuthIndex:             event.AuthIndex,
		AuthProviderSnapshot:  event.AuthProviderSnapshot,
		AuthAccountIDSnapshot: event.AuthAccountIDSnapshot,
		AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
		AccountSnapshot:       event.AccountSnapshot,
		AuthLabelSnapshot:     event.AuthLabelSnapshot,
		Source:                event.Source,
	})
	return key
}

func latestArchiveGeneration(ctx context.Context, db *sql.DB) (string, error) {
	var generation sql.NullString
	err := db.QueryRowContext(ctx, `select id from usage_archive_runs
		where status in ('completed', 'verified') order by coalesce(completed_at_ms, updated_at_ms) desc limit 1`).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read archive generation: %w", err)
	}
	return generation.String, nil
}

// AccountKeyForEvent is exported for pure transformation packages without
// exposing the source database implementation.
func AccountKeyForEvent(event EventRecord) string { return accountKey(event) }
