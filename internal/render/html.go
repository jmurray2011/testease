package render

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
)

//go:embed page.html
var pageSource string

// odd is what stripes a row. Grouping puts each group in its own tbody, so a
// css nth-child would restart the pattern at every heading.
var page = template.Must(template.New("page").
	Funcs(template.FuncMap{"odd": func(n int) bool { return n%2 == 1 }}).
	Parse(pageSource))

// HTML writes a view as one self-contained page.
//
// It is the same Document the other encodings render, laid out for a screen
// rather than a terminal. That is the whole of its ambition: a view with a
// column per record and a value per cell is correct in a terminal and unusable
// there, because a column of hashes is wider than the window.
//
// IT IS STILL A PROJECTION. What the page decides for the reader is decided
// from STRUCTURE and from NAMES: a cell is dimmed where the document has no
// entry for it, rows are grouped by the part of their label before the first
// dot, and a run of readings is set as numbers when every one of them parses as
// one. None of that reads a value for its meaning. Nothing here is coloured by
// what a reading SAYS - an exit code, an HTTP status and a drift result all
// render alike, because deciding which of them is bad is the judge's work and a
// page that did it would be an opinion wearing the clothes of a rendering.
func HTML(data io.Writer, doc Document, title, footer string) error {
	view, err := pageOf(doc, title, footer)
	if err != nil {
		return err
	}
	if err := page.Execute(data, view); err != nil {
		return fmt.Errorf("render html: %w", err)
	}
	return nil
}

// pageView is what the template is given: the same rows, with absence, grouping
// and alignment resolved once here rather than guessed at in the markup.
type pageView struct {
	Title       string
	Footer      string
	View        string
	Columns     []string
	Notices     []string
	Groups      []pageGroup
	Grouped     bool
	RowCount    int
	ColumnCount int
	VaryCount   int
}

// pageGroup is a run of rows sharing the first part of their label. Ungrouped
// pages carry exactly one, unnamed.
type pageGroup struct {
	Name string
	Rows []pageRow

	// Loose rows follow a group without belonging to it: prefixes with a single
	// member, which a heading of their own would only pad out.
	Loose []pageRow
}

type pageRow struct {
	Label  string
	Values []pageCell
	Varies bool

	// Shared is the one reading every column carries, where they all carry the
	// same one. The row then says it once across the width instead of repeating
	// it, which is the same statement in less space - nothing is dropped and
	// nothing is dimmed, and Span says how many columns agreed.
	Shared string
	Span   int
}

type pageCell struct {
	Text    string
	Absent  bool
	Numeric bool
}

func pageOf(doc Document, title, footer string) (pageView, error) {
	if len(doc.Columns) == 0 {
		return pageView{}, fmt.Errorf("render html: the %s view named no columns", doc.View)
	}

	// The first column is the row's name in every view this tool has - the step,
	// the cell, the fact - so it is the one that stays put while the rest scroll.
	label, values := doc.Columns[0], doc.Columns[1:]

	view := pageView{
		Title:       title,
		Footer:      footer,
		View:        doc.View,
		Columns:     doc.Columns,
		Notices:     doc.Notices,
		RowCount:    len(doc.Rows),
		ColumnCount: len(doc.Columns),
	}

	rows := make([]pageRow, 0, len(doc.Rows))
	for _, row := range doc.Rows {
		out := pageRow{Label: row[label], Values: make([]pageCell, 0, len(values))}
		seen := map[string]bool{}
		for _, c := range values {
			text, ok := row[c]
			if !ok {
				// A column the record never mentioned. Records are never
				// rewritten, so the absence is permanent, and an empty cell
				// would read as an empty reading.
				out.Values = append(out.Values, pageCell{Text: absent, Absent: true})
				seen[absent] = true
				continue
			}
			out.Values = append(out.Values, pageCell{Text: text})
			seen[text] = true
		}
		// Whether this row holds more than one distinct rendering, which is what
		// the reader's filter offers to keep. It is a statement about these
		// strings and nothing else - not about what moved, and not about why.
		out.Varies = len(seen) > 1
		if out.Varies {
			view.VaryCount++
		} else if len(values) > 1 && len(out.Values) > 0 && !out.Values[0].Absent {
			// One column is not agreement across columns, and an absence said
			// four times is still worth seeing four times.
			out.Shared = out.Values[0].Text
			out.Span = len(out.Values)
		}
		rows = append(rows, out)
	}

	// A view with no rows has no groups either. One empty group would render as
	// a table with a header and nothing under it, which says less than saying so.
	if len(rows) > 0 {
		markNumeric(rows, len(values))
		view.Groups, view.Grouped = group(rows)
	}
	return view, nil
}

