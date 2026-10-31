package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// A Codex credential imported without a workspace later receives one during
// token refresh. Historical raw events may already have been archived, so the
// codex-2 -> codex-3 migration must fold retained projections instead of
// asking for a source rebuild that can no longer exist.
func TestMigrateFoldsArchivedCodexV2MaterializationsByEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archived-codex-v2.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		oldAccountKeyA = "legacy-file-key"
		oldAccountKeyB = "legacy-workspace-key"
		oldPricing     = "model-1:identity-3:codex-2:fixture-prices"
		newPricing     = "model-1:identity-3:codex-3:fixture-prices"
	)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("fixture query failed: %v\n%s", err, query)
		}
	}
	insertAccount := func(key, email string, calls, tokens, first, last int64) {
		mustExec(`insert into usage_account_model_rollups (
			account_key, account_snapshot, auth_label_snapshot, auth_provider_snapshot,
			auth_index, source, source_hash, model, billing_model, service_tier,
			calls, success_calls, input_tokens, total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
		) values (?, ?, ?, 'codex', ?, ?, ?, 'gpt-test', 'gpt-test', '', ?, ?, ?, ?, ?, ?, ?)`,
			key, email, email, key+"-index", key+".json", key+"-hash", calls, calls, tokens, tokens, first, last, last)
	}
	insertPricing := func(key, email string, calls, tokens, first, last int64) {
		mustExec(`insert into usage_pricing_account_rollups_v1 (
			structure_revision, account_key, account_snapshot, auth_label_snapshot, auth_provider_snapshot,
			auth_index, source, source_hash, model, billing_model, pricing_model, service_tier,
			context_threshold_tokens, calls, success_calls, input_tokens, total_tokens,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values (?, ?, ?, ?, 'codex', ?, ?, ?, 'gpt-test', 'gpt-test', 'gpt-test', '', -1, ?, ?, ?, ?, ?, ?, ?)`,
			oldPricing, key, email, email, key+"-index", key+".json", key+"-hash", calls, calls, tokens, tokens, first, last, last)
	}
	insertProjection := func(eventID int64, key, email string) {
		mustExec(`insert into usage_monitoring_event_projection_v1 (
			event_id, timestamp_ms, search_text, account_key, provider, executor_type,
			model, analytics_model, resolved_model, auth_index, source, source_hash,
			api_key_hash, account_snapshot, auth_label_snapshot, auth_file_snapshot,
			auth_provider_snapshot, auth_project_id_snapshot, reasoning_effort, service_tier,
			failed, input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens,
			cache_read_tokens, cache_creation_tokens, normalized_total_input_tokens, total_tokens,
			header_quota_plan_type, header_error_kind, header_error_code, header_trace_id, updated_at_ms
		) values (?, ?, ?, ?, 'codex', '', 'gpt-test', 'gpt-test', 'gpt-test', ?, ?, ?, '', ?, ?, ?, 'codex', '', '', '', 0, 1, 0, 0, 0, 0, 0, 0, 1, 1, '', '', '', '', ?)`,
			eventID, eventID, email, key, key+"-index", key+".json", key+"-hash", email, email, key+".json", eventID)
	}

	insertAccount(oldAccountKeyA, "same@example.com", 3, 30, 10, 20)
	insertAccount(oldAccountKeyB, " SAME@EXAMPLE.COM ", 5, 50, 5, 30)
	insertPricing(oldAccountKeyA, "same@example.com", 3, 30, 10, 20)
	insertPricing(oldAccountKeyB, " SAME@EXAMPLE.COM ", 5, 50, 5, 30)
	insertProjection(11, oldAccountKeyA, "same@example.com")
	insertProjection(12, oldAccountKeyB, " SAME@EXAMPLE.COM ")
	// A non-email Codex account snapshot is incomplete identity evidence. It
	// must retain its old physical key rather than being reinterpreted during
	// the email-only fold.
	insertProjection(13, "legacy-invalid-codex-key", "workspace-not-an-email")
	insertPricingHourly := func(revision string, calls, tokens int64) {
		mustExec(`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model, service_tier,
			context_threshold_tokens, failed, calls, input_tokens, total_tokens, updated_at_ms
		) values (?, 0, 'gpt-test', 'gpt-test', 'gpt-test', '', -1, 0, ?, ?, ?, ?)`,
			revision, calls, tokens, tokens, calls)
	}
	insertPricingHourly(oldPricing, 8, 80)
	// A prior interrupted rollout can leave a codex-3 bucket beside the old
	// codex-2 bucket. Both revisions describe the same retained facts and must
	// fold rather than collide on the current primary key during migration.
	insertPricingHourly(newPricing, 3, 30)
	insertDaily := func(table, revision string, calls int64) {
		mustExec(`insert into `+table+` (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot, provider,
			auth_provider_snapshot, auth_account_id_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, input_tokens, total_tokens,
			last_seen_ms, updated_at_ms
		) values (?, 0, 'same@example.com', 'same@example.com', 'codex', 'codex', '',
			'idx', 'file.json', 'hash', 'file.json', 'api', '', 'gpt-test', 'gpt-test',
			'gpt-test', '', -1, 0, ?, ?, ?, ?, ?)`,
			revision, calls, calls*10, calls*10, calls, calls)
	}
	insertDaily(usageMonitoringAccountDailyTable, oldPricing, 2)
	insertDaily(usageMonitoringAccountDailyTable, newPricing, 3)
	insertDaily(usageMonitoringAPIKeyDailyTable, oldPricing, 2)
	insertDaily(usageMonitoringAPIKeyDailyTable, newPricing, 3)
	mustExec(`insert into usage_pricing_account_price_cache_v1 (
		account_key, pricing_revision, covered_event_id, total_cost, updated_at_ms, status, last_error
	) values (?, ?, 12, 1.25, 30, 'fresh', '')`, oldAccountKeyA, oldPricing)

	// The raw source has a real archive deletion marker. This is the production
	// condition that previously made startup fail before CPAM could listen.
	insertArchiveRun(t, db, "archived-codex-v2", "manual", "completed")
	mustExec(`insert into usage_archive_segments (
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms, verified_at_ms
	) values ('archived-codex-v2', 1, 'verified', 'archived-codex-v2/segment.jsonl.gz',
		1, 1, 1, 1, 1, 1, 1, 'content', 'events', 1, 1)`)
	mustExec(`insert into usage_event_identity_ledger (
		event_hash, raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version,
		aggregate_structure_revision, first_seen_at_ms, updated_at_ms
	) values ('archived-codex-v2-event', null, 1, 0, 1, 'fixture', 1, 1)`)
	mustExec(`insert into usage_archive_event_refs (
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values ('archived-codex-v2-event', 'archived-codex-v2', 1, 1, 1, 1, 2)`)

	mustExec(`update settings set value = ? where key = ?`,
		legacyAccountHistoryStructureRevisionCodexV2, accountHistoryIdentityFormatVersionKey)
	mustExec(`update usage_monitoring_rollup_state set
		structure_revision = ?, status = 'ready', backfill_last_event_id = 12,
		coverage_event_id = 12, target_event_id = 12, processed_events = 12
		where rollup_name = ?`, legacyMonitoringProjectionRevisionCodexV2, usageMonitoringProjectionRollupName)
	mustExec(`update usage_monitoring_rollup_state set
		structure_revision = ?, status = 'ready', backfill_last_event_id = 12,
		coverage_event_id = 12, target_event_id = 12, processed_events = 12
		where rollup_name = ?`, oldPricing, usageMonitoringStatsRollupName)
	mustExec(`update usage_pricing_rollup_state set
		structure_revision = ?, status = 'ready', backfill_last_event_id = 12,
		coverage_event_id = 12, target_event_id = 12, processed_events = 12
		where rollup_name = 'pricing_v1'`, oldPricing)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate archived codex-v2 state: %v", err)
	}

	canonicalKey, ok := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot: "codex",
		AccountSnapshot:      "same@example.com",
	})
	if !ok {
		t.Fatal("canonical codex email key was not produced")
	}
	var count, calls, tokens int64
	if err := db.QueryRow(`select count(*), coalesce(sum(calls), 0), coalesce(sum(total_tokens), 0)
		from usage_account_model_rollups where account_key = ?`, canonicalKey).Scan(&count, &calls, &tokens); err != nil {
		t.Fatalf("read folded account history: %v", err)
	}
	if count != 1 || calls != 8 || tokens != 80 {
		t.Fatalf("folded account history = count:%d calls:%d tokens:%d", count, calls, tokens)
	}
	if err := db.QueryRow(`select count(*), coalesce(sum(calls), 0), coalesce(sum(total_tokens), 0)
		from usage_pricing_account_rollups_v1 where structure_revision = ? and account_key = ?`, newPricing, canonicalKey).Scan(&count, &calls, &tokens); err != nil {
		t.Fatalf("read folded pricing account history: %v", err)
	}
	if count != 1 || calls != 8 || tokens != 80 {
		t.Fatalf("folded pricing account history = count:%d calls:%d tokens:%d", count, calls, tokens)
	}
	if err := db.QueryRow(`select count(*) from usage_monitoring_event_projection_v1 where account_key = ?`, canonicalKey).Scan(&count); err != nil {
		t.Fatalf("read folded monitoring projection: %v", err)
	}
	if count != 2 {
		t.Fatalf("folded monitoring projection rows = %d, want 2", count)
	}
	if err := db.QueryRow(`select count(*) from usage_monitoring_event_projection_v1
		where account_key = 'legacy-invalid-codex-key'`).Scan(&count); err != nil {
		t.Fatalf("read invalid Codex projection identity: %v", err)
	}
	if count != 1 {
		t.Fatalf("invalid Codex projection identity was rewritten; rows=%d", count)
	}
	if err := db.QueryRow(`select count(*) from usage_pricing_account_price_cache_v1`).Scan(&count); err != nil {
		t.Fatalf("read pricing cache after fold: %v", err)
	}
	if count != 0 {
		t.Fatalf("pricing cache rows = %d, want 0", count)
	}
	if err := db.QueryRow(`select count(*), coalesce(sum(calls), 0), coalesce(sum(total_tokens), 0)
		from usage_pricing_hourly_rollups_v1 where structure_revision = ?`, newPricing).Scan(&count, &calls, &tokens); err != nil {
		t.Fatalf("read folded pricing hourly rows: %v", err)
	}
	if count != 1 || calls != 11 || tokens != 110 {
		t.Fatalf("folded pricing hourly rows = count:%d calls:%d tokens:%d", count, calls, tokens)
	}
	for _, table := range []string{usageMonitoringAccountDailyTable, usageMonitoringAPIKeyDailyTable} {
		if err := db.QueryRow(`select count(*), coalesce(sum(calls), 0) from `+table+` where structure_revision = ?`, newPricing).Scan(&count, &calls); err != nil {
			t.Fatalf("read folded %s rows: %v", table, err)
		}
		if count != 1 || calls != 5 {
			t.Fatalf("folded %s = count:%d calls:%d", table, count, calls)
		}
	}

	var accountRevision, projectionRevision, projectionStatus, pricingRevision string
	var projectionCoverage, pricingCoverage int64
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&accountRevision); err != nil {
		t.Fatalf("read account revision: %v", err)
	}
	if err := db.QueryRow(`select structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = ?`, usageMonitoringProjectionRollupName).Scan(&projectionRevision, &projectionStatus, &projectionCoverage); err != nil {
		t.Fatalf("read projection state: %v", err)
	}
	if err := db.QueryRow(`select structure_revision, coverage_event_id
		from usage_pricing_rollup_state where rollup_name = 'pricing_v1'`).Scan(&pricingRevision, &pricingCoverage); err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if accountRevision != usageidentity.AccountHistoryStructureRevision() ||
		projectionRevision != usageidentity.MonitoringProjectionStructureRevision() ||
		projectionStatus != "ready" || projectionCoverage != 12 ||
		pricingRevision != newPricing || pricingCoverage != 12 {
		t.Fatalf("fold state = account:%q projection:%q/%q/%d pricing:%q/%d",
			accountRevision, projectionRevision, projectionStatus, projectionCoverage, pricingRevision, pricingCoverage)
	}
}
