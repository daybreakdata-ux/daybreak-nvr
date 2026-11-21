package main

import (
	"bytes"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
)

// devicesView is the Devices & Settings tab, rebuilt in v1.3 as a
// professional multi-page settings area: a left nav lists the
// pages and one page is shown at a time — Cameras, Motion &
// Recording, General, Users, Storage. The phone-viewing and
// remote-viewing sections are gone from settings (v1.3): both
// features keep running with their stored settings (the LAN web
// server and the relay push start from the config, not from this
// UI), they just have no toggles here any more.
//
// Since v1.7 this tab uses NO modal dialogs or popups anywhere:
// Fyne's popup layer doesn't paint on some PCs (a white overlay
// with nothing in it — seen on a RealVNC/softpipe machine), which
// stranded the old Add/Edit camera dialog. Instead the tab swaps
// between pages — the settings pages and an inline camera editor
// page — and every confirmation or status message is plain inline
// text in the relevant page.
type devicesView struct {
	n *nvrApp

	root fyne.CanvasObject // page container; exactly one page object at a time
	nav  *widget.List
	// pageHost holds the currently shown settings page inside the
	// nav layout; home is the nav+pageHost split.
	home     fyne.CanvasObject
	pageHost *fyne.Container
	pages    map[string]fyne.CanvasObject
	page     string

	// Cameras page.
	list        *widget.List
	camListBox  *cappedList // sizes the camera list (see below)
	selected    int
	camStatus   *widget.Label
	addBtn      *widget.Button
	editBtn     *widget.Button
	delBtn      *widget.Button
	delArmIdx   int // list index armed for delete-confirm, -1 = none
	delArmAt    time.Time

	// Motion & Recording page (per selected camera).
	motionCamSel  *widget.Select
	motionCamIdx  int
	motionModeSel *widget.Select
	sensSlider    *widget.Slider
	sensLbl       *widget.Label
	zoneGrid      string // working copy for the selected camera
	zoneBtns      []*widget.Button
	zoneCountLbl  *widget.Label
	// Zone picker (v1.6): the camera's picture with the 16x9
	// grid overlaid on it. zonePlainBox holds the old plain
	// button grid, shown only when no camera picture is
	// available (camera unreachable) so the page is never
	// blocked. zoneImgNote explains which is showing and why.
	zonePicker    *zonePicker
	zonePlainBox  *fyne.Container
	zoneRefreshBtn *widget.Button
	zoneImgNote   *widget.Label
	aiForceLbl    *widget.Label
	preEntry      *widget.Entry
	postEntry     *widget.Entry
	aiCheck       *widget.Check
	aiConfSlider  *widget.Slider
	aiConfLbl     *widget.Label
	personNowLbl  *widget.Label
	personEvLbl   *widget.Label
	motionStatus  *widget.Label
	motionSaveBtn *widget.Button

	// General page.
	layoutSel   *widget.Select
	fpsEntry    *widget.Entry
	bootCheck   *widget.Check
	genStatus   *widget.Label
	genSaveBtn  *widget.Button

	// Storage page.
	dirEntry       *widget.Entry
	segEntry       *widget.Entry
	daysEntry      *widget.Entry
	maxGBEntry     *widget.Entry
	storageStatus  *widget.Label
	storageSaveBtn *widget.Button
	usageLbl       *widget.Label
	reaperLbl      *widget.Label

	// Users page (v1.2): the accounts that sign in to this app,
	// the LAN web UI and (via the relay) remote viewing.
	userList      *widget.List
	userListBox   *cappedList // sizes the user list (see below)
	users         []engine.User
	selectedUser  int
	userName      *widget.Entry
	userPass      *widget.Entry
	userRole      *widget.Select
	userStatus    *widget.Label
	userSaveBtn   *widget.Button
	userDelBtn    *widget.Button
	userDelArmIdx int
	userDelArmAt  time.Time
}

var settingsPages = []string{"Cameras", "Motion & Recording", "General", "Users", "Storage"}

// maxVisibleListRows is how many rows a settings list shows in full
// before it starts scrolling internally (v1.3.1).
const maxVisibleListRows = 10

// cappedList wraps a widget.List and sizes it to show up to
// maxVisibleListRows rows; beyond that the list's own internal
// scroller takes over.
//
// Why this exists: a widget.List placed directly in a VBox gets its
// MinSize as its height, and a List's MinSize is a single row — so
// in v1.3 the camera list (and the user list) showed one entry at a
// time with a barely-visible scrollbar. The wrapper's MinSize is the
// height of rows*rowHeight + (rows-1)*separator, matching how the
// list lays its items out, and Refresh() recomputes it from the
// live item count whenever the list changes.
type cappedList struct {
	widget.BaseWidget
	list  *widget.List
	count func() int
}

func newCappedList(list *widget.List, count func() int) *cappedList {
	c := &cappedList{list: list, count: count}
	c.ExtendBaseWidget(c)
	return c
}

// rowHeight is the height of one list row: the List's MinSize height
// is a single row (its item template's height).
func (c *cappedList) rowHeight() float32 {
	return c.list.MinSize().Height
}

// separator is the gap Fyne puts between list rows (theme padding).
func (c *cappedList) separator() float32 {
	return c.list.Theme().Size(theme.SizeNamePadding)
}

// MinSize returns the size for showing up to maxVisibleListRows rows
// in full: rows*rowHeight + (rows-1)*separator. At least one row
// stays visible so the list never collapses to nothing when empty.
func (c *cappedList) MinSize() fyne.Size {
	rows := c.count()
	if rows > maxVisibleListRows {
		rows = maxVisibleListRows
	}
	if rows < 1 {
		rows = 1
	}
	w := c.list.MinSize().Width
	h := float32(rows)*c.rowHeight() + float32(rows-1)*c.separator()
	return fyne.NewSize(w, h)
}

func (c *cappedList) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.list)
}

// refreshCamList refreshes the camera list and re-sizes its box to
// the current camera count (up to maxVisibleListRows rows).
func (v *devicesView) refreshCamList() {
	if v.list != nil {
		v.list.Refresh()
	}
	if v.camListBox != nil {
		v.camListBox.Refresh()
	}
}

// refreshUserList refreshes the user list and re-sizes its box to
// the current user count (up to maxVisibleListRows rows).
func (v *devicesView) refreshUserList() {
	if v.userList != nil {
		v.userList.Refresh()
	}
	if v.userListBox != nil {
		v.userListBox.Refresh()
	}
}

