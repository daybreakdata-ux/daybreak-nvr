// Package engine holds the OS-independent NVR core for Daybreak NVR:
// camera/settings storage, ffmpeg-based recording supervision,
// retention cleanup, live preview frame pumps and the recordings
// library index. The Windows GUI lives in the main package.
package engine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MaxCameras is the product limit Justin asked for.
const MaxCameras = 12

// Record modes for a camera.
const (
	RecordContinuous = "continuous"
	RecordMotion     = "motion"
	// RecordPerson is v1.6: record ONLY when the on-device AI
	// human detection sees a person. Motion with no person does
	// not record. Live view still works, and the motion detector
	// still runs (it feeds the AI, which is motion-gated).
	// Setting a camera to Person-only forces HumanDetect on
	// (see BuildCamera), so the mode just works.
	RecordPerson = "person"
	// RecordOff is v1.5.2: live view only — no recorder, no
	// motion/person detector, no reaper, no finalizer, no
	// snapshots. The camera still appears in the list and its
	// live view keeps working; switching back to Continuous or
	// Motion resumes recording from that moment.
	RecordOff = "off"
)

// ValidRecordMode reports whether m is one of the known record
// modes ("" is not a mode — a brand-new camera has no mode yet).
func ValidRecordMode(m string) bool {
	switch m {
	case RecordContinuous, RecordMotion, RecordPerson, RecordOff:
		return true
	}
	return false
}

// Motion sensitivity levels (index into trigger table).
// v1.3: sensitivity is a 0..100 slider (higher = more sensitive).
// The old 0..2 levels are kept as constants only for migration —
// LoadSettings maps them onto the new scale once (see
// MigrateSensitivity and SensitivityMigrated).
const (
	SensitivityLow    = 0
	SensitivityMedium = 1
	SensitivityHigh   = 2
)

// SensitivityDefault is the 0..100 sensitivity for a new camera.
const SensitivityDefault = 50

// MigrateSensitivity maps a pre-v1.3 sensitivity level (0..2) onto
// the 0..100 scale so existing configs keep behaving the same.
func MigrateSensitivity(old int) int {
	switch old {
	case SensitivityLow:
		return 25
	case SensitivityHigh:
		return 75
	default:
		return 50
	}
}

// Detection zones are a grid over the frame. 16x9 cells (144) so
// each cell maps cleanly onto the motion detector's 96x54 grid
// (6x6 detector cells per zone cell) and matches a 16:9 picture.
// Stored as a 144-character string of '0'/'1', row-major, '1' =
// motion/persons count in that cell. An empty string means the
// whole frame (the pre-zones default).
const (
	ZoneCols  = 16
	ZoneRows  = 9
	ZoneCells = ZoneCols * ZoneRows
)

// DefaultZoneGrid is the whole frame enabled.
func DefaultZoneGrid() string {
	b := make([]byte, ZoneCells)
	for i := range b {
		b[i] = '1'
	}
	return string(b)
}

// ZoneCellIndex maps a zone-picker cell (col, row) onto its index
// in the 144-char zone string (row-major). The desktop zone
// picker and the web UI overlay both use it, so a click on the
// picture toggles exactly the cell under the cursor.
func ZoneCellIndex(col, row int) int {
	return row*ZoneCols + col
}

// ZoneToggle flips one zone cell (col, row) in a zone grid string
// and returns the new grid. A malformed grid is treated as the
// whole frame (fail open) before toggling; out-of-range cells
// leave the grid unchanged.
func ZoneToggle(grid string, col, row int) string {
	if len(grid) != ZoneCells {
		grid = DefaultZoneGrid()
	}
	if col < 0 || col >= ZoneCols || row < 0 || row >= ZoneRows {
		return grid
	}
	b := []byte(grid)
	idx := ZoneCellIndex(col, row)
	if b[idx] == '1' {
		b[idx] = '0'
	} else {
		b[idx] = '1'
	}
	return string(b)
}

// ZoneCellEnabled reports whether a zone cell is enabled. Out of
// range or a malformed grid means enabled (fail open — never
// silently blind a camera).
func ZoneCellEnabled(grid string, col, row int) bool {
	if col < 0 || col >= ZoneCols || row < 0 || row >= ZoneRows {
		return true
	}
	if len(grid) != ZoneCells {
		return true
	}
	return grid[row*ZoneCols+col] == '1'
}

// ZoneEnabledFraction returns the fraction of zone cells enabled
// (1.0 for a missing/malformed grid).
func ZoneEnabledFraction(grid string) float64 {
	if len(grid) != ZoneCells {
		return 1
	}
	on := 0
	for i := 0; i < len(grid); i++ {
		if grid[i] == '1' {
			on++
		}
	}
	return float64(on) / float64(ZoneCells)
}

