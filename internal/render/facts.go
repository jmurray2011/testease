package render

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jmurray2011/testease/internal/record"
)

// FactsOf renders fact keys across several records: a column per record and a
// row per key, optionally narrowed to the requested keys.
//
// A census has Rows because its cells were scheduled together. Work that cannot
// be a census had nothing: a fixture ladder is captured as separate runs,
// because each rung's input is the rung below's published output, and the
// comparison those runs exist for lives across their records rather than inside
// any one of them.
//
// IT DERIVES NOTHING. Every cell here is a value some probe recorded, laid
// beside the others. The moment this view subtracted two columns it would be
// making a claim no probe made, on a line that looks exactly like the ones that
// were measured. A delta worth reading is a fact worth recording.
func FactsOf(data, notices io.Writer, recs []*record.Record, want []string) error {
	for _, rec := range recs {
		if err := writeFactsNotices(notices, rec); err != nil {
			return err
		}
	}

	columns, rows, err := factRowsOf(recs, want)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(data, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(columns, "\t")); err != nil {
		return fmt.Errorf("render facts: %w", err)
	}
	for _, row := range rows {
		line := make([]string, 0, len(columns))
		for _, c := range columns {
			value, ok := row[c]
			if !ok {
				// A record that never mentioned the key does not carry it, and
				// records are never rewritten, so the absence is permanent. An
				// empty column would read as an empty reading and a zero would
				// read as a measurement.
				value = absent
			}
			line = append(line, value)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(line, "\t")); err != nil {
			return fmt.Errorf("render facts: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("render facts: %w", err)
	}
	return nil
}

// ProjectFactsOf is the cross-record view narrowed to the keys asked for.
func ProjectFactsOf(recs []*record.Record, want []string) (Document, error) {
	doc := Document{View: "facts", Rows: []map[string]string{}, Notices: []string{}}

	var notices strings.Builder
	for _, rec := range recs {
		if err := writeFactsNotices(&notices, rec); err != nil {
			return doc, err
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(notices.String(), "\n"), "\n") {
		if line != "" {
			doc.Notices = append(doc.Notices, line)
		}
	}

	columns, rows, err := factRowsOf(recs, want)
	if err != nil {
		return doc, err
	}
	doc.Columns = columns
	doc.Rows = append(doc.Rows, rows...)
	return doc, nil
}

// factRows is the cross-record projection: the run ids as columns, every key any
// of them recorded as rows.
//
// COLUMN ORDER IS ARGUMENT ORDER, not sorted. A ladder is read oldest rung
// first, and run ids fall into that order only by accident - they stop doing so
// the moment a rung is recaptured. Keys ARE sorted, so the same records render
// the same way twice whatever order the probes emitted them in.
func factRowsOf(recs []*record.Record, want []string) ([]string, []map[string]string, error) {
	columns := make([]string, 0, len(recs)+1)
	columns = append(columns, "fact")

	readings := make([]map[string]string, 0, len(recs))
	names := map[string]bool{}
	seen := map[string]bool{}
	for _, rec := range recs {
		id := runID(rec)
		// Two columns under one name is a lossy row rather than a wide one: the
		// second record's readings would land on top of the first's and the
		// table would show them agreeing.
		if seen[id] {
			return nil, nil, fmt.Errorf("render facts: %s was given twice, and two columns cannot share a name", id)
		}
		seen[id] = true

		columns = append(columns, id)
		// The attempt that stands. A resumed record's earlier attempts stay in
		// the file as evidence, and a projection reads one story, not a blend.
		facts, err := recordFacts(rec.Latest().Events)
		if err != nil {
			return nil, nil, err
		}
		readings = append(readings, facts)
		for name := range facts {
			names[name] = true
		}
	}

	if len(want) > 0 {
		// A key no record mentioned is still a row, rendered absent in every
		// column. Leaving it out would answer a narrower question than the one
		// asked, and the table would look like the key was never sought.
		names = map[string]bool{}
		for _, name := range want {
			names[name] = true
		}
	}
	keys := make([]string, 0, len(names))
	for name := range names {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	rows := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		row := map[string]string{"fact": key}
		for i, facts := range readings {
			if value, ok := facts[key]; ok {
				row[columns[i+1]] = value
			}
		}
		rows = append(rows, row)
	}
	return columns, rows, nil
}

// runID is the name a record answers to everywhere else - in a citation, in the
// runs directory, in publish - so it is what a column is headed with.
func runID(rec *record.Record) string { return filepath.Base(rec.Dir) }

// writeFactsNotices adds the one thing this view has to say for itself: that a
// census's facts are not in the record this view reads.
//
// A census parent schedules cells and its cells do the measuring, so rendered
// here it prints a header and nothing under it - which is exactly what a
// census that measured nothing prints. Naming the view that would answer is
// the difference between an empty table and an empty answer.
func writeFactsNotices(w io.Writer, rec *record.Record) error {
	if err := writeNamedNotices(w, rec); err != nil {
		return err
	}
	if len(censusCells(rec)) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w,
		"%s: warning: this is a census - its facts are in its cells, and this view reads a record's own; render --view rows %s is the census view\n",
		runID(rec), runID(rec)); err != nil {
		return fmt.Errorf("render notices: %w", err)
	}
	return nil
}

// writeNamedNotices says which record a warning is about. Several records render
// together here, and "this run was interrupted" names none of them.
func writeNamedNotices(w io.Writer, rec *record.Record) error {
	var b bytes.Buffer
	if err := writeNotices(&b, rec); err != nil {
		return err
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(b.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", runID(rec), line); err != nil {
			return fmt.Errorf("render notices: %w", err)
		}
	}
	return nil
}
