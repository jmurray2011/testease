package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// The Runs screen is a listing of what the runs directory holds, newest first.
// Status is what each run measured; the verdict column shows only what judges
// wrote, and says so when a verdict was reached over bytes that are no longer
// the record's.

func recorded(t *testing.T, runs, id string, write func(w *record.Writer)) string {
	t.Helper()

	dir := filepath.Join(runs, id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	write(w)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

func emit(t *testing.T, w *record.Writer, kind record.Kind, step int, data any) {
	t.Helper()
	if _, err := w.Emit(kind, step, data); err != nil {
		t.Fatalf("Emit %s: %v", kind, err)
	}
}

// installCheck writes a two-step record whose second step failed its probe and
// whose target was kept for inspection, with the first step's output on disk.
func installCheck(t *testing.T, runs, id string) string {
	t.Helper()

	dir := recorded(t, runs, id, func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID:    id,
			Scenario: record.ScenarioRef{Name: "install-check"},
			Tool:     record.ToolRef{Version: "0.9.2"},
			Params:   map[string]string{"version": "1.2.3"},
			Host:     record.HostRef{User: "josh", Hostname: "workstation"},
		})
		emit(t, w, record.KindTargetResolved, 0, record.TargetResolved{
			Host: "app", Resolved: map[string]any{"ref": "ssh:192.0.2.10"},
		})
		out, errPath := record.StepPaths(1, "install package")
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: "install package", Host: "app"})
		emit(t, w, record.KindOperation, 1, record.Operation{
			RC: 0, DurationS: 3.5, Stdout: out, Stderr: errPath, Status: record.StatusMeasured,
		})
		version := "1.2.3-1.el9"
		emit(t, w, record.KindFact, 1, record.Fact{Key: "package.version", Value: &version})
		emit(t, w, record.KindStepStarted, 2, record.StepStarted{Label: "check service", Host: "app"})
		emit(t, w, record.KindOperation, 2, record.Operation{RC: 1, DurationS: 12.4, Status: record.StatusProbeFail})
		emit(t, w, record.KindFact, 2, record.Fact{Key: "http.status", Error: "connection refused"})
		emit(t, w, record.KindTargetDisposition, 0, record.TargetDisposition{Host: "app", Action: "retain-failed"})
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusProbeFail})
	})
	out, errPath := record.StepPaths(1, "install package")
	if err := os.MkdirAll(filepath.Join(dir, record.StepsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(out)), []byte("installed app-1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(errPath)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func judged(t *testing.T, dir, verdict string, current bool) {
	t.Helper()

	events := "sha256:other"
	if current {
		body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
		if err != nil {
			t.Fatal(err)
		}
		events = record.HashBytes(body)
	}
	if _, err := judge.Save(dir, judge.Verdict{
		Schema: judge.Schema, Judge: "expect", Version: "3",
		JudgedAt: time.Date(2026, 11, 23, 17, 22, 0, 0, time.UTC),
		Evidence: judge.Evidence{Events: events}, Verdict: verdict,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func shownRunIDs(v *runsView) []string {
	ids := make([]string, len(v.shown))
	for i, r := range v.shown {
		ids[i] = r.RunID
	}
	return ids
}

func TestTheRunsScreenListsRecordsNewestFirst(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261121T080000Z")
	installCheck(t, runs, "api-check-20261123T171533Z")
	installCheck(t, runs, "install-check-20261122T080000Z-install-old")

	v := newRunsView(runs, func(string) {})
	got := shownRunIDs(v)
	want := []string{"api-check-20261123T171533Z", "install-check-20261122T080000Z-install-old", "install-check-20261121T080000Z"}
	if len(got) != len(want) {
		t.Fatalf("shown %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shown %v, want %v", got, want)
		}
	}
	if rows, _ := v.table.Length(); rows != 3 {
		t.Errorf("table has %d rows, want 3", rows)
	}
	if v.count.Text != "3 records" {
		t.Errorf("count = %q", v.count.Text)
	}
}

func TestARowSaysWhatTheRunMeasuredAndWhatBecameOfItsTarget(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := installCheck(t, runs, "install-check-20261123T171533Z")
	judged(t, dir, "regressed", true)

	v := newRunsView(runs, func(string) {})
	want := map[int]string{
		colRun:      "install-check-20261123T171533Z",
		colScenario: "install-check",
		colMeasured: "2 steps",
		colStatus:   "probe-fail",
		colVerdict:  "expect@3 regressed",
		colFate:     "retained after failure",
	}
	for col, text := range want {
		if got := v.cellText(0, col); got != text {
			t.Errorf("column %d = %q, want %q", col, got, text)
		}
	}
}

// A verdict over other bytes is not this record's verdict.
func TestAVerdictOverOtherBytesIsNotShownAsTheRecordsVerdict(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	judged(t, installCheck(t, runs, "install-check-20261123T171533Z"), "pass", false)
	installCheck(t, runs, "install-check-20261124T000000Z")

	v := newRunsView(runs, func(string) {})
	if got := v.cellText(1, colVerdict); got != "expect@3 pass, over earlier events" {
		t.Errorf("stale verdict shown as %q", got)
	}
	if got := v.cellText(0, colVerdict); got != "not judged" {
		t.Errorf("unjudged run shown as %q", got)
	}
}

func TestTheFilterNarrowsByRunScenarioOrStatus(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261123T171533Z")
	recorded(t, runs, "hello-20261121T083007Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "hello-20261121T083007Z", Scenario: record.ScenarioRef{Name: "hello"}})
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
	})

	v := newRunsView(runs, func(string) {})
	v.filter.SetText("hello")
	if got := shownRunIDs(v); len(got) != 1 || got[0] != "hello-20261121T083007Z" {
		t.Errorf("filtered to %v", got)
	}
	v.filter.SetText("probe-fail")
	if got := shownRunIDs(v); len(got) != 1 || got[0] != "install-check-20261123T171533Z" {
		t.Errorf("filtered to %v", got)
	}
	if v.count.Text != "1 of 2 records" {
		t.Errorf("count = %q", v.count.Text)
	}
}

func TestSelectingARowOpensThatRecord(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := installCheck(t, runs, "install-check-20261123T171533Z")

	var opened string
	v := newRunsView(runs, func(d string) { opened = d })
	v.table.Select(widget.TableCellID{Row: 0, Col: colScenario})
	if opened != dir {
		t.Errorf("opened %q, want %q", opened, dir)
	}
}

// A listing that hid an unreadable record would lose evidence.
func TestAnUnreadableRecordIsListed(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runs, "broken-20261101T000000Z", record.EventsFile), 0o755); err != nil {
		t.Fatal(err)
	}

	v := newRunsView(runs, func(string) {})
	if got := v.cellText(0, colStatus); got != browse.Unreadable {
		t.Errorf("status = %q, want unreadable", got)
	}
}