func newDevicesView(n *nvrApp) *devicesView {
	v := &devicesView{n: n, selected: -1, delArmIdx: -1, selectedUser: -1, userDelArmIdx: -1, motionCamIdx: -1}
	v.pages = map[string]fyne.CanvasObject{}

	v.nav = widget.NewList(
		func() int { return len(settingsPages) },
		func() fyne.CanvasObject { return widget.NewLabel("page") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			obj.(*widget.Label).SetText(settingsPages[id])
		},
	)
	v.nav.OnSelected = func(id widget.ListItemID) { v.showPage(settingsPages[id]) }

	v.pages["Cameras"] = v.buildCamerasPage()
	v.pages["Motion & Recording"] = v.buildMotionPage()
	v.pages["General"] = v.buildGeneralPage()
	v.pages["Users"] = v.buildUsersPage()
	v.pages["Storage"] = v.buildStoragePage()

	v.pageHost = container.NewMax(v.pages["Cameras"])
	v.page = "Cameras"
	v.home = container.NewHSplit(v.nav, v.pageHost)
	v.home.(*container.Split).SetOffset(0.22)
	v.root = container.NewMax(v.home)
	if n.role == engine.RoleViewer {
		v.applyRole(engine.RoleViewer)
	}
	return v
}

// showPage swaps the settings area to one page.
func (v *devicesView) showPage(name string) {
	page, ok := v.pages[name]
	if !ok {
		return
	}
	v.page = name
	if v.pageHost != nil {
		v.pageHost.Objects = []fyne.CanvasObject{page}
		v.pageHost.Refresh()
	}
	switch name {
	case "Motion & Recording":
		v.refreshMotionPage()
	case "Storage":
		v.refreshStoragePage()
	}
	// Keep the nav selection in sync when showPage is called
	// from code (e.g. after saving a camera from the editor).
	for i, p := range settingsPages {
		if p == name {
			v.nav.Select(i)
			break
		}
	}
}

// showHome returns the tab to the settings area (nav + the page
// that was showing, which is the Cameras page after the editor).
func (v *devicesView) showHome() {
	if c, ok := v.root.(*fyne.Container); ok {
		c.Objects = []fyne.CanvasObject{v.home}
		c.Refresh()
	}
}

// ---------- Cameras page ----------

func (v *devicesView) buildCamerasPage() fyne.CanvasObject {
	n := v.n
	v.list = widget.NewList(
		func() int { return len(n.settings.Cameras) },
		func() fyne.CanvasObject { return widget.NewLabel("camera") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(n.settings.Cameras) {
				return
			}
			c := n.settings.Cameras[id]
			detail := c.IP
			if c.MainURL != "" {
				detail = c.MainURL
			}
			state := "enabled"
			if !c.Enabled {
				state = "disabled"
			}
			rec := "preview only"
			if c.Mode() == engine.RecordOff {
				rec = "do not record"
			} else if c.Record && c.Mode() == engine.RecordMotion {
				rec = "motion recording"
			} else if c.Record && c.Mode() == engine.RecordPerson {
				rec = "person only"
			} else if c.Record {
				rec = "recording"
			}
			obj.(*widget.Label).SetText(fmt.Sprintf("%s — %s · %s · %s",
				c.Name, detail, state, rec))
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) { v.selected = id; v.disarmDelete() }
	v.list.OnUnselected = func(widget.ListItemID) { v.selected = -1; v.disarmDelete() }
	v.camListBox = newCappedList(v.list, func() int { return len(n.settings.Cameras) })

	v.camStatus = widget.NewLabel("")
	v.camStatus.Wrapping = fyne.TextWrapWord
	v.addBtn = widget.NewButton("＋ Add camera", func() { v.showEditor(nil) })
	v.editBtn = widget.NewButton("Edit", func() {
		if v.selected >= 0 && v.selected < len(n.settings.Cameras) {
			c := n.settings.Cameras[v.selected]
			v.showEditor(&c)
		}
	})
	// Delete confirms inline: the first click arms the button
	// ("Tap again to delete") for a few seconds; the second click
	// on the same row actually deletes. No confirmation dialog —
	// dialogs don't paint on every PC (see the type comment).
	v.delBtn = widget.NewButton("Delete", func() { v.deleteClicked() })
	countLbl := widget.NewLabel(fmt.Sprintf("Up to %d cameras — the list updates after every change.", engine.MaxCameras))
	note := widget.NewLabel("Record mode, motion sensitivity, detection zones and person detection for each camera live on the Motion & Recording page.")
	note.Wrapping = fyne.TextWrapWord
	return container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Cameras", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(v.addBtn, v.editBtn, v.delBtn),
		countLbl,
		v.camStatus,
		v.camListBox,
		note,
	))
}

// disarmDelete cancels a pending inline delete confirmation.
func (v *devicesView) disarmDelete() {
	v.delArmIdx = -1
	if v.delBtn != nil {
		v.delBtn.SetText("Delete")
	}
}

// deleteClicked implements the two-tap inline delete: first tap
// arms (button changes to "Tap again to delete", valid for a few
// seconds, for the currently selected row only), second tap
// deletes. Replaces the old confirmation dialog.
func (v *devicesView) deleteClicked() {
	n := v.n
	if v.selected < 0 || v.selected >= len(n.settings.Cameras) {
		return
	}
	idx := v.selected
	if v.delArmIdx != idx || time.Since(v.delArmAt) > 6*time.Second {
		v.delArmIdx = idx
		v.delArmAt = time.Now()
		c := n.settings.Cameras[idx]
		v.delBtn.SetText("Tap again to delete")
		v.camStatus.SetText(fmt.Sprintf("Delete %q? Its existing recordings stay on disk. Tap Delete again within a few seconds to confirm.", c.Name))
		time.AfterFunc(6*time.Second, func() {
			fyne.Do(func() {
				if v.delArmIdx == idx {
					v.disarmDelete()
					v.camStatus.SetText("")
				}
			})
		})
		return
	}
	c := n.settings.Cameras[idx]
	s := n.settings
	s.Cameras = append(s.Cameras[:idx], s.Cameras[idx+1:]...)
	v.disarmDelete()
	if err := s.Save(); err != nil {
		v.camStatus.SetText("Could not save after deleting: " + err.Error())
		return
	}
	v.selected = -1
	n.reloadSettings()
	v.refreshCamList()
	v.refreshMotionPage()
	v.camStatus.SetText(fmt.Sprintf("Deleted camera %q. Its recordings stay on disk.", c.Name))
}

// ---------- Motion & Recording page ----------