// markNumeric sets the readings that can line up as digits. A page is read
// across a row when the columns are records and down a column when they are
// steps, so a cell is set as a number when its whole row or its whole column is
// one. Mixing the two inside a block would leave the digits ragged again, which
// is the thing this is for.
//
// It is typography. Nothing about a value's meaning follows from parsing as a
// number, and nothing here treats one number differently from another.
func markNumeric(rows []pageRow, width int) {
	numericRow := make([]bool, len(rows))
	for i, row := range rows {
		numericRow[i] = allNumeric(func(yield func(pageCell) bool) {
			for _, c := range row.Values {
				if !yield(c) {
					return
				}
			}
		})
	}

	numericColumn := make([]bool, width)
	for c := range width {
		numericColumn[c] = allNumeric(func(yield func(pageCell) bool) {
			for _, row := range rows {
				if c < len(row.Values) && !yield(row.Values[c]) {
					return
				}
			}
		})
	}

	for i, row := range rows {
		for c := range row.Values {
			if rows[i].Values[c].Absent {
				continue
			}
			if numericRow[i] || (c < len(numericColumn) && numericColumn[c]) {
				rows[i].Values[c].Numeric = true
			}
		}
	}
}

// allNumeric is true when every reading present parses as a number. An absent
// cell is skipped rather than counted against, and a run with no readings at all
// is not a run of numbers.
func allNumeric(cells func(func(pageCell) bool)) bool {
	any := false
	ok := true
	cells(func(c pageCell) bool {
		if c.Absent {
			return true
		}
		if _, err := strconv.ParseFloat(strings.TrimSpace(c.Text), 64); err != nil {
			ok = false
			return false
		}
		any = true
		return true
	})
	return ok && any
}

// group splits rows on the part of their label before the first dot, which this
// tool's own fact keys are built from. It is a name, not a reading.
//
// Grouping is skipped where it buys nothing: one group is a wrapper around the
// whole table, and a group per row is a heading per row. Views whose labels
// carry no dot - a step number, a census cell - fall into the second.
func group(rows []pageRow) ([]pageGroup, bool) {
	flat := []pageGroup{{Rows: rows}}

	var groups []pageGroup
	at := map[string]int{}
	for _, row := range rows {
		name, _, dotted := strings.Cut(row.Label, ".")
		if !dotted {
			return flat, false
		}
		i, seen := at[name]
		if !seen {
			at[name] = len(groups)
			groups = append(groups, pageGroup{Name: name})
			i = len(groups) - 1
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}

	// A heading over one row costs a row and saves none, so a prefix with a
	// single member is left where it falls rather than given a header.
	kept := make([]pageGroup, 0, len(groups))
	for _, g := range groups {
		if len(g.Rows) < 2 {
			if n := len(kept); n > 0 {
				kept[n-1].Loose = append(kept[n-1].Loose, g.Rows...)
				continue
			}
			kept = append(kept, pageGroup{Loose: g.Rows})
			continue
		}
		kept = append(kept, g)
	}

	named := 0
	for _, g := range kept {
		if g.Name != "" {
			named++
		}
	}
	if named < 2 {
		return flat, false
	}
	return kept, true
}
