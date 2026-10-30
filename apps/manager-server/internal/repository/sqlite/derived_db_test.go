package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlySourceDSNOmitsImmediateTransactionLock(t *testing.T) {
	dsn, err := readOnlyDataSourceName(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("build read-only source DSN: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse read-only source DSN: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("_txlock"); got != "" {
		t.Fatalf("read-only source transaction lock = %q, want unset", got)
	}
	if got := query.Get("mode"); got != "ro" {
		t.Fatalf("read-only source mode = %q, want ro", got)
	}
}

func TestOpenReadOnlySourceRejectsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	primary, err := Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	if _, err := primary.Exec(`insert into settings(key, value, updated_at_ms) values ('fixture', 'before', 1)`); err != nil {
		_ = primary.Close()
		t.Fatalf("seed primary: %v", err)
	}
	if err := primary.Close(); err != nil {
		t.Fatalf("close primary: %v", err)
	}

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only source: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	if _, err := readOnly.Exec(`insert into settings(key, value, updated_at_ms) values ('fixture-write', 'blocked', 2)`); err == nil {
		t.Fatal("read-only source accepted a write")
	}
	var value string
	if err := readOnly.QueryRow(`select value from settings where key = 'fixture'`).Scan(&value); err != nil {
		t.Fatalf("read source after rejected write: %v", err)
	}
	if value != "before" {
		t.Fatalf("read source value = %q, want before", value)
	}
}

func TestOpenReadOnlySourceDoesNotRunPrimaryMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unmigrated.sqlite")
	raw, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(`create table source_fixture (value text not null)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create source fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only source: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	var count int
	if err := readOnly.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'settings'`).Scan(&count); err != nil {
		t.Fatalf("inspect read-only schema: %v", err)
	}
	if count != 0 {
		t.Fatal("read-only source unexpectedly ran primary migration")
	}
}

func TestOpenReadOnlySourceRejectsMissingDatabase(t *testing.T) {
	_, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing.sqlite"))
	if err == nil {
		t.Fatalf("missing database error = %v", err)
	}
}

func TestOpenReadOnlySourceCanBeginReadTransactionWhilePrimaryWriterIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	primary, err := Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	if _, err := primary.Exec(`insert into settings(key, value, updated_at_ms) values ('fixture', 'before', 1)`); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	holdingWrite, err := primary.Begin()
	if err != nil {
		t.Fatalf("begin holding primary writer: %v", err)
	}
	t.Cleanup(func() { _ = holdingWrite.Rollback() })
	if _, err := holdingWrite.Exec(`update settings set value = 'after' where key = 'fixture'`); err != nil {
		t.Fatalf("hold primary writer: %v", err)
	}

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only source: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	readTx, err := readOnly.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read-only source transaction behind primary writer: %v", err)
	}
	defer readTx.Rollback()
	var value string
	if err := readTx.QueryRowContext(ctx, `select value from settings where key = 'fixture'`).Scan(&value); err != nil {
		t.Fatalf("read source behind primary writer: %v", err)
	}
	if value != "before" {
		t.Fatalf("read source value = %q, want committed snapshot", value)
	}
}

func TestOpenDerivedRejectsPrimaryDatabaseWithoutMigratingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	primary, err := Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	if err := primary.Close(); err != nil {
		t.Fatalf("close primary: %v", err)
	}
	if _, err := OpenDerived(path); err == nil || !strings.Contains(err.Error(), "primary schema") {
		t.Fatalf("open derived on primary error = %v, want primary schema refusal", err)
	}
	// The refusal must happen before MigrateDerived runs; a typo in the derived
	// path must never add disposable tables to usage.sqlite.
	check, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("reopen primary: %v", err)
	}
	t.Cleanup(func() { _ = check.Close() })
	var derivedMeta int
	if err := check.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'derived_store_meta'`).Scan(&derivedMeta); err != nil {
		t.Fatalf("inspect primary after refusal: %v", err)
	}
	if derivedMeta != 0 {
		t.Fatal("primary database was migrated as derived storage")
	}
}

func TestPrimaryMigrationCreatesStableUsageSourceIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	primary, err := Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	var first string
	if err := primary.QueryRow(`select source_epoch from usage_source_identity where singleton = 1`).Scan(&first); err != nil {
		_ = primary.Close()
		t.Fatalf("read first source identity: %v", err)
	}
	if len(first) != 32 {
		_ = primary.Close()
		t.Fatalf("source epoch length = %d, want 32", len(first))
	}
	if err := primary.Close(); err != nil {
		t.Fatalf("close primary: %v", err)
	}
	primary, err = Open(path)
	if err != nil {
		t.Fatalf("reopen primary: %v", err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	var second string
	if err := primary.QueryRow(`select source_epoch from usage_source_identity where singleton = 1`).Scan(&second); err != nil {
		t.Fatalf("read stable source identity: %v", err)
	}
	if second != first {
		t.Fatalf("source epoch changed from %q to %q", first, second)
	}
}
