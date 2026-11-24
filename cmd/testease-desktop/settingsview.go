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
	envWidths = []float32{280, 230, 190, 420}
)

// settingsView shows where records are read from and every TESTEASE_*
// variable this app was started with. It changes none of them: there is no
// config file, and a value that changes what is measured is not something an
// app should keep. The variables are a plain list, not a table: there is
// nothing on this screen to pick.
type settingsView struct {
	rows     []environment.Setting
	where    *widget.Label
	toolsBox *fyne.Container
	list     *fyne.Container
	content  fyne.CanvasObject
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

	v.list = container.NewVBox()
	header := make([]fyne.CanvasObject, len(envTitles))
	for i, title := range envTitles {
		l := widget.NewLabel(title)
		l.Importance = widget.LowImportance
		header[i] = l
	}
	v.list.Add(container.New(&fixedColumns{widths: envWidths}, header...))
	for r := range v.rows {
		cells := make([]fyne.CanvasObject, len(envTitles))
		for c := range envTitles {
			text, kind, isPill := v.cell(r, c)
			if isPill {
				cells[c] = container.NewHBox(newPill(text, kind))
				continue
			}
			l := widget.NewLabel(text)
			l.Truncation = fyne.TextTruncateEllipsis
			l.TextStyle = fyne.TextStyle{Monospace: c == ecolName}
			l.Selectable = c == ecolValue
			cells[c] = l
		}
		v.list.Add(container.New(&fixedColumns{widths: envWidths}, cells...))
	}

	v.toolsBox = container.NewVBox()
	v.content = container.NewVBox(
		screenTitle("Settings", nil), widget.NewSeparator(),
		container.NewPadded(runs), v.toolsBox, widget.NewSeparator(),
		container.NewPadded(envHeading), v.list, widget.NewSeparator(),
		note("These are the values this app was started with. A testease started from another shell reads that shell's environment. Variables marked \"pass as a flag\" change what is measured, recorded or judged, and the record does not say they were set."),
	)
	return v
}

// fixedColumns lays a row's cells out at fixed widths, so the rows line up.
type fixedColumns struct{ widths []float32 }

func (c *fixedColumns) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	x := float32(0)
	for i, o := range objects {
		w := c.widths[i]
		if i == len(objects)-1 && size.Width-x > w {
			w = size.Width - x
		}
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(w, size.Height))
		x += w
	}
}

func (c *fixedColumns) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i, o := range objects {
		w += c.widths[i]
		h = max(h, o.MinSize().Height)
	}
	return fyne.NewSize(w, h)
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

// addTools shows where scenarios are and which testease runs are started
// with.
func (v *settingsView) addTools(t tools) {
	line := func(heading, value, from string) {
		h := widget.NewLabelWithStyle(heading, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		text := "not found"
		if value != "" {
			text = value + " (" + from + ")"
		}
		l := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		l.Selectable = true
		l.Wrapping = fyne.TextWrapBreak
		v.toolsBox.Add(container.NewPadded(container.NewVBox(h, l)))
	}
	line("Scenario root", t.scenarioRoot, t.scenarioFrom)
	line("testease binary", t.testease, t.testeaseFrom)
	v.toolsBox.Add(container.NewPadded(note("The scenario root is taken like the runs directory: --scenario-root, then TESTEASE_SCENARIO_ROOT, then the folder last chosen here. The binary is --testease, then the testease beside this app, then the one on PATH.")))
}
