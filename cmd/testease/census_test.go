package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
)

// A census is judged through its parent. The cells hold the evidence, so a
// cell whose events.jsonl the reader had to repair is damage the verdict rests
// on - and a verdict that says `matrix@3 pass` with nothing on stderr tells the
// reader the opposite.
func TestJudgingACensusReportsWhatItsCellsHadToTolerate(t *testing.T) {
	t.Parallel()

	dir := judgeableCensus(t)

	root := newRoot()
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs([]string{"judge", "--judge", "matrix", dir})

	if err := root.Execute(); err != nil {
		t.Fatalf("judge: %v (stderr %q)", err, errs.String())
	}
	if !strings.Contains(out.String(), "pass") {
		t.Fatalf("verdict = %q, want a pass over the cell that measured", out.String())
	}
	if !strings.Contains(errs.String(), "7.2.3") {
		t.Errorf("the judge named no damaged cell: %q", errs.String())
	}
	if !strings.Contains(errs.String(), "not valid JSON") {
		t.Errorf("the judge hid a cell's read repair: %q", errs.String())
	}
}

// judgeableCensus writes a parent with one cell that measured, declared an
// expectation, met it, and carries a line no reader can parse.
func judgeableCensus(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("sweep", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, e := range []struct {
		kind record.Kind
		data any
	}{
		{record.KindRunStarted, record.RunStarted{RunID: filepath.Base(dir)}},
		{record.KindCellStarted, record.CellStarted{Key: "7.2.3"}},
		{record.KindCellFinished, record.CellFinished{Key: "7.2.3", Status: record.StatusMeasured}},
		{record.KindRunFinished, record.RunFinished{Status: record.StatusMeasured}},
	} {
		if _, err := w.Emit(e.kind, 0, e.data); err != nil {
			t.Fatalf("%s: %v", e.kind, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cell := filepath.Join(dir, record.CellsDir, "7.2.3")
	if err := os.MkdirAll(cell, 0o755); err != nil {
		t.Fatalf("make cell: %v", err)
	}
	const lines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"step-started","step":1,"data":{"label":"install","host":"box"}}
{"schema":1,"seq":3,"ts":"2026-08-12T14:03:23.500Z","kind":"control-checked","step":1,"data":{"label":"install","expected":{"rc":"0"},"observed":{"rc":"0"},"ok":true}}
{"schema":1,"seq":4,"ts":"2026-08-12T14:03:24.000Z","kind":"operation","step":1,"data":{"rc":0,"duration_s":1,"claim":{"text":""},"status":"measured"}}
{"schema":1,"seq":5,"ts":"2026-08-12T14:03:25.000Z","kind":"run-finished","step":0,"data":{"status":"measured"}}
{"schema":1,"seq":6,"ts":"2026-08-12T14:03:26.000Z","kind":"fact","step":1,"data":{
`
	if err := os.WriteFile(filepath.Join(cell, record.EventsFile), []byte(lines), 0o644); err != nil {
		t.Fatalf("write cell events: %v", err)
	}
	return dir
}

// A flag that cannot mean anything is a usage error, and usage errors are
// caught before any evidence is read. Caught after judging, the operator is
// told the record has nothing to judge - which is true, and not what they got
// wrong.
func TestForceBaselineIsRefusedBeforeAnyEvidenceIsRead(t *testing.T) {
	t.Parallel()

	dir := unjudgeableRecord(t)

	root := newRoot()
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs([]string{"judge", "--force-baseline", dir})

	err := root.Execute()
	if err == nil {
		t.Fatalf("--force-baseline alone was accepted: %q", out.String())
	}
	if code := codeFor(err); code != exitUsage {
		t.Errorf("exit = %d, want %d (a usage error): %v", code, exitUsage, err)
	}
	if !strings.Contains(err.Error(), "--force-baseline") {
		t.Errorf("error = %v, want the flag named", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "verdicts")); statErr == nil {
		if entries, _ := os.ReadDir(filepath.Join(dir, "verdicts")); len(entries) > 0 {
			t.Errorf("a refused invocation still judged: %v", entries)
		}
	}
}

// The same refusal on a judge that has no baseline to force, and still before
// the record is read.
func TestForceBaselineIsRefusedOnAJudgeThatKeepsNoBaseline(t *testing.T) {
	t.Parallel()

	dir := unjudgeableRecord(t)

	root := newRoot()
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs([]string{"judge", "--judge", "expect", "--promote", "--force-baseline", dir})

	err := root.Execute()
	if err == nil {
		t.Fatalf("--force-baseline was accepted on expect: %q", out.String())
	}
	if code := codeFor(err); code != exitUsage {
		t.Errorf("exit = %d, want %d (a usage error): %v", code, exitUsage, err)
	}
}

// unjudgeableRecord is a run that measured and declared no expectation, so any
// judge reading it refuses. It is the record that made the flag misuse surface
// as somebody else's error.
func unjudgeableRecord(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("one", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	value := "7.2.3"
	if _, err := w.Emit(record.KindFact, 1, record.Fact{Key: "app.version", Value: &value}); err != nil {
		t.Fatalf("fact: %v", err)
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

// MEASURED IS NOT MET. A failed expectation does not stop a cell, so "8 of 8
// steps measured" is true of a record whose expectations were all unmet - and
// the run said nothing about the judge the scenario itself declared.
func TestARunNamesTheJudgeItsScenarioDeclared(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		manifest *scenario.Manifest
		want     string
	}{
		{"a scenario that named one", &scenario.Manifest{Judge: &scenario.JudgeRef{Name: "expect", Version: "3"}}, "expect@3"},
		{"a scenario that named one without a version", &scenario.Manifest{Judge: &scenario.JudgeRef{Name: "matrix"}}, "matrix"},
		{"a scenario that named none", &scenario.Manifest{}, ""},
		{"no manifest at all", nil, ""},
	} {
		if got := declaredJudgeOf(c.manifest); got != c.want {
			t.Errorf("%s: declared = %q, want %q", c.name, got, c.want)
		}
	}
}

// --fact narrows which readings a view shows. A view with no readings in it
// cannot act on that, and saying nothing would read as a table that had them.
func TestNarrowingAViewThatShowsNoReadingsIsRefused(t *testing.T) {
	t.Parallel()

	for _, view := range []string{"tsv", "table", "meta"} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"render", "--view", view, "--fact", "app.version", "some-run"})

		err := root.Execute()
		if err == nil {
			t.Errorf("%s accepted --fact; wrote %q", view, out.String())
			continue
		}
		if !strings.Contains(err.Error(), "--fact") {
			t.Errorf("%s: error = %v, want the flag named", view, err)
		}
	}
}

// A listing that skipped a record it could not parse would be the shortest way
// to lose evidence: the record is still there and the operator cannot see it.
func TestARecordThatWillNotParseIsListedRatherThanSkipped(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	good := filepath.Join(runs, "clean-20260821T000000Z")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, record.EventsFile),
		[]byte(`{"schema":1,"seq":1,"kind":"run-started","step":0,"data":{"scenario":{"name":"clean"}}}`+"\n"+
			`{"schema":1,"seq":2,"kind":"run-finished","step":0,"data":{"status":"measured"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	// A directory that is not a record at all is passed over rather than
	// guessed at; one that IS a record and will not read is named.
	if err := os.MkdirAll(filepath.Join(runs, "not-a-record"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	root := newRoot()
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs([]string{"runs", "--runs-dir", runs})
	if err := root.Execute(); err != nil {
		t.Fatalf("runs: %v (%s)", err, errs.String())
	}

	if !strings.Contains(out.String(), "clean-20260821T000000Z") {
		t.Errorf("the record was not listed: %q", out.String())
	}
	if !strings.Contains(out.String(), "clean") {
		t.Errorf("the scenario was not read off the record: %q", out.String())
	}
	if strings.Contains(out.String(), "not-a-record") {
		t.Errorf("a directory holding no events was listed as a record: %q", out.String())
	}
}
