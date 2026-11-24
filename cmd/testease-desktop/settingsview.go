package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/environment"
)

// The Settings screen's columns.
const (
	ecolName = iota
	ecolValue
	ecolUse
	ecolSets
)

var (
	envTitles = []string{"Variable", "Value", "Use", "Sets"}
	envWidths = []float32{290, 260, 190, 330}
)

// settingsView shows where records are read from and every TESTEASE_*
// variable this app was started with. It changes none of them: there is no
// config file, and a value that changes what is measured is not something an
// app should keep.
type settingsView struct {
	rows    []environment.Setting
	where   *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
}

func newSettingsView(dir, from string, getenv func(string) string) *settingsView {
	v := &settingsView{rows: environment.Current(getenv)}

	v.where = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	v.where.Selectable = true
	v.where.Wrapping = fyne.TextWrapBreak
	if dir == "" {
		v.where.SetText("not chosen")
	} else {
		v.where.SetText(dir + " (from " + from + ")")
	}
	runsHeading := widget.NewLabelWithStyle("Runs directory", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	runs := container.NewVBox(runsHeading, v.where,
		note("Taken from --runs-dir, then TESTEASE_RUNS_DIR, then the folder last chosen here. That folder is the only thing this app remembers."))

	envHeading := widget.NewLabelWithStyle("Environment", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.table = listing(func() int { return len(v.rows) }, envTitles, envWidths, ecolName, v.cell, nil)

	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Settings", nil), widget.NewSeparator(),
			container.NewPadded(runs), widget.NewSeparator(), container.NewPadded(envHeading)),
		container.NewVBox(widget.NewSeparator(), note("These are the values this app was started with. A testease started from another shell reads that shell's environment. Variables marked \"pass as a flag\" change what is measured, recorded or judged, and the record does not say they were set.")),
		nil, nil, v.table,
	)
	return v
}

func (v *settingsView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.rows) {
		return "", pillNeutral, false
	}
	s := v.rows[row]
	switch col {
	case ecolName:
		return s.Name, pillNeutral, false
	case ecolValue:
		if !s.Set {
			return "not set", pillNeutral, false
		}
		return shown(s.Name, s.Value), pillNeutral, false
	case ecolUse:
		switch {
		case s.Safe:
			return "safe to leave set", pillNeutral, false
		case s.Set:
			return "set; pass it as a flag", pillBad, true
		default:
			return "pass as a flag", pillNeutral, false
		}
	case ecolSets:
		return s.Sets, pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *settingsView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}