// ZoneMask expands a zone grid into a per-detector-cell mask for
// the 96x54 motion grid (6x6 detector cells per zone cell). A nil
// return means the whole frame is enabled.
func ZoneMask(grid string) []bool {
	if len(grid) != ZoneCells {
		return nil
	}
	mask := make([]bool, 96*54)
	for row := 0; row < ZoneRows; row++ {
		for col := 0; col < ZoneCols; col++ {
			if grid[row*ZoneCols+col] != '1' {
				continue
			}
			for dy := 0; dy < 6; dy++ {
				for dx := 0; dx < 6; dx++ {
					mask[(row*6+dy)*96+(col*6+dx)] = true
				}
			}
		}
	}
	return mask
}

// Camera is one RTSP source. For TP-Link Tapo cameras the app builds
// the standard Tapo RTSP URLs from IP + the camera-account credentials
// created in the Tapo app (stream1 = main/HD, stream2 = sub/SD).
// MainURL/SubURL, when set, override the built URLs (any RTSP camera).
type Camera struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	MainURL     string `json:"main_url"`
	SubURL      string `json:"sub_url"`
	Record      bool   `json:"record"`
	Enabled     bool   `json:"enabled"`
	RecordMode  string `json:"record_mode"` // "" or RecordContinuous / RecordMotion / RecordPerson / RecordOff
	Sensitivity int    `json:"sensitivity"` // motion sensitivity 0..100 (v1.3; was 0..2)
	// ZoneGrid is the 16x9 detection zone (144 '0'/'1' chars);
	// empty means the whole frame.
	ZoneGrid string `json:"zone_grid"`
	// HumanDetect enables on-device AI person detection for
	// this camera (v1.3). HumanConfidence is its 0..100
	// confidence threshold (50 = 0.50).
	HumanDetect     bool `json:"human_detect"`
	HumanConfidence int  `json:"human_confidence"`
}

// Mode returns the effective record mode (default continuous).
// An empty or unrecognised stored mode is continuous, the
// historical default — see LoadSettings, which normalises
// unknown stored modes on load.
func (c Camera) Mode() string {
	switch c.RecordMode {
	case RecordMotion:
		return RecordMotion
	case RecordPerson:
		return RecordPerson
	case RecordOff:
		return RecordOff
	default:
		return RecordContinuous
	}
}

// MainStreamURL returns the full-quality stream used for recording.
func (c Camera) MainStreamURL() string {
	if strings.TrimSpace(c.MainURL) != "" {
		return c.MainURL
	}
	return tapoURL(c, "stream1")
}

// SubStreamURL returns the low-bandwidth stream used for the live grid.
func (c Camera) SubStreamURL() string {
	if strings.TrimSpace(c.SubURL) != "" {
		return c.SubURL
	}
	return tapoURL(c, "stream2")
}

func tapoURL(c Camera, stream string) string {
	if strings.TrimSpace(c.IP) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("rtsp://")
	if c.Username != "" {
		b.WriteString(url.QueryEscape(c.Username))
		if c.Password != "" {
			b.WriteString(":")
			b.WriteString(url.QueryEscape(c.Password))
		}
		b.WriteString("@")
	}
	b.WriteString(c.IP)
	b.WriteString(":554/")
	b.WriteString(stream)
	return b.String()
}

// Slug is the filesystem-safe folder name for a camera's recordings.
func (c Camera) Slug() string {
	s := strings.ToLower(strings.TrimSpace(c.Name))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "cam-" + c.ID
	}
	return out
}

// Settings is the persisted application configuration.
type Settings struct {
	RecordingsDir  string   `json:"recordings_dir"`
	SegmentMinutes int      `json:"segment_minutes"`
	RetentionDays  int      `json:"retention_days"`
	MaxTotalGB     int      `json:"max_total_gb"`
	PreviewFPS     int      `json:"preview_fps"`
	GridLayout     int      `json:"grid_layout"`
	Cameras        []Camera `json:"cameras"`

	// PreBufferSeconds / PostBufferSeconds are the motion-event
	// padding the reaper keeps around each event (v1.3; the
	// 15s / 20s values were hardcoded before).
	PreBufferSeconds  int `json:"pre_buffer_seconds"`
	PostBufferSeconds int `json:"post_buffer_seconds"`

	// SensitivityMigrated records that the one-time v1.3
	// sensitivity migration (0..2 -> 0..100) has run, so a user
	// who later sets a 0..100 value of 0, 1 or 2 is not
	// re-migrated on the next load.
	SensitivityMigrated bool `json:"sensitivity_migrated"`

	// Phone viewing (built-in web server for the LAN).
	PhoneEnabled    bool `json:"phone_enabled"`
	PhoneConfigured bool `json:"phone_configured"` // distinguishes v1.0 configs
	PhonePort       int  `json:"phone_port"`
	// PhonePassword is kept in the file only so old configs still
	// load. It is NOT used for sign-in any more (v1.2): the LAN
	// web UI and the desktop app sign in with user accounts
	// (see internal/engine/users.go).
	PhonePassword string `json:"phone_password"`

	// Remote viewing (on-demand push to the Oracle relay, watched
	// through the Vercel viewer). Added in v1.2.
	RemoteEnabled     bool   `json:"remote_enabled"`
	RemoteConfigured  bool   `json:"remote_configured"` // distinguishes pre-1.2 configs
	RemoteMode        string `json:"remote_mode"`       // ondemand | always | off
	RemoteRelayHost   string `json:"remote_relay_host"` // HTTPS host of the relay
	RemoteRelayIP     string `json:"remote_relay_ip"`   // RTSP publish target
	RemotePublishUser string `json:"remote_publish_user"`
	RemotePublishPass string `json:"remote_publish_pass"`
	RemoteViewerUser  string `json:"remote_viewer_user"`
	RemoteViewerPass  string `json:"remote_viewer_pass"`
}

