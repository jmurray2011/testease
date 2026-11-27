package main

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Widths of the comparison's columns: what is compared, then one per run.
const (
	compareNameMin = 260
	compareNameMax = 420
	compareRun     = 280
)

// compareView puts two or more runs side by side, oldest first, and says
// what differs before showing it.
type compareView struct {
	sides []side
	rows  []diffRow
	// shown is rows as the table lists them: a title row before each
	// section, and only the rows that differ unless asked for all.
	shown    []diffRow
	onlyDiff *widget.Check
	table    *widget.Table
	// empty says so when nothing differs and only differences are asked for.
	empty *widget.Label
	// runWidth is each run's column: wide enough for its id in full.
	runWidth float32
	window   fyne.Window
	// reproduce asks the reproduce judge whether the runs' stable facts
	// agree; it is offered only for runs of one scenario.
	reproduce *action
	content   fyne.CanvasObject
}

func newCompareView(dirs []string, backLabel string, back func()) (*compareView, error) {
	v := &compareView{reproduce: primaryAction("Check reproducibility", theme.ConfirmIcon())}
	for _, dir := range dirs {
		s, err := loadSide(dir)
		if err != nil {
			return nil, err
		}
		v.sides = append(v.sides, s)
	}
	// Oldest first, so a change reads from before to after.
	sort.SliceStable(v.sides, func(i, j int) bool { return takenAt(v.sides[i].d.RunID) < takenAt(v.sides[j].d.RunID) })
	v.rows = diffRuns(v.sides)

	// What differs, in words, beside each other: what went in on the left,
	// what came out on the right.
	in, out := explain(v.rows, len(v.sides))
	summary := container.NewGridWithColumns(2,
		container.NewVBox(heading("What went in differently"), bullets(in)),
		container.NewVBox(heading("What came out differently"), bullets(out)))

	v.onlyDiff = widget.NewCheck("Only what differs", func(bool) { v.apply() })
	v.table = widget.NewTableWithHeaders(
		func() (int, int) { return len(v.shown), len(v.sides) + 1 },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewStack(canvas.NewRectangle(color.Transparent), label)
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			stack := o.(*fyne.Container)
			v.paintCell(id, stack.Objects[0].(*canvas.Rectangle), stack.Objects[1].(*widget.Label))
		},
	)
	v.table.ShowHeaderColumn = false
	v.table.CreateHeader = func() fyne.CanvasObject {
		l := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		l.Truncation = fyne.TextTruncateEllipsis
		return l
	}
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		l := o.(*widget.Label)
		if id.Col >= 1 && id.Col <= len(v.sides) {
			l.SetText(v.sides[id.Col-1].d.RunID)
		} else {
			l.SetText("")
		}
	}
	v.table.OnSelected = func(id widget.TableCellID) {
		v.table.UnselectAll()
		if id.Row >= 0 && id.Row < len(v.shown) && !v.shown[id.Row].heading {
			v.showRow(v.shown[id.Row])
		}
	}
	v.fit()

	v.empty = widget.NewLabel("Nothing differs. Untick Only what differs to see every row.")
	v.empty.Importance = widget.LowImportance
	v.empty.Hide()

	// The checkbox stays at the right edge whether or not reproduce is
	// offered, so it is never where the button was.
	title := fmt.Sprintf("Compare %d runs", len(v.sides))
	top := backHeader(backLabel, back, title, v.reproduce.Button, v.onlyDiff)
	foot := note("Runs are oldest first. A highlighted value differs from the first run's. Tap a row to read its values in full. Secrets are compared but not shown.")
	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(container.NewVBox(top, summary)), widget.NewSeparator(), v.empty),
		container.NewVBox(widget.NewSeparator(), foot),
		nil, nil, v.table,
	)
	v.onlyDiff.SetChecked(true)
	return v, nil
}

