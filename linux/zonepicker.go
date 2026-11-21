// Zone picker (v1.6): the detection-zone editor as a picture with
// the grid overlaid on it, so the user can see exactly which spots
// they are choosing.
//
// A custom Fyne widget: the camera's still image fills the widget
// (stretched to 16:9, so cell (col,row) maps linearly onto the
// picture), and 16x9 semi-transparent rectangles sit on top of it
// — active cells a teal tint, inactive cells dimmed dark, the
// picture showing through both. A tap toggles the cell under the
// cursor. The 1px gaps between cells let the picture show through
// as the grid lines.
package main

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
)

// zonePickerMinSize is the widget's size: 640x360 keeps the 16:9
// picture at the pump's native frame size.
var zonePickerMinSize = fyne.NewSize(640, 360)

var (
	zoneCellOn  = color.NRGBA{0x16, 0x85, 0x73, 80}  // teal tint, picture shows through
	zoneCellOff = color.NRGBA{0x00, 0x00, 0x00, 140} // dimmed dark, picture shows through
)

type zonePicker struct {
	widget.BaseWidget
	img      image.Image
	grid     string
	disabled bool
	onChange func(grid string)
}

func newZonePicker(grid string, onChange func(string)) *zonePicker {
	z := &zonePicker{grid: grid, onChange: onChange}
	if len(z.grid) != engine.ZoneCells {
		z.grid = engine.DefaultZoneGrid()
	}
	z.ExtendBaseWidget(z)
	return z
}

// SetImage replaces the picture under the grid.
func (z *zonePicker) SetImage(img image.Image) {
	z.img = img
	z.Refresh()
}

// SetGrid replaces the zone grid (from the plain-grid fallback or
// a newly loaded camera) without firing onChange.
func (z *zonePicker) SetGrid(grid string) {
	if len(grid) != engine.ZoneCells {
		grid = engine.DefaultZoneGrid()
	}
	z.grid = grid
	z.Refresh()
}

// Grid returns the current zone grid string.
func (z *zonePicker) Grid() string { return z.grid }

// Disable / Enable / Disabled satisfy fyne.Disableable (viewers
// get the picker disabled, like every other settings control).
func (z *zonePicker) Disable()  { z.disabled = true; z.Refresh() }
func (z *zonePicker) Enable()   { z.disabled = false; z.Refresh() }
func (z *zonePicker) Disabled() bool { return z.disabled }

// Tapped toggles the cell under the cursor. The picture is
// stretched to fill the widget, so the cell is a linear map from
// the tap position.
func (z *zonePicker) Tapped(ev *fyne.PointEvent) {
	if z.disabled {
		return
	}
	s := z.Size()
	if s.Width <= 0 || s.Height <= 0 {
		return
	}
	col := int(ev.Position.X / s.Width * engine.ZoneCols)
	row := int(ev.Position.Y / s.Height * engine.ZoneRows)
	if col < 0 || col >= engine.ZoneCols || row < 0 || row >= engine.ZoneRows {
		return
	}
	z.grid = engine.ZoneToggle(z.grid, col, row)
	z.Refresh()
	if z.onChange != nil {
		z.onChange(z.grid)
	}
}

// TappedSecondary is a no-op (required by fyne.Tappable).
func (z *zonePicker) TappedSecondary(_ *fyne.PointEvent) {}

func (z *zonePicker) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewImageFromImage(z.img)
	bg.FillMode = canvas.ImageFillStretch
	cells := make([]*canvas.Rectangle, engine.ZoneCells)
	objects := []fyne.CanvasObject{bg}
	for i := range cells {
		cells[i] = canvas.NewRectangle(zoneCellOff)
		objects = append(objects, cells[i])
	}
	return &zonePickerRenderer{p: z, bg: bg, cells: cells, objects: objects}
}

type zonePickerRenderer struct {
	p       *zonePicker
	bg      *canvas.Image
	cells   []*canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *zonePickerRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	cw := size.Width / engine.ZoneCols
	ch := size.Height / engine.ZoneRows
	for row := 0; row < engine.ZoneRows; row++ {
		for col := 0; col < engine.ZoneCols; col++ {
			idx := row*engine.ZoneCols + col
			// 1px gaps: the picture shows through as grid lines.
			r.cells[idx].Resize(fyne.NewSize(cw-1, ch-1))
			r.cells[idx].Move(fyne.NewPos(float32(col)*cw+0.5, float32(row)*ch+0.5))
		}
	}
}

func (r *zonePickerRenderer) MinSize() fyne.Size { return zonePickerMinSize }

func (r *zonePickerRenderer) Refresh() {
	r.bg.Image = r.p.img
	r.bg.Refresh()
	for i, cell := range r.cells {
		if i < len(r.p.grid) && r.p.grid[i] == '1' {
			cell.FillColor = zoneCellOn
		} else {
			cell.FillColor = zoneCellOff
		}
		cell.Refresh()
	}
}

func (r *zonePickerRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *zonePickerRenderer) Destroy() {}
