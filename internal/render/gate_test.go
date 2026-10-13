package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/render"
)

// THE GATE. These are check-record-row.sh's cases, ported.
//
// run.tsv was the contract - seven tab-separated columns, one line per step -
// and scenarios read it back with `awk -F'\t'` to reach a verdict. The negative
// control check was literally `$3 ~ /CONTROL negative/ && $6 != "no"`, so a row
// that is not seven fields on one line does not fail loudly; it produces a
// WRONG VERDICT from correct measurements. In testease the tsv is a rendering
// rather than the record, but it is still what a two-year-old finding was read
// through, so the shape is still non-negotiable.
//
// Every case below writes events with hostile input and renders them.

const (
	colSeq = iota
	colHost
	colStep
	colExit
	colSeconds
	colDrift
	colClaim
	colCount
)

const tsvHeader = "seq\thost\tstep\texit\tseconds\tdrift\tclaim"

// step is one step's worth of hostile input. The numeric and fingerprint
// fields arrive as text because that is where a parse can fail: the adhoc
// record verbs and probe output on fd 3.
type step struct {
	label   string
	rc      string
	seconds string
	drift   string
	claim   string
	noOp    bool // a step that started and left no operation behind
	noPrint bool // a step that took no fingerprint
}

func rendered(t *testing.T, steps ...step) []string {
	t.Helper()

	rows, _ := renderTSV(t, built(t, false, steps...))
	return rows
}

