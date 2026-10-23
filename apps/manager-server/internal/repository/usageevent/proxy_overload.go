package usageevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	proxyOverloadTargetBatchSize = 500
	proxyOverloadEventLimit      = 9
)

var ErrLatestRequestIndexesUnavailable = errors.New("latest request indexes are unavailable")

// ProxyOverloadEvidence contains only the per-credential flags required to
// build a proxy-level overload summary. It deliberately excludes event bodies,
// account names, files, headers, and request metadata.
type ProxyOverloadEvidence struct {
	RequestIndex     int
	Active           bool
	ServerOverloaded bool
}

// RecentProxyOverloadEvidence reads a fixed top-nine, time-bounded request
// sample for each credential. It requires the derived latest-request indexes
// and uses bounded VALUES batches instead of an account-by-account query path.
func (r *repository) RecentProxyOverloadEvidence(
	ctx context.Context,
	targets []LatestAccountRequestQuery,
	sinceMS int64,
	perCredentialLimit int,
) ([]ProxyOverloadEvidence, error) {
	if len(targets) == 0 {
		return []ProxyOverloadEvidence{}, nil
	}
	if sinceMS <= 0 {
		return nil, errors.New("proxy overload evidence requires a positive since timestamp")
	}
	if perCredentialLimit != proxyOverloadEventLimit {
		return nil, fmt.Errorf("proxy overload evidence requires per-credential limit %d", proxyOverloadEventLimit)
	}

	ready, err := r.latestRequestIndexesReady(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, ErrLatestRequestIndexesUnavailable
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	evidence := make([]ProxyOverloadEvidence, 0, len(targets))
	for _, batch := range proxyOverloadTargetBatches(targets) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := recentProxyOverloadEvidenceBatch(ctx, tx, batch, sinceMS, perCredentialLimit)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, items...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return evidence, nil
}

func proxyOverloadTargetBatches(targets []LatestAccountRequestQuery) [][]LatestAccountRequestQuery {
	if len(targets) == 0 {
		return nil
	}
	batches := make([][]LatestAccountRequestQuery, 0, (len(targets)+proxyOverloadTargetBatchSize-1)/proxyOverloadTargetBatchSize)
	for start := 0; start < len(targets); start += proxyOverloadTargetBatchSize {
		end := start + proxyOverloadTargetBatchSize
		if end > len(targets) {
			end = len(targets)
		}
		batches = append(batches, targets[start:end])
	}
	return batches
}

func recentProxyOverloadEvidenceBatch(
	ctx context.Context,
	tx *sql.Tx,
	targets []LatestAccountRequestQuery,
	sinceMS int64,
	limit int,
) ([]ProxyOverloadEvidence, error) {
	values := make([]string, 0, len(targets))
	args := make([]any, 0, len(targets)*8+3)
	for _, target := range targets {
		authFileSnapshot := strings.TrimSpace(target.AuthFileSnapshot)
		if authFileSnapshot == "" {
			continue
		}
		provider := normalizeLatestRequestProvider(target.Provider)
		workspaceID, member := "", ""
		identityMode := 0
		if provider == "codex" {
			var identityOK, identityConflict bool
			workspaceID, member, identityOK, identityConflict = resolveLatestRequestCodexIdentity(target)
			switch {
			case identityConflict:
				identityMode = 2
			case identityOK:
				identityMode = 1
			}
		}
		values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(
			args,
			target.RequestIndex,
			authFileSnapshot,
			strings.TrimSpace(target.AuthIndex),
			provider,
			workspaceID,
			member,
			strings.TrimSpace(target.AuthProjectIDSnapshot),
			identityMode,
		)
	}
	if len(values) == 0 {
		return []ProxyOverloadEvidence{}, nil
	}
	args = append(args, sinceMS, sinceMS, limit)

	rows, err := tx.QueryContext(ctx, `with credential_targets(
	request_index, auth_file_snapshot, auth_index, provider, workspace_id, member, project_id, identity_mode
) as (
	values `+strings.Join(values, ",")+`
), snapshot_candidates as (
	select
		t.request_index,
		e.id,
		e.timestamp_ms,
		e.failed,
		coalesce(e.fail_status_code, 0) as fail_status_code,
		coalesce(e.header_error_code, '') as header_error_code,
		coalesce(e.fail_summary, '') as fail_summary
	from credential_targets t
	join usage_events e
		on e.auth_file_snapshot collate nocase = t.auth_file_snapshot
		and coalesce(e.auth_index, '') collate nocase = t.auth_index
		and e.timestamp_ms >= ?
		and `+latestRequestBatchedIdentityPredicate("t", "e")+`
), legacy_source_candidates as (
	select
		t.request_index,
		e.id,
		e.timestamp_ms,
		e.failed,
		coalesce(e.fail_status_code, 0) as fail_status_code,
		coalesce(e.header_error_code, '') as header_error_code,
		coalesce(e.fail_summary, '') as fail_summary
	from credential_targets t
	join usage_events e
		on coalesce(e.auth_file_snapshot, '') = ''
		and e.source collate nocase = t.auth_file_snapshot
		and coalesce(e.auth_index, '') collate nocase = t.auth_index
		and e.timestamp_ms >= ?
		and `+latestRequestBatchedIdentityPredicate("t", "e")+`
), candidates as (
	select * from snapshot_candidates
	union all
	select * from legacy_source_candidates
), ranked as (
	select
		request_index,
		failed,
		fail_status_code,
		header_error_code,
		fail_summary,
		row_number() over (
			partition by request_index
			order by timestamp_ms desc, id desc
		) as row_number
	from candidates
)
select
	request_index,
	failed,
	fail_status_code,
	header_error_code,
	fail_summary
from ranked
where row_number <= ?
order by request_index, row_number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byTarget := make(map[int]ProxyOverloadEvidence, len(targets))
	for rows.Next() {
		var (
			requestIndex    int
			failed          int
			failStatusCode  int
			headerErrorCode string
			failSummary     string
		)
		if err := rows.Scan(
			&requestIndex,
			&failed,
			&failStatusCode,
			&headerErrorCode,
			&failSummary,
		); err != nil {
			return nil, err
		}
		current := byTarget[requestIndex]
		current.RequestIndex = requestIndex
		current.Active = true
		current.ServerOverloaded = current.ServerOverloaded ||
			isExactServerOverloaded(failed != 0, failStatusCode, headerErrorCode, failSummary)
		byTarget[requestIndex] = current
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]ProxyOverloadEvidence, 0, len(byTarget))
	for _, target := range targets {
		if item, ok := byTarget[target.RequestIndex]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

func isExactServerOverloaded(failed bool, status int, headerCode, summary string) bool {
	if !failed || status != http.StatusBadGateway {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(headerCode), "server_is_overloaded") {
		return true
	}
	return containsErrorCodeToken(summary, "server_is_overloaded")
}

func containsErrorCodeToken(value, token string) bool {
	value = strings.ToLower(value)
	token = strings.ToLower(token)
	if token == "" {
		return false
	}
	searchFrom := 0
	for searchFrom < len(value) {
		offset := strings.Index(value[searchFrom:], token)
		if offset < 0 {
			return false
		}
		start := searchFrom + offset
		end := start + len(token)
		if (start == 0 || !isErrorCodeTokenByte(value[start-1])) &&
			(end == len(value) || !isErrorCodeTokenByte(value[end])) {
			return true
		}
		searchFrom = start + 1
	}
	return false
}

func isErrorCodeTokenByte(value byte) bool {
	return value == '_' || ('a' <= value && value <= 'z') || ('0' <= value && value <= '9')
}
