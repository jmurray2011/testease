package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The app's own colour names, beside Fyne's. Pills carry status and verdicts:
// neutral for what a run measured, good and bad for what a judge said, active
// for what is still going.
const (
	colorNav             fyne.ThemeColorName = "testeaseNav"
	colorMuted           fyne.ThemeColorName = "testeaseMuted"
	colorSelected        fyne.ThemeColorName = "testeaseSelected"
	colorPillNeutral     fyne.ThemeColorName = "testeasePillNeutral"
	colorPillNeutralText fyne.ThemeColorName = "testeasePillNeutralText"
	colorPillGood        fyne.ThemeColorName = "testeasePillGood"
	colorPillGoodText    fyne.ThemeColorName = "testeasePillGoodText"
	colorPillBad         fyne.ThemeColorName = "testeasePillBad"
	colorPillBadText     fyne.ThemeColorName = "testeasePillBadText"
	colorPillActive      fyne.ThemeColorName = "testeasePillActive"
	colorPillActiveText  fyne.ThemeColorName = "testeasePillActiveText"
)

func hex(v uint32) color.Color {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// Both palettes are defined here and nowhere else. The light one is the
// mockups'; the dark one keeps the same roles at the same contrast.
//
// Disabled is also what Fyne draws low-importance labels in - captions, counts,
// the footer - so it is a readable grey rather than a washed-out one.
var (
	lightPalette = map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:          hex(0xffffff),
		theme.ColorNameForeground:          hex(0x1c1d21),
		theme.ColorNamePrimary:             hex(0x1d4ed8),
		theme.ColorNameForegroundOnPrimary: hex(0xffffff),
		theme.ColorNameSeparator:           hex(0xe2e2e7),
		theme.ColorNameHeaderBackground:    hex(0xfafafb),
		theme.ColorNameHover:               hex(0xeef2fd),
		theme.ColorNameSelection:           hex(0xe3e8f8),
		theme.ColorNameInputBorder:         hex(0xcfd0d6),
		theme.ColorNameDisabled:            hex(0x6b6c74),
		colorNav:                           hex(0xf4f4f6),
		colorMuted:                         hex(0x55565e),
		colorSelected:                      hex(0xe3e8f8),
		colorPillNeutral:                   hex(0xeceef2),
		colorPillNeutralText:               hex(0x2c2d33),
		colorPillGood:                      hex(0xe3e8f8),
		colorPillGoodText:                  hex(0x1d3a8a),
		colorPillBad:                       hex(0xfdebd8),
		colorPillBadText:                   hex(0x8a3b00),
		colorPillActive:                    hex(0xe3e8f8),
		colorPillActiveText:                hex(0x1d3a8a),
	}
	darkPalette = map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:          hex(0x17181c),
		theme.ColorNameForeground:          hex(0xe7e7ea),
		theme.ColorNamePrimary:             hex(0x8aa4ff),
		theme.ColorNameForegroundOnPrimary: hex(0x0b1530),
		theme.ColorNameSeparator:           hex(0x2e3036),
		theme.ColorNameHeaderBackground:    hex(0x1c1d22),
		theme.ColorNameHover:               hex(0x232a3d),
		theme.ColorNameSelection:           hex(0x26324f),
		theme.ColorNameInputBorder:         hex(0x3a3c44),
		theme.ColorNameDisabled:            hex(0x8e8f98),
		colorNav:                           hex(0x1f2025),
		colorMuted:                         hex(0xa3a4ad),
		colorSelected:                      hex(0x26324f),
		colorPillNeutral:                   hex(0x2c2e35),
		colorPillNeutralText:               hex(0xd4d5db),
		colorPillGood:                      hex(0x1f2f5c),
		colorPillGoodText:                  hex(0xb9c8ff),
		colorPillBad:                       hex(0x4a2a12),
		colorPillBadText:                   hex(0xffc9a1),
		colorPillActive:                    hex(0x1f2f5c),
		colorPillActiveText:                hex(0xb9c8ff),
	}
)

// appTheme answers for whichever variant Fyne asks: Fyne reads the operating
// system's light or dark setting (Windows app mode, the freedesktop
// color-scheme portal on Linux) and re-asks when it changes, so the app
// follows the system live without a setting of its own.
type appTheme struct{}

var _ fyne.Theme = appTheme{}

func (appTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	palette := lightPalette
	if variant == theme.VariantDark {
		palette = darkPalette
	}
	if c, ok := palette[name]; ok {
		return c
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (appTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (appTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (appTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}