var recordModeOptions = []string{"Continuous", "Motion only", "Person only", "Do not record"}

// recordModeForLabel maps a Motion-page dropdown selection onto
// the stored record mode; anything unrecognised is Continuous.
func recordModeForLabel(label string) string {
	switch label {
	case "Motion only":
		return engine.RecordMotion
	case "Person only":
		return engine.RecordPerson
	case "Do not record":
		return engine.RecordOff
	default:
		return engine.RecordContinuous
	}
}

// recordModeLabel is the inverse: the dropdown selection for a
// stored mode.
func recordModeLabel(mode string) string {
	switch mode {
	case engine.RecordMotion:
		return recordModeOptions[1]
	case engine.RecordPerson:
		return recordModeOptions[2]
	case engine.RecordOff:
		return recordModeOptions[3]
	default:
		return recordModeOptions[0]
	}
}

func (v *devicesView) buildMotionPage() fyne.CanvasObject {
	v.motionCamSel = widget.NewSelect([]string{}, func(name string) {
		for i, c := range v.n.settings.Cameras {
			if c.Name == name {
				v.motionCamIdx = i
				v.loadMotionCamera()
				return
			}
		}
	})
	v.motionModeSel = widget.NewSelect(recordModeOptions, func(string) { v.refreshAIModeNote() })
	v.motionModeSel.SetSelected(recordModeOptions[0])

	v.sensSlider = widget.NewSlider(0, 100)
	v.sensSlider.Value = engine.SensitivityDefault
	v.sensLbl = widget.NewLabel("")
	v.sensSlider.OnChanged = func(f float64) { v.updateSensLbl(int(f + 0.5)) }
	v.updateSensLbl(engine.SensitivityDefault)

	// Detection zone editor (v1.6): the camera's picture with the
	// 16x9 grid overlaid on it — tap a cell on the picture to
	// turn it on or off. The plain button grid is kept as a
	// fallback for when the camera picture cannot be fetched
	// (camera unreachable): the page is never blocked by a
	// missing picture.
	v.zoneGrid = engine.DefaultZoneGrid()
	v.zonePicker = newZonePicker(v.zoneGrid, func(grid string) { v.setZoneGrid(grid) })
	v.zoneBtns = make([]*widget.Button, engine.ZoneCells)
	cells := make([]fyne.CanvasObject, 0, engine.ZoneCells)
	for i := 0; i < engine.ZoneCells; i++ {
		idx := i
		b := widget.NewButton("■", func() { v.toggleZoneCell(idx) })
		v.zoneBtns[i] = b
		cells = append(cells, b)
	}
	v.zonePlainBox = container.NewVBox(container.NewGridWithColumns(engine.ZoneCols, cells...))
	v.zoneImgNote = widget.NewLabel("")
	v.zoneImgNote.Wrapping = fyne.TextWrapWord
	v.zoneRefreshBtn = widget.NewButton("Refresh image", func() { v.refreshZoneImage() })
	v.zoneCountLbl = widget.NewLabel("")
	zoneAllBtn := widget.NewButton("Enable whole frame", func() {
		v.setZoneGrid(engine.DefaultZoneGrid())
	})
	zoneClearBtn := widget.NewButton("Clear all", func() {
		b := make([]byte, engine.ZoneCells)
		for i := range b {
			b[i] = '0'
		}
		v.setZoneGrid(string(b))
	})

	v.preEntry = widget.NewEntry()
	v.preEntry.SetText("15")
	v.postEntry = widget.NewEntry()
	v.postEntry.SetText("20")

	v.aiCheck = widget.NewCheck("Human detection (on-device AI)", nil)
	v.aiConfSlider = widget.NewSlider(0, 100)
	v.aiConfSlider.Value = 50
	v.aiConfLbl = widget.NewLabel("")
	v.aiConfSlider.OnChanged = func(f float64) { v.updateAIConfLbl(int(f + 0.5)) }
	v.updateAIConfLbl(50)

	v.personNowLbl = widget.NewLabel("")
	v.personNowLbl.Wrapping = fyne.TextWrapWord
	v.personEvLbl = widget.NewLabel("")
	v.personEvLbl.Wrapping = fyne.TextWrapWord

	v.motionStatus = widget.NewLabel("")
	v.motionStatus.Wrapping = fyne.TextWrapWord
	v.motionSaveBtn = widget.NewButton("Save motion settings", func() { v.saveMotion() })

	aiNote := widget.NewLabel("Human detection runs entirely on this PC (YOLOv8n, bundled with the app). No cloud, no account, no API key — and no video ever leaves the house for detection. It samples the camera's preview frames about once a second while something is moving, and about every 10 seconds otherwise. A person seen inside the detection zone starts/keeps recording in Motion mode and appears in the events list with a tracking ID. In Person-only mode it is the recording trigger: only a person starts a recording.")
	aiNote.Wrapping = fyne.TextWrapWord
	v.aiForceLbl = widget.NewLabel("")
	v.aiForceLbl.Wrapping = fyne.TextWrapWord
	zoneNote := widget.NewLabel("Detection zone: motion and people only count inside the highlighted cells. Tap a cell on the camera picture to turn it on or off — for example, turn off the cells covering a road or a tree that sways, so it stops recording passing cars. With the whole frame on, everything counts (the old behaviour).")
	zoneNote.Wrapping = fyne.TextWrapWord
	bufferNote := widget.NewLabel("Pre/post-event buffer: when motion (or a person) is detected, the recording keeps this many seconds before the event starts and after it ends, so you see what led up to it and what happened next.")
	bufferNote.Wrapping = fyne.TextWrapWord
	sensNote := widget.NewLabel("Sensitivity: higher means a smaller change triggers recording. 25 ≈ the old Low, 50 ≈ Medium, 75 ≈ High.")
	sensNote.Wrapping = fyne.TextWrapWord

	return container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Motion & Recording", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Camera", v.motionCamSel),
			widget.NewFormItem("Record mode", v.motionModeSel),
		),
		widget.NewLabelWithStyle("Motion sensitivity", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.sensSlider, v.sensLbl, sensNote,
		widget.NewLabelWithStyle("Detection zone", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.zonePicker,
		v.zonePlainBox,
		v.zoneImgNote,
		container.NewHBox(zoneAllBtn, zoneClearBtn, v.zoneRefreshBtn),
		v.zoneCountLbl, zoneNote,
		widget.NewLabelWithStyle("Pre/post-event buffer", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Keep before each event (seconds)", v.preEntry),
			widget.NewFormItem("Keep after each event (seconds)", v.postEntry),
		),
		bufferNote,
		widget.NewLabelWithStyle("Human detection", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.aiCheck,
		v.aiConfSlider, v.aiConfLbl,
		v.aiForceLbl,
		aiNote,
		v.personNowLbl,
		v.personEvLbl,
		v.motionSaveBtn, v.motionStatus,
	))
}