// built writes a record and reads it back. A run is only closed when asked,
// because most of these cases are about what a half-finished record renders as.
func built(t *testing.T, closed bool, steps ...step) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("gate", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	for i, s := range steps {
		n := i + 1
		if _, err := w.Emit(record.KindStepStarted, n, record.StepStarted{
			Label: s.label,
			Host:  "alpha",
			Probe: "probes/noop.sh",
		}); err != nil {
			t.Fatalf("step-started: %v", err)
		}
		if s.noOp {
			continue
		}

		var b record.Boundary
		seq, err := w.Emit(record.KindOperation, n, record.Operation{
			RC:        b.Int("operation.rc", s.rc),
			DurationS: b.Seconds("operation.duration_s", s.seconds),
			Claim:     record.NewClaim(s.claim, record.ExtractorLastLine),
			Status:    record.StatusMeasured,
		})
		if err != nil {
			t.Fatalf("operation: %v", err)
		}
		if err := w.Warn(seq, n, b.Warnings()...); err != nil {
			t.Fatalf("warn: %v", err)
		}
		if s.noPrint {
			continue
		}

		var fb record.Boundary
		fseq, err := w.Emit(record.KindFingerprint, n, fb.Fingerprint("fingerprint.result", s.drift))
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		if err := w.Warn(fseq, n, fb.Warnings()...); err != nil {
			t.Fatalf("warn: %v", err)
		}
	}
	if closed {
		if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
			t.Fatalf("run-finished: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

// renderTSV returns the data rows and, separately, whatever the view had to
// say about the record itself. The two never share a channel: a notice inside
// the seven columns would be parsed as a row.
func renderTSV(t *testing.T, rec *record.Record) (rows []string, notices string) {
	t.Helper()

	var data, notice bytes.Buffer
	if err := render.TSV(&data, &notice, rec); err != nil {
		t.Fatalf("TSV: %v", err)
	}

	out := data.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("tsv does not end in a newline: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != tsvHeader {
		t.Fatalf("first line is %q, want the header %q", lines[0], tsvHeader)
	}
	return lines[1:], notice.String()
}

func fields(t *testing.T, row string) []string {
	t.Helper()

	f := strings.Split(row, "\t")
	if len(f) != colCount {
		t.Fatalf("row has %d fields, want %d: %q", len(f), colCount, row)
	}
	return f
}

// 1. The shape is non-negotiable: one line, seven fields, whatever came in.

func TestACleanRow(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "a step", rc: "0", seconds: "3", drift: "unchanged", claim: "tables=25"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	f := fields(t, rows[0])

	if f[colSeq] != "1" {
		t.Errorf("seq = %q, want %q", f[colSeq], "1")
	}
	if f[colHost] != "alpha" {
		t.Errorf("host = %q, want %q", f[colHost], "alpha")
	}
	if f[colStep] != "a step" {
		t.Errorf("step = %q, want %q", f[colStep], "a step")
	}
	if f[colClaim] != "tables=25" {
		t.Errorf("the claim did not survive: %q", f[colClaim])
	}
}

func TestATabInTheClaim(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "a step", rc: "0", seconds: "3", drift: "unchanged", claim: "tables=25\tenv=beta"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	f := fields(t, rows[0])
	if f[colClaim] != "tables=25 env=beta" {
		t.Errorf("claim = %q, want the tab flattened", f[colClaim])
	}
}

func TestANewlineInTheClaim(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "a step", rc: "0", seconds: "3", drift: "unchanged", claim: "line one\nline two"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (a newline must not phantom a second row)", len(rows))
	}
	f := fields(t, rows[0])
	if f[colClaim] != "line one line two" {
		t.Errorf("claim = %q, want the newline flattened", f[colClaim])
	}
}

func TestATabInTheLabel(t *testing.T) {
	t.Parallel()

	// A tab in the LABEL shifted every column after it, and the label is what
	// the negative-control check matches on.
	rows := rendered(t, step{label: "CONTROL\tnegative", rc: "0", seconds: "3", drift: "unchanged", claim: "x"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	f := fields(t, rows[0])
	if f[colStep] != "CONTROL negative" {
		t.Errorf("step = %q, want the tab flattened", f[colStep])
	}
	if f[colDrift] != "no" {
		t.Errorf("drift is not readable as column 6: %q", f[colDrift])
	}
}

func TestACarriageReturnInTheClaimSurvives(t *testing.T) {
	t.Parallel()

	// Translating this rewrote the claim in most of the published corpus. It
	// breaks neither half of the contract: lines split on \n, fields on \t.
	rows := rendered(t, step{label: "a step", rc: "0", seconds: "3", drift: "unchanged", claim: "done\r"})
	f := fields(t, rows[0])
	if f[colClaim] != "done\r" {
		t.Errorf("claim = %q, want the CR preserved", f[colClaim])
	}
}

// 2. drift has exactly four legitimate values, and anything else is the
//    instrument misreporting.

func TestDriftMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		result string
		want   string
	}{
		{"changed", "yes"},
		{"unchanged", "no"},
		{"unavailable", "na"},
		{"errored", "err"},
	}

	for _, c := range cases {
		t.Run(c.result, func(t *testing.T) {
			t.Parallel()

			rows := rendered(t, step{label: "s", rc: "0", seconds: "3", drift: c.result, claim: "x"})
			if got := fields(t, rows[0])[colDrift]; got != c.want {
				t.Errorf("drift %q rendered %q, want %q", c.result, got, c.want)
			}
		})
	}
}

func TestAnUnknownDriftRendersAQuestionMark(t *testing.T) {
	t.Parallel()

	// Not passed through - a check asking `$6 != "no"` would read it as real
	// drift - and not rewritten to a legitimate value either. `?` is neither.
	rows := rendered(t, step{label: "s", rc: "0", seconds: "3", drift: "maybe", claim: "x"})
	if got := fields(t, rows[0])[colDrift]; got != "?" {
		t.Errorf("drift = %q, want %q", got, "?")
	}
}

func TestAnUnknownDriftLeavesAWarningEventCarryingTheRaw(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), record.RunID("gate", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var b record.Boundary
	seq, err := w.Emit(record.KindFingerprint, 1, b.Fingerprint("fingerprint.result", "maybe"))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := w.Warn(seq, 1, b.Warnings()...); err != nil {
		t.Fatalf("Warn: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	fp, err := record.Decode[record.Fingerprint](rec.Events[0])
	if err != nil {
		t.Fatalf("decode fingerprint: %v", err)
	}
	if fp.Result != record.FingerprintUnknown {
		t.Errorf("result = %q, want %q", fp.Result, record.FingerprintUnknown)
	}
	if fp.Raw != "maybe" {
		t.Errorf("raw = %q, want %q - dropping it is normalizing", fp.Raw, "maybe")
	}

	var warned bool
	for _, e := range rec.Events {
		if e.Kind == record.KindRecordWarning {
			warned = true
		}
	}
	if !warned {
		t.Error("an unknown drift value left no record-warning behind")
	}
}

// 3. The numeric columns. `printf %d` on a non-number printed 0, and 0 in the
//    exit column reads as SUCCESS - the worst value for it to invent.

func TestANonNumericExitNeverBecomesZero(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "s", rc: "abc", seconds: "3", drift: "unchanged", claim: "x"})
	if got := fields(t, rows[0])[colExit]; got != "-1" {
		t.Errorf("exit = %q, want %q", got, "-1")
	}
}

func TestANonNumericSecondsNeverBecomesZero(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "s", rc: "0", seconds: "abc", drift: "unchanged", claim: "x"})
	if got := fields(t, rows[0])[colSeconds]; got != "-1" {
		t.Errorf("seconds = %q, want %q", got, "-1")
	}
}

func TestARealExitCodeIsUntouched(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "s", rc: "137", seconds: "3", drift: "unchanged", claim: "x"})
	if got := fields(t, rows[0])[colExit]; got != "137" {
		t.Errorf("exit = %q, want %q", got, "137")
	}
}

func TestSecondsAreFloored(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "s", rc: "0", seconds: "41.9", drift: "unchanged", claim: "x"})
	if got := fields(t, rows[0])[colSeconds]; got != "41" {
		t.Errorf("seconds = %q, want %q", got, "41")
	}
}

// 4. Appending stays appending - a row must never replace the run.

