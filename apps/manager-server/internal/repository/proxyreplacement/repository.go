package proxyreplacement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

var (
	ErrTaskNotFound       = errors.New("proxy replacement task was not found")
	ErrTaskNotConfirmable = errors.New("proxy replacement task cannot be confirmed")
	ErrProxyStateNotFound = errors.New("proxy pool state was not found")
)

// Repository deliberately returns decrypted task URLs only to its caller. The
// only on-disk task URL columns are ciphertext fields.
type Repository interface {
	CreateDraft(context.Context, model.ProxyReplacementTask, []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error)
	GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error)
	ConfirmDraft(context.Context, int64, int64) (model.ProxyReplacementTask, error)
	ClaimNextQueued(context.Context) (model.ProxyReplacementTask, bool, error)
	RequeueRunningOnStartup(context.Context) (int64, error)
	MarkItem(context.Context, int64, model.ProxyReplacementItemStatus, model.ProxyReplacementItemOutcome) error
	ResetInFlightItems(context.Context) (int64, error)
	CompleteTask(context.Context, int64) (model.ProxyReplacementTask, error)
	ExpireDrafts(context.Context, int64) (int64, error)
	UpsertProxyPoolState(context.Context, model.ProxyPoolState) (model.ProxyPoolState, error)
	ListProxyPoolStates(context.Context) ([]model.ProxyPoolState, error)
	RestoreProxyPoolState(context.Context, string) (model.ProxyPoolState, error)
}

type repository struct {
	db        *sql.DB
	protector *security.Protector
}

func New(db *sql.DB, protector ...*security.Protector) Repository {
	var activeProtector *security.Protector
	if len(protector) > 0 {
		activeProtector = protector[0]
	}
	return &repository{db: db, protector: activeProtector}
}

