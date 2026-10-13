package judge_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// A judge is a pure function from a record to a verdict. Measurement is
// immutable and interpretation is replayable: when a judge was wrong - and the
// three withdrawn findings in the corpus were a judge bug, not a measurement
// bug - the record is re-judged, never re-measured.

// --- a record to judge ----------------------------------------------------

type step struct {
	label       string
	rc          int
	status      record.Status
	fingerprint record.FingerprintResult
	facts       []record.Fact
	expect      map[string]string
}

func written(t *testing.T, steps ...step) (dir string, rec *record.Record) {
	t.Helper()

	dir = filepath.Join(t.TempDir(), record.RunID("judged", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID:  filepath.Base(dir),
		Driver: record.DriverManifest,
	}); err != nil {
		t.Fatalf("run-started: %v", err)
	}

	for i, s := range steps {
		n := i + 1
		if _, err := w.Emit(record.KindStepStarted, n, record.StepStarted{Label: s.label, Host: "box"}); err != nil {
			t.Fatalf("step-started: %v", err)
		}
		status := s.status
		if status == "" {
			status = record.StatusMeasured
		}
		if _, err := w.Emit(record.KindOperation, n, record.Operation{
			RC:     s.rc,
			Claim:  record.NewClaim("whatever the tool said", record.ExtractorLastLine),
			Status: status,
		}); err != nil {
			t.Fatalf("operation: %v", err)
		}
		for _, f := range s.facts {
			if _, err := w.Emit(record.KindFact, n, f); err != nil {
				t.Fatalf("fact: %v", err)
			}
		}
		if s.fingerprint != "" {
			if _, err := w.Emit(record.KindFingerprint, n, record.Fingerprint{Result: s.fingerprint}); err != nil {
				t.Fatalf("fingerprint: %v", err)
			}
		}
		if s.expect != nil {
			// What was declared is on the record, so a judge years later needs
			// the record and nothing else. What was observed, the judge works
			// out for itself.
			if _, err := w.Emit(record.KindControlChecked, n, record.ControlChecked{
				Label:    s.label,
				Expected: s.expect,
				Observed: map[string]string{"rc": "the runner's own reading"},
				OK:       true,
			}); err != nil {
				t.Fatalf("control-checked: %v", err)
			}
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err = record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return dir, rec
}

// --- the verdict file -----------------------------------------------------

func TestAVerdictPinsTheBytesItJudged(t *testing.T) {
	t.Parallel()

	dir, rec := written(t, step{label: "a step", rc: 0, expect: map[string]string{"rc": "0"}})

	verdict, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.HasPrefix(verdict.Evidence.Events, "sha256:") {
		t.Errorf("evidence = %+v, want the events hashed", verdict.Evidence)
	}

	// The hash is of the bytes on disk, so a verdict can be checked against
	// the evidence it claims to have read.
	body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if verdict.Evidence.Events != judge.HashBytes(body) {
		t.Errorf("evidence hash does not match the events file")
	}
}

func TestSaveWritesTheVerdictWhereAFindingCanCiteIt(t *testing.T) {
	t.Parallel()

	dir, rec := written(t, step{label: "a step", rc: 0, expect: map[string]string{"rc": "0"}})
	verdict, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	outcome, err := judge.Save(dir, verdict)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if outcome != judge.Written {
		t.Errorf("outcome = %v, want %v", outcome, judge.Written)
	}

	path := filepath.Join(dir, "verdicts", "expect@3.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	var round judge.Verdict
	if err := json.Unmarshal(body, &round); err != nil {
		t.Fatalf("verdict is not json: %v", err)
	}
	if round.Judge != "expect" || round.Version != (judge.Expect{}).Version() {
		t.Errorf("verdict names %s@%s", round.Judge, round.Version)
	}
}

func TestRejudgingReproducesTheVerdictRatherThanRewritingIt(t *testing.T) {
	t.Parallel()

	// Judges are pure. Re-judging the same record with the same judge must
	// reach the same file, and an existing verdict is never edited: editing
	// one is a corpus edit.
	dir, rec := written(t, step{label: "a step", rc: 0, expect: map[string]string{"rc": "0"}})

	first, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := judge.Save(dir, first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(dir, "verdicts", "expect@3.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	second, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	outcome, err := judge.Save(dir, second)
	if err != nil {
		t.Fatalf("Save again: %v", err)
	}
	if outcome != judge.Reproduced {
		t.Errorf("outcome = %v, want %v", outcome, judge.Reproduced)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read again: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("re-judging rewrote the verdict file")
	}
}

// impure is a judge that does not reach the same answer twice. A judge like
// this makes every finding it produced unreproducible, so it has to be caught
// rather than quietly overwrite what it said last time.
type impure struct{ n *int }

func (impure) Name() string    { return "impure" }
func (impure) Version() string { return "1" }

func (j impure) Judge(*record.Record) (judge.Result, error) {
	*j.n++
	return judge.Result{
		Verdict: "pass",
		Steps: map[string]judge.StepVerdict{
			"a step": {Verdict: "met", Because: []string{"call number " + string(rune('0'+*j.n))}},
		},
	}, nil
}

func TestAJudgeThatDisagreesWithItselfIsRefused(t *testing.T) {
	t.Parallel()

	dir, rec := written(t, step{label: "a step", rc: 0, expect: map[string]string{"rc": "0"}})
	calls := 0

	first, err := judge.Apply(impure{n: &calls}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := judge.Save(dir, first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second, err := judge.Apply(impure{n: &calls}, rec)
	if err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	if _, err := judge.Save(dir, second); err == nil {
		t.Fatal("a verdict that changed under the same judge version was written anyway")
	}
}

func TestANewJudgeVersionAddsAFileAndLeavesTheOldOne(t *testing.T) {
	t.Parallel()

	// The central promise: measurement is immutable, interpretation is
	// replayable. A finding cites (run-id, judge@version), so both verdicts
	// have to survive side by side.
	dir, rec := written(t, step{label: "a step", rc: 1, expect: map[string]string{"rc": "0"}})

	strict, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := judge.Save(dir, strict); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if strict.Verdict != judge.Regressed {
		t.Fatalf("verdict = %q, want %q", strict.Verdict, judge.Regressed)
	}

	// A judge that was wrong the first time, corrected and re-versioned.
	forgiving, err := judge.Apply(rcIsAdvisory{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := judge.Save(dir, forgiving); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if forgiving.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want %q", forgiving.Verdict, judge.Pass)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "verdicts"))
	if err != nil {
		t.Fatalf("read verdicts: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("verdict files = %d, want both to survive", len(entries))
	}

	// And the evidence never moved.
	after, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(after.Events) != len(rec.Events) {
		t.Errorf("judging changed the record: %d events, was %d", len(after.Events), len(rec.Events))
	}
	if strict.Evidence.Events != forgiving.Evidence.Events {
		t.Errorf("two judges read different bytes from one record")
	}
}

// rcIsAdvisory is expect, reconsidered: an exit code that did not match is
// worth saying, but not worth calling a regression on its own.
type rcIsAdvisory struct{}

func (rcIsAdvisory) Name() string    { return "expect" }
func (rcIsAdvisory) Version() string { return "4" }

func (rcIsAdvisory) Judge(rec *record.Record) (judge.Result, error) {
	result, err := (judge.Expect{}).Judge(rec)
	if err != nil {
		return judge.Result{}, err
	}
	result.Verdict = judge.Pass
	return result, nil
}

func TestJudgedAtIsEnvelopeNotVerdict(t *testing.T) {
	t.Parallel()

	// A wall clock in the verdict body would make every judge impure. It is
	// stamped on the envelope and excluded from the comparison.
	dir, rec := written(t, step{label: "a step", rc: 0, expect: map[string]string{"rc": "0"}})

	first, err := judge.Apply(judge.Expect{}, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := judge.Save(dir, first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	later := first
	later.JudgedAt = first.JudgedAt.Add(72 * time.Hour)
	outcome, err := judge.Save(dir, later)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if outcome != judge.Reproduced {
		t.Errorf("a different judged_at made the verdict look different")
	}
}

func TestByName(t *testing.T) {
	t.Parallel()

	j, err := judge.ByName("expect")
	if err != nil {
		t.Fatalf("ByName: %v", err)
	}
	if j.Name() != "expect" {
		t.Errorf("got %s", j.Name())
	}

	_, err = judge.ByName("clairvoyance")
	if err == nil {
		t.Fatal("ByName invented a judge")
	}
	if !strings.Contains(err.Error(), "expect") {
		t.Errorf("error %q does not say which judges there are", err)
	}
}

func TestUnknownJudgeIsNotAPanic(t *testing.T) {
	t.Parallel()

	var missing *judge.UnknownJudgeError
	_, err := judge.ByName("clairvoyance")
	if !errors.As(err, &missing) {
		t.Fatalf("error is %T, want *judge.UnknownJudgeError", err)
	}
}
