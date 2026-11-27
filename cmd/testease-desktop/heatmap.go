package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// matrixCell is one cell of a matrix run as the grid shows it. Present is
// false for a slot in the scope that has no cell: excluded, or not reached.
type matrixCell struct {
	Key, Dir, Status string
	Outcome          outcome
	Present          bool
	// Word is what the judge said of the cell, when one did, and By names
	// the judge: a cell's own verdict, or its matrix run's verdict of it.
	Word, By string
}

// heatmap is a matrix run laid out by its axes: the first axis by name down
// the side, the others combined across, as the runner enumerates them.
type heatmap struct {
	RowAxis  string
	ColAxes  []string
	Rows     []string
	Cols     []string
	Cells    [][]matrixCell
	Unplaced []matrixCell
}

// keyUnsafe is what a cell key cannot hold, and keyPart files a value the way
// the runner does: the record's matrix scope keeps the values, and a cell's
// folder is named by them, filed.
var keyUnsafe = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

func keyPart(value string) string {
	return strings.Trim(keyUnsafe.ReplaceAllString(value, "-"), "-")
}

type combo struct{ label, key string }

// buildHeatmap places each cell by its key. A run with no recorded scope is
// a column of its cells by key; a cell the scope has no slot for is kept in
// Unplaced, not dropped.
func buildHeatmap(m *record.Matrix, cells []matrixCell) heatmap {
	var h heatmap
	used := make([]bool, len(cells))
	find := func(key string) matrixCell {
		for i, c := range cells {
			if c.Key == key && !used[i] {
				used[i] = true
				return c
			}
		}
		return matrixCell{Key: key}
	}
	if m == nil || len(m.Axes) == 0 {
		h.Cols = []string{""}
		for i, c := range cells {
			h.Rows = append(h.Rows, c.Key)
			h.Cells = append(h.Cells, []matrixCell{c})
			used[i] = true
		}
		return h
	}

	names := make([]string, 0, len(m.Axes))
	for n := range m.Axes {
		names = append(names, n)
	}
	sort.Strings(names)
	h.RowAxis, h.ColAxes = names[0], names[1:]

	combos := []combo{{}}
	for _, n := range h.ColAxes {
		var next []combo
		for _, c := range combos {
			for _, v := range m.Axes[n] {
				label, key := v, keyPart(v)
				if c.key != "" {
					label, key = c.label+" · "+v, c.key+"+"+key
				}
				next = append(next, combo{label, key})
			}
		}
		combos = next
	}
	for _, c := range combos {
		h.Cols = append(h.Cols, c.label)
	}
	for _, rv := range m.Axes[h.RowAxis] {
		h.Rows = append(h.Rows, rv)
		row := make([]matrixCell, len(combos))
		for j, c := range combos {
			key := keyPart(rv)
			if c.key != "" {
				key += "+" + c.key
			}
			row[j] = find(key)
		}
		h.Cells = append(h.Cells, row)
	}
	for i, c := range cells {
		if !used[i] {
			h.Unplaced = append(h.Unplaced, c)
		}
	}
	return h
}

// tally counts a matrix's cells by what they came to.
func tally(cells []matrixCell) string {
	counts := map[outcome]int{}
	n := 0
	for _, c := range cells {
		if c.Present {
			counts[c.Outcome]++
			n++
		}
	}
	var parts []string
	for _, o := range []outcome{outcomePassed, outcomeFailed, outcomeBroken, outcomeUnjudged, outcomeIncomplete} {
		if counts[o] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[o], o))
		}
	}
	if len(parts) == 0 {
		return plural(n, "cell")
	}
	return plural(n, "cell") + ": " + strings.Join(parts, ", ")
}

// matrixVerdictLines is the Verdicts panel of a matrix run that has no
// verdict of its own but whose cells were judged one by one. Nothing is said
// when no cell has a verdict either: the run is then simply not judged.
func matrixVerdictLines(cells []matrixCell) []string {
	judgedCells := 0
	for _, c := range cells {
		if c.Present && (c.Outcome == outcomePassed || c.Outcome == outcomeFailed) {
			judgedCells++
		}
	}
	if judgedCells == 0 {
		return nil
	}
	counts := tally(cells)
	if _, after, ok := strings.Cut(counts, ": "); ok {
		counts = after
	}
	return []string{
		"No verdict on the run as a whole yet.",
		"Its cells were judged one by one, as the grid shows: " + counts + ".",
	}
}