// ExeDir returns the directory the executable lives in. The app is
// portable: config, ffmpeg and (by default) recordings sit beside it.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// configDir is where settings live. On Linux the app follows the
// XDG layout (~/.config/daybreak-nvr) instead of the portable
// beside-the-exe layout, because the .deb installs the binary into
// /usr/lib/daybreak-nvr, which is not user-writable. On Windows
// the portable layout is kept.
func configDir() string {
	if runtime.GOOS == "linux" {
		if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
			return filepath.Join(cfg, "daybreak-nvr")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".config", "daybreak-nvr")
		}
	}
	return ExeDir()
}

// ConfigPath is where settings are stored.
func ConfigPath() string {
	return filepath.Join(configDir(), "daybreak-nvr-config.json")
}

// DefaultSettings returns sane defaults for a 4-camera Tapo setup.
func DefaultSettings() Settings {
	recordingsDir := filepath.Join(ExeDir(), "Recordings")
	if runtime.GOOS == "linux" {
		// Linux default: ~/DaybreakNVR — visible in the home
		// folder, easy to find in Files, and not hidden inside
		// the system install tree.
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			recordingsDir = filepath.Join(home, "DaybreakNVR")
		}
	}
	return Settings{
		RecordingsDir:   recordingsDir,
		SegmentMinutes:  10,
		RetentionDays:   7,
		MaxTotalGB:      0, // 0 = no size cap, days rule only
		PreviewFPS:      10,
		GridLayout:      4,
		Cameras:         []Camera{},
		PreBufferSeconds:  15,
		PostBufferSeconds: 20,
		SensitivityMigrated: true, // fresh configs are already on the 0..100 scale
		PhoneEnabled:    true,
		PhoneConfigured: true,
		PhonePort:       8080,

		RemoteEnabled:     true,
		RemoteConfigured:  true,
		RemoteMode:        RemoteModeOnDemand,
		RemoteRelayHost:   DefaultRelayHost,
		RemoteRelayIP:     DefaultRelayIP,
		RemotePublishUser: DefaultPublishUser,
		RemotePublishPass: DefaultPublishPass,
		RemoteViewerUser:  DefaultViewerUser,
		RemoteViewerPass:  DefaultViewerPass,
	}
}