// updateSensLbl renders the sensitivity value + its old-scale hint.
func (v *devicesView) updateSensLbl(val int) {
	hint := ""
	switch {
	case val <= 25:
		hint = " (≈ old Low)"
	case val <= 50:
		hint = " (≈ old Medium)"
	case val <= 75:
		hint = " (≈ old High)"
	default:
		hint = " (more sensitive than old High)"
	}
	v.sensLbl.SetText(fmt.Sprintf("Sensitivity: %d%s", val, hint))
}

// updateAIConfLbl renders the AI confidence threshold.
func (v *devicesView) updateAIConfLbl(val int) {
	v.aiConfLbl.SetText(fmt.Sprintf("Confidence threshold: %d%% — only detections this sure count as a person.", val))
}

// setZoneGrid is the single place the working zone grid is
// changed: it updates the plain-grid buttons, the overlaid
// picker and the count label together, so the two editors can
// never disagree.
func (v *devicesView) setZoneGrid(grid string) {
	if len(grid) != engine.ZoneCells {
		grid = engine.DefaultZoneGrid()
	}
	v.zoneGrid = grid
	v.refreshZoneBtns()
	if v.zonePicker != nil {
		v.zonePicker.SetGrid(grid)
	}
}

// toggleZoneCell flips one zone cell and refreshes both editors.
func (v *devicesView) toggleZoneCell(idx int) {
	if idx < 0 || idx >= len(v.zoneGrid) {
		return
	}
	b := []byte(v.zoneGrid)
	if b[idx] == '1' {
		b[idx] = '0'
	} else {
		b[idx] = '1'
	}
	v.setZoneGrid(string(b))
}

// refreshZoneBtns paints the plain toggle grid from v.zoneGrid
// (the fallback editor) and updates the count label. It also
// keeps the overlaid picker in sync.
func (v *devicesView) refreshZoneBtns() {
	if len(v.zoneGrid) != engine.ZoneCells {
		v.zoneGrid = engine.DefaultZoneGrid()
	}
	on := 0
	for i, b := range v.zoneBtns {
		if v.zoneGrid[i] == '1' {
			b.SetText("■")
			on++
		} else {
			b.SetText("·")
		}
	}
	v.zoneCountLbl.SetText(fmt.Sprintf("%d of %d zone cells enabled (%.0f%% of the frame).", on, engine.ZoneCells, 100*float64(on)/float64(engine.ZoneCells)))
	if v.zonePicker != nil {
		v.zonePicker.SetGrid(v.zoneGrid)
	}
}

// refreshZoneImage (re)loads the zone picker's picture for the
// selected camera. The live pump's latest frame is free and
// instant, so it is tried first; otherwise a fresh still is
// grabbed from the camera in the background (up to ~10 s) and the
// plain grid stays up meanwhile. If the camera cannot be reached
// at all, the plain grid stays up with a note — the page is never
// blocked by a missing picture.
func (v *devicesView) refreshZoneImage() {
	n := v.n
	if v.motionCamIdx < 0 || v.motionCamIdx >= len(n.settings.Cameras) {
		return
	}
	c := n.settings.Cameras[v.motionCamIdx]
	if p := n.pumpFor(c.ID); p != nil {
		if img := p.Latest(); img != nil {
			v.showZonePicker(img)
			return
		}
	}
	v.zoneImgNote.SetText("Getting a fresh picture from the camera…")
	go func() {
		jpeg, err := engine.GrabStillJPEG(n.ffmpeg, c.SubStreamURL())
		if err != nil {
			fyne.Do(func() {
				v.showZonePlain("Camera picture unavailable (" + err.Error() + ") — using the plain grid below.")
			})
			return
		}
		img, _, err := image.Decode(bytes.NewReader(jpeg))
		if err != nil {
			fyne.Do(func() {
				v.showZonePlain("Camera picture could not be read — using the plain grid below.")
			})
			return
		}
		fyne.Do(func() { v.showZonePicker(img) })
	}()
}

// showZonePicker swaps the zone editor to the overlaid picker
// with the given picture.
func (v *devicesView) showZonePicker(img image.Image) {
	if v.zonePicker == nil {
		return
	}
	v.zonePicker.SetImage(img)
	v.zonePicker.SetGrid(v.zoneGrid)
	v.zonePicker.Show()
	if v.zonePlainBox != nil {
		v.zonePlainBox.Hide()
	}
	v.zoneImgNote.SetText("Tap a cell on the picture to turn it on or off.")
}

// showZonePlain swaps the zone editor to the plain button grid
// (camera picture unavailable) with an explanatory note.
func (v *devicesView) showZonePlain(note string) {
	if v.zonePicker != nil {
		v.zonePicker.Hide()
	}
	if v.zonePlainBox != nil {
		v.zonePlainBox.Show()
	}
	v.zoneImgNote.SetText(note)
}

// refreshAIModeNote forces the human-detection checkbox on when
// Person-only is selected (that mode cannot record without it)
// and explains why. Saving always forces it on too (BuildCamera),
// so the checkbox can never disagree with the saved camera.
func (v *devicesView) refreshAIModeNote() {
	if v.motionModeSel == nil || v.aiCheck == nil || v.aiForceLbl == nil {
		return
	}
	if v.motionModeSel.Selected == "Person only" {
		v.aiCheck.SetChecked(true)
		v.aiForceLbl.SetText("Person-only recording needs human detection, so it is on for this camera.")
	} else {
		v.aiForceLbl.SetText("")
	}
}

// refreshMotionPage rebuilds the camera selector and loads the
// selected (or first) camera into the form.
func (v *devicesView) refreshMotionPage() {
	if v.motionCamSel == nil {
		return
	}
	names := make([]string, 0, len(v.n.settings.Cameras))
	for _, c := range v.n.settings.Cameras {
		names = append(names, c.Name)
	}
	v.motionCamSel.Options = names
	v.motionCamSel.Refresh()
	if len(names) == 0 {
		v.motionCamIdx = -1
		v.motionCamSel.SetSelected("")
		v.motionStatus.SetText("Add a camera first (Cameras page).")
		return
	}
	if v.motionCamIdx < 0 || v.motionCamIdx >= len(names) {
		v.motionCamIdx = 0
	}
	v.motionCamSel.SetSelected(names[v.motionCamIdx])
	v.loadMotionCamera()
}

