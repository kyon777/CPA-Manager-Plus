package derivedstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type sqlSink struct{ db *sql.DB }

func NewSink(db *sql.DB) Sink {
	if db == nil {
		return &sqlSink{}
	}
	return &sqlSink{db: db}
}

func (s *sqlSink) State(ctx context.Context, stream Stream) (StreamState, error) {
	if s == nil || s.db == nil {
		return StreamState{}, errors.New("derived sink is unavailable")
	}
	return readState(ctx, s.db, stream)
}

func (s *sqlSink) Rows(ctx context.Context, stream Stream, generation string) ([]MaterializedRow, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived sink is unavailable")
	}
	if strings.TrimSpace(string(stream)) == "" || strings.TrimSpace(generation) == "" {
		return []MaterializedRow{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `select stream_name, generation, row_key,
		subject_key, payload_json, first_event_id, last_event_id, updated_at_ms
		from derived_materialized_rows where stream_name = ? and generation = ?
		order by row_key`, string(stream), generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]MaterializedRow, 0)
	for rows.Next() {
		var row MaterializedRow
		var streamName string
		if err := rows.Scan(&streamName, &row.Generation, &row.Key, &row.SubjectKey, &row.PayloadJSON,
			&row.FirstEventID, &row.LastEventID, &row.UpdatedAtMS); err != nil {
			return nil, err
		}
		row.Stream = Stream(streamName)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *sqlSink) RowsByKeys(ctx context.Context, stream Stream, generation string, keys []string) ([]MaterializedRow, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived sink is unavailable")
	}
	keys = nonEmptyStrings(keys)
	if len(keys) == 0 {
		return []MaterializedRow{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+2)
	args = append(args, string(stream), generation)
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := s.db.QueryContext(ctx, `select stream_name, generation, row_key,
		subject_key, payload_json, first_event_id, last_event_id, updated_at_ms
		from derived_materialized_rows where stream_name = ? and generation = ?
		and row_key in (`+placeholders+`) order by row_key`, args...)
	if err != nil {
		return nil, err
	}
	return scanMaterializedRows(rows)
}

func (s *sqlSink) RowsForSubjects(ctx context.Context, stream Stream, generation string, subjects []string) ([]MaterializedRow, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived sink is unavailable")
	}
	subjects = nonEmptyStrings(subjects)
	if len(subjects) == 0 {
		return []MaterializedRow{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(subjects)), ",")
	args := make([]any, 0, len(subjects)+2)
	args = append(args, string(stream), generation)
	for _, subject := range subjects {
		args = append(args, subject)
	}
	rows, err := s.db.QueryContext(ctx, `select stream_name, generation, row_key,
		subject_key, payload_json, first_event_id, last_event_id, updated_at_ms
		from derived_materialized_rows where stream_name = ? and generation = ?
		and subject_key in (`+placeholders+`) order by row_key`, args...)
	if err != nil {
		return nil, err
	}
	return scanMaterializedRows(rows)
}

