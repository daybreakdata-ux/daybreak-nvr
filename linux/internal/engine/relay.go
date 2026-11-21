package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Baked-in relay defaults for remote viewing. The relay is Justin's
// Oracle VM ("Machine 1"), which already fronts the Daybreak CCTV
// streams; the NVR publishes under nvr-<slug> paths and uses the
// same publish/viewer accounts as the CCTV relay. These ship as
// config defaults (like the CCTV .deb did) and are not shown in the
// settings UI, except the viewer login on the remote-viewing card.
const (
	DefaultRelayHost   = "163-192-218-43.sslip.io"
	DefaultRelayIP     = "163.192.218.43"
	DefaultPublishUser = "daybreak-pub"
	DefaultPublishPass = "REPLACE-ME"
	DefaultViewerUser  = "viewer"
	DefaultViewerPass  = "REPLACE-ME"
)

// Remote viewing modes.
const (
	RemoteModeOnDemand = "ondemand"
	RemoteModeAlways   = "always"
	RemoteModeOff      = "off"
)

// RemoteModeEffective resolves the configured mode: off when remote
// viewing is disabled, otherwise the mode (default on-demand).
func (s Settings) RemoteModeEffective() string {
	if !s.RemoteEnabled {
		return RemoteModeOff
	}
	switch s.RemoteMode {
	case RemoteModeAlways, RemoteModeOff:
		return s.RemoteMode
	}
	return RemoteModeOnDemand
}

// RelayCamera is one camera as the relay knows it.
type RelayCamera struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// RelayClip is one recorded segment as the relay's clip index knows
// it. Start is a Unix timestamp; the clip's token (see
// engine.ClipToken) is "<slug>/<start>". Duration is seconds from
// the mp4 mvhd box (0 when unknown); Kind is "person"/"motion"/"".
type RelayClip struct {
	Slug     string  `json:"slug"`
	Name     string  `json:"name"`
	Start    int64   `json:"start"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration,omitempty"`
	Kind     string  `json:"kind,omitempty"`
}

// ClipRequest is one clip a viewer asked the relay for. The NVR
// polls these and uploads the matching file.
type ClipRequest struct {
	Token string `json:"token"`
	Slug  string `json:"slug"`
	Start int64  `json:"start"`
}

// ClipDelete is one clip the relay asks the NVR to delete (a
// viewer or admin deleted it in the remote viewer). The NVR
// deletes the file from disk, re-posts its clip index, and acks
// with PostClipDeleteDone.
type ClipDelete struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Slug  string `json:"slug"`
	Start int64  `json:"start"`
}

// DeleteToken returns the clip token for this delete request.
func (d ClipDelete) DeleteToken() string {
	if d.Token != "" {
		return d.Token
	}
	if d.Slug != "" {
		return fmt.Sprintf("%s/%d", d.Slug, d.Start)
	}
	return ""
}

// RelayClient talks to the nvr-demand signaling service on the
// relay over HTTPS, using the publish account.
type RelayClient struct {
	Base    string // https://<relay host>
	PubUser string
	PubPass string
	HTTP    *http.Client
}

// NewRelayClient builds a client from settings (with defaults).
func NewRelayClient(s Settings) *RelayClient {
	host := s.RemoteRelayHost
	if host == "" {
		host = DefaultRelayHost
	}
	user := s.RemotePublishUser
	if user == "" {
		user = DefaultPublishUser
	}
	pass := s.RemotePublishPass
	if pass == "" {
		pass = DefaultPublishPass
	}
	return &RelayClient{
		Base:    "https://" + host,
		PubUser: user,
		PubPass: pass,
		HTTP:    &http.Client{Timeout: 6 * time.Second},
	}
}

func (c *RelayClient) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.PubUser, c.PubPass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay %s: HTTP %d", path, resp.StatusCode)
	}
	return data, nil
}

// PostCameras publishes the camera list to the relay.
func (c *RelayClient) PostCameras(ctx context.Context, cams []RelayCamera) error {
	if cams == nil {
		cams = []RelayCamera{}
	}
	_, err := c.do(ctx, http.MethodPost, "/api/nvr/cameras",
		map[string]any{"cameras": cams})
	return err
}

// GetDemand returns the slugs viewers are currently asking for.
func (c *RelayClient) GetDemand(ctx context.Context) ([]string, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/nvr/demand", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Demand []string `json:"demand"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Demand == nil {
		out.Demand = []string{}
	}
	return out.Demand, nil
}

