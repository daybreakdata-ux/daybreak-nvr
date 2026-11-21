// Package webview is Daybreak NVR's built-in phone viewing server:
// a small session-login web app on the LAN with a live camera
// grid (MJPEG), per-camera pages and recorded-clip playback.
package webview

import (
	"bytes"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"daybreaknvr/internal/engine"
)

// CameraInfo is what the server needs to know about a camera.
type CameraInfo struct {
	ID   string
	Name string
}

// Provider is implemented by the app: it vends camera lists, live
// frames (keeping the underlying stream alive while watched) and
// the recordings library.
type Provider interface {
	Cameras() []CameraInfo
	// Watch marks a camera as being viewed so its stream stays up;
	// the returned func releases the hold.
	Watch(id string) (release func())
	LatestFrame(id string) image.Image
	Clips() []engine.Clip
	// ClipByToken resolves a clip token from Clips() to a file path.
	ClipByToken(token string) (string, bool)
	// DeleteClip removes one clip from disk by token.
	DeleteClip(token string) bool
}

// ClipToken builds the opaque token used in /clip URLs for a clip.
// It is the engine's clip identity (slug + start timestamp).
func ClipToken(c engine.Clip) string {
	return engine.ClipToken(c)
}

// Server is the phone viewing HTTP server. Sign-in is a session
// cookie issued by POST /ui/login (see auth.go); the old Basic
// auth is gone in v1.2.
type Server struct {
	provider Provider
	admin    Admin // optional management surface for the full web UI
	authFn   Authenticator
	sessions map[string]session

	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
}

// New creates a server. Call SetAuthenticator before Start, or
// every protected route answers 401.
func New(p Provider) *Server {
	return &Server{provider: p, sessions: map[string]session{}}
}

// Start binds the port (0 = any free port, for tests) and serves.
func (s *Server) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return fmt.Errorf("already running")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.auth(s.handleLogout))
	mux.HandleFunc("/api/me", s.auth(s.handleAPIMe))
	mux.HandleFunc("/", s.auth(s.handleHome))
	mux.HandleFunc("/cam", s.auth(s.handleCam))
	mux.HandleFunc("/mjpeg", s.auth(s.handleMJPEG))
	mux.HandleFunc("/snapshot", s.auth(s.handleSnapshot))
	mux.HandleFunc("/clip", s.auth(s.handleClip))
	s.registerAPIRoutes(mux)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.ln = ln
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

// Addr returns the bound address (host:port).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Stop shuts the server down.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
		s.srv = nil
		s.ln = nil
	}
}

// ---------- pages ----------

var pageTpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
 body{background:#18202a;color:#d8e0e9;font-family:system-ui,sans-serif;margin:0;padding:12px}
 h1{font-size:19px;margin:4px 0 12px} h2{font-size:16px;margin:18px 0 8px}
 .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:10px}
 .tile{background:#202a37;border-radius:10px;overflow:hidden;text-decoration:none;color:#d8e0e9}
 .tile img{width:100%;display:block;aspect-ratio:16/9;object-fit:cover;background:#05070a}
 .tile span{display:block;padding:8px 10px;font-weight:600;font-size:14px}
 .big{width:100%;border-radius:10px;background:#05070a}
 .clip{display:block;background:#202a37;border-radius:8px;padding:10px 12px;margin:6px 0;
       text-decoration:none;color:#d8e0e9;font-size:14px}
 .clip small{color:#8b98a8}
 a.back{color:#64b0f0;text-decoration:none}
 video{width:100%;border-radius:10px;background:#000}
</style></head><body>
{{.Body}}
</body></html>`))

type pageData struct {
	Title string
	Body  template.HTML
}

func (s *Server) render(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTpl.Execute(w, pageData{Title: title, Body: template.HTML(body)})
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cams := s.provider.Cameras()
	var b strings.Builder
	b.WriteString("<h1>🎥 Daybreak NVR</h1>")
	if len(cams) == 0 {
		b.WriteString("<p>No cameras yet. Add them in Daybreak NVR on the PC.</p>")
	}
	b.WriteString(`<div class="grid">`)
	for _, c := range cams {
		fmt.Fprintf(&b, `<a class="tile" href="/cam?id=%s"><img src="/mjpeg?id=%s" alt=""><span>%s</span></a>`,
			template.HTMLEscapeString(c.ID), template.HTMLEscapeString(c.ID),
			template.HTMLEscapeString(c.Name))
	}
	b.WriteString(`</div>`)
	clips := s.provider.Clips()
	if len(clips) > 0 {
		b.WriteString("<h2>Latest recordings</h2>")
		n := len(clips)
		if n > 12 {
			n = 12
		}
		for _, c := range clips[:n] {
			fmt.Fprintf(&b, `<a class="clip" href="/clip?f=%s">▶ %s <small>· %s · %s</small></a>`,
				template.HTMLEscapeString(ClipToken(c)),
				template.HTMLEscapeString(c.CameraName),
				c.Start.Format("Mon Jan 2 · 3:04 PM"),
				humanSize(c.Size))
		}
	}
	s.render(w, "Daybreak NVR", b.String())
}

func (s *Server) handleCam(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var name string
	for _, c := range s.provider.Cameras() {
		if c.ID == id {
			name = c.Name
		}
	}
	if name == "" {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	b.WriteString(`<a class="back" href="/">← All cameras</a>`)
	fmt.Fprintf(&b, "<h1>%s</h1>", template.HTMLEscapeString(name))
	fmt.Fprintf(&b, `<img class="big" src="/mjpeg?id=%s" alt="">`,
		template.HTMLEscapeString(id))
	b.WriteString("<h2>Recordings</h2>")
	count := 0
	for _, c := range s.provider.Clips() {
		if c.CameraName != name {
			continue
		}
		fmt.Fprintf(&b, `<a class="clip" href="/clip?f=%s">▶ %s <small>· %s</small></a>`,
			template.HTMLEscapeString(ClipToken(c)),
			c.Start.Format("Mon Jan 2 · 3:04:05 PM"), humanSize(c.Size))
		count++
		if count >= 30 {
			break
		}
	}
	if count == 0 {
		b.WriteString("<p>No recordings yet for this camera.</p>")
	}
	s.render(w, name+" · Daybreak NVR", b.String())
}

// ---------- media ----------

var placeholder = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 640, 360))
	dark := color.RGBA{10, 14, 20, 255}
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, dark)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70})
	return buf.Bytes()
}()

func encodeFrame(img image.Image) []byte {
	if img == nil {
		return placeholder
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 72}); err != nil {
		return placeholder
	}
	return buf.Bytes()
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	release := s.provider.Watch(id)
	defer release()
	// Give a cold stream a moment to produce a frame.
	var img image.Image
	for i := 0; i < 20; i++ {
		img = s.provider.LatestFrame(id)
		if img != nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(encodeFrame(img))
}

func (s *Server) handleMJPEG(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	release := s.provider.Watch(id)
	defer release()
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	tick := time.NewTicker(125 * time.Millisecond) // ~8 fps
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			frame := encodeFrame(s.provider.LatestFrame(id))
			if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) handleClip(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("f")
	path, ok := s.provider.ClipByToken(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// ServeContent gives phones the range requests video needs.
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func humanSize(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
