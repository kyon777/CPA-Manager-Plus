package usagepricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const accountPriceCacheTable = "usage_pricing_account_price_cache_v1"

func (r *repository) LoadAccountPriceCacheTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]PriceCacheRow, error) {
	keys := normalizeValues(accountKeys)
	if len(keys) == 0 {
		return []PriceCacheRow{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		select account_key, pricing_revision, covered_event_id, total_cost,
			updated_at_ms, status, last_error
		from (
			select c.*,
				row_number() over (
					partition by account_key order by updated_at_ms desc, pricing_revision desc
				) as rn
			from %s c
			where account_key in (%s)
		)
		where rn = 1`, accountPriceCacheTable, placeholders), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PriceCacheRow, 0, len(keys))
	for rows.Next() {
		var row PriceCacheRow
		if err := rows.Scan(
			&row.AccountKey,
			&row.PricingRevision,
			&row.CoveredEventID,
			&row.TotalCost,
			&row.UpdatedAtMS,
			&row.Status,
			&row.LastError,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *repository) UpsertAccountPriceCache(ctx context.Context, rows []PriceCacheRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
		insert into %s (
			account_key, pricing_revision, covered_event_id, total_cost,
			updated_at_ms, status, last_error
		) values (?, ?, ?, ?, ?, ?, ?)
		on conflict(account_key, pricing_revision) do update set
			covered_event_id = excluded.covered_event_id,
			total_cost = excluded.total_cost,
			updated_at_ms = excluded.updated_at_ms,
			status = excluded.status,
			last_error = excluded.last_error`, accountPriceCacheTable))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if strings.TrimSpace(row.AccountKey) == "" || strings.TrimSpace(row.PricingRevision) == "" {
			return errors.New("account price cache row requires account_key and pricing_revision")
		}
		status := strings.TrimSpace(row.Status)
		if status == "" {
			status = "fresh"
		}
		if status != "fresh" && status != "stale" && status != "failed" {
			return fmt.Errorf("unsupported account price cache status %q", status)
		}
		if _, err := stmt.ExecContext(ctx,
			row.AccountKey,
			row.PricingRevision,
			row.CoveredEventID,
			row.TotalCost,
			row.UpdatedAtMS,
			status,
			row.LastError,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *repository) MarkAccountPriceCacheFailure(ctx context.Context, accountKeys []string, revision string, message string, nowMS int64) error {
	keys := normalizeValues(accountKeys)
	revision = strings.TrimSpace(revision)
	if len(keys) == 0 || revision == "" {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+3)
	args = append(args, message, nowMS, revision)
	for _, key := range keys {
		args = append(args, key)
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
		update %s
		set status = 'failed', last_error = ?, updated_at_ms = ?
		where pricing_revision = ? and account_key in (%s)`, accountPriceCacheTable, placeholders),
		args...)
	if err != nil {
		return err
	}
	return tx.Commit()
}
