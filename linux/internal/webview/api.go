// Full web UI API for Daybreak NVR (Linux v1.2).
//
// The original phone pages (/, /cam, /mjpeg, /snapshot, /clip) are
// kept exactly as they were. The full UI lives under /ui/ and talks
// to JSON endpoints under /api/. Everything sits behind the
// session login (see auth.go): any signed-in user may read, but
// mutations (settings, cameras, clip delete, user management)
// require the admin role (403 for viewers).
//
// The app supplies the management surface through the Admin
// interface (SetAdmin); without one, the UI renders but the
// management endpoints answer 501. That keeps this package
// testable without the desktop app attached.
package webview

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ---------- data shapes (JSON) ----------

// CameraDTO is a camera as the web UI edits it. Passwords travel
// over the LAN only, behind the same Basic auth as everything
// else — exactly like the desktop editor, which stores them in the
// local settings file.
type CameraDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	MainURL     string `json:"main_url"`
	SubURL      string `json:"sub_url"`
	Enabled     bool   `json:"enabled"`
	Record      bool   `json:"record"`
	RecordMode  string `json:"record_mode"` // "continuous" | "motion" | "person" | "off"
	Sensitivity int    `json:"sensitivity"` // 0..100 (v1.3 slider)
	// ZoneGrid is the 16x9 detection zone (144 '0'/'1' chars).
	ZoneGrid string `json:"zone_grid"`
	// HumanDetect / HumanConfidence are the on-device AI person
	// detection toggle and its 0..100 confidence threshold.
	HumanDetect     bool `json:"human_detect"`
	HumanConfidence int  `json:"human_confidence"`
}

// SettingsDTO is the subset of settings the web UI may change.
// Phone and remote viewing are deliberately NOT here (v1.3): they
// keep running with their stored values and have no toggles in
// settings any more — desktop or web.
type SettingsDTO struct {
	RecordingsDir    string `json:"recordings_dir"`
	SegmentMinutes   int    `json:"segment_minutes"`
	RetentionDays    int    `json:"retention_days"`
	MaxTotalGB       int    `json:"max_total_gb"`
	PreviewFPS       int    `json:"preview_fps"`
	GridLayout       int    `json:"grid_layout"`
	PreBufferSeconds int    `json:"pre_buffer_seconds"`
	PostBufferSeconds int   `json:"post_buffer_seconds"`
}

// UserDTO is one account as the web UI lists it. Password hashes
// are never sent to the browser.
type UserDTO struct {
	Username string `json:"username"`
	Role     string `json:"role"` // "admin" | "viewer"
	BuiltIn  bool   `json:"built_in"`
}

