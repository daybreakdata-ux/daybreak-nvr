package main

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
)

// liveView is the SmartPSS-style live page: device list on the left,
// video wall in the middle, layout controls above the wall.
type liveView struct {
	n *nvrApp

	root     fyne.CanvasObject
	list     *widget.List
	gridWrap *fyne.Container
	tiles    []*camTile
	layout   int
	selected string
}

func newLiveView(n *nvrApp) *liveView {
	v := &liveView{n: n, layout: n.settings.GridLayout}
	if v.layout != 1 && v.layout != 4 && v.layout != 9 && v.layout != 16 {
		v.layout = 4
	}

	v.list = widget.NewList(
		func() int { return len(n.settings.Cameras) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("●"),
				widget.NewLabel("camera"),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(n.settings.Cameras) {
				return
			}
			cam := n.settings.Cameras[id]
			row := obj.(*fyne.Container)
			dot := row.Objects[0].(*widget.Label)
			name := row.Objects[1].(*widget.Label)
			st := n.recorder.Status(cam.ID)
			switch {
			case !cam.Enabled:
				dot.SetText("○")
				name.SetText(cam.Name + "  (disabled)")
			case st.Recording:
				dot.SetText("🔴")
				name.SetText(cam.Name + "  · REC")
			case cam.Record:
				dot.SetText("🟡")
				name.SetText(cam.Name + "  · starting…")
			default:
				dot.SetText("🟢")
				name.SetText(cam.Name + "  · preview only")
			}
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		if id < len(n.settings.Cameras) {
			v.selected = n.settings.Cameras[id].ID
			v.markSelection()
		}
	}

	layoutBtns := container.NewHBox(
		widget.NewLabel("Layout:"),
		widget.NewButton("1", func() { v.setLayout(1) }),
		widget.NewButton("4", func() { v.setLayout(4) }),
		widget.NewButton("9", func() { v.setLayout(9) }),
		widget.NewButton("16", func() { v.setLayout(16) }),
		widget.NewSeparator(),
		widget.NewLabel("Double-click a tile for single view"),
	)
	v.gridWrap = container.NewMax()
	toolbar := container.NewBorder(nil, nil, layoutBtns, nil)

	left := container.NewBorder(
		widget.NewLabelWithStyle("DEVICES", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, v.list)
	split := container.NewHSplit(left, container.NewBorder(toolbar, nil, nil, nil, v.gridWrap))
	split.SetOffset(0.16)
	v.root = split
	v.rebuildGrid()
	return v
}

func (v *liveView) cols() int {
	switch v.layout {
	case 1:
		return 1
	case 9:
		return 3
	case 16:
		return 4
	default:
		return 2
	}
}

// enabledCameras returns the cameras that can show video, in order.
func (v *liveView) enabledCameras() []engine.Camera {
	var out []engine.Camera
	for _, c := range v.n.settings.Cameras {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out
}

// rebuildGrid recreates the tile grid for the current layout.
func (v *liveView) rebuildGrid() {
	cams := v.enabledCameras()
	v.tiles = v.tiles[:0]
	cells := make([]fyne.CanvasObject, 0, v.layout)
	for i := 0; i < v.layout; i++ {
		var tile *camTile
		if i < len(cams) {
			cam := cams[i]
			tile = newCamTile(cam, false, cam.Name,
				func(c engine.Camera) {
					v.selected = c.ID
					v.markSelection()
					if idx := v.indexOfCam(c.ID); idx >= 0 {
						v.list.Select(idx)
					}
				},
				func(c engine.Camera) {
					v.selected = c.ID
					v.focusCamera(c)
				})
		} else {
			tile = newCamTile(engine.Camera{}, true, "", nil, nil)
		}
		v.tiles = append(v.tiles, tile)
		cells = append(cells, tile.root)
	}
	grid := container.NewGridWithColumns(v.cols(), cells...)
	doUI(func() {
		v.gridWrap.Objects = []fyne.CanvasObject{grid}
		v.gridWrap.Refresh()
	})
	v.markSelection()
	v.publishVisible()
}

// publishVisible tells the app's pump manager which cameras are on
// the wall, so it can keep exactly those streams (plus motion and
// phone needs) running.
func (v *liveView) publishVisible() {
	ids := []string{}
	for _, t := range v.tiles {
		if !t.empty {
			ids = append(ids, t.cam.ID)
		}
	}
	v.n.setVisible(ids)
	v.n.syncPumps()
}

func (v *liveView) indexOfCam(id string) int {
	for i, c := range v.n.settings.Cameras {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// focusCamera jumps to single view of one camera (from double-click).
func (v *liveView) focusCamera(cam engine.Camera) {
	v.layout = 1
	// Move the chosen camera to the front for single view by rebuilding
	// with it first: simplest is to rebuild then swap tile order is not
	// supported; instead set a one-off grid with just this camera.
	v.tiles = v.tiles[:0]
	tile := newCamTile(cam, false, cam.Name,
		func(c engine.Camera) { v.selected = c.ID; v.markSelection() },
		func(c engine.Camera) { v.setLayout(v.n.settings.GridLayout) })
	v.tiles = append(v.tiles, tile)
	grid := container.NewGridWithColumns(1, tile.root)
	doUI(func() {
		v.gridWrap.Objects = []fyne.CanvasObject{grid}
		v.gridWrap.Refresh()
	})
	v.markSelection()
	v.publishVisible()
}

func (v *liveView) setLayout(l int) {
	v.layout = l
	v.n.settings.GridLayout = l
	_ = v.n.settings.Save()
	v.rebuildGrid()
}

func (v *liveView) markSelection() {
	for _, t := range v.tiles {
		t.setSelected(!t.empty && t.cam.ID == v.selected && v.selected != "")
	}
}

// tick pushes the newest frames into the tiles and updates status.
// Pumps are owned by the app's shared pump manager; a stopped pump
// is replaced by the manager on the next heartbeat.
func (v *liveView) tick() {
	fyne.Do(func() {
		for _, t := range v.tiles {
			if t.empty {
				continue
			}
			st := v.n.recorder.Status(t.cam.ID)
			pump := v.n.pumpFor(t.cam.ID)
			if pump == nil {
				t.setStatus(st.Recording, "Connecting…")
				continue
			}
			fr := pump.Frames()
			if fr != t.lastSeen {
				t.lastSeen = fr
				if img := pump.Latest(); img != nil {
					t.img.Image = img
					t.img.Refresh()
				}
			}
			switch {
			case pump.Stopped() && fr == 0:
				t.setStatus(st.Recording, "No signal — check camera / Test in Devices")
			case pump.Stopped():
				t.setStatus(st.Recording, "Stream ended — restarting…")
			default:
				t.setStatus(st.Recording, v.statusText(t.cam, st))
			}
		}
	})
}

func (v *liveView) statusText(cam engine.Camera, st engine.CamStatus) string {
	// Motion / Person-only: the event recorder writes one clip
	// per event directly (v1.6), so its status — not the
	// continuous segment recorder's — carries the error.
	if cam.Record && (cam.Mode() == engine.RecordMotion || cam.Mode() == engine.RecordPerson) {
		evSt := engine.EventStatus{}
		if v.n.eventRec != nil {
			evSt = v.n.eventRec.Status(cam.ID)
		}
		if evSt.LastError != "" && !evSt.Recording {
			return "Record error: " + evSt.LastError
		}
		if cam.Mode() == engine.RecordPerson {
			if ev, ok := v.n.tracker.OpenEvent(cam.ID); ok && ev.Kind == "person" {
				return "🔴 Person — recording now"
			}
			if ev, ok := v.n.tracker.LastPersonEvent(cam.ID); ok && !ev.End.IsZero() {
				return "Person watch · last person " + ev.End.Format("3:04 PM")
			}
			return "Person watch · nothing yet"
		}
		if v.n.tracker.Active(cam.ID) {
			return "🔴 Motion — recording now"
		}
		if last := v.n.tracker.LastMotion(cam.ID); !last.IsZero() {
			return "Motion watch · last motion " + last.Format("3:04 PM")
		}
		return "Motion watch · nothing yet"
	}
	// "Do not record": live view only — never "Recorder
	// starting…", the recorder deliberately never runs.
	if cam.Mode() == engine.RecordOff {
		return "Live only · recording off"
	}
	if st.Recording {
		return fmt.Sprintf("Recording · %s", cam.IP)
	}
	if cam.Record && st.LastError != "" {
		return "Record error: " + st.LastError
	}
	if cam.Record {
		return "Recorder starting…"
	}
	return "Preview only (recording off)"
}

func (v *liveView) refreshDeviceList() {
	doUI(func() {
		if v.list != nil {
			v.list.Refresh()
		}
	})
}
