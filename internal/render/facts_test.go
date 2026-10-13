package render_test

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/render"
)

// A ladder is captured as SEPARATE RUNS, not as a census: each rung's input is
// the rung below's published output, so the cells cannot be scheduled against
// one another. That leaves the comparison every one of those runs exists for
// with no view at all - `render` reads one record and `--view rows` reads the
// cells of one census. These cases are that view.

// reading is one fact as a probe recorded it. An unset value is the `no-reading`
// verb, which is a reading that was attempted and refused - not a missing key.
type reading struct {
	key     string
	value   string
	refused string
}

// captured writes one record's worth of readings and reads it back, under a run
// id the view is expected to use as the column name.
func captured(t *testing.T, id string, readings ...reading) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, r := range readings {
		fact := record.Fact{Key: r.key}
		if r.refused != "" {
			fact.Error = r.refused
		} else {
			value := r.value
			fact.Value = &value
		}
		if _, err := w.Emit(record.KindFact, 1, fact); err != nil {
			t.Fatalf("fact: %v", err)
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
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

// facts renders and splits, keeping the two channels apart. A notice among the
// columns would be read as a fact.
func facts(t *testing.T, recs ...*record.Record) (rows []string, notices string) {
	t.Helper()

	var data, notice bytes.Buffer
	if err := render.FactsOf(&data, &notice, recs, nil); err != nil {
		t.Fatalf("Facts: %v", err)
	}

	out := data.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("facts view does not end in a newline: %q", out)
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n"), notice.String()
}

// cells splits a rendered line the way a reader of an aligned table would. The
// view pads its columns, as the census view does, so the separator is a run of
// spaces rather than the tab that went in.
func cells(line string) []string { return columnGap.Split(strings.TrimRight(line, " "), -1) }

var columnGap = regexp.MustCompile(` {2,}`)

func TestFactsColumnsAreTheRunsAndRowsAreTheKeys(t *testing.T) {
	t.Parallel()

	rows, _ := facts(
		t,
		captured(t, "survey-20260101T000100Z", reading{key: "alpha.added", value: "3"}),
		captured(t, "survey-20260101T000200Z", reading{key: "alpha.added", value: "0"}),
	)

	if got, want := cells(rows[0]), []string{"fact", "survey-20260101T000100Z", "survey-20260101T000200Z"}; !equal(got, want) {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got, want := cells(rows[1]), []string{"alpha.added", "3", "0"}; !equal(got, want) {
		t.Fatalf("row = %q, want %q", got, want)
	}
}

// THE COLUMN ORDER IS THE ARGUMENT ORDER. A ladder is read oldest rung first,
// and sorting the columns by run id would put them in capture order instead -
// which is the same order only by accident, and stops being so the moment a rung
// is recaptured.
func TestFactsKeepsTheOrderItWasGivenTheRunsIn(t *testing.T) {
	t.Parallel()

	rows, _ := facts(
		t,
		captured(t, "zzz-later", reading{key: "k", value: "1"}),
		captured(t, "aaa-earlier", reading{key: "k", value: "2"}),
	)

	if got, want := cells(rows[0])[1:], []string{"zzz-later", "aaa-earlier"}; !equal(got, want) {
		t.Fatalf("columns = %q, want the argument order %q", got, want)
	}
}

// A record that predates a fact does not carry it, and records are never
// rewritten, so the absence is permanent and normal. It is not an empty reading
// and it is certainly not zero - `alpha.added` blank in a column
// would read as a run that measured zero.
func TestFactsRendersAKeyARecordNeverMentionedAsAbsent(t *testing.T) {
	t.Parallel()

	rows, _ := facts(
		t,
		captured(t, "old", reading{key: "alpha.rows", value: "835331"}),
		captured(t, "new", reading{key: "alpha.rows", value: "835360"}, reading{key: "alpha.added", value: "0"}),
	)

	row := find(t, rows, "alpha.added")
	if got, want := cells(row), []string{"alpha.added", "-", "0"}; !equal(got, want) {
		t.Fatalf("row = %q, want the absent column rendered %q", got, want)
	}
}

// `no-reading` is the opposite of absence: the probe went looking and reported
// that it could not tell. Collapsing the two would lose the distinction the verb
// exists to carry.
func TestFactsSeparatesARefusedReadingFromAnAbsentOne(t *testing.T) {
	t.Parallel()

	rows, _ := facts(
		t,
		captured(t, "refused", reading{key: "alpha.added", refused: "the source carried nothing to compare against"}),
		captured(t, "absent", reading{key: "alpha.rows", value: "1"}),
	)

	row := find(t, rows, "alpha.added")
	if got, want := cells(row), []string{"alpha.added", "null", "-"}; !equal(got, want) {
		t.Fatalf("row = %q, want a refused reading and an absent one told apart: %q", got, want)
	}
}

// The same rule the census view uses: a scenario that reads a key before and
// after an operation means the second one.
func TestFactsTakesTheLastReadingOfAKey(t *testing.T) {
	t.Parallel()

	rows, _ := facts(t, captured(
		t, "one",
		reading{key: "beta.serving", value: "no"},
		reading{key: "beta.serving", value: "yes"},
	))

	if got, want := cells(find(t, rows, "beta.serving")), []string{"beta.serving", "yes"}; !equal(got, want) {
		t.Fatalf("row = %q, want the later reading %q", got, want)
	}
}

// Keys are sorted so that the same set of records renders the same way twice,
// whatever order the probes happened to emit them in.
func TestFactsSortsTheKeys(t *testing.T) {
	t.Parallel()

	rows, _ := facts(t, captured(
		t, "one",
		reading{key: "alpha.rows", value: "1"},
		reading{key: "alpha.marker", value: "2"},
		reading{key: "zeta.outcome", value: "successful"},
	))

	var got []string
	for _, r := range rows[1:] {
		got = append(got, cells(r)[0])
	}
	want := []string{"alpha.marker", "alpha.rows", "zeta.outcome"}
	if !equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
}

// Damage is reported per record and named, because "one of these five is
// interrupted" is not a usable warning.
func TestFactsNamesTheRecordAWarningIsAbout(t *testing.T) {
	t.Parallel()

	_, notices := facts(t, interrupted(t, "survey-20260101T000100Z"))

	if !strings.Contains(notices, "survey-20260101T000100Z") {
		t.Fatalf("notices = %q, want the run id named", notices)
	}
	if !strings.Contains(notices, "interrupted") {
		t.Fatalf("notices = %q, want the interruption reported", notices)
	}
}

// interrupted writes a record with no run-finished event.
func interrupted(t *testing.T, id string) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	value := "yes"
	if _, err := w.Emit(record.KindFact, 1, record.Fact{Key: "beta.serving", Value: &value}); err != nil {
		t.Fatalf("fact: %v", err)
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

// A view over no records renders its header and nothing else rather than
// failing: the command layer is what decides how many run ids are required.
func TestFactsOverNoRecordsRendersOnlyTheHeader(t *testing.T) {
	t.Parallel()

	rows, _ := facts(t)

	if got, want := cells(rows[0]), []string{"fact"}; !equal(got, want) {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %q, want the header alone", rows)
	}
}

func find(t *testing.T, rows []string, key string) string {
	t.Helper()

	for _, r := range rows {
		if cells(r)[0] == key {
			return r
		}
	}
	t.Fatalf("no row for %q in %q", key, rows)
	return ""
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Two columns under one name would put the second record's readings on top of
// the first's, and the table would show them agreeing.
func TestFactsRefusesTheSameRunTwice(t *testing.T) {
	t.Parallel()

	rec := captured(t, "survey-20260101T000100Z", reading{key: "k", value: "1"})

	var data, notice bytes.Buffer
	err := render.FactsOf(&data, &notice, []*record.Record{rec, rec}, nil)
	if err == nil {
		t.Fatalf("Facts accepted the same run twice; rendered %q", data.String())
	}
	if !strings.Contains(err.Error(), "survey-20260101T000100Z") {
		t.Fatalf("error = %v, want the repeated run id named", err)
	}
}
