package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// matrixCell is one cell of a matrix run as the grid shows it. Present is
// false for a slot in the scope that has no cell: excluded, or not reached.
type matrixCell struct {
	Key, Dir, Status string
	Outcome          outcome
	Present          bool
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

// matrixCells reads each cell of a matrix run: its own record says what it
// came to, verdicts included. A cell with no record yet has only its status.
func matrixCells(d browse.Detail) []matrixCell {
	out := make([]matrixCell, 0, len(d.CellList))
	for _, c := range d.CellList {
		mc := matrixCell{Key: c.Key, Dir: c.Dir, Status: c.Status, Present: true}
		if cd, err := browse.Open(c.Dir); err == nil {
			mc.Outcome = outcomeOf(cd.Run)
		} else {
			status := c.Status
			if status == "" {
				status = string(record.StatusUnfinished)
			}
			mc.Outcome = outcomeOf(browse.Run{Status: status})
		}
		out = append(out, mc)
	}
	return out
}

// Sizes of the grid.
const (
	heatTileW  = 116
	heatTileH  = 38
	heatLabelW = 110
)

// heatmapView draws a matrix run's grid. A tapped cell opens its record.
type heatmapView struct {
	h       heatmap
	dir     string
	summary *widget.Label
	onOpen  func(dir string)
	content fyne.CanvasObject
}

func newHeatmapView(d browse.Detail, m *record.Matrix, onOpen func(string)) *heatmapView {
	cells := matrixCells(d)
	v := &heatmapView{h: buildHeatmap(m, cells), dir: d.Dir, onOpen: onOpen}
	v.summary = widget.NewLabel(tally(cells))

	axisNote := ""
	if v.h.RowAxis != "" {
		axisNote = v.h.RowAxis + " down the side"
		if len(v.h.ColAxes) > 0 {
			axisNote += ", " + strings.Join(v.h.ColAxes, " · ") + " across"
		}
	}
	corner := widget.NewLabelWithStyle(v.h.RowAxis, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	corner.Truncation = fyne.TextTruncateEllipsis
	header := []fyne.CanvasObject{sized(heatLabelW, heatTileH, corner)}
	for _, c := range v.h.Cols {
		l := widget.NewLabelWithStyle(c, fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
		l.Truncation = fyne.TextTruncateEllipsis
		header = append(header, sized(heatTileW, heatTileH, l))
	}
	grid := container.NewVBox(container.NewHBox(header...))
	for i, r := range v.h.Rows {
		l := widget.NewLabelWithStyle(r, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		l.Truncation = fyne.TextTruncateEllipsis
		row := []fyne.CanvasObject{sized(heatLabelW, heatTileH, l)}
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
	word := c.Outcome.String()
	switch c.Outcome {
	case outcomeBroken:
		word = "broke"
	case outcomeIncomplete:
		word = "unfinished"
		if c.Status == "" {
			word = "running"
		}
	}
	return newOutcomeTile("", word, lookOf(c.Outcome), func() { v.open(c) })
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
