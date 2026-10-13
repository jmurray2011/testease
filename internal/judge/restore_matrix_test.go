package judge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func TestRestoreMatrixDerivesPlainProductOutcomesFromReadings(t *testing.T) {
	t.Parallel()

	rec := restoreCensus(
		t,
		restoreCell{key: "5.0.2-to-5.0.3", facts: restoreReadings("5.0.2", "no", "5.0.2", "0", "hang")},
		restoreCell{key: "5.0.2-to-6.0.3", facts: restoreReadings("5.0.2", "yes", "", "", "restored")},
		restoreCell{key: "5.0.2-to-6.1.1", facts: restoreReadings("5.0.2", "no", "", "0", "restored")},
		restoreCell{key: "5.0.2-to-7.1.1", facts: restoreReadings("5.0.2", "no", "5.0.2", "1", "restored")},
	)

	result, err := (judge.RestoreMatrix{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	want := map[string]string{
		"5.0.2-to-5.0.3": judge.Restored,
		"5.0.2-to-6.0.3": judge.RestoreHang,
		"5.0.2-to-6.1.1": judge.SourceDataMissing,
		"5.0.2-to-7.1.1": judge.TargetNotReplaced,
	}
	for key, verdict := range want {
		if got := result.Cells[key].Verdict; got != verdict {
			t.Errorf("%s = %q, want %q (%v)", key, got, verdict, result.Cells[key].Because)
		}
		if result.CellEvidence[key] == "" {
			t.Errorf("%s has no pinned evidence hash", key)
		}
	}
	if result.Verdict != judge.RestoreFailed {
		t.Errorf("census verdict = %q, want %q", result.Verdict, judge.RestoreFailed)
	}
}

func TestRestoreMatrixDoesNotTrustTheProbesOutcomeLabel(t *testing.T) {
	t.Parallel()

	rec := restoreCensus(
		t,
		restoreCell{key: "success", facts: restoreReadings("6.2.4", "no", "6.2.4", "0", "hang")},
		restoreCell{key: "hang", facts: restoreReadings("6.2.4", "yes", "", "", "restored")},
	)
	result, err := (judge.RestoreMatrix{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["success"].Verdict; got != judge.Restored {
		t.Errorf("success = %q, want a verdict derived from marker and sentinel", got)
	}
	if got := result.Cells["hang"].Verdict; got != judge.RestoreHang {
		t.Errorf("hang = %q, want a verdict derived from timeout", got)
	}
}

func TestRestoreMatrixCallsAnInvalidExperimentUnverified(t *testing.T) {
	t.Parallel()

	facts := restoreReadings("5.0.2", "yes", "", "", "hang")
	for i := range facts {
		if facts[i].Key == "pre.restore_ready" {
			facts[i] = fact("pre.restore_ready", "no")
		}
	}
	rec := restoreCensus(t, restoreCell{key: "bad-premise", facts: facts})

	result, err := (judge.RestoreMatrix{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["bad-premise"].Verdict; got != judge.Unverified {
		t.Errorf("verdict = %q, want %q: a hang without its declared premise is not a valid hang control", got, judge.Unverified)
	}
	if result.Verdict != judge.Unverified {
		t.Errorf("census verdict = %q, want %q", result.Verdict, judge.Unverified)
	}
}

func TestRestoreMatrixDoesNotInventAnAnswerFromMissingReadings(t *testing.T) {
	t.Parallel()

	rec := restoreCensus(t, restoreCell{key: "missing", facts: []record.Fact{
		fact("pre.restore_ready", "yes"),
		fact("source.version", "5.0.2"),
		fact("restore.timed_out", "no"),
	}})
	result, err := (judge.RestoreMatrix{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["missing"].Verdict; got != judge.Unverified {
		t.Errorf("verdict = %q, want %q", got, judge.Unverified)
	}
	if len(result.Cells["missing"].Because) == 0 {
		t.Error("the missing reading was not explained")
	}
}

type restoreCell struct {
	key    string
	status record.Status
	facts  []record.Fact
}

func restoreCensus(t *testing.T, cells ...restoreCell) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("restore", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open parent: %v", err)
	}
	for _, c := range cells {
		status := c.status
		if status == "" {
			status = record.StatusMeasured
		}
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: c.key}); err != nil {
			t.Fatalf("cell-started: %v", err)
		}
		if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: c.key, Status: status}); err != nil {
			t.Fatalf("cell-finished: %v", err)
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close parent: %v", err)
	}

	for _, c := range cells {
		status := c.status
		if status != "" && status != record.StatusMeasured {
			continue
		}
		cellDir, _ := written(t, step{
			label:  "restore the source fixture onto the target release",
			facts:  c.facts,
			expect: map[string]string{"fact.pre.restore_ready": "yes"},
		})
		destination := filepath.Join(dir, record.CellsDir, c.key)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatalf("make cells dir: %v", err)
		}
		if err := os.Rename(cellDir, destination); err != nil {
			t.Fatalf("move cell: %v", err)
		}
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read parent: %v", err)
	}
	return rec
}

func restoreReadings(source, timedOut, marker, sentinel, claimedOutcome string) []record.Fact {
	facts := []record.Fact{
		fact("pre.restore_ready", "yes"),
		fact("source.version", source),
		fact("restore.timed_out", timedOut),
		// This derived probe label is intentionally present and contradictory in
		// several tests. RestoreMatrix must never use it as its own evidence.
		fact("restore.outcome", claimedOutcome),
	}
	if marker != "" || timedOut == "no" {
		facts = append(facts, fact("marker.rung", marker))
	}
	if sentinel != "" {
		facts = append(facts, fact("sentinel.count", sentinel))
	}
	return facts
}

func fact(key, value string) record.Fact {
	return record.Fact{Key: key, Value: &value}
}
