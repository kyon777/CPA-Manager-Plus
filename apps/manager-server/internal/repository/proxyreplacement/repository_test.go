package proxyreplacement_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/proxyreplacement"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testProtector(t *testing.T) *security.Protector {
	t.Helper()
	protector, err := security.NewProtector([]byte("proxy-replacement-test-key-material"))
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}
	return protector
}

func TestTaskURLsAreEncryptedAtRestAndReadableToAuthorizedService(t *testing.T) {
	db := openTestDB(t)
	repo := proxyreplacement.New(db, testProtector(t))
	task, err := repo.CreateDraft(context.Background(), model.ProxyReplacementTask{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
	}, []model.ProxyReplacementTaskItem{{
		Target:      model.ProxyReplacementTarget{ClientKey: "run:codex:42", FileName: "alice.json", Provider: "codex"},
		OldProxyURL: "http://old:secret@p:1",
		NewProxyURL: "http://new:secret@p:2",
	}})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if task.ID == 0 {
		t.Fatal("missing task id")
	}
	var raw string
	if err := db.QueryRow(`select new_proxy_ciphertext from proxy_replacement_task_items where task_id = ?`, task.ID).Scan(&raw); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if strings.Contains(raw, "secret") || strings.Contains(raw, "http://new") {
		t.Fatalf("raw proxy credential persisted: %q", raw)
	}
	page, err := repo.GetTask(context.Background(), task.ID, 1, 50)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].NewProxyURL != "http://new:secret@p:2" {
		t.Fatalf("page = %#v", page)
	}
}

func TestDraftExpiryAndRunningRecoveryAreDurable(t *testing.T) {
	db := openTestDB(t)
	repo := proxyreplacement.New(db, testProtector(t))
	task, err := repo.CreateDraft(context.Background(), model.ProxyReplacementTask{
		Source:      model.ProxyReplacementSourceBatchPool,
		Mode:        model.ProxyReplacementModePool,
		ExpiresAtMS: 1,
	}, []model.ProxyReplacementTaskItem{{
		Target: model.ProxyReplacementTarget{ClientKey: "batch:1", FileName: "one.json", Provider: "codex"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ExpireDrafts(context.Background(), 2); err != nil {
		t.Fatalf("expire drafts: %v", err)
	}
	page, err := repo.GetTask(context.Background(), task.ID, 1, 50)
	if err != nil || page.Task.Status != model.ProxyReplacementTaskExpired {
		t.Fatalf("expired task = %#v, err=%v", page.Task, err)
	}
}

func TestCompleteTaskSeparatesWriteFailuresFromRecheckFailures(t *testing.T) {
	db := openTestDB(t)
	repo := proxyreplacement.New(db, testProtector(t))
	ctx := context.Background()
	task, err := repo.CreateDraft(ctx, model.ProxyReplacementTask{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
	}, []model.ProxyReplacementTaskItem{
		{Target: model.ProxyReplacementTarget{ClientKey: "success", FileName: "success.json", Provider: "codex"}},
		{Target: model.ProxyReplacementTarget{ClientKey: "recheck-failed", FileName: "recheck-failed.json", Provider: "codex"}},
		{Target: model.ProxyReplacementTarget{ClientKey: "write-failed", FileName: "write-failed.json", Provider: "codex"}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if _, err := repo.ConfirmDraft(ctx, task.ID, 1); err != nil {
		t.Fatalf("confirm draft: %v", err)
	}
	if _, claimed, err := repo.ClaimNextQueued(ctx); err != nil || !claimed {
		t.Fatalf("claim queued: claimed=%v err=%v", claimed, err)
	}
	page, err := repo.GetTask(ctx, task.ID, 1, 10)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("task page=%#v err=%v", page, err)
	}
	if err := repo.MarkItem(ctx, page.Items[0].ID, model.ProxyReplacementItemRecheckSucceeded, model.ProxyReplacementItemOutcome{}); err != nil {
		t.Fatalf("mark recheck success: %v", err)
	}
	if err := repo.MarkItem(ctx, page.Items[1].ID, model.ProxyReplacementItemRecheckFailed, model.ProxyReplacementItemOutcome{RecheckErrorCode: "timeout"}); err != nil {
		t.Fatalf("mark recheck failure: %v", err)
	}
	if err := repo.MarkItem(ctx, page.Items[2].ID, model.ProxyReplacementItemWriteFailed, model.ProxyReplacementItemOutcome{WriteErrorCode: "write_failed"}); err != nil {
		t.Fatalf("mark write failure: %v", err)
	}

	completed, err := repo.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if completed.UpdatedCount != 2 || completed.FailedCount != 1 || completed.RecheckSuccessCount != 1 || completed.RecheckFailedCount != 1 {
		t.Fatalf("completed task counts = %#v", completed)
	}
	if completed.Status != model.ProxyReplacementTaskPartial {
		t.Fatalf("completed task status = %q, want %q", completed.Status, model.ProxyReplacementTaskPartial)
	}
}
