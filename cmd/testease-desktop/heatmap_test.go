package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// A matrix run reads as a grid: its first axis down the side, the rest
// across, each cell coloured by what it came to.

func cellAt(h heatmap, row, col string) matrixCell {
	r, c := slices.Index(h.Rows, row), slices.Index(h.Cols, col)
	if r < 0 || c < 0 {
		return matrixCell{}
	}
	return h.Cells[r][c]
}

func TestAMatrixIsLaidOutByItsAxes(t *testing.T) {
	m := &record.Matrix{Axes: map[string][]string{
		"version": {"7.2.6", "7.3.0"},
		"docs":    {"1.0.10", "1.1.0"},
		"os":      {"rhel9"},
	}}
	cells := []matrixCell{
		{Key: "1.0.10+rhel9+7.2.6", Outcome: outcomePassed, Present: true},
		{Key: "1.1.0+rhel9+7.3.0", Outcome: outcomeFailed, Present: true},
	}
	h := buildHeatmap(m, cells)
	// Axes are taken in name order, as the runner files them: docs, os,
	// version. The first goes down the side.
	if h.RowAxis != "docs" || !slices.Equal(h.ColAxes, []string{"os", "version"}) {
		t.Fatalf("axes %q by %q", h.RowAxis, h.ColAxes)
	}
	if !slices.Equal(h.Rows, []string{"1.0.10", "1.1.0"}) || !slices.Equal(h.Cols, []string{"rhel9 · 7.2.6", "rhel9 · 7.3.0"}) {
		t.Fatalf("rows %q cols %q", h.Rows, h.Cols)
	}
	if got := cellAt(h, "1.0.10", "rhel9 · 7.2.6"); got.Outcome != outcomePassed || !got.Present {
		t.Errorf("passed cell %+v", got)
	}
	if got := cellAt(h, "1.1.0", "rhel9 · 7.3.0"); got.Outcome != outcomeFailed {
		t.Errorf("failed cell %+v", got)
	}
	if got := cellAt(h, "1.0.10", "rhel9 · 7.3.0"); got.Present {
		t.Errorf("a cell that never ran is shown as run: %+v", got)
	}
}

func TestAValueACellKeyCannotHoldIsFiledAsTheRunnerFilesIt(t *testing.T) {
	m := &record.Matrix{Axes: map[string][]string{"image": {"ubuntu:24.04"}}}
	h := buildHeatmap(m, []matrixCell{{Key: "ubuntu-24.04", Outcome: outcomePassed, Present: true}})
	if got := cellAt(h, "ubuntu:24.04", ""); !got.Present {
		t.Errorf("cell not placed: rows %q cols %q unplaced %+v", h.Rows, h.Cols, h.Unplaced)
	}
}

func TestACellOutsideTheScopeIsListedNotLost(t *testing.T) {
	m := &record.Matrix{Axes: map[string][]string{"v": {"1"}}}
	h := buildHeatmap(m, []matrixCell{{Key: "1", Present: true}, {Key: "2", Present: true}})
	if len(h.Unplaced) != 1 || h.Unplaced[0].Key != "2" {
		t.Errorf("unplaced %+v", h.Unplaced)
	}
}

func TestAMatrixWithNoScopeListsItsCellsByKey(t *testing.T) {
	h := buildHeatmap(nil, []matrixCell{{Key: "a", Present: true}, {Key: "b", Present: true}})
	if !slices.Equal(h.Rows, []string{"a", "b"}) || len(h.Cols) != 1 {
		t.Errorf("rows %q cols %q", h.Rows, h.Cols)
	}
}

func TestAMatrixIsCountedByOutcome(t *testing.T) {
	cells := []matrixCell{
		{Present: true, Outcome: outcomePassed},
		{Present: true, Outcome: outcomePassed},
		{Present: true, Outcome: outcomeFailed},
		{Present: true, Outcome: outcomeBroken},
	}
	if got, want := tally(cells), "4 cells: 2 passed, 1 failed, 1 measurement broke"; got != want {
		t.Errorf("tally %q, want %q", got, want)
	}
}

func docsCompat(t *testing.T, runs string) string {
	t.Helper()
	id := "docs-compat-20261106T001452Z"
	dir := recorded(t, runs, id, func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID: id, Scenario: record.ScenarioRef{Name: "docs-compat"},
			Matrix: &record.Matrix{Axes: map[string][]string{"version": {"7.2.6", "7.3.0"}, "docs": {"1.0.10"}}},
		})
		for _, k := range []string{"1.0.10+7.2.6", "1.0.10+7.3.0"} {
			emit(t, w, record.KindCellStarted, 0, record.CellStarted{Key: k})
			emit(t, w, record.KindCellFinished, 0, record.CellFinished{Key: k, Status: record.StatusMeasured})
		}
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
	})
	cells := filepath.Join(dir, record.CellsDir)
	for i, k := range []string{"1.0.10+7.2.6", "1.0.10+7.3.0"} {
		cell := recorded(t, cells, k, func(w *record.Writer) {
			emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: id + "/" + k, Scenario: record.ScenarioRef{Name: "docs-compat"}})
			emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
		})
		verdict := "pass"
		if i == 1 {
			verdict = "fail"
		}
		judged(t, cell, verdict, true)
	}
	return dir
}