// UserInput is the payload for creating/updating a user. An empty
// password keeps the existing password (updates only); a new user
// must supply one.
type UserInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// MotionEventDTO is one motion event for the Events tab.
// Kind is "" (plain motion) or "person" for an on-device AI
// person detection, which also carries the tracking ID and the
// detection confidence.
type MotionEventDTO struct {
	CameraID   string    `json:"camera_id"`
	CameraName string    `json:"camera_name"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Open       bool      `json:"open"`
	Kind       string    `json:"kind,omitempty"`
	TrackID    int       `json:"track_id,omitempty"`
	Confidence float32   `json:"confidence,omitempty"`
}

// ClipDTO is one recorded clip for the Playback tab.
type ClipDTO struct {
	Token      string    `json:"token"`
	CameraSlug string    `json:"camera_slug"`
	CameraName string    `json:"camera_name"`
	Start      time.Time `json:"start"`
	Size       int64     `json:"size"`
}

// StatusDTO is the at-a-glance status for the UI header.
type StatusDTO struct {
	Version       string `json:"version"`
	Cameras       int    `json:"cameras"`
	Recording     int    `json:"recording"`
	StoredBytes   int64  `json:"stored_bytes"`
	StoredFiles   int    `json:"stored_files"`
	RecordingsDir string `json:"recordings_dir"`
	Remote        string `json:"remote"`
}

// Admin is the management surface the app provides to the web UI.
type Admin interface {
	Version() string
	GetSettings() SettingsDTO
	UpdateSettings(SettingsDTO) error
	GetCameras() []CameraDTO
	SaveCamera(CameraDTO) (CameraDTO, error)
	DeleteCamera(id string) error
	// TestCamera tries the camera's sub-stream for a few seconds
	// and reports whether video arrived.
	TestCamera(c CameraDTO) (bool, string)
	MotionEvents(cameraID string, limit int) []MotionEventDTO
	Status() StatusDTO
	// ListUsers returns the accounts that can sign in (built-in
	// admin included, hashes never exposed).
	ListUsers() []UserDTO
	// SaveUser creates or updates a user (see UserInput).
	SaveUser(UserInput) error
	// DeleteUser removes a user (never the built-in admin).
	DeleteUser(username string) error
	// Reload applies already-saved settings changes to the running
	// recorder, pumps and web server. It must not block the
	// request that triggered it (a phone-port change restarts this
	// very server — see the app's implementation).
	Reload()
	// CameraSnapshot grabs one fresh JPEG still from a camera's
	// sub-stream, for the zone picker on the Motion & Recording
	// page (the browser has no live pump to read a frame from).
	// Returns a clear error when the camera cannot be reached.
	CameraSnapshot(id string) ([]byte, error)
}

// SetAdmin attaches the management surface (nil = read-only UI).
func (s *Server) SetAdmin(a Admin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admin = a
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// requireAdmin resolves the Admin or answers 501.
func (s *Server) requireAdmin(w http.ResponseWriter) (Admin, bool) {
	s.mu.Lock()
	a := s.admin
	s.mu.Unlock()
	if a == nil {
		writeAPIError(w, http.StatusNotImplemented, "management is not available in this build")
		return nil, false
	}
	return a, true
}

// ---------- API handlers ----------

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.Status())
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.GetSettings())
	case http.MethodPost:
		if roleOf(r) != "admin" {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		var dto SettingsDTO
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
			return
		}
		if err := a.UpdateSettings(dto); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *Server) handleAPICameras(w http.ResponseWriter, r *http.Request) {
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.GetCameras())
	case http.MethodPost:
		if roleOf(r) != "admin" {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		var dto CameraDTO
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
			return
		}
		saved, err := a.SaveCamera(dto)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		if roleOf(r) != "admin" {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			writeAPIError(w, http.StatusBadRequest, "missing id")
			return
		}
		if err := a.DeleteCamera(id); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "GET, POST or DELETE only")
	}
}

func (s *Server) handleAPITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if roleOf(r) != "admin" {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	var dto CameraDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	good, msg := a.TestCamera(dto)
	writeJSON(w, http.StatusOK, map[string]any{"ok": good, "message": msg})
}

func (s *Server) handleAPIClips(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		// Deleting a clip is an admin action (viewers may only
		// watch and download).
		if roleOf(r) != "admin" {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			writeAPIError(w, http.StatusBadRequest, "missing token")
			return
		}
		if !s.provider.DeleteClip(token) {
			writeAPIError(w, http.StatusNotFound, "no clip with that token")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET or DELETE only")
		return
	}
	cam := r.URL.Query().Get("camera") // camera ID, slug or "" for all
	date := r.URL.Query().Get("date")  // YYYY-MM-DD or ""
	clips := s.provider.Clips()
	out := make([]ClipDTO, 0, len(clips))
	for _, c := range clips {
		if cam != "" && c.CameraSlug != cam {
			// Also allow matching by camera name.
			if c.CameraName != cam {
				continue
			}
		}
		if date != "" && c.Start.Format("2006-01-02") != date {
			continue
		}
		out = append(out, ClipDTO{
			Token:      ClipToken(c),
			CameraSlug: c.CameraSlug,
			CameraName: c.CameraName,
			Start:      c.Start,
			Size:       c.Size,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	if roleOf(r) != "admin" {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.ListUsers())
	case http.MethodPost:
		var in UserInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
			return
		}
		if err := a.SaveUser(in); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		username := r.URL.Query().Get("username")
		if username == "" {
			writeAPIError(w, http.StatusBadRequest, "missing username")
			return
		}
		if err := a.DeleteUser(username); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "GET, POST or DELETE only")
	}
}

func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, a.MotionEvents(r.URL.Query().Get("camera"), limit))
}

// handleAPICameraSnapshot serves one fresh JPEG still from a
// camera's sub-stream, for the zone picker on the Motion &
// Recording page (the browser has no live pump to read a frame
// from). GET /api/camera-snapshot?id=<camera id>. Admin role
// required (the zone picker lives on the admin-only Settings
// page). When the camera cannot be reached it answers 502 with
// a JSON error, so the page can fall back to the plain zone grid.
func (s *Server) handleAPICameraSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if roleOf(r) != "admin" {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	a, ok := s.requireAdmin(w)
	if !ok {
		return
	}
	jpeg, err := a.CameraSnapshot(r.URL.Query().Get("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(jpeg)
}

// ---------- PWA ----------

const manifestJSON = `{
  "name": "Daybreak NVR",
  "short_name": "Daybreak NVR",
  "description": "Daybreak NVR — live cameras, playback and motion events",
  "start_url": "/ui/",
  "scope": "/",
  "display": "standalone",
  "orientation": "any",
  "background_color": "#0b1118",
  "theme_color": "#0b1118",
  "icons": [
    { "src": "/ui/icon.png", "sizes": "512x512", "type": "image/png", "purpose": "any" },
    { "src": "/ui/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any" }
  ]
}
`

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	_, _ = w.Write([]byte(manifestJSON))
}

const serviceWorkerJS = `// Daybreak NVR service worker — caches the UI shell so the
// installed PWA opens instantly. Video and API calls always go to
// the network: recordings must never be served stale.
self.addEventListener('install', (e) => {
  e.waitUntil(caches.open('daybreak-nvr-v1').then((c) =>
    c.addAll(['/ui/', '/manifest.webmanifest'])));
  self.skipWaiting();
});
self.addEventListener('activate', (e) => { e.waitUntil(self.clients.claim()); });
self.addEventListener('fetch', (e) => {
  const u = new URL(e.request.url);
  if (u.pathname === '/ui/' || u.pathname === '/manifest.webmanifest') {
    e.respondWith(caches.match(e.request).then((hit) =>
      hit || fetch(e.request)));
    return;
  }
  e.respondWith(fetch(e.request));
});
`

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	_, _ = w.Write([]byte(serviceWorkerJS))
}

var (
	iconOnce sync.Once
	iconPNG  []byte
)

// appIcon renders a simple 512x512 app icon (dark slate tile, teal
// play mark) so the PWA manifest has something to install with —
// no asset files needed.
func appIcon() []byte {
	iconOnce.Do(func() {
		const size = 512
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		bg := color.RGBA{0x0b, 0x11, 0x18, 0xff}
		draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
		teal := color.RGBA{0x16, 0x85, 0x73, 0xff}
		// Rounded-ish tile: filled square inset by 64px.
		for y := 64; y < size-64; y++ {
			for x := 64; x < size-64; x++ {
				img.Set(x, y, teal)
			}
		}
		// White play triangle, centered.
		white := color.RGBA{0xff, 0xff, 0xff, 0xff}
		for y := 156; y < 356; y++ {
			half := (y - 156) * 96 / 200
			for x := 206 - half; x < 206+half; x++ {
				if x >= 64 && x < size-64 {
					img.Set(x, y, white)
				}
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil {
			iconPNG = buf.Bytes()
		}
	})
	return iconPNG
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(appIcon())
}

// registerAPIRoutes wires the full-UI routes into the mux. Called
// from Start(); the old phone routes are registered first and
// untouched.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ui/", s.auth(s.handleUI))
	mux.HandleFunc("/ui", s.auth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	}))
	mux.HandleFunc("/api/status", s.auth(s.handleAPIStatus))
	mux.HandleFunc("/api/settings", s.auth(s.handleAPISettings))
	mux.HandleFunc("/api/cameras", s.auth(s.handleAPICameras))
	mux.HandleFunc("/api/test", s.auth(s.handleAPITest))
	mux.HandleFunc("/api/clips", s.auth(s.handleAPIClips))
	mux.HandleFunc("/api/users", s.auth(s.handleAPIUsers))
	mux.HandleFunc("/api/events", s.auth(s.handleAPIEvents))
	mux.HandleFunc("/api/camera-snapshot", s.auth(s.handleAPICameraSnapshot))
	mux.HandleFunc("/manifest.webmanifest", s.auth(s.handleManifest))
	mux.HandleFunc("/sw.js", s.auth(s.handleServiceWorker))
	mux.HandleFunc("/ui/icon.png", s.auth(s.handleIcon))
}
