package main

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
)

// clipRowWidget is one Tapo-style clip card in the playback list:
// a snapshot thumbnail, a selection checkbox, and two lines of
// text (camera + time, duration + size). It is the List's row
// template — Fyne reuses these objects, so bind() re-fills every
// field (including the checkbox state) on every update.
type clipRowWidget struct {
	widget.BaseWidget
	thumb *canvas.Image
	check *widget.Check
	title *widget.Label
	sub   *widget.Label
}

func newClipRowWidget() *clipRowWidget {
	w := &clipRowWidget{
		thumb: canvas.NewImageFromImage(nil),
		check: widget.NewCheck("", nil),
		title: widget.NewLabel(""),
		sub:   widget.NewLabel(""),
	}
	w.thumb.FillMode = canvas.ImageFillContain
	w.thumb.SetMinSize(fyne.NewSize(96, 54))
	w.title.TextStyle = fyne.TextStyle{Bold: true}
	w.ExtendBaseWidget(w)
	return w
}

func (w *clipRowWidget) CreateRenderer() fyne.WidgetRenderer {
	text := container.NewVBox(w.title, w.sub)
	return widget.NewSimpleRenderer(container.NewHBox(w.thumb, w.check, text))
}

// bind fills the row for one clip. thumbImg is the decoded snapshot
// (or the placeholder when the clip has none); onSelect fires when
// the checkbox is toggled for this clip's token.
func (w *clipRowWidget) bind(c engine.Clip, thumbImg image.Image, selected, viewer bool, onSelect func(bool)) {
	w.thumb.Image = thumbImg
	w.thumb.Refresh()
	w.title.SetText(fmt.Sprintf("%s %s — %s", kindIcon(c.Kind), c.CameraName,
		c.Start.Format("Mon Jan 2 · 3:04 PM")))
	dur := ""
	if c.DurationSec > 0 {
		d := int(c.DurationSec + 0.5)
		dur = fmt.Sprintf("%02d:%02d · ", d/60, d%60)
	}
	w.sub.SetText(dur + humanBytes(c.Size))
	// Re-arm the checkbox without firing the old handler: the
	// List reuses this row for other clips.
	w.check.OnChanged = nil
	w.check.SetChecked(selected)
	if viewer {
		w.check.Hide()
	} else {
		w.check.Show()
	}
	w.check.OnChanged = onSelect
}

func kindIcon(kind string) string {
	switch kind {
	case "person":
		return "👤"
	case "motion":
		return "🏃"
	default:
		return ""
	}
}

// playbackView is the Blue-Iris-flavoured half: a clip list on the
// left (newest first, filterable by camera) and an in-app player.
type playbackView struct {
	n *nvrApp

	root      fyne.CanvasObject
	list      *widget.List
	camFilter *widget.Select
	img       *canvas.Image
	infoLbl   *widget.Label
	posLbl    *widget.Label
	playBtn   *widget.Button
	delBtn    *widget.Button
	delArm    bool
	delArmAt  time.Time
	// v1.4 multi-select delete.
	delSelBtn    *widget.Button
	delSelArm    bool
	delSelArmAt  time.Time
	delAllBtn    *widget.Button
	delAllArm    bool
	delAllArmAt  time.Time
	openFolderBtn *widget.Button
	selected     map[string]bool // clip tokens, keyed by ClipToken
	isViewer     bool
	thumbCache   map[string]image.Image

	all       []engine.Clip
	filtered  []engine.Clip
	pump      *engine.Pump
	current   *engine.Clip
	playing   bool
	seekBase  int // seconds into the clip where this pump started
	startedAt time.Time
	lastFrame int64
}