func TestAMatrixRecordShowsItsGridAndATappedCellOpens(t *testing.T) {
	test.NewTempApp(t)
	dir := docsCompat(t, t.TempDir())
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if v.heat == nil {
		t.Fatal("no grid on a matrix record")
	}
	h := v.heat.h
	pass := cellAt(h, "1.0.10", "7.2.6")
	fail := cellAt(h, "1.0.10", "7.3.0")
	if pass.Outcome != outcomePassed || fail.Outcome != outcomeFailed {
		t.Errorf("outcomes %v %v", pass.Outcome, fail.Outcome)
	}
	if !strings.Contains(v.heat.summary.Text, "1 passed, 1 failed") {
		t.Errorf("summary %q", v.heat.summary.Text)
	}
	var opened string
	v.onOpenCell = func(d string) { opened = d }
	v.heat.open(fail)
	if want := filepath.Join(dir, record.CellsDir, "1.0.10+7.3.0"); opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}
}

// matrixJudgedInParent is a matrix run whose cells have no verdicts of their
// own: the matrix judge writes each cell's verdict into the parent record.
func matrixJudgedInParent(t *testing.T, runs string, staleCell bool) string {
	t.Helper()
	id := "docs-vtapi-compat-20261106T041701Z"
	keys := []string{"1.0.10+7.2.6", "1.0.10+7.4.3"}
	dir := recorded(t, runs, id, func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID: id, Scenario: record.ScenarioRef{Name: "docs-vtapi-compat"},
			Matrix: &record.Matrix{Axes: map[string][]string{"version": {"7.2.6", "7.4.3"}, "docs": {"1.0.10"}}},
		})
		for _, k := range keys {
			emit(t, w, record.KindCellStarted, 0, record.CellStarted{Key: k})
			emit(t, w, record.KindCellFinished, 0, record.CellFinished{Key: k, Status: record.StatusMeasured})
		}
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
	})
	cellHashes := map[string]string{}
	for _, k := range keys {
		c := recorded(t, filepath.Join(dir, record.CellsDir), k, func(w *record.Writer) {
			emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: k, Scenario: record.ScenarioRef{Name: "docs-vtapi-compat"}})
			emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
		})
		body, err := os.ReadFile(filepath.Join(c, record.EventsFile))
		if err != nil {
			t.Fatal(err)
		}
		cellHashes[k] = record.HashBytes(body)
	}
	if staleCell {
		cellHashes[keys[1]] = "sha256:other"
	}
	body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := judge.Save(dir, judge.Verdict{
		Schema: judge.Schema, Judge: "matrix", Version: "3", Verdict: "regressed",
		JudgedAt: time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC),
		Evidence: judge.Evidence{Events: record.HashBytes(body), Cells: cellHashes},
		Cells: map[string]judge.StepVerdict{
			keys[0]: {Verdict: "pass"},
			keys[1]: {Verdict: "regressed", Because: []string{"check health: http.status 503, expected 200"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestACellJudgedInItsMatrixRunShowsThatVerdict(t *testing.T) {
	test.NewTempApp(t)
	dir := matrixJudgedInParent(t, t.TempDir(), false)
	d, err := browse.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cells := matrixCells(d, false)
	h := buildHeatmap(&record.Matrix{Axes: map[string][]string{"version": {"7.2.6", "7.4.3"}, "docs": {"1.0.10"}}}, cells)
	pass, bad := cellAt(h, "1.0.10", "7.2.6"), cellAt(h, "1.0.10", "7.4.3")
	if pass.Outcome != outcomePassed || bad.Outcome != outcomeFailed {
		t.Errorf("outcomes %v %v", pass.Outcome, bad.Outcome)
	}
	if bad.Word != "regressed" || bad.By != "matrix@3" {
		t.Errorf("failed cell says %q by %q", bad.Word, bad.By)
	}
}

func TestACellVerdictOverOtherCellEventsIsNotShown(t *testing.T) {
	test.NewTempApp(t)
	dir := matrixJudgedInParent(t, t.TempDir(), true)
	d, err := browse.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range matrixCells(d, false) {
		if c.Key == "1.0.10+7.4.3" && c.Outcome != outcomeUnjudged {
			t.Errorf("a verdict over other events counted: %+v", c)
		}
	}
}

func TestAnOpenedCellSaysWhatItsMatrixRunsJudgeSaid(t *testing.T) {
	test.NewTempApp(t)
	dir := matrixJudgedInParent(t, t.TempDir(), false)
	v, err := newDetailView(filepath.Join(dir, record.CellsDir, "1.0.10+7.4.3"), func() {})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(v.verdicts, "\n")
	if !strings.Contains(got, "matrix@3") || !strings.Contains(got, "regressed") || !strings.Contains(got, "http.status 503") {
		t.Errorf("verdicts %q", got)
	}
}

func TestACellOfAStoppedMatrixRunIsNotCalledRunning(t *testing.T) {
	m := matrixCell{Present: true, Outcome: outcomeIncomplete}
	if got := cellWord(m, false); got != "unfinished" {
		t.Errorf("word %q", got)
	}
	if got := cellWord(m, true); got != "running" {
		t.Errorf("word while the run is written %q", got)
	}
}
