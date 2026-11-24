package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/record"
)

// A record opened: its steps on the left, the selected step's output and facts
// in the middle, and on the right the verdicts judges wrote, what became of
// the targets, and anything the reader or the run had to say about the record.

func TestARecordOpensOnItsFirstStepThatDidNotMeasure(t *testing.T) {
	test.NewTempApp(t)

	dir := installCheck(t, t.TempDir(), "install-check-20261123T171533Z")
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	if v.selected != 1 {
		t.Errorf("selected step index %d, want the probe-fail step", v.selected)
	}
	if got := v.stepTitle.Text; got != "2 · check service" {
		t.Errorf("step title = %q", got)
	}
	if got := v.stepMeta.Text; got != "rc 1 · 12.4 s · host app" {
		t.Errorf("step meta = %q", got)
	}
	facts := v.factsText()
	if !strings.Contains(facts, "http.status") || !strings.Contains(facts, "no reading: connection refused") {
		t.Errorf("facts = %q, want the missing reading shown with its error", facts)
	}
}

func TestSelectingAStepShowsItsOutput(t *testing.T) {
	test.NewTempApp(t)

	dir := installCheck(t, t.TempDir(), "install-check-20261123T171533Z")
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	v.steps.Select(0)
	if got := v.stdout.Text(); got != "installed app-1.2.3\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := v.stderr.Text(); got != "(empty)" {
		t.Errorf("stderr = %q, want it said that it is empty", got)
	}
	v.steps.Select(1)
	if got := v.stdout.Text(); got != "(this step recorded no stdout)" {
		t.Errorf("stdout of a step with none = %q", got)
	}
}

func TestTheHeaderCarriesTheRecordsProvenance(t *testing.T) {
	test.NewTempApp(t)

	dir := installCheck(t, t.TempDir(), "install-check-20261123T171533Z")
	judged(t, dir, "regressed", true)
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	meta := v.metaText()
	for _, want := range []string{"install-check", "version=1.2.3", "josh@workstation", "0.9.2", "sha256:"} {
		if !strings.Contains(meta, want) {
			t.Errorf("header %q is missing %q", meta, want)
		}
	}
	if got := v.verdictsText(); !strings.Contains(got, "expect@3") || !strings.Contains(got, "regressed") ||
		!strings.Contains(got, "against these events") {
		t.Errorf("verdicts = %q", got)
	}
	if got := v.targetsText(); !strings.Contains(got, "app") || !strings.Contains(got, "retained after failure") {
		t.Errorf("targets = %q", got)
	}
	if got := v.notesText(); !strings.Contains(got, "read cleanly") {
		t.Errorf("notes = %q", got)
	}
}

func TestAStaleVerdictSaysItWasReachedOverOtherEvents(t *testing.T) {
	test.NewTempApp(t)

	dir := installCheck(t, t.TempDir(), "install-check-20261123T171533Z")
	judged(t, dir, "pass", false)
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	if got := v.verdictsText(); !strings.Contains(got, "over earlier events") {
		t.Errorf("verdicts = %q", got)
	}
}

// Records travel: they are published and read on machines that did not write
// them. A step's output path that leaves the record is not followed.
func TestAnOutputPathOutsideTheRecordIsNotFollowed(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	if err := os.WriteFile(filepath.Join(runs, "secret"), []byte("do not show"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := recorded(t, runs, "sly-20261101T000000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "sly-20261101T000000Z", Scenario: record.ScenarioRef{Name: "sly"}})
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: "peek", Host: "box"})
		emit(t, w, record.KindOperation, 1, record.Operation{RC: 0, Stdout: "../secret", Status: record.StatusMeasured})
	})
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	if got := v.stdout.Text(); strings.Contains(got, "do not show") || !strings.Contains(got, "outside the record") {
		t.Errorf("stdout = %q", got)
	}
}

func TestReadNotesAreShownNotHidden(t *testing.T) {
	test.NewTempApp(t)

	dir := installCheck(t, t.TempDir(), "install-check-20261123T171533Z")
	f, err := os.OpenFile(filepath.Join(dir, record.EventsFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema":1,"seq":99,"kind":"run-fin`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	if got := v.notesText(); !strings.Contains(got, "torn tail") {
		t.Errorf("notes = %q", got)
	}
}

func TestLongOutputShowsItsTailAndSaysSo(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "big.out")
	body := strings.Repeat("x", 100) + "\nthe end\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readTail(path, 16)
	if !strings.HasSuffix(got, "the end\n") || !strings.Contains(got, "showing the last") {
		t.Errorf("tail = %q", got)
	}
}

// Nothing a record holds may set the window's width. A drive step's output can
// carry lines thousands of characters long, and labels, probe paths and params
// come from scenarios nobody here wrote; the window must scroll or truncate
// rather than grow past the screen.
func TestALongRecordDoesNotWidenTheWindow(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	long := strings.Repeat("a-very-long-unbroken-token-", 60)
	dir := recorded(t, runs, "wide-20261101T000000Z", func(w *record.Writer) {
		params := map[string]string{}
		for i := 0; i < 20; i++ {
			params[fmt.Sprintf("param%02d", i)] = long[:60]
		}
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID: "wide-20261101T000000Z", Scenario: record.ScenarioRef{Name: "wide"}, Params: params,
		})
		emit(t, w, record.KindTargetResolved, 0, record.TargetResolved{Host: "box", Resolved: map[string]any{"ref": long}})
		out, _ := record.StepPaths(1, "drive")
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: long, Host: "box", Probe: "drive:" + long})
		emit(t, w, record.KindOperation, 1, record.Operation{RC: 0, Stdout: out, Status: record.StatusMeasured})
		emit(t, w, record.KindFact, 1, record.Fact{Key: long, Error: long})
	})
	out, _ := record.StepPaths(1, "drive")
	if err := os.MkdirAll(filepath.Join(dir, record.StepsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(out)), []byte(long+long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	w := test.NewWindow(v.content)
	defer w.Close()
	w.Resize(fyne.NewSize(1200, 800))
	if got := v.content.MinSize().Width; got > 1000 {
		t.Errorf("the record asks for a window %.0f wide, want it to fit a 1000-wide screen", got)
	}
}

// A matrix run measured in its cells. Its record lists them, and picking one
// opens that cell's own record.
func TestAMatrixRecordOpensOnItsCells(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := recorded(t, runs, "docs-compat-20261106T001452Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "docs-compat-20261106T001452Z", Scenario: record.ScenarioRef{Name: "docs-compat"}})
		emit(t, w, record.KindCellStarted, 0, record.CellStarted{Key: "7.2.6+1.0.10"})
		emit(t, w, record.KindCellFinished, 0, record.CellFinished{Key: "7.2.6+1.0.10", Status: record.StatusMeasured})
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
	})

	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	if !strings.Contains(v.stepTitle.Text, "1 cell") {
		t.Errorf("title = %q, want it to say the steps are in its cell", v.stepTitle.Text)
	}
	if got := v.targetsText(); !strings.Contains(got, "each cell") {
		t.Errorf("targets = %q, want it to say they are in the cells", got)
	}
	var opened string
	v.onOpenCell = func(d string) { opened = d }
	v.cells.Select(0)
	if want := filepath.Join(dir, record.CellsDir, "7.2.6+1.0.10"); opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}
}