// loadMotionCamera fills the Motion form from the selected camera.
func (v *devicesView) loadMotionCamera() {
	n := v.n
	if v.motionCamIdx < 0 || v.motionCamIdx >= len(n.settings.Cameras) {
		return
	}
	c := n.settings.Cameras[v.motionCamIdx]
	v.motionModeSel.SetSelected(recordModeLabel(c.Mode()))
	v.sensSlider.SetValue(float64(c.Sensitivity))
	v.updateSensLbl(c.Sensitivity)
	if len(c.ZoneGrid) == engine.ZoneCells {
		v.zoneGrid = c.ZoneGrid
	} else {
		v.zoneGrid = engine.DefaultZoneGrid()
	}
	v.setZoneGrid(v.zoneGrid)
	// Zone picture: the camera's latest preview frame, or a
	// fresh still grab — the picker shows the picture with the
	// grid on it, or the plain grid when the camera is
	// unreachable.
	v.refreshZoneImage()
	v.preEntry.SetText(strconv.Itoa(n.settings.PreBufferSeconds))
	v.postEntry.SetText(strconv.Itoa(n.settings.PostBufferSeconds))
	v.aiCheck.SetChecked(c.HumanDetect)
	v.refreshAIModeNote()
	v.aiConfSlider.SetValue(float64(c.HumanConfidence))
	v.updateAIConfLbl(c.HumanConfidence)
	v.refreshPersonLabels(c)
	v.motionStatus.SetText("")
}

// refreshPersonLabels shows live tracks + recent person events
// for the selected camera.
func (v *devicesView) refreshPersonLabels(c engine.Camera) {
	if v.n.person != nil && v.n.person.Enabled() {
		tracks := v.n.person.ActiveTracks(c.ID)
		if len(tracks) > 0 {
			v.personNowLbl.SetText(fmt.Sprintf("People now: %d tracked on this camera.", len(tracks)))
		} else {
			v.personNowLbl.SetText("People now: none tracked on this camera.")
		}
	} else {
		v.personNowLbl.SetText("Human detection is unavailable — the bundled AI model or runtime was not found next to the app.")
	}
	evs := v.n.tracker.Events(c.ID)
	var persons []engine.MotionEvent
	for i := len(evs) - 1; i >= 0 && len(persons) < 5; i-- {
		if evs[i].Kind == "person" {
			persons = append(persons, evs[i])
		}
	}
	if len(persons) == 0 {
		v.personEvLbl.SetText("Recent person events: none yet.")
		return
	}
	var lines []string
	for _, e := range persons {
		end := "still going"
		if !e.End.IsZero() {
			end = e.End.Format("3:04 PM")
		}
		lines = append(lines, fmt.Sprintf("Person #%d — %s to %s (%.0f%% sure)",
			e.TrackID, e.Start.Format("Jan 2, 3:04 PM"), end, e.Confidence*100))
	}
	v.personEvLbl.SetText("Recent person events:\n" + strings.Join(lines, "\n"))
}

// saveMotion writes the Motion form back to the selected camera
// and the global pre/post buffers.
func (v *devicesView) saveMotion() {
	n := v.n
	if v.motionCamIdx < 0 || v.motionCamIdx >= len(n.settings.Cameras) {
		v.motionStatus.SetText("No camera selected.")
		return
	}
	c := n.settings.Cameras[v.motionCamIdx]
	in := engine.EditorInputFromCamera(c)
	in.RecordMode = recordModeForLabel(v.motionModeSel.Selected)
	in.Sensitivity = int(v.sensSlider.Value + 0.5)
	in.ZoneGrid = v.zoneGrid
	in.HumanDetect = v.aiCheck.Checked
	in.HumanConfidence = int(v.aiConfSlider.Value + 0.5)
	saved, err := engine.BuildCamera(c, in)
	if err != nil {
		v.motionStatus.SetText("Can't save yet: " + err.Error())
		return
	}
	s := n.settings
	s.Cameras[v.motionCamIdx] = saved
	if n, err := strconv.Atoi(strings.TrimSpace(v.preEntry.Text)); err == nil && n >= 0 && n <= 120 {
		s.PreBufferSeconds = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v.postEntry.Text)); err == nil && n >= 0 && n <= 120 {
		s.PostBufferSeconds = n
	}
	if err := s.Save(); err != nil {
		v.motionStatus.SetText("Could not save: " + err.Error())
		return
	}
	n.settings = s
	n.reloadSettings()
	v.refreshPersonLabels(saved)
	v.motionStatus.SetText("✓ Saved — motion, zone and human-detection settings applied.")
}

// ---------- General page ----------

var layoutOptions = []string{"1 camera", "4 cameras (2×2)", "9 cameras (3×3)", "16 cameras (4×4)"}

func (v *devicesView) buildGeneralPage() fyne.CanvasObject {
	s := v.n.settings
	v.layoutSel = widget.NewSelect(layoutOptions, nil)
	switch s.GridLayout {
	case 1:
		v.layoutSel.SetSelected(layoutOptions[0])
	case 9:
		v.layoutSel.SetSelected(layoutOptions[2])
	case 16:
		v.layoutSel.SetSelected(layoutOptions[3])
	default:
		v.layoutSel.SetSelected(layoutOptions[1])
	}
	v.fpsEntry = widget.NewEntry()
	v.fpsEntry.SetText(strconv.Itoa(s.PreviewFPS))
	// v1.4: start at boot is an XDG autostart entry for this
	// variant's GUI app — not the systemd service (which would
	// fight the GUI single-instance lock). The checkbox reflects
	// the entry's current existence when the page opens. The
	// handler is attached after the initial SetChecked so opening
	// the page does not rewrite the entry.
	v.bootCheck = widget.NewCheck("Start at boot", nil)
	v.bootCheck.SetChecked(engine.AutostartEnabled())
	v.bootCheck.OnChanged = func(checked bool) {
		if err := engine.SetAutostart(checked); err != nil {
			v.genStatus.SetText("Could not change start-at-boot: " + err.Error())
			v.bootCheck.SetChecked(!checked)
			return
		}
		if checked {
			v.genStatus.SetText("✓ Daybreak NVR will start when you sign in.")
		} else {
			v.genStatus.SetText("✓ Daybreak NVR will not start at boot.")
		}
	}
	v.genStatus = widget.NewLabel("")
	v.genStatus.Wrapping = fyne.TextWrapWord
	v.genSaveBtn = widget.NewButton("Save general settings", func() { v.saveGeneral() })
	note := widget.NewLabel("These apply to the whole app. The live wall layout can also be changed from the Live Preview tab — the two stay in sync.")
	note.Wrapping = fyne.TextWrapWord
	return container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("General", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Live wall layout", v.layoutSel),
			widget.NewFormItem("Live preview frame rate", v.fpsEntry),
		),
		v.bootCheck,
		note,
		v.genSaveBtn, v.genStatus,
	))
}

