// Package servererrorpriority provides the SQLite source of truth for CPAM's
// server-error priority-demotion work. It never stores credential secrets and
// never changes usage, billing, quota, or rollup rows.
package servererrorpriority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	priorityevent "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/servererrorpriority"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

var (
	ErrNotBootstrapped   = errors.New("server-error priority demotion queue is not bootstrapped")
	ErrSourceEventAbsent = errors.New("server-error priority demotion source event is absent")
)

// Repository is intentionally narrow. Its source of truth is SQLite; callers
// may use in-memory channels only to wake workers after these methods commit.
type Repository interface {
	Bootstrap(context.Context, bool, int64) error
	SetEnabled(context.Context, bool, int64) error
	EnqueueEvents(context.Context, []usage.Event, int64) (model.ServerErrorPriorityDemotionEnqueueResult, error)
	Reconcile(context.Context, int, int64) (model.ServerErrorPriorityDemotionReconcileResult, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

// Bootstrap establishes the no-history-replay floor exactly once. Repeated
// process starts retain the original floor and merely synchronize the active
// enabled interval to the current effective setting.
func (r *repository) Bootstrap(ctx context.Context, enabled bool, nowMS int64) error {
	if r == nil || r.db == nil {
		return ErrNotBootstrapped
	}
	nowMS = normalizeNowMS(nowMS)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	maxID, err := latestUsageEventID(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert or ignore into server_error_priority_demotion_cursor (
		id, source_floor_event_id, scan_cursor_event_id, updated_at_ms
	) values (1, ?, ?, ?)`, maxID, maxID, nowMS); err != nil {
		return err
	}
	if err := setEnabledTx(ctx, tx, enabled, maxID, nowMS); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEnabled persists a precise source-event interval boundary. It does not
// erase existing jobs. Reconciliation later admits only source rows whose ID
// falls inside one of these intervals, so an event observed while disabled
// cannot become eligible after a later enable.
func (r *repository) SetEnabled(ctx context.Context, enabled bool, nowMS int64) error {
	if r == nil || r.db == nil {
		return ErrNotBootstrapped
	}
	nowMS = normalizeNowMS(nowMS)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err := cursorTx(ctx, tx); err != nil {
		return err
	}
	maxID, err := latestUsageEventID(ctx, tx)
	if err != nil {
		return err
	}
	if err := setEnabledTx(ctx, tx, enabled, maxID, nowMS); err != nil {
		return err
	}
	return tx.Commit()
}

// EnqueueEvents reads the already-persisted source row by hash rather than
// trusting in-memory event fields. The source lookup makes job identity and
// qualification auditable and lets reconciliation repair a crash between the
// usage-event transaction and this admission transaction.
func (r *repository) EnqueueEvents(ctx context.Context, events []usage.Event, nowMS int64) (model.ServerErrorPriorityDemotionEnqueueResult, error) {
	result := model.ServerErrorPriorityDemotionEnqueueResult{}
	if r == nil || r.db == nil || len(events) == 0 {
		return result, nil
	}
	nowMS = normalizeNowMS(nowMS)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	floor, _, err := cursorTx(ctx, tx)
	if err != nil {
		return result, err
	}

	for _, event := range events {
		hash := strings.TrimSpace(event.EventHash)
		if hash == "" {
			continue
		}
		sourceID, persisted, found, err := sourceUsageEventByHashTx(ctx, tx, hash)
		if err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("%w: %q", ErrSourceEventAbsent, hash)
		}
		if sourceID <= floor {
			result.SkippedFloor++
			continue
		}
		enabled, err := isSourceEventEnabledTx(ctx, tx, sourceID)
		if err != nil {
			return result, err
		}
		if !enabled {
			result.SkippedDisabled++
			continue
		}
		input, eligible := priorityevent.Classify(persisted)
		if !eligible {
			continue
		}
		inserted, ignored, err := insertInputTx(ctx, tx, sourceID, input, nowMS)
		if err != nil {
			return result, err
		}
		if inserted {
			if ignored {
				result.Ignored++
			} else {
				result.Admitted++
			}
		} else {
			result.Duplicate++
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// Reconcile repairs the narrow crash window after usage_events commits but
// before live fanout admits a queue job. It scans monotonically forward and
// shares the exact same classifier/interval logic as EnqueueEvents.
func (r *repository) Reconcile(ctx context.Context, limit int, nowMS int64) (model.ServerErrorPriorityDemotionReconcileResult, error) {
	result := model.ServerErrorPriorityDemotionReconcileResult{}
	if r == nil || r.db == nil {
		return result, ErrNotBootstrapped
	}
	if limit <= 0 {
		return result, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	nowMS = normalizeNowMS(nowMS)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	floor, cursor, err := cursorTx(ctx, tx)
	if err != nil {
		return result, err
	}
	result.CursorEventID = cursor
	rows, err := tx.QueryContext(ctx, `select id, event_hash, timestamp_ms, created_at_ms,
		provider, auth_index, account_snapshot, auth_file_snapshot,
		auth_provider_snapshot, auth_account_id_snapshot,
		failed, fail_status_code, fail_body
		from usage_events where id > ? order by id asc limit ?`, cursor, limit)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	lastID := cursor
	for rows.Next() {
		id, event, err := scanSourceUsageEvent(rows)
		if err != nil {
			return result, err
		}
		result.Scanned++
		lastID = id
		if id <= floor {
			continue
		}
		enabled, err := isSourceEventEnabledTx(ctx, tx, id)
		if err != nil {
			return result, err
		}
		if !enabled {
			result.SkippedDisabled++
			continue
		}
		input, eligible := priorityevent.Classify(event)
		if !eligible {
			continue
		}
		inserted, ignored, err := insertInputTx(ctx, tx, id, input, nowMS)
		if err != nil {
			return result, err
		}
		if inserted {
			if ignored {
				result.Ignored++
			} else {
				result.Admitted++
			}
		} else {
			result.Duplicate++
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if lastID > cursor {
		if _, err := tx.ExecContext(ctx, `update server_error_priority_demotion_cursor
			set scan_cursor_event_id = ?, updated_at_ms = ? where id = 1 and scan_cursor_event_id <= ?`, lastID, nowMS, cursor); err != nil {
			return result, err
		}
	}
	result.CursorEventID = lastID
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func setEnabledTx(ctx context.Context, tx *sql.Tx, enabled bool, maxEventID, nowMS int64) error {
	var activeID, startID int64
	err := tx.QueryRowContext(ctx, `select id, start_event_id
		from server_error_priority_demotion_enabled_intervals
		where end_event_id is null order by id desc limit 1`).Scan(&activeID, &startID)
	active := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if enabled {
		if active {
			return nil
		}
		_, err := tx.ExecContext(ctx, `insert into server_error_priority_demotion_enabled_intervals (
			start_event_id, created_at_ms
		) values (?, ?)`, maxEventID+1, nowMS)
		return err
	}
	if !active {
		return nil
	}
	if startID > maxEventID {
		_, err := tx.ExecContext(ctx, `delete from server_error_priority_demotion_enabled_intervals where id = ? and end_event_id is null`, activeID)
		return err
	}
	_, err = tx.ExecContext(ctx, `update server_error_priority_demotion_enabled_intervals
		set end_event_id = ?, closed_at_ms = ? where id = ? and end_event_id is null`, maxEventID, nowMS, activeID)
	return err
}

func cursorTx(ctx context.Context, tx *sql.Tx) (floor, cursor int64, err error) {
	err = tx.QueryRowContext(ctx, `select source_floor_event_id, scan_cursor_event_id
		from server_error_priority_demotion_cursor where id = 1`).Scan(&floor, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotBootstrapped
	}
	return floor, cursor, err
}

func latestUsageEventID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&id)
	return id, err
}

func sourceUsageEventByHashTx(ctx context.Context, tx *sql.Tx, hash string) (int64, usage.Event, bool, error) {
	row := tx.QueryRowContext(ctx, `select id, event_hash, timestamp_ms, created_at_ms,
		provider, auth_index, account_snapshot, auth_file_snapshot,
		auth_provider_snapshot, auth_account_id_snapshot,
		failed, fail_status_code, fail_body
		from usage_events where event_hash = ?`, hash)
	id, event, err := scanSourceUsageEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, usage.Event{}, false, nil
	}
	return id, event, err == nil, err
}

func isSourceEventEnabledTx(ctx context.Context, tx *sql.Tx, sourceEventID int64) (bool, error) {
	var enabled int
	err := tx.QueryRowContext(ctx, `select exists(
		select 1 from server_error_priority_demotion_enabled_intervals
		where start_event_id <= ? and (end_event_id is null or end_event_id >= ?)
	)`, sourceEventID, sourceEventID).Scan(&enabled)
	return enabled != 0, err
}

func insertInputTx(ctx context.Context, tx *sql.Tx, sourceEventID int64, input priorityevent.Input, nowMS int64) (inserted, ignored bool, err error) {
	state := model.ServerErrorPriorityDemotionPending
	availableAtMS := nowMS
	lastErrorCode := any(nil)
	completedReason := any(nil)
	completedAtMS := any(nil)
	if input.IgnoredReason != "" {
		state = model.ServerErrorPriorityDemotionIgnored
		lastErrorCode = input.IgnoredReason
		completedReason = input.IgnoredReason
		completedAtMS = nowMS
		ignored = true
	}
	result, err := tx.ExecContext(ctx, `insert or ignore into server_error_priority_demotion_jobs (
		event_hash, source_event_id, credential_key, file_name, auth_index, provider,
		account_snapshot, account_id_snapshot, status_code, qualification_code,
		event_at_ms, state, available_at_ms, last_error_code, completed_reason,
		created_at_ms, updated_at_ms, completed_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.EventHash, sourceEventID, input.CredentialKey, input.FileName, input.AuthIndex, input.Provider,
		input.AccountSnapshot, input.AccountIDSnapshot, input.StatusCode, input.QualificationCode,
		input.EventAtMS, state, availableAtMS, lastErrorCode, completedReason,
		nowMS, nowMS, completedAtMS)
	if err != nil {
		return false, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, false, err
	}
	return changed == 1, ignored, nil
}

func scanSourceUsageEvent(scanner interface{ Scan(...any) error }) (int64, usage.Event, error) {
	var (
		id, failed                                             int64
		event                                                  usage.Event
		provider, authIndex, accountSnapshot, authFileSnapshot sql.NullString
		authProviderSnapshot, authAccountIDSnapshot, failBody  sql.NullString
		statusCode                                             sql.NullInt64
	)
	err := scanner.Scan(&id, &event.EventHash, &event.TimestampMS, &event.CreatedAtMS,
		&provider, &authIndex, &accountSnapshot, &authFileSnapshot,
		&authProviderSnapshot, &authAccountIDSnapshot,
		&failed, &statusCode, &failBody)
	if err != nil {
		return 0, usage.Event{}, err
	}
	event.Provider = provider.String
	event.AuthIndex = authIndex.String
	event.AccountSnapshot = accountSnapshot.String
	event.AuthFileSnapshot = authFileSnapshot.String
	event.AuthProviderSnapshot = authProviderSnapshot.String
	event.AuthAccountIDSnapshot = authAccountIDSnapshot.String
	event.Failed = failed != 0
	if statusCode.Valid {
		event.FailStatusCode = int(statusCode.Int64)
	}
	event.FailBody = failBody.String
	return id, event, nil
}

func normalizeNowMS(nowMS int64) int64 {
	if nowMS > 0 {
		return nowMS
	}
	return time.Now().UnixMilli()
}
