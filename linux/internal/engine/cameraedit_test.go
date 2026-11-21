package engine

import "testing"

// Adding: the blank-defaults base gets the form values, keeps its
// (empty) ID for the caller to fill, and defaults to Motion only
// (v1.5.2: a brand-new camera with no mode chosen yet records on
// motion).
func TestBuildCameraAdd(t *testing.T) {
	base := Camera{Enabled: true, Record: true, Sensitivity: 50}
	got, err := BuildCamera(base, EditorInput{
		Name: "  Front Door ", IP: " 192.168.1.50 ",
		Username: " tapo ", Password: " p w ",
		Enabled: true, Record: true, Sensitivity: 75,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.Name != "Front Door" || got.IP != "192.168.1.50" || got.Username != "tapo" {
		t.Errorf("trimmed fields wrong: %+v", got)
	}
	if got.Password != " p w " {
		t.Errorf("password must not be trimmed, got %q", got.Password)
	}
	if got.Mode() != RecordMotion {
		t.Errorf("mode = %q, want motion (new-camera default)", got.Mode())
	}
	if got.Sensitivity != 75 {
		t.Errorf("sensitivity = %d, want 75", got.Sensitivity)
	}
	if got.ID != "" {
		t.Errorf("new camera ID should stay empty for the caller, got %q", got.ID)
	}
}

// Editing: the ID (and therefore recordings/motion/remote paths)
// survives a round trip through the editor mapping.
func TestBuildCameraEditPreservesID(t *testing.T) {
	stored := Camera{
		ID: "cam123", Name: "Porch", IP: "192.168.1.60",
		RecordMode: RecordMotion, Sensitivity: 150, // out of range on purpose
	}
	in := EditorInputFromCamera(stored)
	if in.Sensitivity != SensitivityDefault {
		t.Errorf("prefill sensitivity = %d, want clamped default %d", in.Sensitivity, SensitivityDefault)
	}
	if in.RecordMode != RecordMotion {
		t.Error("prefill should show motion mode for a motion camera")
	}
	in.Name = "Back Porch"
	in.RecordMode = RecordContinuous
	got, err := BuildCamera(stored, in)
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.ID != "cam123" {
		t.Errorf("edit changed the camera ID: %q", got.ID)
	}
	if got.Name != "Back Porch" || got.Mode() != RecordContinuous {
		t.Errorf("edit not applied: %+v", got)
	}
}

// Validation failures surface as errors instead of a saved camera.
func TestBuildCameraValidation(t *testing.T) {
	if _, err := BuildCamera(Camera{}, EditorInput{IP: "192.168.1.50"}); err == nil {
		t.Error("empty name should fail validation")
	}
	if _, err := BuildCamera(Camera{}, EditorInput{Name: "X"}); err == nil {
		t.Error("no IP and no RTSP URL should fail validation")
	}
	// A full RTSP URL with no IP is fine (non-Tapo cameras).
	if _, err := BuildCamera(Camera{}, EditorInput{
		Name: "Barn", MainURL: "rtsp://192.168.1.70/main",
	}); err != nil {
		t.Errorf("RTSP-URL-only camera should validate: %v", err)
	}
}

// A 0..100 sensitivity from the form is kept as-is; an
// out-of-range one falls back to the default.
func TestBuildCameraSensitivityClamp(t *testing.T) {
	got, err := BuildCamera(Camera{}, EditorInput{
		Name: "Yard", IP: "192.168.1.80", Sensitivity: 42,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.Sensitivity != 42 {
		t.Errorf("sensitivity = %d, want 42", got.Sensitivity)
	}
	got, err = BuildCamera(Camera{}, EditorInput{
		Name: "Yard", IP: "192.168.1.80", Sensitivity: 150,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.Sensitivity != SensitivityDefault {
		t.Errorf("sensitivity = %d, want default %d", got.Sensitivity, SensitivityDefault)
	}
}

// Zone and AI fields round-trip through the editor mapping.
func TestBuildCameraZoneAndAI(t *testing.T) {
	grid := DefaultZoneGrid()
	grid = grid[:10] + "0000000000" + grid[20:] // disable a block
	got, err := BuildCamera(Camera{}, EditorInput{
		Name: "Yard", IP: "192.168.1.80",
		ZoneGrid: grid, HumanDetect: true, HumanConfidence: 70,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.ZoneGrid != grid {
		t.Error("zone grid not kept")
	}
	if !got.HumanDetect || got.HumanConfidence != 70 {
		t.Errorf("AI fields not kept: %+v", got)
	}
	in := EditorInputFromCamera(got)
	if in.ZoneGrid != grid || !in.HumanDetect || in.HumanConfidence != 70 {
		t.Errorf("prefill did not round-trip zone/AI: %+v", in)
	}
	// A malformed zone grid is cleared (whole frame).
	got, err = BuildCamera(Camera{}, EditorInput{
		Name: "Yard", IP: "192.168.1.80", ZoneGrid: "101",
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.ZoneGrid != "" {
		t.Errorf("malformed zone grid should clear, got %q", got.ZoneGrid)
	}
}