func (r *repository) CreateDraft(ctx context.Context, task model.ProxyReplacementTask, items []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error) {
	if r == nil || r.db == nil {
		return model.ProxyReplacementTask{}, errors.New("proxy replacement repository is unavailable")
	}
	if !validSource(task.Source) || !validMode(task.Mode) {
		return model.ProxyReplacementTask{}, errors.New("proxy replacement task source or mode is invalid")
	}
	now := time.Now().UnixMilli()
	if task.CreatedAtMS <= 0 {
		task.CreatedAtMS = now
	}
	task.UpdatedAtMS = now
	if task.Status == "" {
		task.Status = model.ProxyReplacementTaskDraft
	}
	if task.Status != model.ProxyReplacementTaskDraft && task.Status != model.ProxyReplacementTaskUnavailable {
		return model.ProxyReplacementTask{}, errors.New("proxy replacement draft status is invalid")
	}
	task.TotalCount = len(items)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `insert into proxy_replacement_tasks (
		source, mode, status, recheck_after_write, task_offset, total_count,
		updated_count, failed_count, skipped_count, recheck_success_count, recheck_failed_count, recheck_inconclusive_count,
		unused_normal_count, fallback_normal_count, suspect_excluded_count, expires_at_ms,
		created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Source, task.Mode, task.Status, boolToInt(task.RecheckAfterWrite), task.TaskOffset, task.TotalCount,
		task.UpdatedCount, task.FailedCount, task.SkippedCount, task.RecheckSuccessCount, task.RecheckFailedCount, task.RecheckInconclusiveCount,
		task.UnusedNormalCount, task.FallbackNormalCount, task.SuspectExcludedCount, nullableInt64(task.ExpiresAtMS),
		task.CreatedAtMS, task.UpdatedAtMS,
	)
	if err != nil {
		return model.ProxyReplacementTask{}, fmt.Errorf("create proxy replacement task: %w", err)
	}
	taskID, err := result.LastInsertId()
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}

	for index, item := range items {
		if err := validateItem(item); err != nil {
			return model.ProxyReplacementTask{}, err
		}
		oldProxyURL := model.NormalizeProxyURL(item.OldProxyURL)
		newProxyURL := model.NormalizeProxyURL(item.NewProxyURL)
		oldCiphertext, err := r.protect(oldProxyURL)
		if err != nil {
			return model.ProxyReplacementTask{}, fmt.Errorf("protect old proxy URL: %w", err)
		}
		newCiphertext, err := r.protect(newProxyURL)
		if err != nil {
			return model.ProxyReplacementTask{}, fmt.Errorf("protect new proxy URL: %w", err)
		}
		status := item.Status
		if status == "" {
			status = model.ProxyReplacementItemPlanned
		}
		if _, err := tx.ExecContext(ctx, `insert into proxy_replacement_task_items (
			task_id, item_order, client_key, inspection_run_id, inspection_account_key,
			file_name, auth_index, account_email, provider,
			runtime_id, account_id, account_snapshot, old_proxy_fingerprint, old_proxy_ciphertext,
			new_proxy_fingerprint, new_proxy_ciphertext, allocation_source, status,
			write_error_code, write_error_message, recheck_error_code, recheck_error_message,
			created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, index, item.Target.ClientKey, item.Target.InspectionRunID, item.Target.InspectionAccountKey,
			item.Target.FileName, item.Target.AuthIndex,
			item.Target.AccountEmail, item.Target.Provider, item.Target.RuntimeID, item.Target.AccountID,
			item.Target.AccountSnapshot, model.ProxyFingerprint(oldProxyURL), oldCiphertext,
			model.ProxyFingerprint(newProxyURL), newCiphertext, item.AllocationSource, status,
			nullableString(item.WriteErrorCode), nullableString(item.WriteErrorMessage),
			nullableString(item.RecheckErrorCode), nullableString(item.RecheckErrorMessage), now, now,
		); err != nil {
			return model.ProxyReplacementTask{}, fmt.Errorf("create proxy replacement task item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.ProxyReplacementTask{}, err
	}
	page, err := r.GetTask(ctx, taskID, 1, 1)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	return page.Task, nil
}

func (r *repository) GetTask(ctx context.Context, taskID int64, page, pageSize int) (model.ProxyReplacementTaskPage, error) {
	if taskID <= 0 {
		return model.ProxyReplacementTaskPage{}, ErrTaskNotFound
	}
	page, pageSize = normalizePage(page, pageSize)
	task, err := readTask(ctx, r.db, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProxyReplacementTaskPage{}, ErrTaskNotFound
	}
	if err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	task.CanConfirm = task.Status == model.ProxyReplacementTaskDraft && (task.ExpiresAtMS <= 0 || task.ExpiresAtMS > time.Now().UnixMilli())

	var total int
	if err := r.db.QueryRowContext(ctx, `select count(*) from proxy_replacement_task_items where task_id = ?`, taskID).Scan(&total); err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	offset := (page - 1) * pageSize
	rows, err := r.db.QueryContext(ctx, `select
		id, task_id, item_order, client_key, inspection_run_id, inspection_account_key,
		file_name, auth_index, account_email, provider,
		runtime_id, account_id, account_snapshot, old_proxy_fingerprint, old_proxy_ciphertext,
		new_proxy_fingerprint, new_proxy_ciphertext, allocation_source, status,
		coalesce(write_error_code, ''), coalesce(write_error_message, ''),
		coalesce(recheck_error_code, ''), coalesce(recheck_error_message, '')
		from proxy_replacement_task_items where task_id = ?
		order by item_order asc, id asc limit ? offset ?`, taskID, pageSize, offset)
	if err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	defer rows.Close()
	items := make([]model.ProxyReplacementTaskItem, 0, pageSize)
	for rows.Next() {
		item, err := r.scanTaskItem(rows)
		if err != nil {
			return model.ProxyReplacementTaskPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return model.ProxyReplacementTaskPage{}, err
	}
	return model.ProxyReplacementTaskPage{Task: task, Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *repository) ConfirmDraft(ctx context.Context, taskID, nowMS int64) (model.ProxyReplacementTask, error) {
	if taskID <= 0 {
		return model.ProxyReplacementTask{}, ErrTaskNotFound
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `update proxy_replacement_tasks set
		status = ?, confirmed_at_ms = ?, updated_at_ms = ?
		where id = ? and status = ? and (expires_at_ms is null or expires_at_ms = 0 or expires_at_ms > ?)`,
		model.ProxyReplacementTaskQueued, nowMS, nowMS, taskID, model.ProxyReplacementTaskDraft, nowMS)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	if affected != 1 {
		if _, err := tx.ExecContext(ctx, `update proxy_replacement_tasks set status = ?, updated_at_ms = ?
			where id = ? and status = ? and expires_at_ms is not null and expires_at_ms > 0 and expires_at_ms <= ?`,
			model.ProxyReplacementTaskExpired, nowMS, taskID, model.ProxyReplacementTaskDraft, nowMS); err != nil {
			return model.ProxyReplacementTask{}, err
		}
		return model.ProxyReplacementTask{}, ErrTaskNotConfirmable
	}
	task, err := readTask(ctx, tx, taskID)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ProxyReplacementTask{}, err
	}
	return task, nil
}

func (r *repository) ClaimNextQueued(ctx context.Context) (model.ProxyReplacementTask, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var taskID int64
	err = tx.QueryRowContext(ctx, `select id from proxy_replacement_tasks
		where status = ? order by created_at_ms asc, id asc limit 1`, model.ProxyReplacementTaskQueued).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProxyReplacementTask{}, false, nil
	}
	if err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update proxy_replacement_tasks set status = ?, started_at_ms = ?, updated_at_ms = ?
		where id = ? and status = ?`, model.ProxyReplacementTaskRunning, now, now, taskID, model.ProxyReplacementTaskQueued)
	if err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	if affected != 1 {
		return model.ProxyReplacementTask{}, false, nil
	}
	task, err := readTask(ctx, tx, taskID)
	if err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ProxyReplacementTask{}, false, err
	}
	return task, true, nil
}

func (r *repository) RequeueRunningOnStartup(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	result, err := r.db.ExecContext(ctx, `update proxy_replacement_tasks set status = ?, started_at_ms = null, updated_at_ms = ?
		where status = ?`, model.ProxyReplacementTaskQueued, now, model.ProxyReplacementTaskRunning)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *repository) MarkItem(ctx context.Context, itemID int64, status model.ProxyReplacementItemStatus, outcome model.ProxyReplacementItemOutcome) error {
	if itemID <= 0 || !validItemStatus(status) {
		return errors.New("proxy replacement item update is invalid")
	}
	result, err := r.db.ExecContext(ctx, `update proxy_replacement_task_items set
		status = ?, write_error_code = ?, write_error_message = ?, recheck_error_code = ?, recheck_error_message = ?, updated_at_ms = ?
		where id = ?`, status, nullableString(outcome.WriteErrorCode), nullableString(outcome.WriteErrorMessage),
		nullableString(outcome.RecheckErrorCode), nullableString(outcome.RecheckErrorMessage), time.Now().UnixMilli(), itemID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTaskNotFound
	}
	return nil
}

// ResetInFlightItems makes interrupted writes/probes replay-safe after a
// Manager restart. The worker re-reads CPA before every write, so a PATCH that
// succeeded just before a crash is recognized as already applied.
func (r *repository) ResetInFlightItems(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	result, err := r.db.ExecContext(ctx, `update proxy_replacement_task_items set status = ?, updated_at_ms = ?
		where status in (?, ?)`, model.ProxyReplacementItemPlanned, now,
		model.ProxyReplacementItemWriting, model.ProxyReplacementItemRecheckRunning)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *repository) CompleteTask(ctx context.Context, taskID int64) (model.ProxyReplacementTask, error) {
	if taskID <= 0 {
		return model.ProxyReplacementTask{}, ErrTaskNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var updated, failed, skipped, recheckSucceeded, recheckFailed, recheckInconclusive int
	err = tx.QueryRowContext(ctx, `select
		sum(case when status in (?, ?, ?, ?) then 1 else 0 end),
		sum(case when status = ? then 1 else 0 end),
		sum(case when status = ? then 1 else 0 end),
		sum(case when status = ? then 1 else 0 end),
		sum(case when status = ? then 1 else 0 end),
		sum(case when status = ? then 1 else 0 end)
		from proxy_replacement_task_items where task_id = ?`,
		model.ProxyReplacementItemUpdated, model.ProxyReplacementItemRecheckSucceeded, model.ProxyReplacementItemRecheckFailed, model.ProxyReplacementItemRecheckInconclusive,
		model.ProxyReplacementItemWriteFailed,
		model.ProxyReplacementItemSkippedStale,
		model.ProxyReplacementItemRecheckSucceeded,
		model.ProxyReplacementItemRecheckFailed,
		model.ProxyReplacementItemRecheckInconclusive,
		taskID).Scan(&updated, &failed, &skipped, &recheckSucceeded, &recheckFailed, &recheckInconclusive)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	status := model.ProxyReplacementTaskCompleted
	if failed > 0 || skipped > 0 || recheckFailed > 0 {
		status = model.ProxyReplacementTaskPartial
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update proxy_replacement_tasks set
		status = ?, updated_count = ?, failed_count = ?, skipped_count = ?,
		recheck_success_count = ?, recheck_failed_count = ?, recheck_inconclusive_count = ?, completed_at_ms = ?, updated_at_ms = ?
		where id = ? and status in (?, ?)`, status, updated, failed, skipped, recheckSucceeded, recheckFailed, recheckInconclusive, now, now, taskID,
		model.ProxyReplacementTaskRunning, model.ProxyReplacementTaskQueued)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	if affected != 1 {
		return model.ProxyReplacementTask{}, ErrTaskNotFound
	}
	task, err := readTask(ctx, tx, taskID)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ProxyReplacementTask{}, err
	}
	return task, nil
}

