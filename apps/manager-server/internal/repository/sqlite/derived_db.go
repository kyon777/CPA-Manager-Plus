package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	derivedReadOnlyBusyTimeoutMS = 1000
	derivedWriterBusyTimeoutMS   = 1000
)

// OpenReadOnly opens the primary usage database exclusively for bounded source
// reads. It deliberately never runs Migrate and its DSN rejects writes even if
// a caller accidentally reaches the underlying *sql.DB.
//
// The separate-derived runtime must expose this connection only through a
// query-only source interface. The defensive SQLite flags here are the runtime
// backstop for that compile-time boundary.
func OpenReadOnly(path string) (*sql.DB, error) {
	dbPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("resolve read-only SQLite source path: %w", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("open read-only SQLite source %q: %w", dbPath, err)
	}
	readOnlyDSN, err := readOnlyDataSourceName(dbPath)
	if err != nil {
		return nil, fmt.Errorf("build read-only SQLite source DSN: %w", err)
	}
	db, err := sql.Open("sqlite", readOnlyDSN)
	if err != nil {
		return nil, enrichOpenError(dbPath, err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, enrichOpenError(dbPath, err)
	}
	return db, nil
}

// OpenDerived opens the rebuildable derived database. It is intentionally not
// opened through Store.Open: the primary Store owns billing facts and control
// state, while this database owns only independently rebuildable projections.
func OpenDerived(path string) (*sql.DB, error) {
	dbPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("resolve derived SQLite path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create derived SQLite directory: %w", err)
	}
	db, err := sql.Open("sqlite", derivedDataSourceName(dbPath))
	if err != nil {
		return nil, enrichOpenError(dbPath, err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	if err := rejectPrimarySchema(db, dbPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := MigrateDerived(db); err != nil {
		_ = db.Close()
		return nil, enrichOpenError(dbPath, err)
	}
	return db, nil
}

// rejectPrimarySchema is a destructive-boundary guard. A separate-derived
// path must never be an existing usage.sqlite (including a symlink/hard-link
// alias); otherwise the harmless-looking derived migration would mutate the
// billing database schema before the caller notices the configuration typo.
func rejectPrimarySchema(db *sql.DB, dbPath string) error {
	var tableName string
	err := db.QueryRow(`select name from sqlite_master
		where type = 'table' and name in (
			'usage_events', 'usage_event_identity_ledger', 'usage_source_identity',
			'model_prices', 'settings'
		) order by case name
			when 'usage_events' then 1
			when 'usage_event_identity_ledger' then 2
			when 'usage_source_identity' then 3
			when 'model_prices' then 4
			else 5 end limit 1`).Scan(&tableName)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect derived SQLite target %q: %w", dbPath, err)
	}
	return fmt.Errorf("refusing derived SQLite path %q: primary schema table %q detected", dbPath, tableName)
}

func derivedDataSourceName(path string) string {
	dsn := databaseURL(path)
	query := dsn.Query()
	query.Add("_txlock", "immediate")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", derivedWriterBusyTimeoutMS))
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(NORMAL)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func databaseURL(path string) *url.URL {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return &url.URL{Scheme: "file", Path: uriPath}
}