// PostClips publishes the recordings index to the relay so the
// remote viewer can list clips without the PC being reachable.
func (c *RelayClient) PostClips(ctx context.Context, clips []RelayClip) error {
	if clips == nil {
		clips = []RelayClip{}
	}
	_, err := c.do(ctx, http.MethodPost, "/api/nvr/clips",
		map[string]any{"clips": clips})
	return err
}

// GetClipRequests returns the clip tokens viewers are currently
// waiting on.
func (c *RelayClient) GetClipRequests(ctx context.Context) ([]ClipRequest, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/nvr/clip-requests", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Requests []ClipRequest `json:"requests"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Requests == nil {
		out.Requests = []ClipRequest{}
	}
	return out.Requests, nil
}

// PostUsers syncs the NVR's user accounts (username, bcrypt hash,
// role) to the relay so remote viewers can sign in against them.
// The relay never sees a plaintext password.
func (c *RelayClient) PostUsers(ctx context.Context, users []User) error {
	if users == nil {
		users = []User{}
	}
	_, err := c.do(ctx, http.MethodPost, "/api/nvr/users",
		map[string]any{"users": users})
	return err
}

// GetClipDeletes returns the clips the relay asks this NVR to
// delete. The relay answers either {"deletes":[...]} or a bare
// JSON array.
func (c *RelayClient) GetClipDeletes(ctx context.Context) ([]ClipDelete, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/nvr/clip-deletes", nil)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Deletes []ClipDelete `json:"deletes"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}
	if wrapped.Deletes != nil {
		return wrapped.Deletes, nil
	}
	var bare []ClipDelete
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, err
	}
	if bare == nil {
		bare = []ClipDelete{}
	}
	return bare, nil
}

// PostClipDeleteDone tells the relay one clip delete has been
// carried out, so it stops reporting it as pending.
func (c *RelayClient) PostClipDeleteDone(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/nvr/clip-delete-done",
		map[string]any{"id": id})
	return err
}

// UploadClip sends one recorded MP4 file to the relay, where the
// viewer can then stream it. A clip upload is one plain HTTPS POST
// of the file bytes; it can take a minute on a slow uplink, so it
// uses its own long-timeout client and must never run on the
// manager's short poll context.
func (c *RelayClient) UploadClip(ctx context.Context, token string, data io.Reader, size int64) error {
	u := c.Base + "/api/nvr/clip-upload?token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, data)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.PubUser, c.PubPass)
	req.Header.Set("Content-Type", "video/mp4")
	req.ContentLength = size
	hc := &http.Client{Timeout: 10 * time.Minute}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay clip-upload: HTTP %d", resp.StatusCode)
	}
	return nil
}

// UploadSnapshot sends one motion snapshot JPEG to the relay,
// keyed by the clip token (<slug>/<start>). Snapshots are tiny,
// so they are uploaded eagerly by the RemoteManager rather than
// on viewer request.
func (c *RelayClient) UploadSnapshot(ctx context.Context, token string, data io.Reader, size int64) error {
	u := c.Base + "/api/nvr/snapshot-upload?token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, data)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.PubUser, c.PubPass)
	req.Header.Set("Content-Type", "image/jpeg")
	req.ContentLength = size
	hc := &http.Client{Timeout: 2 * time.Minute}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay snapshot-upload: HTTP %d", resp.StatusCode)
	}
	return nil
}

// PublishURL is the RTSP address a pusher sends one camera to.
func (s Settings) PublishURL(cam Camera) string {
	ip := s.RemoteRelayIP
	if ip == "" {
		ip = DefaultRelayIP
	}
	user := s.RemotePublishUser
	if user == "" {
		user = DefaultPublishUser
	}
	pass := s.RemotePublishPass
	if pass == "" {
		pass = DefaultPublishPass
	}
	u := url.URL{
		Scheme: "rtsp",
		User:   url.UserPassword(user, pass),
		Host:   ip + ":8554",
		Path:   "/nvr-" + cam.Slug(),
	}
	return u.String()
}

// RelayCameras lists enabled cameras in relay form.
func (s Settings) RelayCameras() []RelayCamera {
	out := []RelayCamera{}
	seen := map[string]bool{}
	for _, c := range s.Cameras {
		if !c.Enabled || c.SubStreamURL() == "" {
			continue
		}
		slug := c.Slug()
		if seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, RelayCamera{Slug: slug, Name: c.Name})
	}
	return out
}