func TestRowsAccumulate(t *testing.T) {
	t.Parallel()

	rows := rendered(
		t,
		step{label: "one", rc: "0", seconds: "1", drift: "unchanged", claim: "a"},
		step{label: "two", rc: "0", seconds: "1", drift: "unchanged", claim: "b"},
	)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if got := fields(t, rows[0])[colClaim]; got != "a" {
		t.Errorf("row 1 claim = %q, want %q", got, "a")
	}
	if got := fields(t, rows[1])[colClaim]; got != "b" {
		t.Errorf("row 2 claim = %q, want %q", got, "b")
	}
	if got := fields(t, rows[1])[colSeq]; got != "2" {
		t.Errorf("row 2 seq = %q, want %q", got, "2")
	}
}

// New cases with no bash ancestor.

func TestAStepThatLeftNoOperationStillRendersARow(t *testing.T) {
	t.Parallel()

	// step-started is emitted before the probe runs, so even a kill -9 leaves
	// this trace. Absence of a row would be a step that happened leaving none.
	rows := rendered(
		t,
		step{label: "one", rc: "0", seconds: "1", drift: "unchanged", claim: "a"},
		step{label: "killed", noOp: true},
	)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	f := fields(t, rows[1])
	if f[colStep] != "killed" {
		t.Errorf("step = %q, want %q", f[colStep], "killed")
	}
	// -1, not empty. The numeric columns hold integers and consumers do
	// arithmetic on them: `$4 + 0` on an empty field is 0, and 0 in the exit
	// column reads as SUCCESS. The old harness agreed - rec__int on a missing
	// value produced -1, never a blank.
	if f[colExit] != "-1" {
		t.Errorf("exit = %q, want %q", f[colExit], "-1")
	}
	if f[colSeconds] != "-1" {
		t.Errorf("seconds = %q, want %q", f[colSeconds], "-1")
	}
	if f[colDrift] != "?" {
		t.Errorf("drift = %q, want %q", f[colDrift], "?")
	}
}

func TestAStepThatTookNoFingerprintRendersNA(t *testing.T) {
	t.Parallel()

	rows := rendered(t, step{label: "s", rc: "0", seconds: "3", claim: "x", noPrint: true})
	if got := fields(t, rows[0])[colDrift]; got != "na" {
		t.Errorf("drift = %q, want %q", got, "na")
	}
}

func TestTheClaimIsCutToPresentationWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 200)
	rows := rendered(t, step{label: "s", rc: "0", seconds: "3", drift: "unchanged", claim: long})
	got := fields(t, rows[0])[colClaim]
	if len([]rune(got)) != 120 {
		t.Errorf("claim rendered %d runes, want 120", len([]rune(got)))
	}
}

func TestAnEmptyRecordRendersItsHeaderAndNothingElse(t *testing.T) {
	t.Parallel()

	if rows := rendered(t); len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
}

func TestAnInterruptedRunSaysSoInEveryView(t *testing.T) {
	t.Parallel()

	// No run-finished is the machine-readable definition of an interrupted
	// run, and a reader who cannot tell an interrupted record from a complete
	// one will read a partial run as a whole one.
	rec := built(t, false, step{label: "one", rc: "0", seconds: "1", drift: "unchanged", claim: "a"})

	rows, notices := renderTSV(t, rec)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.Contains(notices, "interrupted") {
		t.Errorf("tsv said nothing about an interrupted run: %q", notices)
	}
	for _, row := range rows {
		if strings.Contains(row, "interrupted") {
			t.Errorf("the notice leaked into the data: %q", row)
		}
	}

	var data, notice bytes.Buffer
	if err := render.Table(&data, &notice, rec); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if !strings.Contains(notice.String(), "interrupted") {
		t.Errorf("table said nothing about an interrupted run: %q", notice.String())
	}
}

func TestAClosedRunIsNotCalledInterrupted(t *testing.T) {
	t.Parallel()

	rec := built(t, true, step{label: "one", rc: "0", seconds: "1", drift: "unchanged", claim: "a"})

	_, notices := renderTSV(t, rec)
	if strings.Contains(notices, "interrupted") {
		t.Errorf("a closed run was called interrupted: %q", notices)
	}
}

func TestEveryViewReportsWhatTheReaderHadToTolerate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const lines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"step-started","step":1,"data":{"label":"one","host":"alpha"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:2`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	rows, notices := renderTSV(t, rec)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.Contains(notices, "torn") {
		t.Errorf("the view hid a read repair: %q", notices)
	}
}

func TestALongLabelIsNeverCut(t *testing.T) {
	t.Parallel()

	// The claim is cut at render and the label is not. The label is
	// author-written and is what verdict logic matches on - the negative
	// control check reads it - so a cut landing mid-label would produce a
	// wrong verdict from correct measurements.
	label := "CONTROL negative " + strings.Repeat("x", 200)
	rows := rendered(t, step{label: label, rc: "0", seconds: "3", drift: "unchanged", claim: "x"})

	if got := fields(t, rows[0])[colStep]; got != label {
		t.Errorf("label rendered %d runes, want all %d of them", len([]rune(got)), len([]rune(label)))
	}
}
