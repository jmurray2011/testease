package record_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
)

// handmade writes an events.jsonl verbatim. Readers must cope with records
// produced by a crashed runner, an older tool, or the adhoc verbs - not only
// with what this writer emits.
func handmade(t *testing.T, lines string) *record.Record {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

func reportsMentioning(rec *record.Record, substr string) int {
	n := 0
	for _, r := range rec.Reports {
		if strings.Contains(r.About, substr) || strings.Contains(r.Message, substr) {
			n++
		}
	}
	return n
}

func TestReadToleratesAndReportsATornTail(t *testing.T) {
	t.Parallel()

	// A crash can leave a partial final line. Ignore it, report it, and never
	// rewrite the file: repairs are read-time.
	const lines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"step-started","step":1,"data":{"label":"one","host":"alpha"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"step-st`

	rec := handmade(t, lines)

	if len(rec.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.Events))
	}
	if reportsMentioning(rec, "torn") == 0 {
		t.Errorf("a torn tail was tolerated but not reported: %+v", rec.Reports)
	}
}

func TestReadNeverRewritesTheFile(t *testing.T) {
	t.Parallel()

	const lines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"step-started","step":1,"data":{"label":"one","host":"alpha"}}
{"schema":1,"seq":2,"ts":"2026-08`

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := record.Read(dir); err != nil {
		t.Fatalf("Read: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != lines {
		t.Errorf("Read rewrote the record:\n got %q\nwant %q", after, lines)
	}
}

func TestReadReportsSequenceDefects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		lines string
		want  string
	}{
		{
			name: "a gap",
			lines: line(1, "step-started", 1) + "\n" +
				line(3, "step-started", 2) + "\n",
			want: "gap",
		},
		{
			name: "a duplicate",
			lines: line(1, "step-started", 1) + "\n" +
				line(1, "step-started", 2) + "\n",
			want: "duplicate",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			rec := handmade(t, c.lines)
			if len(rec.Events) != 2 {
				t.Errorf("events = %d, want 2 (a defect is reported, never dropped)", len(rec.Events))
			}
			if reportsMentioning(rec, c.want) == 0 {
				t.Errorf("no report mentioning %q: %+v", c.want, rec.Reports)
			}
		})
	}
}

func TestReadPreservesUnknownKinds(t *testing.T) {
	t.Parallel()

	// Invariant 3 applied to ourselves: an unrecognized kind is neither
	// dropped nor rewritten to one we know.
	const lines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"quorum-reached","step":1,"data":{"n":3}}
`
	rec := handmade(t, lines)

	if len(rec.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.Events))
	}
	if rec.Events[0].Kind != "quorum-reached" {
		t.Errorf("kind = %q, want it preserved verbatim", rec.Events[0].Kind)
	}
	if reportsMentioning(rec, "quorum-reached") == 0 {
		t.Errorf("an unknown kind was preserved but not reported: %+v", rec.Reports)
	}
}

func TestReadRejectsAFutureMajorSchema(t *testing.T) {
	t.Parallel()

	const lines = `{"schema":99,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"step-started","step":1,"data":{}}
`
	rec := handmade(t, lines)

	// Readers reject versions they do not know rather than guessing - but the
	// line still leaves a trace.
	if reportsMentioning(rec, "schema") == 0 {
		t.Errorf("an unknown schema version was not reported: %+v", rec.Reports)
	}
}

func TestInterruptedIsTheAbsenceOfRunFinished(t *testing.T) {
	t.Parallel()

	started := handmade(t, line(1, "run-started", 0)+"\n")
	if !started.Interrupted() {
		t.Error("a record with no run-finished is not interrupted")
	}

	closed := handmade(t, line(1, "run-started", 0)+"\n"+line(2, "run-finished", 0)+"\n")
	if closed.Interrupted() {
		t.Error("a record with run-finished is interrupted")
	}
}

func TestInterruptedAnswersAboutTheLatestAttempt(t *testing.T) {
	t.Parallel()

	// The first attempt closed; the resumed one did not. The record as a whole
	// is interrupted: the attempt that stands never wrote run-finished.
	resumed := handmade(t, line(1, "run-started", 0)+"\n"+
		line(2, "run-finished", 0)+"\n"+
		line(3, "run-started", 0)+"\n")
	if !resumed.Interrupted() {
		t.Error("the latest attempt never finished, and the record says it is not interrupted")
	}
}

func TestReadMissingRecord(t *testing.T) {
	t.Parallel()

	if _, err := record.Read(filepath.Join(t.TempDir(), "no-such-run")); err == nil {
		t.Fatal("reading a record that does not exist succeeded")
	}
}

func line(seq int, kind string, step int) string {
	s := `{"schema":1,"seq":` + strconv.Itoa(seq) + `,"ts":"2026-08-12T14:03:22.184Z","kind":"` + kind + `"`
	if step > 0 {
		s += `,"step":` + strconv.Itoa(step)
	}
	return s + `,"data":{}}`
}

func TestLatestIsTheAttemptThatStands(t *testing.T) {
	t.Parallel()

	// A cell that failed and was re-run on a resume appends a second attempt
	// into the same file: records are append-only and the first attempt stays
	// as evidence. But the two are different stories, and a reader that mixed
	// them would let a step which failed and then passed read as though only
	// the second had happened - while the first is still in the file.
	lines := line(1, "run-started", 0) + "\n" +
		line(2, "step-started", 1) + "\n" +
		line(3, "operation", 1) + "\n" +
		line(4, "run-started", 0) + "\n" +
		line(5, "step-started", 1) + "\n" +
		line(6, "operation", 1) + "\n"

	rec := handmade(t, lines)
	if len(rec.Events) != 6 {
		t.Fatalf("events = %d, want every attempt kept", len(rec.Events))
	}

	latest := rec.Latest()
	if len(latest.Events) != 3 {
		t.Fatalf("latest attempt = %d events, want 3", len(latest.Events))
	}
	if latest.Events[0].Seq != 4 {
		t.Errorf("latest starts at seq %d, want 4", latest.Events[0].Seq)
	}
	if latest.Dir != rec.Dir {
		t.Errorf("the attempt lost the record it came from")
	}
}

func TestLatestOfASingleAttemptIsTheWholeRecord(t *testing.T) {
	t.Parallel()

	rec := handmade(t, line(1, "run-started", 0)+"\n"+line(2, "operation", 1)+"\n")
	if got := len(rec.Latest().Events); got != 2 {
		t.Errorf("latest = %d events, want the whole record", got)
	}
}

func TestLatestOfARecordThatNeverOpenedIsItself(t *testing.T) {
	t.Parallel()

	// A record written by the thin-CLI verbs may have no run-started at all.
	rec := handmade(t, line(1, "operation", 1)+"\n")
	if got := len(rec.Latest().Events); got != 1 {
		t.Errorf("latest = %d events, want the whole record", got)
	}
}