func TestAMissingRunsDirectoryIsSaidRatherThanShownEmpty(t *testing.T) {
	test.NewTempApp(t)

	missing := filepath.Join(t.TempDir(), "nowhere")
	v := newRunsView(missing, func(string) {})
	if !v.message.Visible() || v.message.Text != "No runs directory at "+missing {
		t.Errorf("message = %q (visible %v)", v.message.Text, v.message.Visible())
	}
}

func TestReloadPicksUpANewRecord(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	v := newRunsView(runs, func(string) {})
	if len(v.shown) != 0 {
		t.Fatalf("shown %v", shownRunIDs(v))
	}
	installCheck(t, runs, "install-check-20261123T171533Z")
	v.reload()
	if len(v.shown) != 1 {
		t.Errorf("shown %v after reload", shownRunIDs(v))
	}
}

// A record with no run-finished is either still being written or was left by
// a run that died. Only the lock can tell them apart, and the screen says which.
func TestARecordStillBeingWrittenReadsAsRunning(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := filepath.Join(runs, "long-20261124T010000Z")
	w, err := record.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "long-20261124T010000Z", Scenario: record.ScenarioRef{Name: "long"}})
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}

	v := newRunsView(runs, func(string) {})
	if got := v.cellText(0, colStatus); got != "running" {
		t.Errorf("status while writing = %q, want running", got)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	v.reload()
	if got := v.cellText(0, colStatus); got != string(record.StatusUnfinished) {
		t.Errorf("status after the writer let go = %q, want unfinished", got)
	}
}

// Run ids carry a scenario, a timestamp and sometimes a batch's name; a column
// that cut them off would hide the one thing that tells two runs apart.
func TestTheRunColumnFitsTheLongestRunID(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	long := "vtdocs-sysadmin-sso-landing-20261121T083545Z-install-old"
	installCheck(t, runs, long)
	installCheck(t, runs, "hello-20261121T083007Z")

	v := newRunsView(runs, func(string) {})
	need := fyne.MeasureText(long, theme.TextSize(), fyne.TextStyle{Monospace: true}).Width
	if v.widths[colRun] < need {
		t.Errorf("run column is %.0f wide, want at least %.0f for %s", v.widths[colRun], need, long)
	}
}
