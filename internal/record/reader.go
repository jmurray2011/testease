package record

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Report is something a reader noticed about the record it read: a torn tail,
// a sequence defect, an event kind or schema version it does not know.
//
// Reports are read-time only. The file on disk is never rewritten - a record
// is immutable evidence, and quietly repairing one is a corpus edit dressed up
// as a validation fix. A caller that hides these is hiding the defect.
type Report struct {
	About   string
	Message string
}

// Record is a record as read: its events, and everything the reader had to say
// about them.
type Record struct {
	Dir     string
	Events  []Event
	Reports []Report
}

// Read loads a record's events. It tolerates damage - a crash can leave a
// partial final line - and reports what it tolerated rather than hiding it.
func Read(dir string) (*Record, error) {
	path := filepath.Join(dir, EventsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read record: %w", err)
	}
	defer func() { _ = f.Close() }()

	rec := &Record{Dir: dir}
	r := bufio.NewReader(f)
	seen := make(map[int]bool)
	last := 0
	lineNo := 0

	for {
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		terminated := err == nil
		if line == "" && !terminated {
			break
		}
		lineNo++

		if !terminated {
			// The last line has no newline: a writer died mid-event. Ignore
			// it, say so, and leave the bytes exactly where they are.
			rec.report(fmt.Sprintf("line %d", lineNo),
				"torn tail: the final line is unterminated and was ignored; the file is left as it is")
			break
		}

		text := strings.TrimSuffix(line, "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}

		var e Event
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			rec.report(fmt.Sprintf("line %d", lineNo), fmt.Sprintf("not valid JSON, ignored: %v", err))
			continue
		}
		if e.Schema != Schema {
			rec.report(fmt.Sprintf("line %d", lineNo),
				fmt.Sprintf("envelope schema %d is not one this reader knows (%d); rejected rather than guessed at", e.Schema, Schema))
			continue
		}

		switch {
		case seen[e.Seq]:
			rec.report(fmt.Sprintf("seq %d", e.Seq), "duplicate sequence number")
		case last == 0 && e.Seq != 1:
			rec.report(fmt.Sprintf("seq %d", e.Seq), fmt.Sprintf("gap in sequence: the record starts at %d, not 1", e.Seq))
		case last > 0 && e.Seq > last+1:
			rec.report(fmt.Sprintf("seq %d", e.Seq), fmt.Sprintf("gap in sequence: %d follows %d", e.Seq, last))
		case last > 0 && e.Seq < last:
			rec.report(fmt.Sprintf("seq %d", e.Seq), fmt.Sprintf("sequence went backwards: %d follows %d", e.Seq, last))
		}
		seen[e.Seq] = true
		if e.Seq > last {
			last = e.Seq
		}

		if !knownKinds[e.Kind] {
			rec.report(fmt.Sprintf("seq %d", e.Seq),
				fmt.Sprintf("unknown event kind %q, preserved rather than dropped", e.Kind))
		}
		rec.Events = append(rec.Events, e)
	}

	return rec, nil
}

// Interrupted reports whether the run ended without closing its record. The
// absence of run-finished is the machine-readable definition: it is a status,
// not a verdict, and a judge may still judge the steps that measured.
//
// It answers about the attempt that stands. A resumed record carries the
// earlier attempts' run-finished events too, and one of those closing cleanly
// says nothing about whether the latest did.
func (r *Record) Interrupted() bool {
	for _, e := range r.Latest().Events {
		if e.Kind == KindRunFinished {
			return false
		}
	}
	return true
}

// Latest is the attempt that stands.
//
// A record is append-only, so a cell re-run on a resume appends a second
// attempt into the same file rather than replacing the first: the failed
// attempt is evidence about what happened, and evidence is never deleted. But
// the attempts are different stories, and anything reaching a conclusion must
// read one of them rather than a blend - a step that failed and was re-run
// would otherwise read as though only the second attempt existed, while the
// first sits in the same file contradicting it.
//
// The last attempt is the one that stands, because it is the one that
// re-measured. A census parent is the exception and does not use this: its
// cells accumulate across attempts, and the union of them is the census.
func (r *Record) Latest() *Record {
	start := 0
	for i, e := range r.Events {
		if e.Kind == KindRunStarted {
			start = i
		}
	}
	return &Record{Dir: r.Dir, Events: r.Events[start:], Reports: r.Reports}
}

// CellReports is what the readers of a census's cells had to tolerate, in the
// same shape as a record's own reports and named by the cell it came from.
//
// A census is read through its parent - the rows view, the matrix judge - so a
// repair reported only when a cell is read directly is a repair nobody sees.
// Cells are read in name order, so the same census reports the same way twice.
//
// A cell with no readable record is not reported here: the parent already says
// what happened to it, and a reader that could not open a file has nothing to
// say about what was in it.
func CellReports(rec *Record) []Report {
	entries, err := os.ReadDir(filepath.Join(rec.Dir, CellsDir))
	if err != nil {
		return nil
	}

	var reports []Report
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cell, err := Read(filepath.Join(rec.Dir, CellsDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, report := range cell.Reports {
			reports = append(reports, Report{
				About:   CellsDir + "/" + entry.Name() + " " + report.About,
				Message: report.Message,
			})
		}
	}
	return reports
}

func (r *Record) report(about, message string) {
	r.Reports = append(r.Reports, Report{About: about, Message: message})
}
