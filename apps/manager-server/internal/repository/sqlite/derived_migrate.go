package sqlite

import (
	"database/sql"
	"fmt"
)

const derivedSchemaRevision = "derived-store-v1"

// MigrateDerived creates only disposable, non-sensitive materializations.
//
// Do not add primary facts or control tables here. In particular, this schema
// must never gain usage_events, usage_event_identity_ledger, model_prices,
// settings, archive metadata, auth recovery tasks, credentials, or proxy data.
// A failure can therefore be recovered by rebuilding this database without
// changing billing or operational state in usage.sqlite.
func MigrateDerived(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("derived SQLite database is unavailable")
	}
	statements := []string{
		`pragma journal_mode = WAL`,
		`pragma synchronous = NORMAL`,
		`pragma busy_timeout = 1000`,
		`pragma foreign_keys = ON`,
		`create table if not exists derived_store_meta (
			singleton integer primary key check (singleton = 1),
			source_epoch text not null default '',
			schema_revision text not null,
			archive_generation text not null default '',
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists derived_stream_state (
			stream_name text primary key,
			source_epoch text not null default '',
			generation text not null default '',
			status text not null default 'pending',
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			pricing_revision text not null default '',
			archive_generation text not null default '',
			processed_events integer not null default 0,
			last_success_at_ms integer,
			last_run_started_at_ms integer,
			last_batch_duration_ms integer not null default 0,
			backoff_count integer not null default 0,
			last_error_code text not null default '',
			updated_at_ms integer not null default 0
		)`,
		`create table if not exists derived_applied_events (
			stream_name text not null,
			generation text not null,
			event_id integer not null,
			event_hash text not null,
			applied_at_ms integer not null,
			primary key (stream_name, generation, event_id),
			unique (stream_name, generation, event_hash)
		)`,
		`create index if not exists idx_derived_applied_events_stream_coverage
			on derived_applied_events(stream_name, generation, event_id desc)`,
		`create table if not exists derived_materialized_rows (
			stream_name text not null,
			generation text not null,
			row_key text not null,
			subject_key text not null default '',
			payload_json text not null,
			first_event_id integer not null default 0,
			last_event_id integer not null default 0,
			updated_at_ms integer not null,
			primary key (stream_name, generation, row_key)
		)`,
		`create index if not exists idx_derived_materialized_rows_lookup
			on derived_materialized_rows(stream_name, generation, row_key)`,
		createUsageAccountModelRollupsTable,
		createUsagePricingAccountRollupsTable,
		createUsagePricingAccountPriceCacheTable,
		`create index if not exists idx_usage_pricing_account_price_cache_account
			on usage_pricing_account_price_cache_v1(account_key, updated_at_ms desc)`,
		`create index if not exists idx_usage_pricing_account_price_cache_revision
			on usage_pricing_account_price_cache_v1(pricing_revision, covered_event_id)`,
		`create table if not exists usage_pricing_hourly_rollups_v1 (
			generation text not null default '',
			structure_revision text not null,
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			pricing_model text not null,
			service_tier text not null,
			context_threshold_tokens integer not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (
				generation, structure_revision, bucket_ms, model, billing_model,
				pricing_model, service_tier, context_threshold_tokens, failed
			)
		)`,
		createUsageDashboardHourlyRollupsTable,
		createUsageHourlyAggregateTable,
		`create table if not exists derived_monitoring_account_daily_rollups_v1 (
			generation text not null,
			bucket_ms integer not null,
			account_key text not null,
			provider text not null,
			model text not null,
			billing_model text not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			total_tokens integer not null default 0,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (generation, bucket_ms, account_key, provider, model, billing_model, failed)
		)`,
		`create table if not exists derived_monitoring_api_key_daily_rollups_v1 (
			generation text not null,
			bucket_ms integer not null,
			api_key_hash text not null,
			provider text not null,
			model text not null,
			billing_model text not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null,
			total_tokens integer not null default 0,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (generation, bucket_ms, api_key_hash, provider, model, billing_model, failed)
		)`,
		`create table if not exists derived_monitoring_header_latest_v1 (
			generation text not null,
			snapshot_key text not null,
			event_id integer not null,
			timestamp_ms integer not null,
			account_key text not null,
			provider text not null,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text not null default '',
			header_error_kind text not null default '',
			header_error_code text not null default '',
			updated_at_ms integer not null,
			primary key (generation, snapshot_key)
		)`,
		`create index if not exists idx_derived_monitoring_account_daily_lookup
			on derived_monitoring_account_daily_rollups_v1(generation, account_key, bucket_ms)`,
		`create index if not exists idx_derived_monitoring_api_key_daily_lookup
			on derived_monitoring_api_key_daily_rollups_v1(generation, api_key_hash, bucket_ms)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("migrate derived SQLite schema: %w", err)
		}
	}
	// Derived storage is disposable, but keep upgrades of an already-created
	// candidate database forward-compatible when a new materialization column
	// is added between restarts.
	if err := ensureDerivedMaterializedSubjectKey(db); err != nil {
		return err
	}
	if _, err := db.Exec(`create index if not exists idx_derived_materialized_rows_subject
		on derived_materialized_rows(stream_name, generation, subject_key, row_key)`); err != nil {
		return fmt.Errorf("create derived materialization subject index: %w", err)
	}
	return nil
}

func ensureDerivedMaterializedSubjectKey(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(derived_materialized_rows)`)
	if err != nil {
		return fmt.Errorf("inspect derived materialization schema: %w", err)
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "subject_key" {
			seen = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen {
		// Older candidate databases may have the column but no subject key
		// values. Recover the opaque key from the already-redacted payload so a
		// restart does not make previously materialized rows invisible to the
		// visible-page lookup.
		if _, err := db.Exec(`update derived_materialized_rows
			set subject_key = json_extract(payload_json, '$.accountKey')
			where subject_key = '' and json_valid(payload_json)
			and coalesce(json_extract(payload_json, '$.accountKey'), '') <> ''`); err != nil {
			return fmt.Errorf("backfill derived materialization subject key: %w", err)
		}
		return nil
	}
	if _, err := db.Exec(`alter table derived_materialized_rows add column subject_key text not null default ''`); err != nil {
		return fmt.Errorf("add derived materialization subject key: %w", err)
	}
	if _, err := db.Exec(`update derived_materialized_rows
		set subject_key = json_extract(payload_json, '$.accountKey')
		where subject_key = '' and json_valid(payload_json)
		and coalesce(json_extract(payload_json, '$.accountKey'), '') <> ''`); err != nil {
		return fmt.Errorf("backfill derived materialization subject key: %w", err)
	}
	_, err = db.Exec(`create index if not exists idx_derived_materialized_rows_subject
		on derived_materialized_rows(stream_name, generation, subject_key, row_key)`)
	return err
}