// LoadSettings reads the config file, or returns defaults. The second
// return value reports whether defaults/migrations were applied and
// the caller should persist the result.
func LoadSettings() (Settings, bool) {
	s := DefaultSettings()
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return s, true
	}
	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s, true
	}
	migrated := false
	if loaded.SegmentMinutes <= 0 {
		loaded.SegmentMinutes = s.SegmentMinutes
	}
	if loaded.RetentionDays <= 0 {
		loaded.RetentionDays = s.RetentionDays
	}
	if loaded.PreBufferSeconds <= 0 {
		loaded.PreBufferSeconds = s.PreBufferSeconds
	}
	if loaded.PostBufferSeconds <= 0 {
		loaded.PostBufferSeconds = s.PostBufferSeconds
	}
	if !loaded.SensitivityMigrated {
		// One-time v1.3 migration: old 0..2 sensitivity levels
		// become 0..100 points (0->25, 1->50, 2->75) so existing
		// cameras keep triggering exactly as before.
		for i := range loaded.Cameras {
			if loaded.Cameras[i].Sensitivity >= SensitivityLow &&
				loaded.Cameras[i].Sensitivity <= SensitivityHigh {
				loaded.Cameras[i].Sensitivity = MigrateSensitivity(loaded.Cameras[i].Sensitivity)
			}
		}
		loaded.SensitivityMigrated = true
		migrated = true
	}
	// Clamp sensitivity into the 0..100 scale.
	for i := range loaded.Cameras {
		if loaded.Cameras[i].Sensitivity < 0 {
			loaded.Cameras[i].Sensitivity = 0
		}
		if loaded.Cameras[i].Sensitivity > 100 {
			loaded.Cameras[i].Sensitivity = 100
		}
		if loaded.Cameras[i].HumanConfidence < 0 {
			loaded.Cameras[i].HumanConfidence = 0
		}
		if loaded.Cameras[i].HumanConfidence > 100 {
			loaded.Cameras[i].HumanConfidence = 100
		}
		if loaded.Cameras[i].HumanConfidence == 0 {
			loaded.Cameras[i].HumanConfidence = 50
		}
	}
	// Reject unknown record modes on load: a stored mode that is
	// not one of the four known values falls back to Continuous,
	// the historical default. An empty mode is left alone — it is
	// what cameras stored before modes were recorded, and Mode()
	// already treats it as continuous — so an existing camera is
	// never silently re-moded (in particular never to Off) by an
	// upgrade. "off" and "person" themselves are known modes and
	// are kept as-is.
	for i := range loaded.Cameras {
		m := loaded.Cameras[i].RecordMode
		if m != "" && !ValidRecordMode(m) {
			loaded.Cameras[i].RecordMode = RecordContinuous
			migrated = true
		}
	}
	if loaded.PreviewFPS <= 0 {
		loaded.PreviewFPS = s.PreviewFPS
	}
	if loaded.RecordingsDir == "" {
		loaded.RecordingsDir = s.RecordingsDir
	}
	if loaded.GridLayout <= 0 {
		loaded.GridLayout = 4
	}
	if loaded.Cameras == nil {
		loaded.Cameras = []Camera{}
	}
	if loaded.PhonePort <= 0 {
		loaded.PhonePort = 8080
		migrated = true
	}
	if !loaded.PhoneConfigured {
		// First run of a build with phone viewing (includes every
		// v1.0 config): turn it on. The old single shared
		// "daybreak" password is retired in v1.2 — sign-in uses
		// user accounts now, so no password is generated here.
		loaded.PhoneEnabled = true
		loaded.PhoneConfigured = true
		migrated = true
	}
	if !loaded.RemoteConfigured {
		// First run of a v1.2+ build (includes every pre-1.2
		// config): enable remote viewing in on-demand mode with
		// the baked-in relay defaults.
		loaded.RemoteEnabled = true
		loaded.RemoteConfigured = true
		if loaded.RemoteMode == "" {
			loaded.RemoteMode = RemoteModeOnDemand
		}
		if loaded.RemoteRelayHost == "" {
			loaded.RemoteRelayHost = DefaultRelayHost
		}
		if loaded.RemoteRelayIP == "" {
			loaded.RemoteRelayIP = DefaultRelayIP
		}
		if loaded.RemotePublishUser == "" {
			loaded.RemotePublishUser = DefaultPublishUser
		}
		if loaded.RemotePublishPass == "" {
			loaded.RemotePublishPass = DefaultPublishPass
		}
		if loaded.RemoteViewerUser == "" {
			loaded.RemoteViewerUser = DefaultViewerUser
		}
		if loaded.RemoteViewerPass == "" {
			loaded.RemoteViewerPass = DefaultViewerPass
		}
		migrated = true
	}
	return loaded, migrated
}

// Save writes the settings atomically.
func (s Settings) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// The Linux config dir (~/.config/daybreak-nvr) may not exist
	// yet on first run; create it (harmless on Windows, where the
	// config sits beside the exe).
	_ = os.MkdirAll(filepath.Dir(ConfigPath()), 0o755)
	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath())
}

// FindFFmpeg locates an ffmpeg binary: the bundled copy next to the
// executable first, then the system PATH.
func FindFFmpeg() string {
	exeName := "ffmpeg"
	if strings.HasSuffix(strings.ToLower(os.Args[0]), ".exe") || filepath.Ext(ExeDir()) == ".exe" {
		exeName = "ffmpeg.exe"
	}
	candidates := []string{
		filepath.Join(ExeDir(), "ffmpeg.exe"),
		filepath.Join(ExeDir(), "ffmpeg"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := lookPath(exeName); err == nil {
		return p
	}
	if p, err := lookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// Validate checks a camera entry before saving.
func (c Camera) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("camera name is required")
	}
	if strings.TrimSpace(c.MainURL) == "" && strings.TrimSpace(c.IP) == "" {
		return fmt.Errorf("enter the camera IP address (or a full RTSP URL)")
	}
	if c.RecordMode != "" && !ValidRecordMode(c.RecordMode) {
		return fmt.Errorf("unknown record mode %q", c.RecordMode)
	}
	return nil
}