func (v *devicesView) saveGeneral() {
	s := v.n.settings
	switch v.layoutSel.SelectedIndex() {
	case 0:
		s.GridLayout = 1
	case 2:
		s.GridLayout = 9
	case 3:
		s.GridLayout = 16
	default:
		s.GridLayout = 4
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v.fpsEntry.Text)); err == nil && n >= 1 && n <= 30 {
		s.PreviewFPS = n
	}
	if err := s.Save(); err != nil {
		v.genStatus.SetText("Could not save: " + err.Error())
		return
	}
	v.n.settings = s
	v.n.reloadSettings()
	v.genStatus.SetText("✓ Saved — general settings applied.")
}

// ---------- Users page ----------

// buildUsersPage is the v1.2 user management card, moved unchanged
// into its own page: the accounts that sign in to this app, the
// LAN web UI and (via the relay sync) remote viewing. Admins see
// the built-in "admin" user as undeletable; changing its password
// writes a stored entry that overrides the built-in password.
// Viewers never get here with working controls — applyRole
// disables the whole page.
func (v *devicesView) buildUsersPage() fyne.CanvasObject {
	v.userList = widget.NewList(
		func() int { return len(v.users) },
		func() fyne.CanvasObject { return widget.NewLabel("user") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(v.users) {
				return
			}
			u := v.users[id]
			builtIn := ""
			if u.BuiltIn {
				builtIn = " (built-in)"
			}
			obj.(*widget.Label).SetText(fmt.Sprintf("%s — %s%s", u.Username, u.Role, builtIn))
		},
	)
	v.userList.OnSelected = func(id widget.ListItemID) {
		v.selectedUser = id
		v.disarmUserDelete()
		if id < len(v.users) {
			u := v.users[id]
			v.userName.SetText(u.Username)
			v.userPass.SetText("")
			v.userRole.SetSelected(u.Role)
		}
	}
	v.userList.OnUnselected = func(widget.ListItemID) {
		v.selectedUser = -1
		v.disarmUserDelete()
	}
	v.userListBox = newCappedList(v.userList, func() int { return len(v.users) })
	v.userName = widget.NewEntry()
	v.userName.SetPlaceHolder("viewer1")
	v.userPass = widget.NewPasswordEntry()
	v.userPass.SetPlaceHolder("at least 4 characters")
	v.userRole = widget.NewSelect([]string{engine.RoleViewer, engine.RoleAdmin}, nil)
	v.userRole.SetSelected(engine.RoleViewer)
	v.userStatus = widget.NewLabel("")
	v.userStatus.Wrapping = fyne.TextWrapWord
	v.userSaveBtn = widget.NewButton("Save user", func() { v.saveUser() })
	v.userDelBtn = widget.NewButton("Delete user", func() { v.deleteUserClicked() })
	v.refreshUsers()
	note := widget.NewLabel("Admins can do everything, including changing settings and deleting clips. Viewers can watch live, play back and download clips — nothing else. Leave the password blank to keep the current password when changing a role.")
	note.Wrapping = fyne.TextWrapWord
	form := widget.NewForm(
		widget.NewFormItem("Username", v.userName),
		widget.NewFormItem("Password", v.userPass),
		widget.NewFormItem("Role", v.userRole),
	)
	return container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Users", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(v.userSaveBtn, v.userDelBtn),
		v.userStatus,
		v.userListBox,
		form,
		note,
	))
}

func (v *devicesView) refreshUsers() {
	v.users = engine.EffectiveUsers()
	v.selectedUser = -1
	v.refreshUserList()
}

func (v *devicesView) saveUser() {
	name := strings.TrimSpace(v.userName.Text)
	role := v.userRole.Selected
	if role == "" {
		role = engine.RoleViewer
	}
	if _, err := engine.SetUser(name, v.userPass.Text, role); err != nil {
		v.userStatus.SetText("Can't save user: " + err.Error())
		return
	}
	v.refreshUsers()
	v.userPass.SetText("")
	v.n.syncUsersToRelay()
	v.userStatus.SetText(fmt.Sprintf("✓ Saved user %q (%s).", name, role))
}

func (v *devicesView) disarmUserDelete() {
	v.userDelArmIdx = -1
	if v.userDelBtn != nil {
		v.userDelBtn.SetText("Delete user")
	}
}

// deleteUserClicked mirrors the camera delete: first tap arms
// ("Tap again to delete"), second tap deletes. The built-in
// admin can never be deleted.
func (v *devicesView) deleteUserClicked() {
	if v.selectedUser < 0 || v.selectedUser >= len(v.users) {
		return
	}
	idx := v.selectedUser
	u := v.users[idx]
	if u.BuiltIn || strings.EqualFold(u.Username, engine.FallbackAdminUsername) {
		v.userStatus.SetText("The built-in admin user cannot be deleted. Change its password instead.")
		return
	}
	if v.userDelArmIdx != idx || time.Since(v.userDelArmAt) > 6*time.Second {
		v.userDelArmIdx = idx
		v.userDelArmAt = time.Now()
		v.userDelBtn.SetText("Tap again to delete")
		v.userStatus.SetText(fmt.Sprintf("Delete user %q? Tap Delete again within a few seconds to confirm.", u.Username))
		time.AfterFunc(6*time.Second, func() {
			fyne.Do(func() {
				if v.userDelArmIdx == idx {
					v.disarmUserDelete()
					v.userStatus.SetText("")
				}
			})
		})
		return
	}
	v.disarmUserDelete()
	if err := engine.DeleteUser(u.Username); err != nil {
		v.userStatus.SetText("Could not delete user: " + err.Error())
		return
	}
	v.refreshUsers()
	v.n.syncUsersToRelay()
	v.userStatus.SetText(fmt.Sprintf("Deleted user %q.", u.Username))
}

// ---------- Storage page ----------

