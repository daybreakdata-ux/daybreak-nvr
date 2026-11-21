package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A pre-v1.3 config (sensitivity 0..2, no migration flag, no
// pre/post buffers) loads with its sensitivities mapped onto the
// 0..100 scale and the new defaults filled in — and a second load
// does not re-migrate (a user-set 0..100 value of 0, 1 or 2 is
// left alone).
func TestLoadSettingsSensitivityMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cfgDir := filepath.Dir(ConfigPath())
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := Settings{
		RecordingsDir: "/tmp/rec",
		Cameras: []Camera{
			{ID: "a", Name: "Low", IP: "192.168.1.10", Sensitivity: 0},
			{ID: "b", Name: "Med", IP: "192.168.1.11", Sensitivity: 1},
			{ID: "c", Name: "High", IP: "192.168.1.12", Sensitivity: 2},
		},
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, migrated := LoadSettings()
	if !migrated {
		t.Error("migration should be reported so the caller persists it")
	}
	want := []int{25, 50, 75}
	for i, w := range want {
		if loaded.Cameras[i].Sensitivity != w {
			t.Errorf("camera %d sensitivity = %d, want %d", i, loaded.Cameras[i].Sensitivity, w)
		}
	}
	if !loaded.SensitivityMigrated {
		t.Error("SensitivityMigrated should be set after migration")
	}
	if loaded.PreBufferSeconds != 15 || loaded.PostBufferSeconds != 20 {
		t.Errorf("buffers = %d/%d, want 15/20", loaded.PreBufferSeconds, loaded.PostBufferSeconds)
	}
	// Persist and reload: the migrated values must stick, and a
	// user who then sets sensitivity 2 (a valid 0..100 value)
	// must not have it re-migrated to 75.
	if err := loaded.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded.Cameras[0].Sensitivity = 2
	if err := loaded.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, _ := LoadSettings()
	if reloaded.Cameras[0].Sensitivity != 2 {
		t.Errorf("reloaded sensitivity = %d, want 2 (no re-migration)", reloaded.Cameras[0].Sensitivity)
	}
	if reloaded.Cameras[1].Sensitivity != 50 {
		t.Errorf("reloaded sensitivity = %d, want 50", reloaded.Cameras[1].Sensitivity)
	}
}
