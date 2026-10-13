package render

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/jmurray2011/testease/internal/record"
)

// RowsOf renders a census: one line per cell, its status, and the requested
// facts its probes recorded. Nil facts selects every recorded fact.
//
// It is a judge-independent projection on purpose. This is the view someone
// scans a 78-cell sweep with before anybody has decided what the cells mean,
// and the moment it started carrying a verdict column it would be a rendering
// of a conclusion rather than of the evidence. Status is the runner's axis and
// nothing here derives anything from it.
func RowsOf(data, notices io.Writer, rec *record.Record, facts []string) error {
	if err := writeNotices(notices, rec); err != nil {
		return err
	}

	columns, rows, err := cellRowsOf(rec, facts)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(data, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(columns, "\t")); err != nil {
		return fmt.Errorf("render rows: %w", err)
	}
	for _, row := range rows {
		line := make([]string, 0, len(columns))
		for _, c := range columns {
			value, ok := row[c]
			if !ok {
				// A cell that never reported a fact did not report it. An empty
				// column would read as an empty reading.
				value = absent
			}
			line = append(line, value)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(line, "\t")); err != nil {
			return fmt.Errorf("render rows: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("render rows: %w", err)
	}
	return nil
}

type censusCell struct {
	status record.Status
	facts  map[string]string
}

// censusCells is every cell the parent scheduled, and what the scheduler said
// about it. A cell that started and never finished is still a cell: leaving it
// out would turn a census that stopped into one that never covered it.
func censusCells(rec *record.Record) map[string]*censusCell {
	cells := map[string]*censusCell{}

	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindCellStarted:
			started, err := record.Decode[record.CellStarted](e)
			if err != nil {
				continue
			}
			if _, seen := cells[started.Key]; !seen {
				cells[started.Key] = &censusCell{status: record.StatusUnfinished}
			}

		case record.KindCellFinished:
			finished, err := record.Decode[record.CellFinished](e)
			if err != nil {
				continue
			}
			if cells[finished.Key] == nil {
				cells[finished.Key] = &censusCell{}
			}
			cells[finished.Key].status = finished.Status
		}
	}
	return cells
}

// cellFacts reads the readings a cell's probes recorded. The attempt that
// stands, not a blend: a resumed cell's first attempt is still in the file,
// and a key only it recorded must not surface beside the re-measured ones.
func cellFacts(parent, key string) (map[string]string, error) {
	cell, err := record.Read(filepath.Join(parent, record.CellsDir, key))
	if err != nil {
		// A cell with no readable record is not an error in the view: the
		// parent already says what happened to it.
		return nil, nil
	}
	return recordFacts(cell.Latest().Events)
}

// recordFacts is what a record's probes read, keyed by fact. Later readings of
// the same key win, because a scenario that reads a fact before and after an
// operation means the second one.
func recordFacts(events []record.Event) (map[string]string, error) {
	facts := map[string]string{}
	for _, e := range events {
		if e.Kind != record.KindFact {
			continue
		}
		fact, err := record.Decode[record.Fact](e)
		if err != nil {
			return nil, err
		}
		if fact.Value == nil {
			// A reading the probe went looking for and could not take is
			// itself evidence, and it is not an empty string. It is also not
			// the same as a key the record never mentions.
			facts[fact.Key] = "null"
			continue
		}
		facts[fact.Key] = record.Flatten(*fact.Value)
	}
	return facts, nil
}
