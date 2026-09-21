package tokenrecovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

var (
	ErrInvalidTarget = errors.New("token recovery target is invalid")
	ErrTaskNotFound  = errors.New("token recovery task was not found")
)

type Repository interface {
	SignalAutomatic(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, error)
	RequestManual(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, error)
	Get(context.Context, model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error)
	GetByID(context.Context, int64) (model.TokenRecoveryTask, bool, error)
	ClaimNextQueued(context.Context) (model.TokenRecoveryTask, bool, error)
	ClaimNextEligible(context.Context, bool) (model.TokenRecoveryTask, bool, error)
	Complete(context.Context, int64) (model.TokenRecoveryTask, error)
	Fail(context.Context, int64, string, string) (model.TokenRecoveryTask, error)
	FailRunningOnStartup(context.Context) (int64, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) SignalAutomatic(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	normalized, identityKey, err := normalizeTarget(target)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	now := time.Now().UnixMilli()
	seenAt := normalized.ObservedAtMS
	if seenAt <= 0 {
		seenAt = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	defer tx.Rollback()

	existing, found, err := getCompatibleTask(ctx, tx, normalized, identityKey)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if !found {
		// A signal only queues the work. The durable audit marker is written when
		// a worker actually claims the automatic task, so it accurately means an
		// external token-acquisition attempt has begun.
		id, err := insertTask(ctx, tx, identityKey, normalized, model.TokenRecoveryStatusAutoQueued, model.TokenRecoveryModeAuto, 0, seenAt, now)
		if err != nil {
			return model.TokenRecoveryTask{}, err
		}
		item, err := getByID(ctx, tx, id)
		if err != nil {
			return model.TokenRecoveryTask{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.TokenRecoveryTask{}, err
		}
		return item, nil
	}

	if isQueuedOrRunning(existing.Status) {
		// There is already one recovery cycle in flight. Do not relabel a manual
		// cycle as automatic, and do not enqueue a duplicate automatic cycle.
		err = nil
	} else if hasAutomaticAttempt(existing) {
		if existing.AutoAttemptedAtMS <= 0 {
			attemptedAt := firstPositive(existing.StartedAtMS, existing.CompletedAtMS, existing.CreatedAtMS, now)
			_, err = tx.ExecContext(ctx, `update token_recovery_tasks set auto_attempted_at_ms = ?, updated_at_ms = ? where id = ?`, attemptedAt, now, existing.ID)
		}
	} else {
		_, err = tx.ExecContext(ctx, `update token_recovery_tasks set
			status = ?, mode = ?, last_error_code = null, last_error_message = null,
			account_email = case when account_email = '' and ? != '' then ? else account_email end,
			auto_attempted_at_ms = 0, last_signal_at_ms = ?,
			started_at_ms = null, completed_at_ms = null, updated_at_ms = ? where id = ?`,
			model.TokenRecoveryStatusAutoQueued, model.TokenRecoveryModeAuto,
			normalized.AccountEmail, normalized.AccountEmail, seenAt, now, existing.ID)
	}
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	item, err := getByID(ctx, tx, existing.ID)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TokenRecoveryTask{}, err
	}
	return item, nil
}

func (r *repository) RequestManual(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, error) {
	normalized, identityKey, err := normalizeTarget(target)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	now := time.Now().UnixMilli()
	seenAt := normalized.ObservedAtMS
	if seenAt <= 0 {
		seenAt = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	defer tx.Rollback()

	existing, found, err := getCompatibleTask(ctx, tx, normalized, identityKey)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if !found {
		id, err := insertTask(ctx, tx, identityKey, normalized, model.TokenRecoveryStatusManualQueued, model.TokenRecoveryModeManual, 0, seenAt, now)
		if err != nil {
			return model.TokenRecoveryTask{}, err
		}
		item, err := getByID(ctx, tx, id)
		if err != nil {
			return model.TokenRecoveryTask{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.TokenRecoveryTask{}, err
		}
		return item, nil
	}
	if existing.Status == model.TokenRecoveryStatusAutoQueued {
		// An automatic queue can be paused by the persisted switch. A manual
		// operator action must be able to take ownership of that queued row;
		// otherwise the scheduler would intentionally ignore it while the
		// automatic policy is off and the manual retry could never start.
		_, err = tx.ExecContext(ctx, `update token_recovery_tasks set
			status = ?, mode = ?, last_error_code = null, last_error_message = null,
			account_email = case when account_email = '' and ? != '' then ? else account_email end,
			last_signal_at_ms = ?, started_at_ms = null, completed_at_ms = null, updated_at_ms = ? where id = ?`,
			model.TokenRecoveryStatusManualQueued, model.TokenRecoveryModeManual,
			normalized.AccountEmail, normalized.AccountEmail, seenAt, now, existing.ID)
	} else if isQueuedOrRunning(existing.Status) {
		_, err = tx.ExecContext(ctx, `update token_recovery_tasks set
			account_email = case when account_email = '' and ? != '' then ? else account_email end,
			last_signal_at_ms = case when ? > last_signal_at_ms then ? else last_signal_at_ms end,
			updated_at_ms = ? where id = ?`,
			normalized.AccountEmail, normalized.AccountEmail, seenAt, seenAt, now, existing.ID)
	} else {
		_, err = tx.ExecContext(ctx, `update token_recovery_tasks set
			status = ?, mode = ?, last_error_code = null, last_error_message = null,
			account_email = case when account_email = '' and ? != '' then ? else account_email end,
			last_signal_at_ms = ?,
			started_at_ms = null, completed_at_ms = null, updated_at_ms = ? where id = ?`,
			model.TokenRecoveryStatusManualQueued, model.TokenRecoveryModeManual,
			normalized.AccountEmail, normalized.AccountEmail, seenAt, now, existing.ID)
	}
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	item, err := getByID(ctx, tx, existing.ID)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TokenRecoveryTask{}, err
	}
	return item, nil
}

func (r *repository) Get(ctx context.Context, target model.TokenRecoveryTarget) (model.TokenRecoveryTask, bool, error) {
	normalized, identityKey, err := normalizeTarget(target)
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	return getCompatibleTask(ctx, r.db, normalized, identityKey)
}

func (r *repository) GetByID(ctx context.Context, id int64) (model.TokenRecoveryTask, bool, error) {
	if id <= 0 {
		return model.TokenRecoveryTask{}, false, nil
	}
	item, err := getByID(ctx, r.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TokenRecoveryTask{}, false, nil
	}
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	return item, true, nil
}

func (r *repository) ClaimNextQueued(ctx context.Context) (model.TokenRecoveryTask, bool, error) {
	return r.ClaimNextEligible(ctx, true)
}

// ClaimNextEligible claims the oldest manual task and, only when permitted,
// the oldest automatic task. It leaves paused automatic work durable and
// untouched while the operator's one-shot switch is off.
func (r *repository) ClaimNextEligible(ctx context.Context, allowAutomatic bool) (model.TokenRecoveryTask, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	defer tx.Rollback()
	var id int64
	var status string
	err = tx.QueryRowContext(ctx, `select id, status from token_recovery_tasks
		where status = ? or (? and status = ?)
		order by created_at_ms asc, id asc limit 1`,
		model.TokenRecoveryStatusManualQueued, allowAutomatic, model.TokenRecoveryStatusAutoQueued).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TokenRecoveryTask{}, false, nil
	}
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	runningStatus := model.TokenRecoveryStatusAutoRunning
	if status == model.TokenRecoveryStatusManualQueued {
		runningStatus = model.TokenRecoveryStatusManualRunning
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `update token_recovery_tasks set
		status = ?, started_at_ms = ?,
		auto_attempted_at_ms = case when ? = ? and coalesce(auto_attempted_at_ms, 0) = 0 then ? else auto_attempted_at_ms end,
		updated_at_ms = ? where id = ? and status = ?`,
		runningStatus, now, status, model.TokenRecoveryStatusAutoQueued, now, now, id, status)
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	if affected != 1 {
		return model.TokenRecoveryTask{}, false, nil
	}
	item, err := getByID(ctx, tx, id)
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	return item, true, nil
}

func (r *repository) Complete(ctx context.Context, id int64) (model.TokenRecoveryTask, error) {
	if id <= 0 {
		return model.TokenRecoveryTask{}, ErrTaskNotFound
	}
	now := time.Now().UnixMilli()
	res, err := r.db.ExecContext(ctx, `update token_recovery_tasks set
		status = ?, last_error_code = null, last_error_message = null, completed_at_ms = ?, updated_at_ms = ?
		where id = ? and status in (?, ?)`, model.TokenRecoveryStatusSucceeded, now, now, id,
		model.TokenRecoveryStatusAutoRunning, model.TokenRecoveryStatusManualRunning)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if affected != 1 {
		return model.TokenRecoveryTask{}, ErrTaskNotFound
	}
	item, _, err := r.GetByID(ctx, id)
	return item, err
}

func (r *repository) Fail(ctx context.Context, id int64, errorCode string, errorMessage string) (model.TokenRecoveryTask, error) {
	if id <= 0 {
		return model.TokenRecoveryTask{}, ErrTaskNotFound
	}
	now := time.Now().UnixMilli()
	res, err := r.db.ExecContext(ctx, `update token_recovery_tasks set
		status = case mode when ? then ? else ? end,
		last_error_code = ?, last_error_message = ?, completed_at_ms = ?, updated_at_ms = ?
		where id = ? and status in (?, ?)`,
		model.TokenRecoveryModeManual,
		model.TokenRecoveryStatusManualFailedManualOnly,
		model.TokenRecoveryStatusAutoFailedManualOnly,
		sanitizeErrorCode(errorCode), sanitizeErrorMessage(errorMessage), now, now, id,
		model.TokenRecoveryStatusAutoRunning, model.TokenRecoveryStatusManualRunning)
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return model.TokenRecoveryTask{}, err
	}
	if affected != 1 {
		return model.TokenRecoveryTask{}, ErrTaskNotFound
	}
	item, _, err := r.GetByID(ctx, id)
	return item, err
}

func (r *repository) FailRunningOnStartup(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	res, err := r.db.ExecContext(ctx, `update token_recovery_tasks set
		status = case mode when ? then ? else ? end,
		last_error_code = 'interrupted', last_error_message = null, completed_at_ms = ?, updated_at_ms = ?
		where status in (?, ?)`,
		model.TokenRecoveryModeManual,
		model.TokenRecoveryStatusManualFailedManualOnly,
		model.TokenRecoveryStatusAutoFailedManualOnly,
		now, now, model.TokenRecoveryStatusAutoRunning, model.TokenRecoveryStatusManualRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func insertTask(ctx context.Context, tx *sql.Tx, identityKey string, target model.TokenRecoveryTarget, status, mode string, autoAttemptedAt, seenAt, now int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `insert into token_recovery_tasks (
		identity_key, file_name, auth_index, account_email, provider, status, mode,
		auto_attempted_at_ms, last_signal_at_ms, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identityKey, target.FileName, target.AuthIndex, target.AccountEmail, target.Provider, status, mode, autoAttemptedAt, seenAt, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const selectTasks = `select id, file_name, auth_index, account_email, provider, status, mode,
	coalesce(last_error_code, ''), coalesce(last_error_message, ''), coalesce(auto_attempted_at_ms, 0), last_signal_at_ms, coalesce(started_at_ms, 0), coalesce(completed_at_ms, 0), created_at_ms, updated_at_ms
	from token_recovery_tasks`

func getByIdentityKey(ctx context.Context, q rowQueryer, identityKey string) (model.TokenRecoveryTask, bool, error) {
	item, err := scanTask(q.QueryRowContext(ctx, selectTasks+` where identity_key = ?`, identityKey))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TokenRecoveryTask{}, false, nil
	}
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	return item, true, nil
}

// getCompatibleTask keeps a single automatic-recovery cycle for the same
// physical credential even when one signal source only knows the auth index
// while another also has an email snapshot. auth_index is the stable locator
// when present; email is still retained and checked by the recovery service
// before any write. A prior email-only task is also found when an auth index
// later becomes available for the same file and provider.
func getCompatibleTask(ctx context.Context, q rowQueryer, target model.TokenRecoveryTarget, identityKey string) (model.TokenRecoveryTask, bool, error) {
	item, found, err := getByIdentityKey(ctx, q, identityKey)
	if err != nil || found {
		return item, found, err
	}
	if target.AuthIndex != "" {
		item, err = scanTask(q.QueryRowContext(ctx, selectTasks+` where lower(file_name) = lower(?) and auth_index = ? and provider = ? order by id asc limit 1`, target.FileName, target.AuthIndex, target.Provider))
		if !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return model.TokenRecoveryTask{}, false, err
			}
			return item, true, nil
		}
	}
	if target.AuthIndex == "" || target.AccountEmail == "" {
		return model.TokenRecoveryTask{}, false, nil
	}
	item, err = scanTask(q.QueryRowContext(ctx, selectTasks+` where lower(file_name) = lower(?) and auth_index = '' and lower(account_email) = lower(?) and provider = ? order by id asc limit 1`, target.FileName, target.AccountEmail, target.Provider))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TokenRecoveryTask{}, false, nil
	}
	if err != nil {
		return model.TokenRecoveryTask{}, false, err
	}
	return item, true, nil
}

func getByID(ctx context.Context, q rowQueryer, id int64) (model.TokenRecoveryTask, error) {
	return scanTask(q.QueryRowContext(ctx, selectTasks+` where id = ?`, id))
}

func scanTask(row interface{ Scan(...any) error }) (model.TokenRecoveryTask, error) {
	var item model.TokenRecoveryTask
	err := row.Scan(
		&item.ID, &item.FileName, &item.AuthIndex, &item.AccountEmail, &item.Provider, &item.Status, &item.Mode,
		&item.LastErrorCode, &item.LastErrorMessage, &item.AutoAttemptedAtMS, &item.LastSignalAtMS, &item.StartedAtMS, &item.CompletedAtMS, &item.CreatedAtMS, &item.UpdatedAtMS,
	)
	return item, err
}

func normalizeTarget(target model.TokenRecoveryTarget) (model.TokenRecoveryTarget, string, error) {
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.AccountEmail = strings.ToLower(strings.TrimSpace(target.AccountEmail))
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	if target.FileName == "" || target.Provider != "codex" || (target.AuthIndex == "" && target.AccountEmail == "") {
		return model.TokenRecoveryTarget{}, "", ErrInvalidTarget
	}
	identityKey := strings.ToLower(target.FileName) + "\x1f" + target.AuthIndex + "\x1f" + target.AccountEmail
	return target, identityKey, nil
}

func isQueuedOrRunning(status string) bool {
	switch status {
	case model.TokenRecoveryStatusAutoQueued,
		model.TokenRecoveryStatusAutoRunning,
		model.TokenRecoveryStatusManualQueued,
		model.TokenRecoveryStatusManualRunning:
		return true
	default:
		return false
	}
}

func hasAutomaticAttempt(task model.TokenRecoveryTask) bool {
	if task.AutoAttemptedAtMS > 0 {
		return true
	}
	// Compatibility for tasks created before auto_attempted_at_ms existed:
	// only a terminal task with auto mode proves an automatic cycle completed.
	return task.Mode == model.TokenRecoveryModeAuto && !isQueuedOrRunning(task.Status)
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func sanitizeErrorCode(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLower(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
		if builder.Len() >= 96 {
			break
		}
	}
	if builder.Len() == 0 {
		return "recovery_failed"
	}
	return builder.String()
}

func sanitizeErrorMessage(value string) string {
	var builder strings.Builder
	pendingSpace := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			pendingSpace = builder.Len() > 0
			continue
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteRune(r)
		if builder.Len() >= 384 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}
