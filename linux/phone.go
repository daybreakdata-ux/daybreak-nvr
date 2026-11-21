package main

import (
	"image"
	"net"
	"strings"
	"time"

	"fyne.io/fyne/v2"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/startlog"
	"daybreaknvr/internal/webview"
)

// phoneProvider adapts the app to the webview server's needs.
type phoneProvider struct{ n *nvrApp }

func (p phoneProvider) Cameras() []webview.CameraInfo {
	var out []webview.CameraInfo
	for _, c := range p.n.settings.Cameras {
		if c.Enabled {
			out = append(out, webview.CameraInfo{ID: c.ID, Name: c.Name})
		}
	}
	return out
}

func (p phoneProvider) Watch(id string) func() {
	return p.n.watchHold(id)
}

func (p phoneProvider) LatestFrame(id string) image.Image {
	pump := p.n.pumpFor(id)
	if pump == nil {
		return nil
	}
	return pump.Latest()
}

func (p phoneProvider) Clips() []engine.Clip {
	return engine.ScanLibrary(p.n.settings.RecordingsDir, p.n.settings.Cameras)
}

func (p phoneProvider) ClipByToken(token string) (string, bool) {
	root := p.n.settings.RecordingsDir
	for _, c := range p.Clips() {
		if webview.ClipToken(c) == token {
			// Defence in depth: the path must live under the
			// recordings folder.
			if strings.HasPrefix(c.Path, root) {
				return c.Path, true
			}
			return "", false
		}
	}
	return "", false
}

func (p phoneProvider) DeleteClip(token string) bool {
	return engine.DeleteClipFile(p.n.settings.RecordingsDir, p.n.settings.Cameras, token)
}

// webAdmin builds the full-web-UI management surface for the
// desktop app. The reload hook runs on the Fyne UI thread (the
// Devices tab widgets are not thread-safe) and restarts the web
// server after a short delay, so an API response that changed the
// phone port gets out before the server restarts on the new port.
func (n *nvrApp) webAdmin() *adminBackend {
	return &adminBackend{
		get: func() engine.Settings { return n.settings },
		reload: func() {
			fyne.Do(func() { n.reloadSettings() })
			go func() {
				time.Sleep(300 * time.Millisecond)
				n.restartPhoneServer()
			}()
		},
		ffmpeg:   n.ffmpeg,
		tracker:  n.tracker,
		recorder: n.recorder,
		eventRec: n.eventRec,
		onUsersChanged: func() {
			n.syncUsersToRelay()
		},
		remote: func() string {
			if n.remote == nil {
				return ""
			}
			st := n.remote.Status()
			switch st.Mode {
			case engine.RemoteModeOff, "":
				return "Off"
			}
			if st.Connected {
				return "Connected"
			}
			if st.LastError != "" {
				return "Relay unreachable"
			}
			return "Connecting…"
		},
	}
}

// restartPhoneServer (re)starts the phone viewing server from the
// current settings.
func (n *nvrApp) restartPhoneServer() {
	if n.phone != nil {
		n.phone.Stop()
		n.phone = nil
	}
	if !n.settings.PhoneEnabled {
		return
	}
	srv := webview.New(phoneProvider{n})
	srv.SetAuthenticator(func(username, password string) (string, bool) {
		u, ok := engine.Authenticate(username, password)
		if !ok {
			return "", false
		}
		return u.Role, true
	})
	srv.SetAdmin(n.webAdmin())
	if err := srv.Start(n.settings.PhonePort); err != nil {
		// Never fatal: log it and carry on without phone viewing
		// (a taken port must not stop the NVR from opening).
		startlog.Stage("phone viewing server failed to start on port %d (continuing without it): %v",
			n.settings.PhonePort, err)
		return
	}
	n.phone = srv
	startlog.Stage("phone viewing server listening on port %d", n.settings.PhonePort)
}

// lanIP returns the PC's primary LAN IPv4 address for display.
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip != nil && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}

// phoneURL is the address to open on the phone.
func (n *nvrApp) phoneURL() string {
	return "http://" + lanIP() + ":" + itoa(n.settings.PhonePort)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
