package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSeparateDerivedDefaultsToDataDir(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "state")
	t.Setenv(configEnvKey, filepath.Join(dir, "missing-config.json"))
	t.Setenv("USAGE_DATA_DIR", dataDir)
	t.Setenv("USAGE_DERIVED_STORAGE_MODE", "separate")

	cfg, err := LoadWithoutCreatingDefault()
	if err != nil {
		t.Fatalf("LoadWithoutCreatingDefault() error = %v", err)
	}
	if cfg.DerivedStorageMode != DerivedStorageSeparate {
		t.Fatalf("DerivedStorageMode = %q, want %q", cfg.DerivedStorageMode, DerivedStorageSeparate)
	}
	if want := filepath.Join(dataDir, "usage-derived.sqlite"); cfg.DerivedDBPath != want {
		t.Fatalf("DerivedDBPath = %q, want %q", cfg.DerivedDBPath, want)
	}
}

func TestLoadRejectsSeparateDerivedPathEqualToPrimary(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	t.Setenv(configEnvKey, filepath.Join(dir, "missing-config.json"))
	t.Setenv("USAGE_DERIVED_STORAGE_MODE", "separate")
	t.Setenv("USAGE_DB_PATH", dbPath)
	t.Setenv("USAGE_DERIVED_DB_PATH", dbPath)

	_, err := LoadWithoutCreatingDefault()
	if err == nil || !strings.Contains(err.Error(), "USAGE_DERIVED_DB_PATH must differ from USAGE_DB_PATH") {
		t.Fatalf("LoadWithoutCreatingDefault() error = %v, want primary/derived path rejection", err)
	}
}

func TestLoadRejectsUnknownDerivedStorageMode(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv(configEnvKey, filepath.Join(dir, "missing-config.json"))
	t.Setenv("USAGE_DERIVED_STORAGE_MODE", "other")

	_, err := LoadWithoutCreatingDefault()
	if err == nil || !strings.Contains(err.Error(), "USAGE_DERIVED_STORAGE_MODE") {
		t.Fatalf("LoadWithoutCreatingDefault() error = %v, want invalid mode rejection", err)
	}
}
