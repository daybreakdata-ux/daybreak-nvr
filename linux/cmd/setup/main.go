//go:build windows

// Daybreak NVR setup — fallback installer used when NSIS is not
// available. It is a CONSOLE program on purpose: progress and
// errors are visible in a window, never silent. It embeds the
// portable payload (DaybreakNVR.exe, ffmpeg.exe, README, icon),
// installs per-user to %LOCALAPPDATA%\Programs\DaybreakNVR with
// no admin prompt, creates Start Menu (+ optional desktop)
// shortcuts via a small VBScript, writes the standard Add/Remove
// Programs registry keys, and installs a copy of itself as
// Uninstall.exe (invoked with --uninstall it removes everything).
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

//go:embed payload.zip
var payloadZip []byte

const (
	product   = "Daybreak NVR"
	version   = "1.3"
	publisher = "Daybreak Digital"
	uninstKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\DaybreakNVR`
	appKey    = `Software\DaybreakNVR`
)

var payloadFiles = []string{"DaybreakNVR.exe", "ffmpeg.exe", "README.txt", "icon.png"}

func installDir() string {
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		la = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}
	return filepath.Join(la, "Programs", "DaybreakNVR")
}

func startMenuDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Daybreak NVR")
}

func desktopDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("USERPROFILE")
	}
	return filepath.Join(home, "Desktop")
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--uninstall" || a == "/uninstall" {
			runUninstall()
			return
		}
	}
	// Also act as uninstaller when invoked under that name.
	if strings.EqualFold(filepath.Base(os.Args[0]), "Uninstall.exe") {
		runUninstall()
		return
	}
	runInstall()
}

func ask(r *bufio.Reader, prompt, def string) string {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func runInstall() {
	in := bufio.NewReader(os.Stdin)
	dir := installDir()
	fmt.Printf("Daybreak NVR %s — setup\n", version)
	fmt.Printf("Installing to %s\n\n", dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal("could not create the install folder: %v", err)
	}

	// Locked-exe guard: if the app is running, ask to close it.
	target := filepath.Join(dir, "DaybreakNVR.exe")
	if _, err := os.Stat(target); err == nil {
		if f, err := os.OpenFile(target, os.O_WRONLY, 0); err != nil {
			fmt.Println("Daybreak NVR appears to be running. Close it, then press Enter.")
			_, _ = in.ReadString('\n')
		} else {
			_ = f.Close()
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(payloadZip), int64(len(payloadZip)))
	if err != nil {
		fatal("setup file is damaged (payload): %v", err)
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			fatal("setup file is damaged (bad entry %q)", f.Name)
		}
		dst := filepath.Join(dir, name)
		fmt.Printf("  %s\n", name)
		if err := extract(f, dst); err != nil {
			fatal("could not write %s: %v", name, err)
		}
	}

	// Uninstaller: a copy of this setup program.
	self, _ := os.Executable()
	if self != "" {
		data, err := os.ReadFile(self)
		if err == nil {
			_ = os.WriteFile(filepath.Join(dir, "Uninstall.exe"), data, 0o755)
		}
	}

	// Registry — per-user app key + Add/Remove Programs.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstKey, registry.ALL_ACCESS)
	if err != nil {
		fatal("could not write uninstall registry keys: %v", err)
	}
	_ = k.SetStringValue("DisplayName", product)
	_ = k.SetStringValue("DisplayVersion", version)
	_ = k.SetStringValue("Publisher", publisher)
	_ = k.SetStringValue("InstallLocation", dir)
	_ = k.SetStringValue("DisplayIcon", target)
	_ = k.SetStringValue("UninstallString", `"`+filepath.Join(dir, "Uninstall.exe")+`"`)
	_ = k.SetStringValue("QuietUninstallString", `"`+filepath.Join(dir, "Uninstall.exe")+`" --uninstall`)
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	_ = k.SetDWordValue("EstimatedSize", 105000)
	_ = k.Close()
	if ak, _, err := registry.CreateKey(registry.CURRENT_USER, appKey, registry.ALL_ACCESS); err == nil {
		_ = ak.SetStringValue("InstallDir", dir)
		_ = ak.Close()
	}

	// Shortcuts.
	sm := startMenuDir()
	_ = os.MkdirAll(sm, 0o755)
	shortcut(filepath.Join(sm, "Daybreak NVR.lnk"), target, dir)
	shortcut(filepath.Join(sm, "Uninstall Daybreak NVR.lnk"), filepath.Join(dir, "Uninstall.exe"), dir)
	if strings.EqualFold(ask(in, "Desktop shortcut? [Y/n] ", "y"), "y") {
		shortcut(filepath.Join(desktopDir(), "Daybreak NVR.lnk"), target, dir)
	}

	fmt.Println("\nInstalled.")
	if strings.EqualFold(ask(in, "Run Daybreak NVR now? [Y/n] ", "y"), "y") {
		cmd := exec.Command(target)
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			fmt.Printf("Could not start it: %v\n", err)
			fmt.Println("If it won't open, check the startup log:")
			fmt.Println("  " + filepath.Join(os.Getenv("LOCALAPPDATA"), "DaybreakNVR", "startup.log"))
		}
	}
	fmt.Println("Press Enter to close setup.")
	_, _ = in.ReadString('\n')
}

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

const vbsTemplate = `Set sh = CreateObject("WScript.Shell")
Set lnk = sh.CreateShortcut(WScript.Arguments(0))
lnk.TargetPath = WScript.Arguments(1)
lnk.WorkingDirectory = WScript.Arguments(2)
lnk.IconLocation = WScript.Arguments(1) & ",0"
lnk.Save
`

func shortcut(linkPath, target, workdir string) {
	tmp := filepath.Join(os.TempDir(), "daybreak-nvr-shortcut.vbs")
	if err := os.WriteFile(tmp, []byte(vbsTemplate), 0o644); err != nil {
		fmt.Printf("  (shortcut skipped: %v)\n", err)
		return
	}
	cmd := exec.Command("cscript", "//nologo", tmp, linkPath, target, workdir)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("  (shortcut skipped: %v %s)\n", err, strings.TrimSpace(string(out)))
	}
}

func runUninstall() {
	dir := installDir()
	fmt.Printf("Removing Daybreak NVR from %s\n", dir)
	_ = os.Remove(filepath.Join(desktopDir(), "Daybreak NVR.lnk"))
	sm := startMenuDir()
	_ = os.Remove(filepath.Join(sm, "Daybreak NVR.lnk"))
	_ = os.Remove(filepath.Join(sm, "Uninstall Daybreak NVR.lnk"))
	_ = os.Remove(sm)
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstKey)
	_ = registry.DeleteKey(registry.CURRENT_USER, appKey)
	for _, f := range payloadFiles {
		_ = os.Remove(filepath.Join(dir, f))
	}
	// User data (config + recordings) stays; remove the folder
	// only if it is now empty, then remove this uninstaller via a
	// detached cleanup command (an exe cannot delete itself).
	_ = os.Remove(dir) // fails silently if not empty — fine
	self, _ := os.Executable()
	if self != "" && strings.EqualFold(filepath.Dir(self), dir) {
		cleanup := fmt.Sprintf("ping -n 3 127.0.0.1 >nul & del /f /q %q & rmdir %q",
			self, dir)
		cmd := exec.Command("cmd", "/c", cleanup)
		cmd.Dir = os.TempDir()
		_ = cmd.Start()
	}
	fmt.Println("Daybreak NVR uninstalled. Your recordings and settings were kept.")
	fmt.Println("Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func fatal(format string, args ...any) {
	fmt.Printf("\nSetup stopped: "+format+"\n", args...)
	fmt.Println("Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}
