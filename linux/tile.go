package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
)

// tapBox makes any canvas object tappable / double-tappable.
type tapBox struct {
	widget.BaseWidget
	child    fyne.CanvasObject
	OnTap    func()
	OnDouble func()
}

func newTapBox(child fyne.CanvasObject, onTap, onDouble func()) *tapBox {
	t := &tapBox{child: child, OnTap: onTap, OnDouble: onDouble}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.child)
}

func (t *tapBox) Tapped(*fyne.PointEvent) {
	if t.OnTap != nil {
		t.OnTap()
	}
}

func (t *tapBox) DoubleTapped(*fyne.PointEvent) {
	if t.OnDouble != nil {
		t.OnDouble()
	}
}

// camTile is one cell of the video wall: black video area, camera
// name top-left, REC indicator top-right, state line at the bottom.
type camTile struct {
	cam   engine.Camera
	empty bool
	label string

	img      *canvas.Image
	nameTxt  *canvas.Text
	recTxt   *canvas.Text
	statTxt  *canvas.Text
	border   *canvas.Rectangle
	root     fyne.CanvasObject
	pump     *engine.Pump
	lastSeen int64
}

var (
	colBlack  = color.NRGBA{R: 5, G: 7, B: 10, A: 255}
	colWhite  = color.NRGBA{R: 240, G: 244, B: 248, A: 255}
	colRed    = color.NRGBA{R: 238, G: 64, B: 64, A: 255}
	colGrey   = color.NRGBA{R: 130, G: 142, B: 156, A: 255}
	colAccent = color.NRGBA{R: 64, G: 176, B: 240, A: 255}
	colTransp = color.NRGBA{A: 0}
)

func newCamTile(cam engine.Camera, empty bool, label string,
	onTap, onDouble func(engine.Camera)) *camTile {
	t := &camTile{cam: cam, empty: empty, label: label}
	bg := canvas.NewRectangle(colBlack)
	t.img = canvas.NewImageFromImage(nil)
	t.img.FillMode = canvas.ImageFillContain
	t.img.ScaleMode = canvas.ImageScaleSmooth

	t.nameTxt = canvas.NewText(label, colWhite)
	t.nameTxt.TextStyle = fyne.TextStyle{Bold: true}
	t.nameTxt.TextSize = 13
	t.recTxt = canvas.NewText("", colRed)
	t.recTxt.TextSize = 12
	t.recTxt.TextStyle = fyne.TextStyle{Bold: true}
	t.statTxt = canvas.NewText("", colGrey)
	t.statTxt.TextSize = 11.5

	top := container.NewBorder(nil, nil, t.nameTxt, t.recTxt)
	overlay := container.NewBorder(top, t.statTxt, nil, nil)
	inner := container.NewStack(bg, t.img, overlay)
	t.border = canvas.NewRectangle(colTransp)
	t.border.StrokeColor = colSeparator
	t.border.StrokeWidth = 1
	framed := container.NewStack(t.border, inner)
	tap := newTapBox(framed, func() {
		if onTap != nil {
			onTap(cam)
		}
	}, func() {
		if onDouble != nil {
			onDouble(cam)
		}
	})
	t.root = tap
	if empty {
		t.statTxt.Text = "No camera"
		t.statTxt.Refresh()
	}
	return t
}

func (t *camTile) setSelected(sel bool) {
	if sel {
		t.border.StrokeColor = colAccent
		t.border.StrokeWidth = 2
	} else {
		t.border.StrokeColor = colSeparator
		t.border.StrokeWidth = 1
	}
	t.border.Refresh()
}

func (t *camTile) setStatus(rec bool, text string) {
	if rec {
		t.recTxt.Text = "● REC"
	} else {
		t.recTxt.Text = ""
	}
	t.recTxt.Refresh()
	t.statTxt.Text = text
	t.statTxt.Refresh()
}
