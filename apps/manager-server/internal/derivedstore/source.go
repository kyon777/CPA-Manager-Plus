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

// subjectTailScanLimit bounds the primary-source work a visible-page read may
// perform while the isolated worker is catching up. The result remains
// explicitly pending when the worker is behind; this limit prevents a sparse
// page lookup from walking the entire historical event table.
const subjectTailScanLimit int64 = 50_000

func boundedSubjectTailRange(afterID, targetID int64) (int64, int64, bool) {
	if targetID <= afterID {
		return afterID, targetID, true
	}
	floor := targetID - subjectTailScanLimit
	if floor < 0 {
		floor = 0
	}
	if floor < afterID {
		floor = afterID
	}
	return floor, targetID, floor == afterID
}

type sqlSource struct {
	db         *sql.DB
	archiveDir string
}

// NewSource wraps a query-only primary connection. It accepts *sql.DB only so
// tests can inject a read-only connection; production callers must pass the
// result of sqlite.OpenReadOnly.
func NewSource(db *sql.DB) Source {
	if db == nil {
		return &sqlSource{}
	}
	return &sqlSource{db: db}
}

// NewSourceWithArchiveDir attaches the published archive directory to the
// query-only primary source. Archive files are opened only for event refs that
// have already been marked raw_deleted; the source never writes either store.
func NewSourceWithArchiveDir(db *sql.DB, archiveDir string) Source {
	return &sqlSource{db: db, archiveDir: strings.TrimSpace(archiveDir)}
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
	if s == nil || s.db == nil {
		return SourceSnapshot{}, errors.New("derived source is unavailable")
	}
	identity, err := s.Identity(ctx)
	if err != nil {
		return SourceSnapshot{}, err
	}
	var target, archivedTarget int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&target); err != nil {
		return SourceSnapshot{}, fmt.Errorf("read usage source target: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(raw_event_id), 0)
		from usage_archive_event_refs where raw_deleted_at_ms is not null`).Scan(&archivedTarget); err != nil {
		return SourceSnapshot{}, fmt.Errorf("read archived usage source target: %w", err)
	}
	if archivedTarget > target {
		target = archivedTarget
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
	if s == nil || s.db == nil {
		return nil, errors.New("derived source is unavailable")
	}
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

// EventsForSubjectsAfter reads only the live tail for the requested visible
// account subjects. The id range remains bounded by the derived watermark and
// the target snapshot; the account-key predicate prevents a current-page
// request from scanning and decoding unrelated credentials in Go.
func (s *sqlSource) EventsForSubjectsAfter(ctx context.Context, subjectKeys []string, afterID, targetID int64, limit int) ([]EventRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived source is unavailable")
	}
	keys := normalizeSubjectKeys(subjectKeys)
	if limit <= 0 || len(keys) == 0 || targetID <= afterID {
		return []EventRecord{}, nil
	}
	boundedAfterID, boundedTargetID, complete := boundedSubjectTailRange(afterID, targetID)
	if !complete {
		// A truncated range cannot be merged into a verified aggregate without
		// under-counting the omitted middle. Leave the page on its last verified
		// snapshot and let the isolated worker catch up asynchronously.
		return []EventRecord{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	query := `select
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
		from usage_events e
		where e.id > ? and e.id <= ? and ` + usageidentity.SQLAccountKeyExpression("e") + ` in (` + placeholders + `)
		order by e.id asc limit ?`
	args := make([]any, 0, len(keys)+3)
	args = append(args, boundedAfterID, boundedTargetID)
	for _, key := range keys {
		args = append(args, key)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read bounded subject usage tail: %w", err)
	}
	defer rows.Close()
	result := make([]EventRecord, 0, minInt(limit, 256))
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

func normalizeSubjectKeys(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