func newPlaybackView(n *nvrApp) *playbackView {
	v := &playbackView{n: n, selected: map[string]bool{}, thumbCache: map[string]image.Image{}}

	v.camFilter = widget.NewSelect([]string{"All cameras"}, func(string) { v.applyFilter() })
	v.camFilter.SetSelected("All cameras")

	v.list = widget.NewList(
		func() int { return len(v.filtered) },
		func() fyne.CanvasObject { return newClipRowWidget() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(v.filtered) {
				return
			}
			c := v.filtered[id]
			row, ok := obj.(*clipRowWidget)
			if !ok {
				return
			}
			tok := engine.ClipToken(c)
			row.bind(c, v.thumbnail(c), v.selected[tok], v.isViewer, func(checked bool) {
				if checked {
					v.selected[tok] = true
				} else {
					delete(v.selected, tok)
				}
			})
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		if id < len(v.filtered) {
			c := v.filtered[id]
			v.play(&c, 0)
		}
	}

	v.img = canvas.NewImageFromImage(nil)
	v.img.FillMode = canvas.ImageFillContain
	v.img.ScaleMode = canvas.ImageScaleSmooth
	playerBg := canvas.NewRectangle(colBlack)
	player := container.NewStack(playerBg, v.img)

	v.infoLbl = widget.NewLabel("Pick a recording on the left to play it here.")
	v.posLbl = widget.NewLabel("")
	v.playBtn = widget.NewButton("⏸ Pause", func() { v.togglePlay() })
	seekBack := widget.NewButton("⏪ 30s", func() { v.seekBy(-30) })
	seekFwd := widget.NewButton("30s ⏩", func() { v.seekBy(30) })
	openExt := widget.NewButton("Open in default player", func() {
		if v.current != nil {
			_ = openExternal(v.current.Path)
		}
	})
	v.delBtn = widget.NewButton("Delete clip", func() { v.deleteCurrentClicked() })
	v.delSelBtn = widget.NewButton("Delete selected", func() { v.deleteSelectedClicked() })
	v.delAllBtn = widget.NewButton("Delete all", func() { v.deleteAllClicked() })
	v.openFolderBtn = widget.NewButton("Open recordings folder", func() { v.openRecordingsFolder() })
	refresh := widget.NewButton("↻ Refresh list", func() { v.refreshClips() })
	controls := container.NewHBox(v.playBtn, seekBack, seekFwd, openExt, v.delBtn, v.delSelBtn, v.delAllBtn, v.posLbl)

	left := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("RECORDINGS", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			v.camFilter, refresh, v.openFolderBtn),
		nil, nil, nil, v.list)
	right := container.NewBorder(v.infoLbl, controls, nil, nil, player)
	split := container.NewHSplit(left, right)
	split.SetOffset(0.30)
	v.root = split
	v.refreshClips()
	if n.role == engine.RoleViewer {
		v.applyRole(engine.RoleViewer)
	}
	return v
}

