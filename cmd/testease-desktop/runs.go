package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
)

// The Runs screen's columns.
const (
	colRun = iota
	colMeasured
	colStatus
	colVerdict
	colFate
	columns
)

var (
	// No Scenario column: a run id starts with its scenario by contract, and
	// the filter matches on scenario all the same.
	columnTitles = [columns]string{"Run", "Measured", "Status", "Latest verdict", "Target fate"}
	columnWidths = [columns]float32{260, 85, 105, 240, 180}
)

// runsView lists the records under a runs directory, newest first.
type runsView struct {
	dir    string
	onOpen func(dir string)

	all   []browse.Run
	shown []browse.Run
	// status is read once per reload: for a record that has not closed it
	// asks the lock, which is not something to do on every repaint.
	status map[string]string
	// widths are the columns' current widths. Run and Scenario grow to fit
	// what is in them, up to a cap, so a run id is not cut off.
	widths [columns]float32

	filter   *widget.Entry
	scenario *widget.Select
	outcome  *widget.Select
	since    *widget.Select
	clearAll *widget.Button
	// choosing is set while the screen sets its own selects, so their
	// change handlers do not re-filter or overwrite what was remembered.
	choosing bool
	now      func() time.Time
	count    *widget.Label
	message  *widget.Label
	table    *widget.Table
	// compare puts runs picked from the listing side by side.
	compare *action
	content fyne.CanvasObject
}

func newRunsView(dir string, onOpen func(dir string)) *runsView {
	v := &runsView{dir: dir, onOpen: onOpen, now: time.Now}

	title := widget.NewLabelWithStyle("Runs", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameSubHeadingText
	v.count = widget.NewLabel("")
	v.count.Importance = widget.LowImportance

	v.filter = widget.NewEntry()
	v.filter.OnChanged = func(string) { v.apply() }
	// Refresh is an icon alone, so the header keeps room for the filters.
	refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), v.reload)

	v.message = widget.NewLabel("")
	v.message.Wrapping = fyne.TextWrapWord
	v.message.Hide()

	v.table = widget.NewTableWithHeaders(
		func() (int, int) { return len(v.shown), columns },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewStack(label, container.NewHBox(newPill("", pillNeutral)))
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			stack := cell.(*fyne.Container)
			label := stack.Objects[0].(*widget.Label)
			box := stack.Objects[1].(*fyne.Container)
			p := box.Objects[0].(*pill)
			text, kind, isPill := v.cell(id.Row, id.Col)
			if isPill {
				label.Hide()
				box.Show()
				p.Set(text, kind)
				return
			}
			box.Hide()
			label.Show()
			label.TextStyle = fyne.TextStyle{Monospace: id.Col == colRun}
			label.SetText(text)
		},
	)
	v.table.ShowHeaderColumn = false
	v.table.CreateHeader = func() fyne.CanvasObject {
		l := widget.NewLabel("")
		l.Importance = widget.LowImportance
		return l
	}
	v.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < columns {
			o.(*widget.Label).SetText(columnTitles[id.Col])
		}
	}
	v.widths = columnWidths
	v.table.OnSelected = func(id widget.TableCellID) {
		v.table.UnselectAll()
		if id.Row >= 0 && id.Row < len(v.shown) {
			v.onOpen(v.shown[id.Row].Dir)
		}
	}

	prefs := fyne.CurrentApp().Preferences()
	choose := func(key string) func(string) {
		return func(picked string) {
			if v.choosing {
				return
			}
			if picked == anyScenario || picked == anyOutcome || picked == anyTime {
				prefs.RemoveValue(key)
			} else {
				prefs.SetString(key, picked)
			}
			v.apply()
		}
	}
	v.scenario = widget.NewSelect([]string{anyScenario}, choose(prefRunsScenario))
	v.outcome = widget.NewSelect(outcomeChoices(), choose(prefRunsOutcome))
	v.since = widget.NewSelect(sinceChoices, choose(prefRunsSince))
	v.choosing = true
	v.scenarioOptions(nil, firstSet(prefs.String(prefRunsScenario), anyScenario))
	v.outcome.SetSelected(firstSet(prefs.String(prefRunsOutcome), anyOutcome))
	v.since.SetSelected(firstSet(prefs.String(prefRunsSince), anyTime))
	v.choosing = false
	v.clearAll = widget.NewButtonWithIcon("Clear", theme.ContentClearIcon(), v.clear)
	v.compare = newAction("Compare…", theme.ListIcon())

	v.filter.SetPlaceHolder("Search runs")
	height := v.filter.MinSize().Height
	header := container.NewHBox(title, v.count, layout.NewSpacer(),
		container.NewGridWrap(fyne.NewSize(180, height), v.scenario),
		container.NewGridWrap(fyne.NewSize(180, height), v.outcome),
		container.NewGridWrap(fyne.NewSize(140, height), v.since),
		container.NewGridWrap(fyne.NewSize(180, height), v.filter),
		v.clearAll, v.compare.Button, refresh)
	footer := widget.NewLabel("Status is what the run measured. Verdicts come only from judges; a run with no verdict has not been judged yet.")
	footer.Importance = widget.LowImportance
	footer.Wrapping = fyne.TextWrapWord

	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(header), widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), footer),
		nil, nil,
		v.table,
	)
	v.reload()
	return v
}

