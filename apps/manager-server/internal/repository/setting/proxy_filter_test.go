package setting_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestProxyFilterSettingsRoundTripNormalizesValues(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	saved, err := st.SaveProxyFilterSettings(context.Background(), model.ProxyFilterSettings{
		URLs: []string{" http://one/// ", "http://two/", "http://one"},
	})
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if got, want := saved.URLs, []string{"http://one", "http://two"}; !equalStrings(got, want) {
		t.Fatalf("saved URLs = %#v, want %#v", got, want)
	}

	loaded, ok, err := st.LoadProxyFilterSettings(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !ok || !equalStrings(loaded.URLs, saved.URLs) || loaded.UpdatedAtMS <= 0 {
		t.Fatalf("loaded settings = %#v, present = %v", loaded, ok)
	}
}

func TestProxyFilterSettingsUsesDataProtectorWhenConfigured(t *testing.T) {
	protector, err := security.NewProtector([]byte("proxy-filter-protector-test-key"))
	if err != nil {
		t.Fatalf("new protector: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.sqlite"), protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const proxyURL = "http://username:password@proxy.example:8080"
	if _, err := st.SaveProxyFilterSettings(context.Background(), model.ProxyFilterSettings{URLs: []string{proxyURL}}); err != nil {
		t.Fatalf("save protected settings: %v", err)
	}
	loaded, ok, err := st.LoadProxyFilterSettings(context.Background())
	if err != nil || !ok || !equalStrings(loaded.URLs, []string{proxyURL}) {
		t.Fatalf("load protected settings = %#v, present = %v, err = %v", loaded, ok, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