func scanMaterializedRows(rows *sql.Rows) ([]MaterializedRow, error) {
	defer rows.Close()
	result := make([]MaterializedRow, 0)
	for rows.Next() {
		var row MaterializedRow
		var streamName string
		if err := rows.Scan(&streamName, &row.Generation, &row.Key, &row.SubjectKey, &row.PayloadJSON,
			&row.FirstEventID, &row.LastEventID, &row.UpdatedAtMS); err != nil {
			return nil, err
		}
		row.Stream = Stream(streamName)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func nonEmptyStrings(values []string) []string {
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

func (s *sqlSink) Apply(ctx context.Context, batch ApplyBatch) (StreamState, error) {
	if s == nil || s.db == nil {
		return StreamState{}, errors.New("derived sink is unavailable")
	}
	if strings.TrimSpace(string(batch.Stream)) == "" {
		return StreamState{}, errors.New("derived stream is required")
	}
	if strings.TrimSpace(batch.SourceEpoch) == "" {
		return StreamState{}, errors.New("derived source epoch is required")
	}
	if strings.TrimSpace(batch.Generation) == "" {
		return StreamState{}, errors.New("derived generation is required")
	}
	targetEventID := batch.SourceTargetEventID
	if targetEventID < batch.ToEventID {
		targetEventID = batch.ToEventID
	}
	now := batch.AppliedAtMS
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StreamState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSourceEpoch(ctx, tx, batch.SourceEpoch, now); err != nil {
		return StreamState{}, err
	}
	var existingEpoch, existingGeneration string
	err = tx.QueryRowContext(ctx, `select source_epoch, generation from derived_stream_state where stream_name = ?`, string(batch.Stream)).Scan(&existingEpoch, &existingGeneration)
	existing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StreamState{}, err
	}
	if existing && existingEpoch != "" && existingEpoch != batch.SourceEpoch {
		return StreamState{}, fmt.Errorf("derived source epoch mismatch for %s: have %s, got %s", batch.Stream, existingEpoch, batch.SourceEpoch)
	}
	generationChanged := existing && existingGeneration != "" && existingGeneration != batch.Generation
	// A prior process may have left more than one stale generation behind
	// (for example, after a crash between the state update and cleanup). The
	// current generation is the only readable one, so purge every other
	// materialization/ledger row for this stream before accepting new rows.
	if !existing || existingGeneration != batch.Generation {
		if _, err := tx.ExecContext(ctx, `delete from derived_materialized_rows where stream_name = ? and generation <> ?`, string(batch.Stream), batch.Generation); err != nil {
			return StreamState{}, fmt.Errorf("clear stale derived materializations for %s: %w", batch.Stream, err)
		}
		if _, err := tx.ExecContext(ctx, `delete from derived_applied_events where stream_name = ? and generation <> ?`, string(batch.Stream), batch.Generation); err != nil {
			return StreamState{}, fmt.Errorf("clear stale derived applied events for %s: %w", batch.Stream, err)
		}
	}
	if generationChanged {
		// A generation change means the identity or pricing algorithm changed.
		// The stream state is reset atomically below after all stale generations
		// have been removed, so the new email-key generation cannot coexist with
		// the old workspace/file buckets.
		if _, err := tx.ExecContext(ctx, `update derived_stream_state set
			source_epoch=?, generation=?, status='rebuilding', coverage_event_id=0,
			target_event_id=?, pricing_revision=?, archive_generation=?, processed_events=0,
			last_success_at_ms=null, last_run_started_at_ms=null, last_batch_duration_ms=0,
			backoff_count=0, last_error_code='', updated_at_ms=? where stream_name=?`,
			batch.SourceEpoch, batch.Generation, targetEventID, batch.PricingRevision,
			batch.ArchiveGeneration, now, string(batch.Stream)); err != nil {
			return StreamState{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `insert into derived_stream_state(
		stream_name, source_epoch, generation, status, coverage_event_id,
		target_event_id, pricing_revision, archive_generation, processed_events,
		last_success_at_ms, updated_at_ms)
		values (?, ?, ?, 'rebuilding', 0, ?, ?, ?, 0, null, ?)
		on conflict(stream_name) do update set
		 source_epoch=excluded.source_epoch,
		 generation=excluded.generation,
		 status='rebuilding',
		 target_event_id=max(derived_stream_state.target_event_id, excluded.target_event_id),
		 pricing_revision=excluded.pricing_revision,
		 archive_generation=excluded.archive_generation,
		 updated_at_ms=excluded.updated_at_ms`,
		string(batch.Stream), batch.SourceEpoch, batch.Generation, targetEventID,
		batch.PricingRevision, batch.ArchiveGeneration, now); err != nil {
		return StreamState{}, err
	}
	for _, row := range batch.Rows {
		if strings.TrimSpace(row.Key) == "" || strings.TrimSpace(row.PayloadJSON) == "" {
			return StreamState{}, errors.New("derived materialized row key and payload are required")
		}
		if row.Stream == "" {
			row.Stream = batch.Stream
		}
		if row.Generation == "" {
			row.Generation = batch.Generation
		}
		if row.SubjectKey == "" {
			row.SubjectKey = row.Key
		}
		if _, err := tx.ExecContext(ctx, `insert into derived_materialized_rows(
			stream_name, generation, row_key, subject_key, payload_json, first_event_id,
			last_event_id, updated_at_ms)
			values (?, ?, ?, ?, ?, ?, ?, ?)
			on conflict(stream_name, generation, row_key) do update set
			subject_key=excluded.subject_key,
			payload_json=excluded.payload_json,
			first_event_id=min(derived_materialized_rows.first_event_id, excluded.first_event_id),
			last_event_id=max(derived_materialized_rows.last_event_id, excluded.last_event_id),
			updated_at_ms=excluded.updated_at_ms`,
			string(row.Stream), row.Generation, row.Key, row.SubjectKey, row.PayloadJSON,
			row.FirstEventID, row.LastEventID, now); err != nil {
			return StreamState{}, fmt.Errorf("write derived materialization %s: %w", row.Key, err)
		}
	}
	for _, event := range batch.Events {
		if _, err := tx.ExecContext(ctx, `insert or ignore into derived_applied_events(
			stream_name, generation, event_id, event_hash, applied_at_ms)
			values (?, ?, ?, ?, ?)`, string(batch.Stream), batch.Generation,
			event.ID, event.EventHash, now); err != nil {
			return StreamState{}, err
		}
	}
	if batch.ToEventID > 0 {
		if _, err := tx.ExecContext(ctx, `update derived_stream_state set
			coverage_event_id=max(coverage_event_id, ?),
			target_event_id=max(target_event_id, ?),
			processed_events=processed_events+?, status='ready',
			last_success_at_ms=?, last_error_code='', updated_at_ms=?
			where stream_name=? and generation=?`, batch.ToEventID, targetEventID,
			len(batch.Events), now, now, string(batch.Stream), batch.Generation); err != nil {
			return StreamState{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `update derived_stream_state set
			status='ready', last_success_at_ms=?, last_error_code='', updated_at_ms=?
			where stream_name=? and generation=?`, now, now, string(batch.Stream), batch.Generation); err != nil {
		return StreamState{}, err
	}
	if err := tx.Commit(); err != nil {
		return StreamState{}, err
	}
	return readState(ctx, s.db, batch.Stream)
}

const derivedSchemaRevision = "derived-store-v1"

func ensureSourceEpoch(ctx context.Context, tx *sql.Tx, epoch string, now int64) error {
	var current string
	err := tx.QueryRowContext(ctx, `select source_epoch from derived_store_meta where singleton = 1`).Scan(&current)
	switch {
	case err == nil:
		if current != "" && current != epoch {
			return fmt.Errorf("derived source epoch mismatch: have %s, got %s", current, epoch)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	default:
		_, err := tx.ExecContext(ctx, `insert into derived_store_meta(
			singleton, source_epoch, schema_revision, created_at_ms, updated_at_ms)
			values (1, ?, ?, ?, ?)`, epoch, derivedSchemaRevision, now, now)
		return err
	}
}

func (s *sqlSink) Status(ctx context.Context) (Status, error) {
	if s == nil || s.db == nil {
		return Status{}, errors.New("derived sink is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `select stream_name, source_epoch, generation,
		status, coverage_event_id, target_event_id, pricing_revision,
		archive_generation, processed_events, coalesce(last_success_at_ms, 0),
		last_error_code from derived_stream_state order by stream_name`)
	if err != nil {
		return Status{}, err
	}
	defer rows.Close()
	status := Status{Streams: []StreamState{}}
	for rows.Next() {
		var state StreamState
		var streamName string
		if err := rows.Scan(&streamName, &state.SourceEpoch, &state.Generation,
			&state.Status, &state.CoverageEventID, &state.TargetEventID,
			&state.PricingRevision, &state.ArchiveGeneration, &state.ProcessedEvents,
			&state.LastSuccessAtMS, &state.LastErrorCode); err != nil {
			return Status{}, err
		}
		state.Stream = Stream(streamName)
		if status.SourceEpoch == "" {
			status.SourceEpoch = state.SourceEpoch
		}
		status.Streams = append(status.Streams, state)
	}
	return status, rows.Err()
}

func readState(ctx context.Context, db *sql.DB, stream Stream) (StreamState, error) {
	var state StreamState
	var streamName string
	err := db.QueryRowContext(ctx, `select stream_name, source_epoch, generation,
		status, coverage_event_id, target_event_id, pricing_revision,
		archive_generation, processed_events, coalesce(last_success_at_ms, 0),
		last_error_code from derived_stream_state where stream_name = ?`, string(stream)).Scan(
		&streamName, &state.SourceEpoch, &state.Generation, &state.Status,
		&state.CoverageEventID, &state.TargetEventID, &state.PricingRevision,
		&state.ArchiveGeneration, &state.ProcessedEvents, &state.LastSuccessAtMS,
		&state.LastErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamState{Stream: stream, Status: "pending"}, nil
	}
	if err != nil {
		return StreamState{}, err
	}
	state.Stream = Stream(streamName)
	return state, nil
}
