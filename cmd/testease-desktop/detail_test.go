package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