// matrixCells reads each cell of a matrix run: its own record says what it
// came to, verdicts included. A cell a matrix judge read from the parent has
// its verdict in the parent's verdict, which counts where the cell has none
// of its own. A cell with no record yet has only its status.
func matrixCells(d browse.Detail, running bool) []matrixCell {
	judged := censusVerdicts(d.Dir)
	out := make([]matrixCell, 0, len(d.CellList))
	for _, c := range d.CellList {
		mc := matrixCell{Key: c.Key, Dir: c.Dir, Status: c.Status, Present: true}
		if cd, err := browse.Open(c.Dir); err == nil {
			mc.Outcome = outcomeOf(cd.Run)
			if mc.Outcome == outcomeFailed {
				mc.Word = attentionWord(cd.Run)
			}
		} else {
			status := c.Status
			if status == "" {
				status = string(record.StatusUnfinished)
			}
			mc.Outcome = outcomeOf(browse.Run{Status: status})
		}
		if cj, ok := judged[c.Key]; ok && mc.Outcome == outcomeUnjudged {
			mc.Outcome, mc.Word, mc.By = outcomeFailed, cj.Verdict, cj.Judge
			if cj.Passed {
				mc.Outcome = outcomePassed
			}
		}
		mc.Word = cellWord(mc, running)
		out = append(out, mc)
	}
	return out
}

// cellWord is what a cell's tile says: the judge's own word for a failure,
// and "running" only while the matrix run is still being written.
func cellWord(c matrixCell, running bool) string {
	switch c.Outcome {
	case outcomeFailed:
		if c.Word != "" {
			return c.Word
		}
		return "failed"
	case outcomeBroken:
		return "broke"
	case outcomeIncomplete:
		if running && c.Status == "" {
			return "running"
		}
		return "unfinished"
	default:
		return c.Outcome.String()
	}
}

// cellJudgment is a matrix judge's verdict of one cell, as the parent
// record keeps it.
type cellJudgment struct {
	Judge, Verdict string
	Passed         bool
	Because        []string
}

// censusVerdicts reads the per-cell verdicts a shipped judge wrote into a
// matrix run's own record. A verdict counts only over the events on disk
// now: the parent's, and the cell's where the verdict pinned them. Where two
// judges speak of one cell, one that did not pass is the one kept.
func censusVerdicts(dir string) map[string]cellJudgment {
	body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
	if err != nil {
		return nil
	}
	events := record.HashBytes(body)
	entries, err := os.ReadDir(filepath.Join(dir, judge.VerdictsDir))
	if err != nil {
		return nil
	}
	out := map[string]cellJudgment{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, judge.VerdictsDir, e.Name()))
		if err != nil {
			continue
		}
		var v judge.Verdict
		if json.Unmarshal(raw, &v) != nil || v.Judge == "" || v.Evidence.Events != events {
			continue
		}
		if _, err := judge.ByName(v.Judge); err != nil {
			continue
		}
		for key, sv := range v.Cells {
			if pinned := v.Evidence.Cells[key]; pinned != "" && filepath.IsLocal(key) {
				cell, err := os.ReadFile(filepath.Join(dir, record.CellsDir, key, record.EventsFile))
				if err != nil || record.HashBytes(cell) != pinned {
					continue
				}
			}
			cj := cellJudgment{Judge: v.Judge + "@" + v.Version, Verdict: sv.Verdict, Passed: judge.Passed(sv.Verdict), Because: sv.Because}
			if was, ok := out[key]; ok && !was.Passed {
				continue
			}
			out[key] = cj
		}
	}
	return out
}

