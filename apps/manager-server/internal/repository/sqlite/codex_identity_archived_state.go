package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// ensureArchivedCodexV2StateFold upgrades the immediately previous custom
// Codex identity without replaying usage_events. A production database can
// legitimately have its older raw events compacted into archives; the
// account/pricing/monitoring materializations are the retained facts for that
// interval and must be folded in place before the normal migration guards run.
//
// The operation is deliberately one primary-database transaction. Either every
// retained projection has the email-only codex-3 identity and matching state
// revisions, or the next startup retries from the untouched codex-2 state.
func ensureArchivedCodexV2StateFold(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite database is unavailable")
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin archived codex identity fold: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	err = tx.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&version)
	switch {
	case err == nil && version != legacyAccountHistoryStructureRevisionCodexV2:
		return tx.Commit()
	case err == nil:
		// Continue below.
	case err == sql.ErrNoRows:
		return tx.Commit()
	default:
		return fmt.Errorf("read archived codex identity revision: %w", err)
	}

	if err := foldCodexAccountHistoryRows(tx); err != nil {
		return err
	}
	if err := foldCodexPricingAccountRows(tx); err != nil {
		return err
	}
	if err := rekeyCodexMonitoringProjection(tx); err != nil {
		return err
	}
	// Pricing-hourly has its identity revision in its primary key. An
	// interrupted prior rollout may have left a c2 and a c3 row for the same
	// physical bucket, so this must fold rather than blindly UPDATE c2 -> c3.
	if err := foldCodexPricingHourlyRows(tx); err != nil {
		return err
	}
	// Daily monitoring tables may already have partial codex-3 rows from a
	// prior interrupted rollout. Their primary keys include the revision, so a
	// blind UPDATE could collide. Fold each physical daily row through a
	// revision-agnostic aggregate before replacing the old rows.
	if err := foldCodexMonitoringDailyRows(tx, usageMonitoringAccountDailyTable, false); err != nil {
		return err
	}
	if err := foldCodexMonitoringDailyRows(tx, usageMonitoringAPIKeyDailyTable, true); err != nil {
		return err
	}
	if err := replaceCodexRevision(tx, "usage_pricing_rollup_state", "structure_revision"); err != nil {
		return err
	}
	if err := replaceCodexRevision(tx, usageMonitoringRollupStateTable, "structure_revision"); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from ` + usagePricingAccountPriceCacheTable); err != nil {
		return fmt.Errorf("clear account price cache during archived codex identity fold: %w", err)
	}
	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		accountHistoryIdentityFormatVersionKey,
		usageidentity.AccountHistoryStructureRevision(),
		time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("record archived codex identity fold: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit archived codex identity fold: %w", err)
	}
	return nil
}

func replaceCodexRevision(tx *sql.Tx, tableName, columnName string) error {
	if tx == nil {
		return fmt.Errorf("archived codex revision fold transaction is unavailable")
	}
	// All identifiers are private constants selected by this package, never
	// caller input. SQLite replace is intentionally scoped to the custom
	// codex-2 segment, so unrelated historical price revisions remain intact.
	query := `update ` + tableName + ` set ` + columnName + ` = replace(` + columnName + `, ':codex-2:', ':codex-3:')
		where instr(` + columnName + `, ':codex-2:') > 0`
	if _, err := tx.Exec(query); err != nil {
		return fmt.Errorf("rewrite %s codex identity revision: %w", tableName, err)
	}
	return nil
}

func foldCodexPricingHourlyRows(tx *sql.Tx) error {
	if tx == nil {
		return fmt.Errorf("archived codex pricing-hourly fold transaction is unavailable")
	}
	columns := []string{
		"structure_revision", "bucket_ms", "model", "billing_model", "pricing_model", "service_tier",
		"context_threshold_tokens", "failed", "calls", "input_tokens", "output_tokens", "reasoning_tokens",
		"cached_tokens", "cache_read_tokens", "cache_creation_tokens", "long_input_tokens", "long_output_tokens",
		"long_cached_tokens", "long_cache_read_tokens", "long_cache_creation_tokens", "total_tokens",
		"latency_sum_ms", "latency_samples", "zero_token_calls", "updated_at_ms",
	}
	selectColumns := strings.Join(columns, ", ")
	rows, err := tx.Query(`select ` + selectColumns + ` from usage_pricing_hourly_rollups_v1
		where instr(structure_revision, ':codex-2:') > 0 or instr(structure_revision, ':codex-3:') > 0`)
	if err != nil {
		return fmt.Errorf("read archived pricing-hourly rows: %w", err)
	}
	defer rows.Close()
	type hourlyRow struct{ values []any }
	result := make(map[string]hourlyRow)
	for rows.Next() {
		values, scans := scannedArchivedValues(len(columns))
		if err := rows.Scan(scans...); err != nil {
			return fmt.Errorf("scan archived pricing-hourly row: %w", err)
		}
		dereferenceArchivedValues(values)
		values[0] = codexV3Revision(sqlString(values[0]))
		const callsIndex = 8
		keyParts := make([]string, 0, callsIndex)
		for index := 0; index < callsIndex; index++ {
			keyParts = append(keyParts, fmt.Sprint(values[index]))
		}
		key := strings.Join(keyParts, "\x00")
		if current, exists := result[key]; exists {
			for index := callsIndex; index < len(values); index++ {
				if columns[index] == "updated_at_ms" {
					if sqlInt64(values[index]) > sqlInt64(current.values[index]) {
						current.values[index] = values[index]
					}
					continue
				}
				current.values[index] = sqlInt64(current.values[index]) + sqlInt64(values[index])
			}
			result[key] = current
			continue
		}
		result[key] = hourlyRow{values: values}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archived pricing-hourly rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archived pricing-hourly rows: %w", err)
	}
	if len(result) == 0 {
		return nil
	}
	if _, err := tx.Exec(`delete from usage_pricing_hourly_rollups_v1
		where instr(structure_revision, ':codex-2:') > 0 or instr(structure_revision, ':codex-3:') > 0`); err != nil {
		return fmt.Errorf("clear archived pricing-hourly rows: %w", err)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement, err := tx.Prepare(`insert into usage_pricing_hourly_rollups_v1 (` + selectColumns + `) values (` + placeholders + `)`)
	if err != nil {
		return fmt.Errorf("prepare archived pricing-hourly insert: %w", err)
	}
	defer statement.Close()
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := statement.Exec(result[key].values...); err != nil {
			return fmt.Errorf("insert folded archived pricing-hourly row: %w", err)
		}
	}
	return nil
}

func scannedArchivedValues(count int) ([]any, []any) {
	values := make([]any, count)
	scans := make([]any, count)
	for index := range values {
		var value any
		values[index] = &value
		scans[index] = &value
	}
	return values, scans
}

func dereferenceArchivedValues(values []any) {
	for index := range values {
		values[index] = *(values[index].(*any))
	}
}

func foldCodexMonitoringDailyRows(tx *sql.Tx, tableName string, apiKeyFirst bool) error {
	if tx == nil {
		return fmt.Errorf("archived codex daily fold transaction is unavailable")
	}
	columns := []string{
		"structure_revision", "bucket_ms", "account_snapshot", "auth_label_snapshot", "provider",
		"auth_provider_snapshot", "auth_account_id_snapshot", "auth_index", "source", "source_hash",
		"auth_file_snapshot", "api_key_hash", "executor_type", "model", "billing_model", "pricing_model",
		"service_tier", "context_threshold_tokens", "failed", "calls", "input_tokens", "output_tokens",
		"reasoning_tokens", "cached_tokens", "cache_read_tokens", "cache_creation_tokens", "long_input_tokens",
		"long_output_tokens", "long_cached_tokens", "long_cache_read_tokens", "long_cache_creation_tokens",
		"total_tokens", "zero_token_calls", "latency_sum_ms", "latency_samples", "last_seen_ms", "updated_at_ms",
	}
	if apiKeyFirst {
		columns = []string{
			"structure_revision", "bucket_ms", "api_key_hash", "account_snapshot", "auth_label_snapshot", "provider",
			"auth_provider_snapshot", "auth_account_id_snapshot", "auth_index", "source", "source_hash",
			"auth_file_snapshot", "executor_type", "model", "billing_model", "pricing_model", "service_tier",
			"context_threshold_tokens", "failed", "calls", "input_tokens", "output_tokens", "reasoning_tokens",
			"cached_tokens", "cache_read_tokens", "cache_creation_tokens", "long_input_tokens", "long_output_tokens",
			"long_cached_tokens", "long_cache_read_tokens", "long_cache_creation_tokens", "total_tokens",
			"zero_token_calls", "latency_sum_ms", "latency_samples", "last_seen_ms", "updated_at_ms",
		}
	}
	selectColumns := strings.Join(columns, ", ")
	rows, err := tx.Query(`select ` + selectColumns + ` from ` + tableName + `
		where instr(structure_revision, ':codex-2:') > 0 or instr(structure_revision, ':codex-3:') > 0`)
	if err != nil {
		return fmt.Errorf("read archived %s rows: %w", tableName, err)
	}
	defer rows.Close()
	type dailyRow struct {
		values []any
		key    string
	}
	result := make(map[string]dailyRow)
	for rows.Next() {
		values, scans := scannedArchivedValues(len(columns))
		if err := rows.Scan(scans...); err != nil {
			return fmt.Errorf("scan archived %s row: %w", tableName, err)
		}
		// Dereference each scan pointer into a stable value slice.
		dereferenceArchivedValues(values)
		values[0] = codexV3Revision(sqlString(values[0]))
		// Numeric aggregation columns start at calls; only dimensions before
		// that point identify a physical daily bucket.
		// Both daily schemas keep the failed counter as the final dimension
		// before the numeric aggregates.  The API-key variant only moves the
		// API key column; it does not change the calls offset.
		callsIndex := 19
		keyParts := make([]string, 0, callsIndex)
		for index := 0; index < callsIndex; index++ {
			keyParts = append(keyParts, fmt.Sprint(values[index]))
		}
		key := strings.Join(keyParts, "\x00")
		if current, exists := result[key]; exists {
			for index := callsIndex; index < len(values); index++ {
				switch columns[index] {
				case "last_seen_ms", "updated_at_ms":
					if sqlInt64(values[index]) > sqlInt64(current.values[index]) {
						current.values[index] = values[index]
					}
				default:
					current.values[index] = sqlInt64(current.values[index]) + sqlInt64(values[index])
				}
			}
			result[key] = current
		} else {
			result[key] = dailyRow{values: values, key: key}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archived %s rows: %w", tableName, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archived %s rows: %w", tableName, err)
	}
	if len(result) == 0 {
		return nil
	}
	if _, err := tx.Exec(`delete from ` + tableName + ` where instr(structure_revision, ':codex-2:') > 0 or instr(structure_revision, ':codex-3:') > 0`); err != nil {
		return fmt.Errorf("clear archived %s rows: %w", tableName, err)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement, err := tx.Prepare(`insert into ` + tableName + ` (` + selectColumns + `) values (` + placeholders + `)`)
	if err != nil {
		return fmt.Errorf("prepare archived %s insert: %w", tableName, err)
	}
	defer statement.Close()
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := result[key].values
		if _, err := statement.Exec(values...); err != nil {
			return fmt.Errorf("insert folded archived %s row: %w", tableName, err)
		}
	}
	return nil
}

func sqlString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func sqlInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case []byte:
		var parsed int64
		_, _ = fmt.Sscan(string(typed), &parsed)
		return parsed
	case string:
		var parsed int64
		_, _ = fmt.Sscan(typed, &parsed)
		return parsed
	default:
		var parsed int64
		_, _ = fmt.Sscan(fmt.Sprint(value), &parsed)
		return parsed
	}
}

func rekeyCodexMonitoringProjection(tx *sql.Tx) error {
	if tx == nil {
		return fmt.Errorf("archived codex projection fold transaction is unavailable")
	}
	key := usageidentity.SQLAccountKeyExpression("")
	canonical := usageidentity.SQLHasCanonicalCodexMemberExpression("")
	if _, err := tx.Exec(`update ` + usageprojection.EventTable + ` set account_key = ` + key + `
		where ` + canonical + ` and account_key <> ` + key); err != nil {
		return fmt.Errorf("rekey archived codex monitoring projection: %w", err)
	}
	return nil
}

type archivedCodexAccountRow struct {
	accountKey, accountSnapshot, authLabelSnapshot, authProviderSnapshot string
	authIndex, source, sourceHash, model, billingModel, serviceTier      string
	calls, successCalls, failureCalls                                    int64
	inputTokens, outputTokens, reasoningTokens, cachedTokens             int64
	cacheReadTokens, cacheCreationTokens                                 int64
	longInputTokens, longOutputTokens, longCachedTokens                  int64
	longCacheReadTokens, longCacheCreationTokens                         int64
	totalTokens, firstSeenMS, lastSeenMS, updatedAtMS                    int64
}

func (r archivedCodexAccountRow) compositeKey() string {
	return strings.Join([]string{r.accountKey, r.model, r.billingModel, r.serviceTier}, "\x00")
}

func foldCodexAccountHistoryRows(tx *sql.Tx) error {
	rows, err := tx.Query(`select account_key, coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''),
		coalesce(auth_provider_snapshot, ''), coalesce(auth_index, ''), coalesce(source, ''), coalesce(source_hash, ''),
		model, billing_model, service_tier, calls, success_calls, failure_calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens, long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
		from ` + usageAccountModelRollupsTable)
	if err != nil {
		return fmt.Errorf("read archived account history rows: %w", err)
	}
	defer rows.Close()

	originals := make([]archivedCodexAccountRow, 0)
	folded := make(map[string]archivedCodexAccountRow)
	for rows.Next() {
		var row archivedCodexAccountRow
		if err := rows.Scan(
			&row.accountKey, &row.accountSnapshot, &row.authLabelSnapshot, &row.authProviderSnapshot,
			&row.authIndex, &row.source, &row.sourceHash, &row.model, &row.billingModel, &row.serviceTier,
			&row.calls, &row.successCalls, &row.failureCalls,
			&row.inputTokens, &row.outputTokens, &row.reasoningTokens, &row.cachedTokens, &row.cacheReadTokens, &row.cacheCreationTokens,
			&row.longInputTokens, &row.longOutputTokens, &row.longCachedTokens, &row.longCacheReadTokens, &row.longCacheCreationTokens,
			&row.totalTokens, &row.firstSeenMS, &row.lastSeenMS, &row.updatedAtMS,
		); err != nil {
			return fmt.Errorf("scan archived account history row: %w", err)
		}
		canonical, email, ok := archivedCodexCanonicalKey(row.authProviderSnapshot, row.accountSnapshot)
		if !ok {
			continue
		}
		originals = append(originals, row)
		row.accountKey = canonical
		row.accountSnapshot = email
		row.authProviderSnapshot = "codex"
		key := row.compositeKey()
		if current, exists := folded[key]; exists {
			folded[key] = mergeArchivedCodexAccountRows(current, row)
		} else {
			folded[key] = row
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archived account history rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archived account history rows: %w", err)
	}
	if len(originals) == 0 {
		return nil
	}
	if err := deleteArchivedCodexAccountRows(tx, originals); err != nil {
		return err
	}
	return insertArchivedCodexAccountRows(tx, folded)
}

func deleteArchivedCodexAccountRows(tx *sql.Tx, rows []archivedCodexAccountRow) error {
	statement, err := tx.Prepare(`delete from ` + usageAccountModelRollupsTable + `
		where account_key = ? and model = ? and billing_model = ? and service_tier = ?`)
	if err != nil {
		return fmt.Errorf("prepare archived account history delete: %w", err)
	}
	defer statement.Close()
	for _, row := range rows {
		if _, err := statement.Exec(row.accountKey, row.model, row.billingModel, row.serviceTier); err != nil {
			return fmt.Errorf("delete archived account history row: %w", err)
		}
	}
	return nil
}

func insertArchivedCodexAccountRows(tx *sql.Tx, values map[string]archivedCodexAccountRow) error {
	keys := sortedArchivedCodexKeys(values)
	statement, err := tx.Prepare(`insert into ` + usageAccountModelRollupsTable + ` (
		account_key, account_snapshot, auth_label_snapshot, auth_provider_snapshot,
		auth_index, source, source_hash, model, billing_model, service_tier,
		calls, success_calls, failure_calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens, long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare archived account history insert: %w", err)
	}
	defer statement.Close()
	for _, key := range keys {
		row := values[key]
		if _, err := statement.Exec(
			row.accountKey, row.accountSnapshot, row.authLabelSnapshot, row.authProviderSnapshot,
			row.authIndex, row.source, row.sourceHash, row.model, row.billingModel, row.serviceTier,
			row.calls, row.successCalls, row.failureCalls,
			row.inputTokens, row.outputTokens, row.reasoningTokens, row.cachedTokens, row.cacheReadTokens, row.cacheCreationTokens,
			row.longInputTokens, row.longOutputTokens, row.longCachedTokens, row.longCacheReadTokens, row.longCacheCreationTokens,
			row.totalTokens, row.firstSeenMS, row.lastSeenMS, row.updatedAtMS,
		); err != nil {
			return fmt.Errorf("insert folded archived account history row: %w", err)
		}
	}
	return nil
}