// thumbnail returns the decoded snapshot for a clip, cached by
// path, or nil when the clip has no snapshot (the row shows an
// empty thumbnail area).
func (v *playbackView) thumbnail(c engine.Clip) image.Image {
	if c.SnapshotPath == "" {
		return nil
	}
	if img, ok := v.thumbCache[c.SnapshotPath]; ok {
		return img
	}
	f, err := os.Open(c.SnapshotPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	// Bound the cache: snapshots are ~640px JPEGs, but a long
	// list could still grow it; drop the oldest entries past 200.
	if len(v.thumbCache) > 200 {
		v.thumbCache = map[string]image.Image{}
	}
	v.thumbCache[c.SnapshotPath] = img
	return img
}

// applyRole hides clip delete for viewers (admin only) — including
// the multi-select controls and the row checkboxes.
func (v *playbackView) applyRole(role string) {
	v.isViewer = role == engine.RoleViewer
	if v.delBtn == nil {
		return
	}
	if v.isViewer {
		v.delBtn.Hide()
		v.delSelBtn.Hide()
		v.delAllBtn.Hide()
	} else {
		v.delBtn.Show()
		v.delSelBtn.Show()
		v.delAllBtn.Show()
	}
	if v.list != nil {
		v.list.Refresh()
	}
}

// confirmTwoTap is the shared inline-confirm pattern (no dialogs):
// the first tap arms the button ("Tap again to …"), the second tap
// within ~6s runs the action. Returns true when the action ran.
func (v *playbackView) confirmTwoTap(btn *widget.Button, armed *bool, armedAt *time.Time, label, confirmLabel, info string, action func()) bool {
	if !*armed || time.Since(*armedAt) > 6*time.Second {
		*armed = true
		*armedAt = time.Now()
		btn.SetText(confirmLabel)
		v.infoLbl.SetText(info)
		time.AfterFunc(6*time.Second, func() {
			fyne.Do(func() {
				if *armed {
					*armed = false
					btn.SetText(label)
				}
			})
		})
		return false
	}
	*armed = false
	btn.SetText(label)
	action()
	return true
}

// deleteCurrentClicked deletes the playing clip from disk.
// First tap arms the button ("Tap again to delete"), second tap
// deletes — inline confirmation, no dialog.
func (v *playbackView) deleteCurrentClicked() {
	if v.current == nil {
		return
	}
	v.confirmTwoTap(v.delBtn, &v.delArm, &v.delArmAt, "Delete clip", "Tap again to delete",
		"Delete this clip? The file is removed from the PC. Tap Delete again within a few seconds to confirm.",
		func() {
			token := engine.ClipToken(*v.current)
			v.stopPump()
			v.current = nil
			if v.deleteTokens([]string{token}) == 1 {
				v.infoLbl.SetText("✓ Clip deleted.")
			} else {
				v.infoLbl.SetText("Could not delete that clip — it may already be gone.")
			}
			v.playBtn.SetText("▶ Play")
		})
}

// deleteSelectedClicked deletes every checked clip.
func (v *playbackView) deleteSelectedClicked() {
	if len(v.selected) == 0 {
		v.infoLbl.SetText("No clips selected — tick the boxes on the clips to delete.")
		return
	}
	n := len(v.selected)
	v.confirmTwoTap(v.delSelBtn, &v.delSelArm, &v.delSelArmAt, "Delete selected", "Tap again to delete",
		fmt.Sprintf("Delete %d selected clip(s)? The files are removed from the PC. Tap Delete again within a few seconds to confirm.", n),
		func() {
			tokens := make([]string, 0, len(v.selected))
			for tok := range v.selected {
				tokens = append(tokens, tok)
			}
			sort.Strings(tokens)
			deleted := v.deleteTokens(tokens)
			v.selected = map[string]bool{}
			v.infoLbl.SetText(fmt.Sprintf("✓ Deleted %d clip(s).", deleted))
		})
}

// deleteAllClicked deletes every clip in the library.
func (v *playbackView) deleteAllClicked() {
	if len(v.all) == 0 {
		v.infoLbl.SetText("No clips to delete.")
		return
	}
	n := len(v.all)
	v.confirmTwoTap(v.delAllBtn, &v.delAllArm, &v.delAllArmAt, "Delete all", "Tap again to delete all",
		fmt.Sprintf("Delete ALL %d clips? Every recording file is removed from the PC. Tap Delete again within a few seconds to confirm.", n),
		func() {
			tokens := make([]string, 0, len(v.all))
			for _, c := range v.all {
				tokens = append(tokens, engine.ClipToken(c))
			}
			deleted := v.deleteTokens(tokens)
			v.selected = map[string]bool{}
			v.infoLbl.SetText(fmt.Sprintf("✓ Deleted %d clip(s).", deleted))
		})
}

// deleteTokens removes clips (file + snapshot) by token, clears the
// relay upload dedup for each, and refreshes the list. It returns
// how many were actually deleted. If the playing clip is among
// them, playback stops.
func (v *playbackView) deleteTokens(tokens []string) int {
	deleted := 0
	for _, token := range tokens {
		if v.current != nil && engine.ClipToken(*v.current) == token {
			v.stopPump()
			v.current = nil
			v.playBtn.SetText("▶ Play")
		}
		if engine.DeleteClipFile(v.n.settings.RecordingsDir, v.n.settings.Cameras, token) {
			deleted++
		}
		if v.n.remote != nil {
			v.n.remote.DropUpload(token)
		}
		delete(v.selected, token)
	}
	v.refreshClips()
	return deleted
}

// openRecordingsFolder opens the recordings directory in the file
// manager. On failure the path is shown in the info label.
func (v *playbackView) openRecordingsFolder() {
	dir := v.n.settings.RecordingsDir
	if err := openExternal(dir); err != nil {
		v.infoLbl.SetText("Recordings folder: " + dir)
		return
	}
	v.infoLbl.SetText("Opened recordings folder: " + dir)
}

func (v *playbackView) refreshClips() {
	v.all = engine.ScanLibrary(v.n.settings.RecordingsDir, v.n.settings.Cameras)
	// Refresh the camera filter options.
	opts := []string{"All cameras"}
	seen := map[string]bool{}
	for _, c := range v.n.settings.Cameras {
		if !seen[c.Name] {
			opts = append(opts, c.Name)
			seen[c.Name] = true
		}
	}
	cur := v.camFilter.Selected
	doUI(func() {
		v.camFilter.Options = opts
		if cur != "" {
			v.camFilter.SetSelected(cur)
		}
		v.camFilter.Refresh()
	})
	v.applyFilter()
}

func (v *playbackView) applyFilter() {
	sel := "All cameras"
	if v.camFilter != nil && v.camFilter.Selected != "" {
		sel = v.camFilter.Selected
	}
	v.filtered = v.filtered[:0]
	for _, c := range v.all {
		if sel == "All cameras" || c.CameraName == sel {
			v.filtered = append(v.filtered, c)
		}
	}
	doUI(func() {
		if v.list != nil {
			v.list.Refresh()
		}
	})
}

func (v *playbackView) play(c *engine.Clip, seekSec int) {
	v.stopPump()
	cp := *c
	v.current = &cp
	v.seekBase = seekSec
	v.startedAt = time.Now()
	v.lastFrame = 0
	v.pump = engine.StartPump(v.n.ffmpeg, engine.PumpOptions{
		Input:   c.Path,
		IsFile:  true,
		SeekSec: seekSec,
		Width:   960,
		FPS:     15,
	})
	v.playing = true
	fyne.Do(func() {
		v.infoLbl.SetText(fmt.Sprintf("%s — %s",
			c.CameraName, c.Start.Format("Monday Jan 2, 3:04:05 PM")))
		v.playBtn.SetText("⏸ Pause")
	})
}

func (v *playbackView) stopPump() {
	if v.pump != nil {
		v.pump.Stop()
		v.pump = nil
	}
	v.playing = false
}

func (v *playbackView) position() int {
	if v.current == nil {
		return 0
	}
	pos := v.seekBase
	if v.playing {
		pos += int(time.Since(v.startedAt).Seconds())
	}
	return pos
}

func (v *playbackView) togglePlay() {
	if v.current == nil {
		return
	}
	if v.playing {
		pos := v.position()
		v.stopPump()
		v.seekBase = pos
		fyne.Do(func() { v.playBtn.SetText("▶ Play") })
	} else {
		v.play(v.current, v.seekBase)
	}
}

func (v *playbackView) seekBy(delta int) {
	if v.current == nil {
		return
	}
	pos := v.position() + delta
	if pos < 0 {
		pos = 0
	}
	wasPlaying := v.playing
	v.play(v.current, pos)
	if !wasPlaying {
		// play() starts it; freeze on the seek frame instead.
		v.stopPump()
		v.seekBase = pos
		fyne.Do(func() { v.playBtn.SetText("▶ Play") })
	}
}

func (v *playbackView) tick() {
	if v.pump == nil || v.current == nil {
		return
	}
	fr := v.pump.Frames()
	fyne.Do(func() {
		if v.pump != nil && fr != v.lastFrame {
			v.lastFrame = fr
			if img := v.pump.Latest(); img != nil {
				v.img.Image = img
				v.img.Refresh()
			}
		}
		pos := v.position()
		v.posLbl.SetText(fmt.Sprintf("%02d:%02d", pos/60, pos%60))
		if v.pump != nil && v.pump.Stopped() && v.playing {
			v.playing = false
			v.playBtn.SetText("▶ Play")
			v.posLbl.SetText("End of clip")
		}
	})
}