// bullets is lines as one wrapped list, so a few of them take a few lines.
func bullets(lines []string) *widget.Label {
	if len(lines) == 0 {
		lines = []string{"Nothing."}
	}
	l := widget.NewLabel("•  " + strings.Join(lines, "\n•  "))
	l.Wrapping = fyne.TextWrapWord
	return l
}

// apply lists the rows asked for, each section under its title.
func (v *compareView) apply() {
	v.shown = v.shown[:0]
	for _, sec := range diffSections {
		var rows []diffRow
		for _, r := range v.rows {
			if r.Section == sec && (r.Differs || !v.onlyDiff.Checked) {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			continue
		}
		v.shown = append(v.shown, diffRow{Section: sec, heading: true})
		v.shown = append(v.shown, rows...)
	}
	if len(v.shown) == 0 {
		v.empty.Show()
	} else {
		v.empty.Hide()
	}
	v.table.Refresh()
}

// fit sizes the name column to its longest name, within bounds.
func (v *compareView) fit() {
	pad := 2*theme.InnerPadding() + theme.Padding()
	w := float32(compareNameMin)
	for _, r := range v.rows {
		if m := fyne.MeasureText(r.Name, theme.TextSize(), fyne.TextStyle{Bold: true}).Width + pad; m > w {
			w = m
		}
	}
	v.table.SetColumnWidth(0, min(w, compareNameMax))
	v.runWidth = compareRun
	for _, s := range v.sides {
		if m := fyne.MeasureText(s.d.RunID, theme.TextSize(), fyne.TextStyle{Monospace: true}).Width + pad; m > v.runWidth {
			v.runWidth = m
		}
	}
	v.runWidth = min(v.runWidth, fitCap)
	for i := range v.sides {
		v.table.SetColumnWidth(i+1, v.runWidth)
	}
}

// paintCell fills one cell: a section's title, a row's name, or one run's
// value, highlighted where it differs from the first run's.
func (v *compareView) paintCell(id widget.TableCellID, bg *canvas.Rectangle, label *widget.Label) {
	bg.FillColor = color.Transparent
	label.TextStyle = fyne.TextStyle{}
	label.Importance = widget.MediumImportance
	text := ""
	if id.Row >= 0 && id.Row < len(v.shown) {
		r := v.shown[id.Row]
		switch {
		case r.heading:
			if id.Col == 0 {
				text = r.Section
				label.TextStyle.Bold = true
				label.Importance = widget.LowImportance
			}
		case id.Col == 0:
			text = r.Name
			label.TextStyle.Bold = r.Differs
		case id.Col-1 < len(r.Values):
			value := r.Values[id.Col-1]
			text = orDash(value)
			if value == "" {
				label.Importance = widget.LowImportance
			}
			if r.Changed[id.Col-1] {
				bg.FillColor = theme.Color(colorSelected)
				label.TextStyle.Bold = true
			}
		}
	}
	bg.Refresh()
	label.SetText(text)
}

// showRow shows each run's value of one row in full.
func (v *compareView) showRow(r diffRow) {
	if v.window == nil {
		return
	}
	box := container.NewVBox()
	for i, s := range v.sides {
		id := widget.NewLabelWithStyle(s.d.RunID, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		id.Importance = widget.LowImportance
		val := widget.NewLabelWithStyle(orDash(r.Values[i]), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		val.Wrapping = fyne.TextWrapBreak
		val.Selectable = true
		box.Add(id)
		box.Add(val)
	}
	d := dialog.NewCustom(r.Section+" · "+r.Name, "Close", container.NewVScroll(box), v.window)
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
}

// oneScenario says whether the runs are all of one scenario, which is what
// the reproduce judge compares.
func (v *compareView) oneScenario() bool {
	for _, s := range v.sides {
		if s.d.Scenario == "" || s.d.Scenario != v.sides[0].d.Scenario {
			return false
		}
	}
	return true
}

func (v *compareView) dirs() []string {
	out := make([]string, len(v.sides))
	for i, s := range v.sides {
		out[i] = s.d.Dir
	}
	return out
}
