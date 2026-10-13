package runner_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
)

// THE RESUME GATE. These are check-sweep-resume.sh's cases, ported.
//
// A sweep is a census, and the one thing worse than a census that dies at cell
// 70 of 78 is one that resumes and then reads as a complete run over a set it
// did not actually measure. What is already done has to be derived from the
// record itself - anything else is bookkeeping that can disagree with the
// evidence.
//
// One rule inverts on the way across, deliberately. The bash counted a rung
// done when it had a row, and a NOBOOT row is a row - so an infra-failed cell
// counted as done and a "complete" census could quietly contain cells nobody
// measured. Here a cell is done when it is measured, and nothing else is.

func scopeOf(t *testing.T, values ...string) runner.Scope {
	t.Helper()

	return runner.Scope{
		Axes:   map[string][]string{"from": values},
		Source: "cli",
	}
}

func keysOf(cells []runner.Cell) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, c.Key)
	}
	return out
}

// prior writes a parent record that finished the given cells with the given
// statuses, in order.
func prior(t *testing.T, finished map[string]record.Status, order ...string) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("census", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: filepath.Base(dir)}); err != nil {
		t.Fatalf("run-started: %v", err)
	}
	for _, key := range order {
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: key}); err != nil {
			t.Fatalf("cell-started: %v", err)
		}
		if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: key, Status: finished[key]}); err != nil {
			t.Fatalf("cell-finished: %v", err)
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

func TestCellsFollowTheScopesOwnOrder(t *testing.T) {
	t.Parallel()

	cells, err := scopeOf(t, "6.2.4", "5.0.2", "7.0.1").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if got := strings.Join(keysOf(cells), ","); got != "6.2.4,5.0.2,7.0.1" {
		t.Errorf("cells = %s, want the order the scope gave", got)
	}
	if got := cells[0].Params["from"]; got != "6.2.4" {
		t.Errorf("cell params = %v", cells[0].Params)
	}
}

func TestMultipleAxesJoinTheirValues(t *testing.T) {
	t.Parallel()

	scope := runner.Scope{
		Axes:   map[string][]string{"from": {"6.2.4", "7.0.1"}, "os": {"el8", "el9"}},
		Source: "cli",
	}
	cells, err := scope.Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if got := strings.Join(keysOf(cells), " "); got != "6.2.4+el8 6.2.4+el9 7.0.1+el8 7.0.1+el9" {
		t.Errorf("cells = %s", got)
	}
}

func TestExcludedCellsAreNotScheduled(t *testing.T) {
	t.Parallel()

	scope := runner.Scope{
		Axes: map[string][]string{
			"from": {"6.2.4", "7.0.1"},
			"to":   {"7.2.1", "7.2.3"},
		},
		Exclude: []map[string]string{{"from": "7.0.1", "to": "7.2.1"}},
		Source:  "scenario",
	}
	cells, err := scope.Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if got := strings.Join(keysOf(cells), " "); got != "6.2.4+7.2.1 6.2.4+7.2.3 7.0.1+7.2.3" {
		t.Errorf("cells = %s", got)
	}
}

func TestExclusionsCannotRemoveEveryCell(t *testing.T) {
	t.Parallel()

	_, err := (runner.Scope{
		Axes:    map[string][]string{"from": {"old"}},
		Exclude: []map[string]string{{"from": "old"}},
	}).Cells()
	if err == nil || !strings.Contains(err.Error(), "every cell") {
		t.Fatalf("err = %v, want an empty scope error", err)
	}
}

func TestACellKeyThatCannotBeToldApartIsRefused(t *testing.T) {
	t.Parallel()

	// Sanitising a key is lossy, and two values that sanitise to the same
	// string would write into one directory. Better to refuse the scope than
	// to have two cells overwrite each other's evidence.
	_, err := scopeOf(t, "a/b", "a b").Cells()
	if err == nil {
		t.Fatal("a scope whose cells share a key was accepted")
	}
	if !strings.Contains(err.Error(), "a-b") {
		t.Errorf("error %q does not name the key they collide on", err)
	}
}

func TestAnEmptyScopeHasNoCells(t *testing.T) {
	t.Parallel()

	cells, err := runner.Scope{Source: "cli"}.Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if len(cells) != 0 {
		t.Errorf("cells = %v, want none", keysOf(cells))
	}
}

func TestDoneIsReadOutOfTheRecord(t *testing.T) {
	t.Parallel()

	rec := prior(t, map[string]record.Status{
		"5.0.2": record.StatusMeasured,
		"6.0.3": record.StatusMeasured,
		"6.2.4": record.StatusMeasured,
	}, "5.0.2", "6.0.3", "6.2.4")

	done := runner.Measured(rec)
	for _, key := range []string{"5.0.2", "6.0.3", "6.2.4"} {
		if !done[key] {
			t.Errorf("%s has a measured cell-finished and is not counted done", key)
		}
	}
}

func TestARecordWithNoCellsHasNothingDone(t *testing.T) {
	t.Parallel()

	if done := runner.Measured(prior(t, nil)); len(done) != 0 {
		t.Errorf("done = %v, want nothing", done)
	}
}

func TestRemainingIsTheScopeMinusWhatMeasured(t *testing.T) {
	t.Parallel()

	// The whole point: 5.0.5 and 7.0.1 were never reached, so they are what
	// remains, in the order the scope gave.
	cells, err := scopeOf(t, "5.0.2", "5.0.5", "6.0.3", "6.2.4", "7.0.1").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	rec := prior(t, map[string]record.Status{
		"5.0.2": record.StatusMeasured,
		"6.0.3": record.StatusMeasured,
		"6.2.4": record.StatusMeasured,
	}, "5.0.2", "6.0.3", "6.2.4")

	if got := strings.Join(keysOf(runner.Remaining(cells, runner.Measured(rec))), ","); got != "5.0.5,7.0.1" {
		t.Errorf("remaining = %s, want 5.0.5,7.0.1", got)
	}
}

func TestAFullyCoveredCensusHasNothingRemaining(t *testing.T) {
	t.Parallel()

	cells, err := scopeOf(t, "5.0.2", "6.0.3", "6.2.4").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	rec := prior(t, map[string]record.Status{
		"5.0.2": record.StatusMeasured,
		"6.0.3": record.StatusMeasured,
		"6.2.4": record.StatusMeasured,
	}, "5.0.2", "6.0.3", "6.2.4")

	if got := runner.Remaining(cells, runner.Measured(rec)); len(got) != 0 {
		t.Errorf("remaining = %v, want none", keysOf(got))
	}
}

func TestWithNoPriorRecordEveryCellRemains(t *testing.T) {
	t.Parallel()

	cells, err := scopeOf(t, "6.2.4", "5.0.2").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if got := strings.Join(keysOf(runner.Remaining(cells, nil)), ","); got != "6.2.4,5.0.2" {
		t.Errorf("remaining = %s, want the scope in the order given", got)
	}
}

func TestALongerKeySharingAPrefixIsNotDone(t *testing.T) {
	t.Parallel()

	// Version-shaped strings compare as whole keys, never as substrings: 6.2.4
	// having measured must not make 6.2.40 look done.
	cells, err := scopeOf(t, "6.2.4", "6.2.40").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	rec := prior(t, map[string]record.Status{"6.2.4": record.StatusMeasured}, "6.2.4")

	if got := strings.Join(keysOf(runner.Remaining(cells, runner.Measured(rec))), ","); got != "6.2.40" {
		t.Errorf("remaining = %s, want 6.2.40", got)
	}
}

func TestACellThatFailedItsInfrastructureIsNotDone(t *testing.T) {
	t.Parallel()

	// The inversion. The bash counted a rung done when it had a row, and a
	// NOBOOT row is a row - so a census could close "complete" over cells
	// nobody had measured. Here only measured is done, and everything else
	// comes back on a resume.
	cells, err := scopeOf(t, "5.0.2", "6.0.3", "6.2.4", "7.0.1").Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	rec := prior(t, map[string]record.Status{
		"5.0.2": record.StatusMeasured,
		"6.0.3": record.StatusInfraFail,
		"6.2.4": record.StatusProbeFail,
		"7.0.1": record.StatusInterrupted,
	}, "5.0.2", "6.0.3", "6.2.4", "7.0.1")

	if got := strings.Join(keysOf(runner.Remaining(cells, runner.Measured(rec))), ","); got != "6.0.3,6.2.4,7.0.1" {
		t.Errorf("remaining = %s, want every cell that was not measured", got)
	}
}

func TestACellThatStartedAndNeverFinishedIsNotDone(t *testing.T) {
	t.Parallel()

	// A cell that died mid-flight wrote no cell-finished, which is exactly the
	// set a resume must re-run, and it needs no extra bookkeeping to know it.
	dir := filepath.Join(t.TempDir(), record.RunID("census", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: "6.2.4"}); err != nil {
		t.Fatalf("cell-started: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if done := runner.Measured(rec); done["6.2.4"] {
		t.Error("a cell that started and never finished counted as done")
	}
}

func TestScopeFromAPriorRecordIsTheSetItWroteDown(t *testing.T) {
	t.Parallel()

	// A census claims to cover the set it recorded. Re-discovering on resume
	// would make the claim host-dependent: two people with different stores
	// get differently scoped sweeps whose records read identically unless the
	// set is written down.
	dir := filepath.Join(t.TempDir(), record.RunID("census", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID: filepath.Base(dir),
		Matrix: &record.Matrix{
			Axes:         map[string][]string{"from": {"1.0", "2.0", "3.0"}},
			Exclude:      []map[string]string{{"from": "2.0"}},
			Source:       "cli",
			DiscoveredOn: "labhost",
		},
	}); err != nil {
		t.Fatalf("run-started: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	scope, err := runner.RecordedScope(rec)
	if err != nil {
		t.Fatalf("RecordedScope: %v", err)
	}
	cells, err := scope.Cells()
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if got := strings.Join(keysOf(cells), ","); got != "1.0,3.0" {
		t.Errorf("scope = %s, want what the record wrote down", got)
	}
	if len(scope.Exclude) != 1 || scope.Exclude[0]["from"] != "2.0" {
		t.Fatalf("scope exclusions = %#v", scope.Exclude)
	}
}

func TestARecordWithNoScopeCannotBeResumed(t *testing.T) {
	t.Parallel()

	if _, err := runner.RecordedScope(prior(t, nil)); err == nil {
		t.Fatal("a single-target record was resumed as a census")
	}
}