// reload reads the runs directory again.
func (v *runsView) reload() {
	runs, err := browse.List(v.dir)
	switch {
	case v.dir == "":
		v.all = nil
		v.showMessage("Choose the folder your records are written to. testease calls it the runs directory (--runs-dir, TESTEASE_RUNS_DIR).")
	case errors.Is(err, os.ErrNotExist):
		v.all = nil
		v.showMessage("No runs directory at " + v.dir)
	case err != nil:
		v.all = nil
		v.showMessage(fmt.Sprintf("Could not read %s: %v", v.dir, err))
	default:
		v.all = runs
		if len(runs) == 0 {
			v.showMessage("No records under " + v.dir + " yet.")
		} else {
			v.message.Hide()
		}
	}

	v.status = make(map[string]string, len(v.all))
	for _, r := range v.all {
		v.status[r.Dir] = statusOf(r)
	}
	// Newest first, by the timestamp every run id carries.
	sort.SliceStable(v.all, func(i, j int) bool {
		a, b := takenAt(v.all[i].RunID), takenAt(v.all[j].RunID)
		if a != b {
			return a > b
		}
		return v.all[i].RunID < v.all[j].RunID
	})
	v.scenarioOptions(v.all, v.scenario.Selected)
	v.fit()
	v.apply()
}

// scenarioOptions offers every scenario in the listing, and keeps the one
// chosen even when none of its runs is here.
func (v *runsView) scenarioOptions(runs []browse.Run, chosen string) {
	seen := map[string]bool{}
	var names []string
	for _, r := range runs {
		if r.Scenario != "" && !seen[r.Scenario] {
			seen[r.Scenario] = true
			names = append(names, r.Scenario)
		}
	}
	if chosen != "" && chosen != anyScenario && !seen[chosen] {
		names = append(names, chosen)
	}
	sort.Strings(names)
	was := v.choosing
	v.choosing = true
	v.scenario.Options = append([]string{anyScenario}, names...)
	v.scenario.SetSelected(firstSet(chosen, anyScenario))
	v.choosing = was
}

// fitCap is as wide as the Run column gets: what is left beside the sections
// and the other columns in the window the app opens at. Past it, an id is cut
// with an ellipsis like any other text.
const fitCap = 520

// fit widens the Run column to the longest run id.
func (v *runsView) fit() {
	pad := 2*theme.InnerPadding() + theme.Padding()
	widest := func(col int, style fyne.TextStyle, text func(browse.Run) string) float32 {
		w := columnWidths[col]
		for _, r := range v.all {
			if m := fyne.MeasureText(text(r), theme.TextSize(), style).Width + pad; m > w {
				w = m
			}
		}
		return min(w, fitCap)
	}
	v.widths[colRun] = widest(colRun, fyne.TextStyle{Monospace: true}, func(r browse.Run) string { return r.RunID })
	for i, w := range v.widths {
		v.table.SetColumnWidth(i, w)
	}
}

func (v *runsView) showMessage(text string) {
	v.message.SetText(text)
	v.message.Show()
}

// apply narrows the listing to what the filter matches.
func (v *runsView) apply() {
	f := v.current()
	now := v.now()
	status := func(r browse.Run) string { return v.status[r.Dir] }
	v.shown = v.shown[:0]
	for _, r := range v.all {
		if f.matches(r, status, now) {
			v.shown = append(v.shown, r)
		}
	}
	if !f.active() {
		v.count.SetText(plural(len(v.all), "record"))
		v.clearAll.Disable()
	} else {
		v.count.SetText(fmt.Sprintf("%d of %s", len(v.shown), plural(len(v.all), "record")))
		v.clearAll.Enable()
	}
	v.table.Refresh()
}

func (v *runsView) current() runFilter {
	return runFilter{Text: v.filter.Text, Scenario: v.scenario.Selected, Outcome: v.outcome.Selected, Since: v.since.Selected}
}

// clear shows every record again, and forgets the choices.
func (v *runsView) clear() {
	prefs := fyne.CurrentApp().Preferences()
	for _, k := range []string{prefRunsScenario, prefRunsOutcome, prefRunsSince} {
		prefs.RemoveValue(k)
	}
	v.choosing = true
	v.scenario.SetSelected(anyScenario)
	v.outcome.SetSelected(anyOutcome)
	v.since.SetSelected(anyTime)
	v.filter.SetText("")
	v.choosing = false
	v.apply()
}

// onlyScenario lists one scenario's runs, whatever else was chosen.
func (v *runsView) onlyScenario(name string) {
	v.choosing = true
	v.outcome.SetSelected(anyOutcome)
	v.since.SetSelected(anyTime)
	v.filter.SetText("")
	v.choosing = false
	v.scenarioOptions(v.all, name)
	fyne.CurrentApp().Preferences().SetString(prefRunsScenario, name)
	v.apply()
}

// cell is what one cell of the listing says, and whether it is a pill.
func (v *runsView) cell(row, col int) (text string, kind pillKind, isPill bool) {
	if row < 0 || row >= len(v.shown) {
		return "", pillNeutral, false
	}
	r := v.shown[row]
	switch col {
	case colRun:
		return r.RunID, pillNeutral, false
	case colMeasured:
		return measuredText(r), pillNeutral, false
	case colStatus:
		s := v.status[r.Dir]
		return s, statusKind(s), true
	case colVerdict:
		text, kind := verdictSummary(r.Verdicts)
		return text, kind, len(r.Verdicts) > 0
	case colFate:
		return fatesText(r.Targets), pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *runsView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}