// cellVerdictLines says what a matrix run's judge said of a cell, for the
// cell's own record, when the cell sits under a matrix run.
func cellVerdictLines(dir string) []string {
	parent := filepath.Dir(filepath.Dir(dir))
	if filepath.Base(filepath.Dir(dir)) != record.CellsDir {
		return nil
	}
	cj, ok := censusVerdicts(parent)[filepath.Base(dir)]
	if !ok {
		return nil
	}
	lines := []string{cj.Judge + " " + cj.Verdict + ", judged as a cell of " + filepath.Base(parent)}
	return append(lines, cj.Because...)
}

// Sizes of the grid.
const (
	heatTileW  = 116
	heatTileH  = 38
	heatLabelW = 110
	// heatLabelMax is as wide as the side labels get before they are cut.
	heatLabelMax = 240
)

// heatmapView draws a matrix run's grid. A tapped cell opens its record.
type heatmapView struct {
	h       heatmap
	cells   []matrixCell
	dir     string
	summary *widget.Label
	onOpen  func(dir string)
	content fyne.CanvasObject
}

func newHeatmapView(d browse.Detail, m *record.Matrix, onOpen func(string)) *heatmapView {
	cells := matrixCells(d, statusOf(d.Run) == statusRunning)
	v := &heatmapView{h: buildHeatmap(m, cells), cells: cells, dir: d.Dir, onOpen: onOpen}
	v.summary = widget.NewLabel(tally(cells))

	axisNote := ""
	if v.h.RowAxis != "" {
		axisNote = v.h.RowAxis + " down the side"
		if len(v.h.ColAxes) > 0 {
			axisNote += ", " + strings.Join(v.h.ColAxes, " · ") + " across"
		}
	}
	labelW := float32(heatLabelW)
	pad := 2*theme.InnerPadding() + theme.Padding()
	for _, t := range append([]string{v.h.RowAxis}, v.h.Rows...) {
		if t == "" {
			continue
		}
		labelW = max(labelW, fyne.MeasureText(t, theme.TextSize(), fyne.TextStyle{Monospace: true}).Width+pad)
	}
	labelW = min(labelW, heatLabelMax)
	corner := widget.NewLabelWithStyle(v.h.RowAxis, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	corner.Truncation = fyne.TextTruncateEllipsis
	header := []fyne.CanvasObject{sized(labelW, heatTileH, corner)}
	for _, c := range v.h.Cols {
		l := widget.NewLabelWithStyle(c, fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
		l.Truncation = fyne.TextTruncateEllipsis
		header = append(header, sized(heatTileW, heatTileH, l))
	}
	grid := container.NewVBox(container.NewHBox(header...))
	for i, r := range v.h.Rows {
		l := widget.NewLabelWithStyle(r, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		l.Truncation = fyne.TextTruncateEllipsis
		row := []fyne.CanvasObject{sized(labelW, heatTileH, l)}
		for _, c := range v.h.Cells[i] {
			row = append(row, sized(heatTileW, heatTileH, v.cellTile(c)))
		}
		grid.Add(container.NewHBox(row...))
	}

	top := container.NewVBox(v.summary, legend())
	if axisNote != "" {
		top.Add(note(axisNote + ". Tap a cell to open its record."))
	}
	if len(v.h.Unplaced) > 0 {
		var keys []string
		for _, c := range v.h.Unplaced {
			keys = append(keys, c.Key)
		}
		top.Add(note("Cells the recorded scope has no place for: " + strings.Join(keys, ", ") + ". They are in the list on the left."))
	}
	v.content = container.NewBorder(container.NewPadded(top), nil, nil, nil, container.NewScroll(container.NewPadded(grid)))
	return v
}

func (v *heatmapView) cellTile(c matrixCell) fyne.CanvasObject {
	if !c.Present {
		return newOutcomeTile("", "—", lookEmpty, nil)
	}
	return newOutcomeTile("", c.Word, lookOf(c.Outcome), func() { v.open(c) })
}

// open opens a cell's record. A cell's folder is under its parent's; one
// that would leave the record is not followed.
func (v *heatmapView) open(c matrixCell) {
	if v.onOpen == nil || !c.Present {
		return
	}
	if rel, err := filepath.Rel(v.dir, c.Dir); err == nil && filepath.IsLocal(rel) {
		v.onOpen(c.Dir)
	}
}
