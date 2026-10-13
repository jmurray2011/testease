package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// absent is how every view says a record has no entry for a column. A Document
// leaves the key out of the row entirely, and each encoding puts this back: an
// empty cell would read as an empty reading, and a zero as a measurement.
const absent = "-"

// Document is a view, encoded for something that will parse it rather than read
// it. Values are the view's own - the seven columns collapse distinctions the
// events keep, and this is the same rendering in a different encoding, not a
// second opinion about the record. events.jsonl stays the typed source.
type Document struct {
	View    string              `json:"view"`
	Columns []string            `json:"columns"`
	Rows    []map[string]string `json:"rows"`

	// Notices are in the document because a json consumer reads one stream. In
	// the text views they are a separate channel so a warning cannot be parsed
	// as a row; here that separation is the field name.
	Notices []string `json:"notices"`
}

// DocumentJSON writes an already-made Document, for a caller that projected it
// with something the view itself does not know - which facts to show.
func DocumentJSON(data io.Writer, doc Document) error {
	return encodeJSON(data, doc)
}

// ProjectFiltered is the one place a view becomes columns and rows, optionally
// narrowed to requested facts. Only the census view has facts to narrow; the
// step views ignore them.
func ProjectFiltered(rec *record.Record, view string, facts []string) (Document, error) {
	doc := Document{View: view, Rows: []map[string]string{}, Notices: []string{}}

	var notices strings.Builder
	if err := writeNotices(&notices, rec); err != nil {
		return doc, err
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(notices.String(), "\n"), "\n") {
		if line != "" {
			doc.Notices = append(doc.Notices, line)
		}
	}

	switch view {
	case "tsv", "table":
		rows, err := stepRows(rec)
		if err != nil {
			return doc, err
		}
		doc.Columns = header
		for _, r := range rows {
			doc.Rows = append(doc.Rows, zip(header, r.fields()))
		}

	case "rows":
		columns, lines, err := cellRowsOf(rec, facts)
		if err != nil {
			return doc, err
		}
		doc.Columns = columns
		doc.Rows = append(doc.Rows, lines...)

	default:
		return doc, fmt.Errorf("no such view %q; there is tsv, table and rows", view)
	}
	return doc, nil
}

func encodeJSON(data io.Writer, doc Document) error {
	enc := json.NewEncoder(data)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}

// cellRowsOf is the census projection narrowed to the facts asked for. Nil
// takes every fact any cell recorded.
//
// SELECTING IS NOT DERIVING. A 136-cell sweep records dozens of keys per cell
// and the table is then thousands of columns wide, which is a projection
// nobody can read - so the reader says which readings the question is about.
// Every value shown is still one a probe recorded, laid beside the others; a
// key nobody asked for is left out of the view and stays in the record.
func cellRowsOf(rec *record.Record, want []string) ([]string, []map[string]string, error) {
	cells := censusCells(rec)

	keys := make([]string, 0, len(cells))
	for key := range cells {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	names := map[string]bool{}
	for _, key := range keys {
		facts, err := cellFacts(rec.Dir, key)
		if err != nil {
			return nil, nil, err
		}
		cells[key].facts = facts
		for name := range facts {
			names[name] = true
		}
	}
	if len(want) > 0 {
		// A key nobody recorded is still a column, and every cell renders it
		// absent. Dropping it would answer a narrower question than the one
		// asked, and silently: the table would look complete.
		names = map[string]bool{}
		for _, name := range want {
			names[name] = true
		}
	}
	factColumns := make([]string, 0, len(names))
	for name := range names {
		factColumns = append(factColumns, name)
	}
	sort.Strings(factColumns)

	columns := append([]string{"cell", "status"}, factColumns...)
	rows := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		cell := cells[key]
		row := map[string]string{"cell": key, "status": string(cell.status)}
		for _, name := range factColumns {
			// A cell that never reported a fact did not report it. An empty
			// string would read as an empty reading.
			if value, ok := cell.facts[name]; ok {
				row[name] = value
			}
		}
		rows = append(rows, row)
	}
	return columns, rows, nil
}

func zip(columns, values []string) map[string]string {
	row := make(map[string]string, len(columns))
	for i, c := range columns {
		if i < len(values) {
			row[c] = values[i]
		}
	}
	return row
}
