// Session login for the LAN web UI (v1.2).
//
// The old single shared Basic-auth login ("daybreak" + one
// password) is gone. Sign-in is now a session cookie issued by
// POST /ui/login, verified against the engine user store
// (including the built-in fallback admin). Sessions live in a
// server-side map and expire after 24 hours. The signed-in
// user's role travels in the request context: "admin" may change
// settings and delete clips; "viewer" may only view, play and
// download.
package webview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// Authenticator verifies a username/password and returns the
// user's role ("admin" or "viewer") and whether the credentials
// are valid. The app wires this to engine.Authenticate.
type Authenticator func(username, password string) (role string, ok bool)

type session struct {
	username string
	role     string
	expires  time.Time
}

const sessionCookieName = "nvr_session"
const sessionTTL = 24 * time.Hour

type ctxKey string

const ctxRoleKey ctxKey = "role"
const ctxUserKey ctxKey = "username"

// SetAuthenticator attaches the credential checker. Until one is
// set, every protected route answers 401.
func (s *Server) SetAuthenticator(fn Authenticator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authFn = fn
}

func newSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *Server) createSession(username, role string) string {
	tok := newSessionToken()
	if tok == "" {
		return ""
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]session{}
	}
	s.sessions[tok] = session{username: username, role: role, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return tok
}

// sessionRole returns the signed-in user and role for a request,
// or ok=false when there is no live session.
func (s *Server) sessionRole(r *http.Request) (username, role string, ok bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, found := s.sessions[c.Value]
	if !found {
		return "", "", false
	}
	if time.Now().After(sess.expires) {
		delete(s.sessions, c.Value)
		return "", "", false
	}
	return sess.username, sess.role, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
	})
}

// roleOf reads the role the auth middleware stored in the request
// context ("" when unauthenticated).
func roleOf(r *http.Request) string {
	if v, ok := r.Context().Value(ctxRoleKey).(string); ok {
		return v
	}
	return ""
}

// auth wraps a handler: it requires a live session and stores the
// user/role in the request context. API calls get a JSON 401;
// page loads are redirected to the login page.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, role, ok := s.sessionRole(r)
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeAPIError(w, http.StatusUnauthorized, "sign in required")
				return
			}
			http.Redirect(w, r, "/ui/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), ctxRoleKey, role)
		ctx = context.WithValue(ctx, ctxUserKey, username)
		next(w, r.WithContext(ctx))
	}
}

// authAdmin wraps a handler: session required AND role admin.
// Viewers get 403 (JSON for API calls, plain text otherwise).
func (s *Server) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if roleOf(r) != "admin" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeAPIError(w, http.StatusForbidden, "admin role required")
				return
			}
			http.Error(w, "Admin role required", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// ---------- login page ----------

const loginHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · Daybreak NVR</title>
<style>
 body{background:#0b1118;color:#d8e0e9;font-family:system-ui,sans-serif;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
 .card{background:#18202a;border:1px solid #2a3644;border-radius:12px;padding:28px;width:min(360px,90vw)}
 h1{font-size:19px;margin:0 0 4px} h1 b{color:#b2c13a} h1 span{color:#168573}
 p{color:#8b98a8;font-size:14px;margin:4px 0 18px}
 label{display:block;font-size:13px;color:#8b98a8;margin:12px 0 4px}
 input{width:100%;background:#0e141c;border:1px solid #2a3644;color:#d8e0e9;border-radius:7px;padding:10px;font-size:15px;box-sizing:border-box}
 button{width:100%;background:#168573;color:#fff;border:0;border-radius:8px;padding:11px;font-size:15px;cursor:pointer;margin-top:18px}
 .err{color:#e06c5a;font-size:14px;margin-top:12px;min-height:18px}
</style></head><body>
<div class="card">
 <h1><b>DAYBREAK</b> <span>NVR</span></h1>
 <p>Sign in to view your cameras.</p>
 <form method="post" action="/ui/login">
  <label>Username</label><input name="username" autocomplete="username" autofocus>
  <label>Password</label><input name="password" type="password" autocomplete="current-password">
  <button type="submit">Sign in</button>
  <div class="err">{{.Error}}</div>
 </form>
</div>
</body></html>`

var loginTpl = template.Must(template.New("login").Parse(loginHTML))

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	authFn := s.authFn
	s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.sessionRole(r); ok {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginTpl.Execute(w, map[string]string{"Error": ""})
	case http.MethodPost:
		var username, password string
		isJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json")
		if isJSON {
			var body struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "bad JSON")
				return
			}
			username, password = body.Username, body.Password
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			username, password = r.Form.Get("username"), r.Form.Get("password")
		}
		role := ""
		ok := false
		if authFn != nil {
			role, ok = authFn(username, password)
		}
		if !ok {
			if isJSON {
				writeAPIError(w, http.StatusUnauthorized, "wrong username or password")
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = loginTpl.Execute(w, map[string]string{"Error": "Wrong username or password."})
			return
		}
		tok := s.createSession(username, role)
		if tok == "" {
			http.Error(w, "could not create session", http.StatusInternalServerError)
			return
		}
		s.setSessionCookie(w, tok)
		if isJSON {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": username, "role": role})
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAPIMe reports who is signed in (used by the UI to decide
// which tabs and buttons a viewer may see).
func (s *Server) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	username, _ := r.Context().Value(ctxUserKey).(string)
	writeJSON(w, http.StatusOK, map[string]string{"username": username, "role": roleOf(r)})
}
