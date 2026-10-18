package proxypoolstate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/proxyreplacement"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxypoolstate"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPoolStateCanBeRestoredWithoutPersistingURL(t *testing.T) {
	db := openTestDB(t)
	protector, err := security.NewProtector([]byte("proxy-pool-state-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	service := proxypoolstate.New(proxyreplacement.New(db, protector))
	if err := service.MarkSuspect(context.Background(), "http://user:secret@proxy:8080/", "context deadline exceeded while contacting upstream"); err != nil {
		t.Fatalf("mark suspect: %v", err)
	}
	states, err := service.List(context.Background())
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%#v err=%v", states, err)
	}
	state := states[0]
	if state.Status != model.ProxyPoolStatusSuspect || state.Fingerprint == "" {
		t.Fatalf("state=%#v", state)
	}
	if state.LastFailureReason != "transport_timeout" {
		t.Fatalf("reason=%q", state.LastFailureReason)
	}
	var raw string
	if err := db.QueryRow(`select proxy_fingerprint || ' ' || coalesce(last_failure_reason, '') from proxy_pool_state`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" || containsSecret(raw, "user:secret") {
		t.Fatalf("raw proxy state leaked URL: %q", raw)
	}
	restored, err := service.Restore(context.Background(), state.Fingerprint)
	if err != nil || restored.Status != model.ProxyPoolStatusNormal {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
}

func containsSecret(value, secret string) bool {
	return len(secret) > 0 && len(value) >= len(secret) && (value == secret || contains(value, secret))
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