type archivedCodexPricingRow struct {
	archivedCodexAccountRow
	structureRevision, pricingModel string
	contextThresholdTokens          int64
}

func (r archivedCodexPricingRow) compositeKey() string {
	return strings.Join([]string{
		r.structureRevision, r.accountKey, r.model, r.billingModel, r.pricingModel,
		r.serviceTier, fmt.Sprintf("%d", r.contextThresholdTokens),
	}, "\x00")
}

func foldCodexPricingAccountRows(tx *sql.Tx) error {
	rows, err := tx.Query(`select structure_revision, account_key, coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''),
		coalesce(auth_provider_snapshot, ''), coalesce(auth_index, ''), coalesce(source, ''), coalesce(source_hash, ''),
		model, billing_model, pricing_model, service_tier, context_threshold_tokens,
		calls, success_calls, failure_calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens, long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
		from ` + usagePricingAccountRollupsTable + `
		where instr(structure_revision, ':codex-2:') > 0 or instr(structure_revision, ':codex-3:') > 0`)
	if err != nil {
		return fmt.Errorf("read archived pricing account rows: %w", err)
	}
	defer rows.Close()

	originals := make([]archivedCodexPricingRow, 0)
	folded := make(map[string]archivedCodexPricingRow)
	for rows.Next() {
		var row archivedCodexPricingRow
		if err := rows.Scan(
			&row.structureRevision, &row.accountKey, &row.accountSnapshot, &row.authLabelSnapshot,
			&row.authProviderSnapshot, &row.authIndex, &row.source, &row.sourceHash,
			&row.model, &row.billingModel, &row.pricingModel, &row.serviceTier, &row.contextThresholdTokens,
			&row.calls, &row.successCalls, &row.failureCalls,
			&row.inputTokens, &row.outputTokens, &row.reasoningTokens, &row.cachedTokens, &row.cacheReadTokens, &row.cacheCreationTokens,
			&row.longInputTokens, &row.longOutputTokens, &row.longCachedTokens, &row.longCacheReadTokens, &row.longCacheCreationTokens,
			&row.totalTokens, &row.firstSeenMS, &row.lastSeenMS, &row.updatedAtMS,
		); err != nil {
			return fmt.Errorf("scan archived pricing account row: %w", err)
		}
		originals = append(originals, row)
		row.structureRevision = codexV3Revision(row.structureRevision)
		if canonical, email, ok := archivedCodexCanonicalKey(row.authProviderSnapshot, row.accountSnapshot); ok {
			row.accountKey = canonical
			row.accountSnapshot = email
			row.authProviderSnapshot = "codex"
		}
		key := row.compositeKey()
		if current, exists := folded[key]; exists {
			merged := mergeArchivedCodexAccountRows(current.archivedCodexAccountRow, row.archivedCodexAccountRow)
			row.archivedCodexAccountRow = merged
			folded[key] = row
		} else {
			folded[key] = row
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archived pricing account rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close archived pricing account rows: %w", err)
	}
	if len(originals) == 0 {
		return nil
	}
	if err := deleteArchivedCodexPricingRows(tx, originals); err != nil {
		return err
	}
	return insertArchivedCodexPricingRows(tx, folded)
}

func deleteArchivedCodexPricingRows(tx *sql.Tx, rows []archivedCodexPricingRow) error {
	statement, err := tx.Prepare(`delete from ` + usagePricingAccountRollupsTable + `
		where structure_revision = ? and account_key = ? and model = ? and billing_model = ?
		and pricing_model = ? and service_tier = ? and context_threshold_tokens = ?`)
	if err != nil {
		return fmt.Errorf("prepare archived pricing account delete: %w", err)
	}
	defer statement.Close()
	for _, row := range rows {
		if _, err := statement.Exec(row.structureRevision, row.accountKey, row.model, row.billingModel,
			row.pricingModel, row.serviceTier, row.contextThresholdTokens); err != nil {
			return fmt.Errorf("delete archived pricing account row: %w", err)
		}
	}
	return nil
}

func insertArchivedCodexPricingRows(tx *sql.Tx, values map[string]archivedCodexPricingRow) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	statement, err := tx.Prepare(`insert into ` + usagePricingAccountRollupsTable + ` (
		structure_revision, account_key, account_snapshot, auth_label_snapshot, auth_provider_snapshot,
		auth_index, source, source_hash, model, billing_model, pricing_model, service_tier,
		context_threshold_tokens, calls, success_calls, failure_calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens, long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare archived pricing account insert: %w", err)
	}
	defer statement.Close()
	for _, key := range keys {
		row := values[key]
		if _, err := statement.Exec(
			row.structureRevision, row.accountKey, row.accountSnapshot, row.authLabelSnapshot, row.authProviderSnapshot,
			row.authIndex, row.source, row.sourceHash, row.model, row.billingModel, row.pricingModel, row.serviceTier,
			row.contextThresholdTokens, row.calls, row.successCalls, row.failureCalls,
			row.inputTokens, row.outputTokens, row.reasoningTokens, row.cachedTokens, row.cacheReadTokens, row.cacheCreationTokens,
			row.longInputTokens, row.longOutputTokens, row.longCachedTokens, row.longCacheReadTokens, row.longCacheCreationTokens,
			row.totalTokens, row.firstSeenMS, row.lastSeenMS, row.updatedAtMS,
		); err != nil {
			return fmt.Errorf("insert folded archived pricing account row: %w", err)
		}
	}
	return nil
}

func archivedCodexCanonicalKey(provider, account string) (string, string, bool) {
	provider = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(provider), "_", "-"))
	if provider != "codex" {
		return "", "", false
	}
	email, ok := usageidentity.NormalizeCodexMemberSnapshot(account)
	if !ok {
		return "", "", false
	}
	key, ok := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot: "codex",
		AccountSnapshot:      email,
	})
	return key, email, ok
}

func codexV3Revision(revision string) string {
	return strings.Replace(revision, ":codex-2:", ":codex-3:", 1)
}

func sortedArchivedCodexKeys(values map[string]archivedCodexAccountRow) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeArchivedCodexAccountRows(current, incoming archivedCodexAccountRow) archivedCodexAccountRow {
	useIncomingMetadata := incoming.lastSeenMS > current.lastSeenMS ||
		(incoming.lastSeenMS == current.lastSeenMS && incoming.updatedAtMS > current.updatedAtMS) ||
		(incoming.lastSeenMS == current.lastSeenMS && incoming.updatedAtMS == current.updatedAtMS &&
			incoming.source+"\x00"+incoming.authIndex > current.source+"\x00"+current.authIndex)
	if useIncomingMetadata {
		current.accountSnapshot = incoming.accountSnapshot
		current.authLabelSnapshot = incoming.authLabelSnapshot
		current.authProviderSnapshot = incoming.authProviderSnapshot
		current.authIndex = incoming.authIndex
		current.source = incoming.source
		current.sourceHash = incoming.sourceHash
	}
	current.calls += incoming.calls
	current.successCalls += incoming.successCalls
	current.failureCalls += incoming.failureCalls
	current.inputTokens += incoming.inputTokens
	current.outputTokens += incoming.outputTokens
	current.reasoningTokens += incoming.reasoningTokens
	current.cachedTokens += incoming.cachedTokens
	current.cacheReadTokens += incoming.cacheReadTokens
	current.cacheCreationTokens += incoming.cacheCreationTokens
	current.longInputTokens += incoming.longInputTokens
	current.longOutputTokens += incoming.longOutputTokens
	current.longCachedTokens += incoming.longCachedTokens
	current.longCacheReadTokens += incoming.longCacheReadTokens
	current.longCacheCreationTokens += incoming.longCacheCreationTokens
	current.totalTokens += incoming.totalTokens
	current.firstSeenMS = earliestArchivedTimestamp(current.firstSeenMS, incoming.firstSeenMS)
	if incoming.lastSeenMS > current.lastSeenMS {
		current.lastSeenMS = incoming.lastSeenMS
	}
	if incoming.updatedAtMS > current.updatedAtMS {
		current.updatedAtMS = incoming.updatedAtMS
	}
	return current
}

func earliestArchivedTimestamp(current, incoming int64) int64 {
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