func (r *repository) ExpireDrafts(ctx context.Context, nowMS int64) (int64, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	result, err := r.db.ExecContext(ctx, `update proxy_replacement_tasks set status = ?, updated_at_ms = ?
		where status = ? and expires_at_ms is not null and expires_at_ms > 0 and expires_at_ms <= ?`,
		model.ProxyReplacementTaskExpired, nowMS, model.ProxyReplacementTaskDraft, nowMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *repository) UpsertProxyPoolState(ctx context.Context, state model.ProxyPoolState) (model.ProxyPoolState, error) {
	state.Fingerprint = strings.TrimSpace(state.Fingerprint)
	if state.Fingerprint == "" || !validPoolStatus(state.Status) {
		return model.ProxyPoolState{}, errors.New("proxy pool state is invalid")
	}
	if state.UpdatedAtMS <= 0 {
		state.UpdatedAtMS = time.Now().UnixMilli()
	}
	if state.LastFailureAtMS <= 0 {
		state.LastFailureAtMS = state.UpdatedAtMS
	}
	_, err := r.db.ExecContext(ctx, `insert into proxy_pool_state (
		proxy_fingerprint, status, last_failure_at_ms, last_failure_reason, updated_at_ms
	) values (?, ?, ?, ?, ?)
		on conflict(proxy_fingerprint) do update set
		status = excluded.status, last_failure_at_ms = excluded.last_failure_at_ms,
		last_failure_reason = excluded.last_failure_reason, updated_at_ms = excluded.updated_at_ms`,
		state.Fingerprint, state.Status, nullableInt64(state.LastFailureAtMS), nullableString(state.LastFailureReason), state.UpdatedAtMS)
	if err != nil {
		return model.ProxyPoolState{}, err
	}
	return state, nil
}

func (r *repository) ListProxyPoolStates(ctx context.Context) ([]model.ProxyPoolState, error) {
	rows, err := r.db.QueryContext(ctx, `select proxy_fingerprint, status, coalesce(last_failure_at_ms, 0),
		coalesce(last_failure_reason, ''), updated_at_ms from proxy_pool_state order by updated_at_ms desc, proxy_fingerprint asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]model.ProxyPoolState, 0)
	for rows.Next() {
		var state model.ProxyPoolState
		if err := rows.Scan(&state.Fingerprint, &state.Status, &state.LastFailureAtMS, &state.LastFailureReason, &state.UpdatedAtMS); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (r *repository) RestoreProxyPoolState(ctx context.Context, fingerprint string) (model.ProxyPoolState, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return model.ProxyPoolState{}, ErrProxyStateNotFound
	}
	now := time.Now().UnixMilli()
	result, err := r.db.ExecContext(ctx, `update proxy_pool_state set status = ?, updated_at_ms = ? where proxy_fingerprint = ?`,
		model.ProxyPoolStatusNormal, now, fingerprint)
	if err != nil {
		return model.ProxyPoolState{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.ProxyPoolState{}, err
	}
	if affected != 1 {
		return model.ProxyPoolState{}, ErrProxyStateNotFound
	}
	var state model.ProxyPoolState
	if err := r.db.QueryRowContext(ctx, `select proxy_fingerprint, status, coalesce(last_failure_at_ms, 0),
		coalesce(last_failure_reason, ''), updated_at_ms from proxy_pool_state where proxy_fingerprint = ?`, fingerprint).
		Scan(&state.Fingerprint, &state.Status, &state.LastFailureAtMS, &state.LastFailureReason, &state.UpdatedAtMS); err != nil {
		return model.ProxyPoolState{}, err
	}
	return state, nil
}

type taskRowScanner interface {
	Scan(...any) error
}

func readTask(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID int64) (model.ProxyReplacementTask, error) {
	var task model.ProxyReplacementTask
	var recheckAfterWrite int
	err := queryer.QueryRowContext(ctx, `select
		id, source, mode, status, recheck_after_write, task_offset, total_count,
		updated_count, failed_count, skipped_count, recheck_success_count, recheck_failed_count, recheck_inconclusive_count,
		unused_normal_count, fallback_normal_count, suspect_excluded_count,
		coalesce(expires_at_ms, 0), created_at_ms, updated_at_ms
		from proxy_replacement_tasks where id = ?`, taskID).Scan(
		&task.ID, &task.Source, &task.Mode, &task.Status, &recheckAfterWrite, &task.TaskOffset, &task.TotalCount,
		&task.UpdatedCount, &task.FailedCount, &task.SkippedCount, &task.RecheckSuccessCount, &task.RecheckFailedCount, &task.RecheckInconclusiveCount,
		&task.UnusedNormalCount, &task.FallbackNormalCount, &task.SuspectExcludedCount,
		&task.ExpiresAtMS, &task.CreatedAtMS, &task.UpdatedAtMS,
	)
	if err != nil {
		return model.ProxyReplacementTask{}, err
	}
	task.RecheckAfterWrite = recheckAfterWrite != 0
	return task, nil
}

func (r *repository) scanTaskItem(scanner taskRowScanner) (model.ProxyReplacementTaskItem, error) {
	var item model.ProxyReplacementTaskItem
	var oldCiphertext, newCiphertext string
	err := scanner.Scan(
		&item.ID, &item.TaskID, &item.ItemOrder,
		&item.Target.ClientKey, &item.Target.InspectionRunID, &item.Target.InspectionAccountKey,
		&item.Target.FileName, &item.Target.AuthIndex, &item.Target.AccountEmail, &item.Target.Provider,
		&item.Target.RuntimeID, &item.Target.AccountID, &item.Target.AccountSnapshot,
		&item.OldProxyFingerprint, &oldCiphertext, &item.NewProxyFingerprint, &newCiphertext, &item.AllocationSource, &item.Status,
		&item.WriteErrorCode, &item.WriteErrorMessage, &item.RecheckErrorCode, &item.RecheckErrorMessage,
	)
	if err != nil {
		return model.ProxyReplacementTaskItem{}, err
	}
	item.OldProxyURL, err = r.unprotect(oldCiphertext)
	if err != nil {
		return model.ProxyReplacementTaskItem{}, errors.New("decrypt proxy replacement task URL")
	}
	item.NewProxyURL, err = r.unprotect(newCiphertext)
	if err != nil {
		return model.ProxyReplacementTaskItem{}, errors.New("decrypt proxy replacement task URL")
	}
	return item, nil
}

func (r *repository) protect(value string) (string, error) {
	if r.protector == nil {
		return value, nil
	}
	return r.protector.ProtectString(value)
}

func (r *repository) unprotect(value string) (string, error) {
	if r.protector == nil {
		return value, nil
	}
	return r.protector.UnprotectString(value)
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func validateItem(item model.ProxyReplacementTaskItem) error {
	if strings.TrimSpace(item.Target.ClientKey) == "" || strings.TrimSpace(item.Target.FileName) == "" || strings.TrimSpace(item.Target.Provider) == "" {
		return errors.New("proxy replacement task target is invalid")
	}
	return nil
}

func validSource(source model.ProxyReplacementSource) bool {
	return source == model.ProxyReplacementSourceInspection || source == model.ProxyReplacementSourceBatchUniform || source == model.ProxyReplacementSourceBatchPool
}

func validMode(mode model.ProxyReplacementMode) bool {
	return mode == model.ProxyReplacementModeUniform || mode == model.ProxyReplacementModePool
}

func validPoolStatus(status model.ProxyPoolStatus) bool {
	return status == model.ProxyPoolStatusNormal ||
		status == model.ProxyPoolStatusPendingVerification ||
		status == model.ProxyPoolStatusSuspect
}

func validItemStatus(status model.ProxyReplacementItemStatus) bool {
	switch status {
	case model.ProxyReplacementItemPlanned, model.ProxyReplacementItemWriting, model.ProxyReplacementItemUpdated,
		model.ProxyReplacementItemSkippedStale, model.ProxyReplacementItemWriteFailed,
		model.ProxyReplacementItemRecheckRunning, model.ProxyReplacementItemRecheckSucceeded,
		model.ProxyReplacementItemRecheckFailed, model.ProxyReplacementItemRecheckInconclusive:
		return true
	default:
		return false
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
