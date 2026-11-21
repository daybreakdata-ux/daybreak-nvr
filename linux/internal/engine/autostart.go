// Start at boot (v1.4, desktop GUI only): an XDG autostart .desktop
// entry for the GUI app at ~/.config/autostart/<app>.desktop.
//
// The app name is derived from the config directory base
// (daybreak-nvr, or daybreak-nvr-linux in the renamed variant — the
// rename sed in build_linux_renamed.sh rewrites the literal in
// configDir, so both variants name their own file and Exec their
// own installed binary). The systemd user service is deliberately
// NOT used for this: it conflicts with the GUI single-instance lock.
package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// autostartBase is the app identity for the autostart entry:
// "daybreak-nvr" in the original variant, "daybreak-nvr-linux" in
// the renamed variant (see configDir).
func autostartBase() string {
	return filepath.Base(configDir())
}

// AutostartPath is the autostart .desktop file for this variant.
func AutostartPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "autostart", autostartBase()+".desktop")
}

// AutostartEnabled reports whether the autostart entry exists.
func AutostartEnabled() bool {
	p := AutostartPath()
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// SetAutostart creates (enabled) or removes (disabled) the
// autostart entry. The entry Execs the installed binary
// (/usr/bin/<app>).
func SetAutostart(enabled bool) error {
	p := AutostartPath()
	if p == "" {
		return fmt.Errorf("cannot locate home directory for autostart entry")
	}
	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	name := "Daybreak NVR"
	if autostartBase() == "daybreak-nvr-linux" {
		name = "Daybreak NVR Linux"
	}
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + name + "\n" +
		"Comment=Daybreak NVR — desktop NVR for Tapo cameras\n" +
		"Exec=/usr/bin/" + autostartBase() + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return err
	}
	return nil
}
