package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// nvrTheme is a dark slate theme in the spirit of SmartPSS Lite:
// deep blue-grey panels, one blue accent, red reserved for REC.
type nvrTheme struct{}

var (
	colBackground  = color.NRGBA{R: 24, G: 32, B: 42, A: 255} // #18202A
	colPanel       = color.NRGBA{R: 32, G: 42, B: 55, A: 255} // #202A37
	colInput       = color.NRGBA{R: 18, G: 24, B: 32, A: 255} // #121820
	colForeground  = color.NRGBA{R: 216, G: 224, B: 233, A: 255}
	colDim         = color.NRGBA{R: 139, G: 152, B: 168, A: 255}
	colPrimary     = color.NRGBA{R: 43, G: 144, B: 199, A: 255} // #2B90C7
	colButton      = color.NRGBA{R: 43, G: 55, B: 70, A: 255}
	colSelect      = color.NRGBA{R: 27, G: 94, B: 133, A: 255}
	colSeparator   = color.NRGBA{R: 52, G: 65, B: 80, A: 255}
	colPlaceholder = color.NRGBA{R: 110, G: 122, B: 138, A: 255}
)

func (nvrTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colBackground
	case theme.ColorNameButton:
		return colButton
	case theme.ColorNameForeground:
		return colForeground
	case theme.ColorNamePrimary:
		return colPrimary
	case theme.ColorNameInputBackground:
		return colInput
	case theme.ColorNameSelection:
		return colSelect
	case theme.ColorNameSeparator:
		return colSeparator
	case theme.ColorNamePlaceHolder:
		return colPlaceholder
	case theme.ColorNameDisabled:
		return colDim
	case theme.ColorNameHover:
		return colPanel
	case theme.ColorNameFocus:
		return colPrimary
	default:
		return theme.DefaultTheme().Color(name, theme.VariantDark)
	}
}

func (nvrTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (nvrTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (nvrTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 6
	default:
		return theme.DefaultTheme().Size(name)
	}
}