func (v *devicesView) buildStoragePage() fyne.CanvasObject {
	s := v.n.settings
	v.dirEntry = widget.NewEntry()
	v.dirEntry.SetText(s.RecordingsDir)
	v.segEntry = widget.NewEntry()
	v.segEntry.SetText(strconv.Itoa(s.SegmentMinutes))
	v.daysEntry = widget.NewEntry()
	v.daysEntry.SetText(strconv.Itoa(s.RetentionDays))
	v.maxGBEntry = widget.NewEntry()
	v.maxGBEntry.SetText(strconv.Itoa(s.MaxTotalGB))
	v.storageStatus = widget.NewLabel("")
	v.storageStatus.Wrapping = fyne.TextWrapWord
	v.storageSaveBtn = widget.NewButton("Save storage settings", func() { v.saveStorage() })
	v.usageLbl = widget.NewLabel("Loading disk usage…")
	v.usageLbl.Wrapping = fyne.TextWrapWord
	refreshBtn := widget.NewButton("Refresh disk usage", func() { v.refreshStoragePage() })
	v.reaperLbl = widget.NewLabel("")
	v.reaperLbl.Wrapping = fyne.TextWrapWord
	note := widget.NewLabel("Motion and Person-only mode record one clip per event, directly — the recording starts when motion (or a person) is detected and stops after the keep-after buffer ends, so each event is a single clip in the playback list. Continuous mode records in segments of the length above. Retention (days / size cap) deletes the oldest clips first.")
	note.Wrapping = fyne.TextWrapWord
	return container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Storage", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Recordings folder", v.dirEntry),
			widget.NewFormItem("Clip length — continuous mode (minutes)", v.segEntry),
			widget.NewFormItem("Keep recordings (days)", v.daysEntry),
			widget.NewFormItem("Max total size (GB, 0 = no cap)", v.maxGBEntry),
		),
		v.storageSaveBtn, v.storageStatus,
		widget.NewLabelWithStyle("Disk usage", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.usageLbl, refreshBtn,
		widget.NewLabelWithStyle("What the reaper keeps and deletes", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.reaperLbl, note,
	))
}

// refreshStoragePage recomputes the per-camera and total disk
// usage and the reaper summary.
func (v *devicesView) refreshStoragePage() {
	if v.usageLbl == nil {
		return
	}
	s := v.n.settings
	perCam, total, files := engine.DiskUsageByCamera(s)
	free, diskTotal := engine.DiskFree(s.RecordingsDir)
	var lines []string
	for _, u := range perCam {
		lines = append(lines, fmt.Sprintf("%s: %s in %d clips", u.CameraName, humanBytes(u.Bytes), u.Files))
	}
	lines = append(lines, fmt.Sprintf("Total recordings: %s in %d clips", humanBytes(total), files))
	if diskTotal > 0 {
		lines = append(lines, fmt.Sprintf("Disk: %s free of %s (%.0f%% used).", humanBytes(free), humanBytes(diskTotal), 100*float64(diskTotal-free)/float64(diskTotal)))
	}
	v.usageLbl.SetText(strings.Join(lines, "\n"))
	cap := "no size cap"
	if s.MaxTotalGB > 0 {
		cap = fmt.Sprintf("a %d GB cap", s.MaxTotalGB)
	}
	v.reaperLbl.SetText(fmt.Sprintf("Retention: clips older than %d days are deleted, then the oldest clips go until the library fits %s. Motion and Person-only recordings are one clip per event (event + %d seconds before + %d seconds after); nothing is deleted for having no motion in it — the recording only starts when there is motion (or a person) in the first place.",
		s.RetentionDays, cap, s.PreBufferSeconds, s.PostBufferSeconds))
}

func (v *devicesView) saveStorage() {
	s := v.n.settings
	s.RecordingsDir = strings.TrimSpace(v.dirEntry.Text)
	if n, err := strconv.Atoi(strings.TrimSpace(v.segEntry.Text)); err == nil && n >= 1 && n <= 120 {
		s.SegmentMinutes = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v.daysEntry.Text)); err == nil && n >= 1 && n <= 365 {
		s.RetentionDays = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v.maxGBEntry.Text)); err == nil && n >= 0 {
		s.MaxTotalGB = n
	}
	if err := s.Save(); err != nil {
		v.storageStatus.SetText("Could not save: " + err.Error())
		return
	}
	v.n.settings = s
	v.n.reloadSettings()
	v.refreshStoragePage()
	v.storageStatus.SetText("✓ Saved — storage settings saved and applied.")
}

// ---------- roles ----------

// applyRole disables every settings control for viewers. Admins
// get the full GUI; viewers keep live view + playback only.
func (v *devicesView) applyRole(role string) {
	viewer := role == engine.RoleViewer
	set := func(w fyne.Disableable, disable bool) {
		if w == nil {
			return
		}
		if disable {
			w.Disable()
		} else {
			w.Enable()
		}
	}
	// Cameras page.
	set(v.addBtn, viewer)
	set(v.editBtn, viewer)
	set(v.delBtn, viewer)
	// Motion page.
	set(v.motionCamSel, viewer)
	set(v.motionModeSel, viewer)
	set(v.sensSlider, viewer)
	for _, b := range v.zoneBtns {
		set(b, viewer)
	}
	if v.zonePicker != nil {
		set(v.zonePicker, viewer)
	}
	set(v.zoneRefreshBtn, viewer)
	set(v.preEntry, viewer)
	set(v.postEntry, viewer)
	set(v.aiCheck, viewer)
	set(v.aiConfSlider, viewer)
	set(v.motionSaveBtn, viewer)
	// General page.
	set(v.layoutSel, viewer)
	set(v.fpsEntry, viewer)
	set(v.bootCheck, viewer)
	set(v.genSaveBtn, viewer)
	// Users page.
	set(v.userName, viewer)
	set(v.userPass, viewer)
	set(v.userRole, viewer)
	set(v.userSaveBtn, viewer)
	set(v.userDelBtn, viewer)
	// Storage page.
	set(v.dirEntry, viewer)
	set(v.segEntry, viewer)
	set(v.daysEntry, viewer)
	set(v.maxGBEntry, viewer)
	set(v.storageSaveBtn, viewer)
}

// ---------- camera editor ----------

