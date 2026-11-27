package main

import (
	"image/color"
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The app follows the operating system's light or dark setting. Fyne hands a
// theme the variant the OS asked for; the theme's job is to have an answer for
// both that a person can read.

func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	channel := func(v uint32) float64 {
		s := float64(v) / 0xffff
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestTheThemeAnswersForTheVariantTheSystemAsksFor(t *testing.T) {
	t.Parallel()

	th := appTheme{}
	light := th.Color(theme.ColorNameBackground, theme.VariantLight)
	dark := th.Color(theme.ColorNameBackground, theme.VariantDark)
	if luminance(light) < 0.9 {
		t.Errorf("light background %v is not light", light)
	}
	if luminance(dark) > 0.05 {
		t.Errorf("dark background %v is not dark", dark)
	}
}

// Every pairing of text on a surface the app draws has to read in both
// variants: WCAG AA for body text.
func TestEveryTextPairingReadsInBothVariants(t *testing.T) {
	t.Parallel()

	pairs := []struct{ text, on fyne.ThemeColorName }{
		{theme.ColorNameForeground, theme.ColorNameBackground},
		{theme.ColorNameForeground, colorNav},
		{colorMuted, theme.ColorNameBackground},
		{colorMuted, colorNav},
		{theme.ColorNameDisabled, theme.ColorNameBackground},
		{theme.ColorNameDisabled, colorNav},
		{theme.ColorNameDisabled, theme.ColorNameHeaderBackground},
		{theme.ColorNameForegroundOnPrimary, theme.ColorNamePrimary},
		{colorPillNeutralText, colorPillNeutral},
		{colorPillGoodText, colorPillGood},
		{colorPillBadText, colorPillBad},
		{colorPillActiveText, colorPillActive},
		{theme.ColorNameForeground, colorSelected},
		{theme.ColorNameForeground, theme.ColorNameButton},
		{theme.ColorNameDisabled, theme.ColorNameDisabledButton},
	}
	th := appTheme{}
	for _, variant := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
		for _, p := range pairs {
			got := contrast(th.Color(p.text, variant), th.Color(p.on, variant))
			if got < 4.5 {
				t.Errorf("variant %d: %s on %s has contrast %.2f, want at least 4.5", variant, p.text, p.on, got)
			}
		}
	}
}

// A button has to look like one: a fill a step off the screen and off the
// sidebar, so an enabled button does not read as disabled, and a disabled one
// nearer the background than an enabled one.
func TestButtonsStandOutFromWhatTheySitOn(t *testing.T) {
	t.Parallel()

	th := appTheme{}
	for _, variant := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
		button := th.Color(theme.ColorNameButton, variant)
		for _, on := range []fyne.ThemeColorName{theme.ColorNameBackground, colorNav} {
			if got := contrast(button, th.Color(on, variant)); got < 1.15 {
				t.Errorf("variant %d: a button on %s has contrast %.2f, want at least 1.15", variant, on, got)
			}
		}
		bg := th.Color(theme.ColorNameBackground, variant)
		if contrast(th.Color(theme.ColorNameDisabledButton, variant), bg) >= contrast(button, bg) {
			t.Errorf("variant %d: a disabled button stands out as much as an enabled one", variant)
		}
	}
}
