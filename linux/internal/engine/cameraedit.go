package engine

import "strings"

// EditorInput carries the plain values collected by the camera
// editor form (Devices & Settings). It is deliberately UI-free so
// the prefill/save mapping can be unit-tested headlessly — the
// Fyne widget layer only reads its widgets into this struct and
// displays the result.
type EditorInput struct {
	Name        string
	IP          string
	Username    string
	Password    string
	MainURL     string
	SubURL      string
	Enabled     bool
	Record      bool
	// RecordMode is the record mode: "" (no mode chosen yet — a
	// brand-new camera, defaults to Motion only), or one of
	// RecordContinuous / RecordMotion / RecordPerson / RecordOff
	// ("off" = live view only, no recording at all; "person" =
	// record only when the on-device AI sees a person).
	RecordMode  string
	Sensitivity int // 0..100 (v1.3 slider)
	// ZoneGrid is the 16x9 detection zone (144 '0'/'1' chars).
	ZoneGrid string
	// HumanDetect / HumanConfidence are the on-device AI person
	// detection toggle and its 0..100 confidence threshold.
	HumanDetect     bool
	HumanConfidence int
}

// BuildCamera applies editor input on top of base — the existing
// camera when editing, or the blank defaults when adding — and
// validates the result. The camera's ID is always preserved from
// base: recordings folders, motion events and remote-viewing paths
// all key off it, so an edit must never change it. Callers assign
// a fresh ID only for a genuinely new camera.
func BuildCamera(base Camera, in EditorInput) (Camera, error) {
	c := base
	c.Name = strings.TrimSpace(in.Name)
	c.IP = strings.TrimSpace(in.IP)
	c.Username = strings.TrimSpace(in.Username)
	c.Password = in.Password // passwords are never trimmed
	c.MainURL = strings.TrimSpace(in.MainURL)
	c.SubURL = strings.TrimSpace(in.SubURL)
	c.Enabled = in.Enabled
	c.Record = in.Record
	// "" means no mode chosen yet (a brand-new camera): default
	// to Motion only. An unrecognised mode is rejected by
	// Validate below — it is never silently stored.
	c.RecordMode = in.RecordMode
	if c.RecordMode == "" {
		c.RecordMode = RecordMotion
	}
	c.Sensitivity = in.Sensitivity
	if c.Sensitivity < 0 || c.Sensitivity > 100 {
		c.Sensitivity = SensitivityDefault
	}
	c.ZoneGrid = in.ZoneGrid
	if len(c.ZoneGrid) != ZoneCells {
		c.ZoneGrid = ""
	}
	c.HumanDetect = in.HumanDetect
	// Person-only mode records only when the on-device AI sees a
	// person, so it cannot work with human detection off: force
	// it on here, in the one place every save path (desktop
	// editor, Motion page, web UI) goes through. The UIs show
	// the toggle as on for a Person-only camera, so this is never
	// a silent surprise — and turning human detection off while
	// in Person-only mode is a no-op (it comes back on at save).
	if c.RecordMode == RecordPerson {
		c.HumanDetect = true
	}
	c.HumanConfidence = in.HumanConfidence
	if c.HumanConfidence < 0 || c.HumanConfidence > 100 {
		c.HumanConfidence = 50
	}
	if err := c.Validate(); err != nil {
		return Camera{}, err
	}
	return c, nil
}

// EditorInputFromCamera is the inverse of BuildCamera: it maps a
// stored camera onto the values the editor form should prefill.
// Sensitivity is clamped into the 0..100 range.
func EditorInputFromCamera(c Camera) EditorInput {
	sens := c.Sensitivity
	if sens < 0 || sens > 100 {
		sens = SensitivityDefault
	}
	conf := c.HumanConfidence
	if conf < 0 || conf > 100 {
		conf = 50
	}
	return EditorInput{
		Name:            c.Name,
		IP:              c.IP,
		Username:        c.Username,
		Password:        c.Password,
		MainURL:         c.MainURL,
		SubURL:          c.SubURL,
		Enabled:         c.Enabled,
		Record:          c.Record,
		RecordMode:      c.Mode(),
		Sensitivity:     sens,
		ZoneGrid:        c.ZoneGrid,
		HumanDetect:     c.HumanDetect,
		HumanConfidence: conf,
	}
}