// showEditor swaps the tab to the inline camera editor page for an
// existing camera (cam != nil) or a blank one (cam == nil). It
// replaces the modal dialog this used through v1.6: the dialog's
// popup layer never painted on Justin's PC (white overlay, no
// content), while ordinary tab content renders fine — so the
// editor is now ordinary tab content, with "Save camera" and
// "Cancel" buttons returning to the camera list.
//
// Record mode, motion sensitivity, the detection zone and human
// detection are NOT here — they live on the Motion & Recording
// page (v1.3). The editor preserves them from the camera being
// edited, so editing a camera's name or address never resets them.
func (v *devicesView) showEditor(cam *engine.Camera) {
	n := v.n
	if cam == nil && len(n.settings.Cameras) >= engine.MaxCameras {
		v.camStatus.SetText(fmt.Sprintf("Limit reached — Daybreak NVR supports up to %d cameras.", engine.MaxCameras))
		return
	}
	editing := engine.Camera{Enabled: true, Record: true, RecordMode: engine.RecordMotion, Sensitivity: engine.SensitivityDefault}
	isNew := cam == nil
	if cam != nil {
		editing = *cam
	}
	prefill := engine.EditorInputFromCamera(editing)

	nameE := widget.NewEntry()
	nameE.SetText(prefill.Name)
	nameE.SetPlaceHolder("Front Door")
	ipE := widget.NewEntry()
	ipE.SetText(prefill.IP)
	ipE.SetPlaceHolder("192.168.1.50")
	userE := widget.NewEntry()
	userE.SetText(prefill.Username)
	passE := widget.NewPasswordEntry()
	passE.SetText(prefill.Password)
	mainE := widget.NewEntry()
	mainE.SetText(prefill.MainURL)
	mainE.SetPlaceHolder("optional — overrides the Tapo address")
	subE := widget.NewEntry()
	subE.SetText(prefill.SubURL)
	subE.SetPlaceHolder("optional — overrides the Tapo address")
	enabledC := widget.NewCheck("Enabled", nil)
	enabledC.SetChecked(prefill.Enabled)
	recordC := widget.NewCheck("Record this camera", nil)
	recordC.SetChecked(prefill.Record)

	help := widget.NewLabel("Tapo cameras: in the Tapo app open the camera → Settings → " +
		"Advanced Settings → Camera Account and create a username and password. " +
		"Enter them here with the camera's IP — Daybreak NVR builds the Tapo RTSP " +
		"addresses for you (stream1 records, stream2 previews). For other brands, " +
		"paste full RTSP URLs in the override boxes instead. " +
		"Record mode, sensitivity, zones and human detection are set on the Motion & Recording page.")
	help.Wrapping = fyne.TextWrapWord

	testLbl := widget.NewLabel("")
	testBtn := widget.NewButton("Test connection", func() {
		candidate := engine.Camera{
			Name: nameE.Text, IP: ipE.Text, Username: userE.Text,
			Password: passE.Text, MainURL: mainE.Text, SubURL: subE.Text,
		}
		url := candidate.SubStreamURL()
		if url == "" {
			testLbl.SetText("Enter an IP address (or RTSP URL) first.")
			return
		}
		testLbl.SetText("Connecting…")
		go func() {
			p := engine.StartPump(n.ffmpeg, engine.PumpOptions{Input: url, Width: 320, FPS: 5})
			ok := false
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				if p.Frames() > 0 {
					ok = true
					break
				}
				if p.Stopped() {
					break
				}
				time.Sleep(120 * time.Millisecond)
			}
			p.Stop()
			fyne.Do(func() {
				if ok {
					testLbl.SetText("✓ Connected — the camera answered with video.")
				} else {
					testLbl.SetText("✗ No video. Check the IP, the Camera Account username/password, and that the camera is on the same network.")
				}
			})
		}()
	})

	form := widget.NewForm(
		widget.NewFormItem("Name", nameE),
		widget.NewFormItem("Camera IP", ipE),
		widget.NewFormItem("Camera Account user", userE),
		widget.NewFormItem("Camera Account password", passE),
		widget.NewFormItem("Main RTSP URL override", mainE),
		widget.NewFormItem("Sub RTSP URL override", subE),
		widget.NewFormItem("", enabledC),
		widget.NewFormItem("", recordC),
	)

	title := "Add camera"
	if !isNew {
		title = "Edit camera — " + editing.Name
	}
	statusLbl := widget.NewLabel("")
	statusLbl.Wrapping = fyne.TextWrapWord

	// saveEditor persists the form: the widget values are mapped
	// onto the camera by engine.BuildCamera (which preserves the
	// camera ID, so recordings/motion/remote paths are unaffected
	// by an edit) and validated; problems are reported inline and
	// the editor stays open. On success it runs the same post-save
	// sync the old dialog did (reloadSettings restarts recorders
	// and pumps and re-posts the remote camera list) and returns
	// to the camera list.
	saveEditor := func() {
		in := engine.EditorInput{
			Name: nameE.Text, IP: ipE.Text, Username: userE.Text,
			Password: passE.Text, MainURL: mainE.Text, SubURL: subE.Text,
			Enabled: enabledC.Checked, Record: recordC.Checked,
			// Motion settings are preserved from the camera
			// being edited (they live on the Motion page).
			RecordMode:      prefill.RecordMode,
			Sensitivity:     prefill.Sensitivity,
			ZoneGrid:        prefill.ZoneGrid,
			HumanDetect:     prefill.HumanDetect,
			HumanConfidence: prefill.HumanConfidence,
		}
		saved, err := engine.BuildCamera(editing, in)
		if err != nil {
			statusLbl.SetText("Can't save yet: " + err.Error())
			return
		}
		s := n.settings
		if isNew {
			saved.ID = fmt.Sprintf("cam%d", time.Now().UnixNano())
			s.Cameras = append(s.Cameras, saved)
		} else {
			for i := range s.Cameras {
				if s.Cameras[i].ID == saved.ID {
					s.Cameras[i] = saved
				}
			}
		}
		if err := s.Save(); err != nil {
			statusLbl.SetText("Could not save: " + err.Error())
			return
		}
		n.settings = s
		n.reloadSettings()
		v.refreshCamList()
		v.refreshMotionPage()
		v.showHome()
		v.camStatus.SetText("✓ Saved camera \"" + saved.Name + "\".")
	}
	saveBtn := widget.NewButton("Save camera", saveEditor)
	cancelBtn := widget.NewButton("Cancel", func() { v.showHome() })

	editorPage := container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		help, form,
		container.NewHBox(testBtn, testLbl),
		statusLbl,
		container.NewHBox(saveBtn, cancelBtn),
	))
	if c, ok := v.root.(*fyne.Container); ok {
		c.Objects = []fyne.CanvasObject{editorPage}
		c.Refresh()
	}
}
