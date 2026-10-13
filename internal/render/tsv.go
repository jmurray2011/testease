// Package render turns a record into the views people and old scripts read.
//
// Presentation is not record. Nothing here is evidence: every view is
// regenerated from events on demand, and no view is ever written back into the
// record it came from.
package render

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/jmurray2011/testease/internal/record"
)

// claimWidth is the cut the old run.tsv applied to the claim column. It is
// presentation: the stored claim keeps its own, larger cap, and the step log
// is not cut at all.
const claimWidth = 120

var header = []string{"seq", "host", "step", "exit", "seconds", "drift", "claim"}

// TSV writes the seven-column view the old run.tsv was: one line per step,
// header first and never sorted into the data.
//
// The shape is not decoration. Scenarios read this back with awk -F'\t' to
// reach a verdict - the negative control check was literally
// `$3 ~ /CONTROL negative/ && $6 != "no"` - so a row that is not seven fields
// on one line does not fail loudly, it produces a wrong verdict from correct
// measurements.
//
// What the view has to say about the record itself - that the run was
// interrupted, that the reader had to tolerate damage - goes to notices, on
// its own channel. Every view says both things, and neither may be said in
// the other's channel: a warning among the rows would be parsed as one.
func TSV(data, notices io.Writer, rec *record.Record) error {
	rows, err := stepRows(rec)
	if err != nil {
		return err
	}
	if err := writeNotices(notices, rec); err != nil {
		return err
	}

	bw := bufio.NewWriter(data)
	if _, err := fmt.Fprintln(bw, strings.Join(header, "\t")); err != nil {
		return fmt.Errorf("render tsv: %w", err)
	}
	for _, r := range rows {
		if _, err := fmt.Fprintln(bw, strings.Join(r.fields(), "\t")); err != nil {
			return fmt.Errorf("render tsv: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("render tsv: %w", err)
	}
	return nil
}

// Table writes the same seven columns aligned for a terminal, and the same
// notices on their own channel.
func Table(data, notices io.Writer, rec *record.Record) error {
	rows, err := stepRows(rec)
	if err != nil {
		return err
	}
	if err := writeNotices(notices, rec); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(data, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(header, "\t")); err != nil {
		return fmt.Errorf("render table: %w", err)
	}
	for _, r := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(r.fields(), "\t")); err != nil {
			return fmt.Errorf("render table: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("render table: %w", err)
	}
	return nil
}

// writeNotices says what a reader must know before believing the rows: that
// the run never closed, and everything the reader had to tolerate to read it.
// A record with damage that renders silently is worse than one that will not
// render at all.
//
// A census's damage is in its cells, not in the file this record was read
// from, and nobody renders a 136-cell sweep one cell at a time - so the cells'
// reports are notices on the parent too.
func writeNotices(w io.Writer, rec *record.Record) error {
	if rec.Interrupted() {
		if _, err := fmt.Fprintln(w, "warning: this run was interrupted - it has no run-finished event, so the rows below are what was measured before it stopped"); err != nil {
			return fmt.Errorf("render notices: %w", err)
		}
	}
	for _, reports := range [][]record.Report{rec.Reports, record.CellReports(rec)} {
		for _, report := range reports {
			if _, err := fmt.Fprintf(w, "warning: %s: %s\n", report.About, report.Message); err != nil {
				return fmt.Errorf("render notices: %w", err)
			}
		}
	}
	return nil
}

// row is one step's worth of the record, gathered from the events that
// mention it. A step with no operation still gets a row: step-started is
// emitted before the probe runs precisely so that a step which happened leaves
// a trace, and rendering that step as absence would throw the trace away.
type row struct {
	step        int
	host        string
	label       string
	op          *record.Operation
	fingerprint *record.Fingerprint
}

func stepRows(whole *record.Record) ([]*row, error) {
	// One attempt, not a blend of them: a re-run step appends rather than
	// replacing, and rendering both would put two rows under one step number.
	rec := whole.Latest()

	var rows []*row
	current := make(map[int]*row)

	at := func(step int) *row {
		r, ok := current[step]
		if !ok {
			r = &row{step: step}
			current[step] = r
			rows = append(rows, r)
		}
		return r
	}

	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindStepStarted:
			started, err := record.Decode[record.StepStarted](e)
			if err != nil {
				return nil, err
			}
			r := &row{step: e.Step, host: started.Host, label: started.Label}
			current[e.Step] = r
			rows = append(rows, r)

		case record.KindOperation:
			op, err := record.Decode[record.Operation](e)
			if err != nil {
				return nil, err
			}
			at(e.Step).op = &op

		case record.KindFingerprint:
			fp, err := record.Decode[record.Fingerprint](e)
			if err != nil {
				return nil, err
			}
			at(e.Step).fingerprint = &fp
		}
	}
	return rows, nil
}

// fields is the seven columns. The label is flattened but never cut: it is
// author-written and it is what verdict logic matches on, so a cut that landed
// mid-label would produce a wrong verdict from correct measurements. Only the
// claim carries a presentation cut.
func (r *row) fields() []string {
	return []string{
		strconv.Itoa(r.step),
		record.Flatten(r.host),
		record.Flatten(r.label),
		r.exit(),
		r.seconds(),
		r.drift(),
		r.claim(),
	}
}

// exit and seconds are -1 when the step left no operation behind, never blank.
// These columns hold integers and consumers do arithmetic on them: `$4 + 0` on
// an empty field is 0, and 0 in the exit column reads as SUCCESS - the worst
// value for a missing reading to take. rec__int reached the same answer, by
// the same reasoning, for a missing value.
//
// The -1 does not distinguish "never reported" from "reported something that
// would not parse". It does not have to: the events keep that distinction (no
// operation event at all, versus one carrying a record-warning), and the tsv
// is presentation.
func (r *row) exit() string {
	if r.op == nil {
		return "-1"
	}
	return strconv.Itoa(r.op.RC)
}

func (r *row) seconds() string {
	if r.op == nil {
		return "-1"
	}
	return strconv.FormatInt(int64(math.Floor(r.op.DurationS)), 10)
}

// drift is the old four-valued column. `na` covers both a target that cannot
// fingerprint and a step that took no fingerprint - the distinction survives in
// the events, and collapsing it is presentation. `?` is what an unrecognized
// instrument reading renders as, and what a step that left no reading at all
// renders as: neither is passed through as real drift, and neither is rewritten
// into one of the four legitimate values.
func (r *row) drift() string {
	if r.fingerprint == nil {
		if r.op == nil {
			return "?"
		}
		return "na"
	}
	switch r.fingerprint.Result {
	case record.FingerprintChanged:
		return "yes"
	case record.FingerprintUnchanged:
		return "no"
	case record.FingerprintUnavailable:
		return "na"
	case record.FingerprintErrored:
		return "err"
	default:
		return "?"
	}
}

func (r *row) claim() string {
	if r.op == nil {
		return ""
	}
	return cut(record.Flatten(r.op.Claim.Text), claimWidth)
}

func cut(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width])
}
