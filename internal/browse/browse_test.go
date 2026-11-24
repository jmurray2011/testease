package browse_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// A person browsing records needs what each one says about itself, read from
// its events the same way every other projection reads them. Nothing here
// concludes anything: status is what the run measured, and a verdict is shown
// only as the verdict file a judge wrote, against the bytes it judged.

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

func installCheck(t *testing.T, runs string) string {
	t.Helper()

	return recorded(t, runs, "install-check-20261123T171533Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID:    "install-check-20261123T171533Z",
			Scenario: record.ScenarioRef{Name: "install-check", Hash: "sha256:abc"},
			Tool:     record.ToolRef{Version: "0.9.2", Commit: "c0ffee"},
			Driver:   record.DriverManifest,
			Params:   map[string]string{"version": "1.2.3"},
			Host:     record.HostRef{User: "josh", Hostname: "workstation"},
		})
		emit(t, w, record.KindTargetResolved, 0, record.TargetResolved{
			Host:      "app",
			Requested: map[string]any{"target": "vm-lane"},
			Resolved:  map[string]any{"ref": "ssh:192.0.2.10"},
			Facts:     map[string]string{"os": "rhel9"},
		})
		out, errPath := record.StepPaths(1, "install package")
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: "install package", Host: "app", Probe: "probes/install.sh"})
		emit(t, w, record.KindOperation, 1, record.Operation{
			RC: 0, DurationS: 3.5, Stdout: out, Stderr: errPath, Status: record.StatusMeasured,
		})
		version := "1.2.3-1.el9"
		emit(t, w, record.KindFact, 1, record.Fact{Key: "package.version", Value: &version})
		emit(t, w, record.KindStepStarted, 2, record.StepStarted{Label: "check service", Host: "app", Probe: "probes/service.sh"})
		emit(t, w, record.KindOperation, 2, record.Operation{RC: 1, DurationS: 12.4, Status: record.StatusProbeFail})
		emit(t, w, record.KindFact, 2, record.Fact{Key: "http.status", Error: "connection refused"})
		emit(t, w, record.KindTargetDisposition, 0, record.TargetDisposition{Host: "app", Ref: "ssh:192.0.2.10", Action: "retain-failed"})
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusProbeFail, DurationS: 16})
	})
}

func judged(t *testing.T, dir string, verdict, events string) {
	t.Helper()
	judgedBy(t, dir, "expect", verdict, events)
}

func judgedBy(t *testing.T, dir, name, verdict, events string) {
	t.Helper()

	if _, err := judge.Save(dir, judge.Verdict{
		Schema:   judge.Schema,
		Judge:    name,
		Version:  "3",
		JudgedAt: time.Date(2026, 11, 23, 17, 22, 0, 0, time.UTC),
		Evidence: judge.Evidence{Events: events},
		Verdict:  verdict,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func eventsHash(t *testing.T, dir string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	return record.HashBytes(body)
}

func TestAListingReadsEachRecordForWhatItIs(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := installCheck(t, runs)

	got, err := browse.List(runs)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d records, want 1: %+v", len(got), got)
	}
	run := got[0]
	if run.RunID != "install-check-20261123T171533Z" || run.Scenario != "install-check" {
		t.Errorf("run = %+v", run)
	}
	if run.Steps != 2 || run.Status != string(record.StatusProbeFail) || run.Dir != dir {
		t.Errorf("run = %+v", run)
	}
	if len(run.Targets) != 1 || run.Targets[0].Host != "app" || run.Targets[0].Fate != "retain-failed" {
		t.Errorf("targets = %+v, want the app host's last disposition", run.Targets)
	}
}

func TestAListingSortsByRunIDAndPassesOverWhatIsNotARecord(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	for _, id := range []string{"zeta-20261101T000000Z", "alpha-20261102T000000Z"} {
		recorded(t, runs, id, func(w *record.Writer) {
			emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: id, Scenario: record.ScenarioRef{Name: "s"}})
		})
	}
	if err := os.MkdirAll(filepath.Join(runs, "batches"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := browse.List(runs)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].RunID != "alpha-20261102T000000Z" || got[1].RunID != "zeta-20261101T000000Z" {
		t.Errorf("listed %+v", got)
	}
	// No run-finished: the run did not close its record.
	if got[0].Status != string(record.StatusUnfinished) {
		t.Errorf("status = %q, want unfinished", got[0].Status)
	}
}

// A listing that skipped a record it could not parse would be the shortest way
// to lose evidence.
func TestARecordThatWillNotReadIsListedAsUnreadable(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := filepath.Join(runs, "broken-20261101T000000Z")
	if err := os.MkdirAll(filepath.Join(dir, record.EventsFile), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := browse.List(runs)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Status != "unreadable" {
		t.Errorf("listed %+v, want the record named as unreadable", got)
	}
}

func TestAMissingRunsDirectoryIsSaidSo(t *testing.T) {
	t.Parallel()

	_, err := browse.List(filepath.Join(t.TempDir(), "nowhere"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want one that says the directory does not exist", err)
	}
}

// A verdict is a conclusion about exact bytes. One whose evidence no longer
// matches the events file is not this record's verdict, and must not read as
// though it were.
func TestAVerdictIsCurrentOnlyAgainstTheBytesItJudged(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := installCheck(t, runs)
	judged(t, dir, "regressed", eventsHash(t, dir))

	got, err := browse.List(runs)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	verdicts := got[0].Verdicts
	if len(verdicts) != 1 || verdicts[0].Judge != "expect" || verdicts[0].Version != "3" ||
		verdicts[0].Verdict != "regressed" || !verdicts[0].Current {
		t.Fatalf("verdicts = %+v, want expect@3 regressed, current", verdicts)
	}

	other := t.TempDir()
	stale := installCheck(t, other)
	judged(t, stale, "pass", "sha256:somethingelse")
	got, err = browse.List(other)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if v := got[0].Verdicts; len(v) != 1 || v[0].Current {
		t.Errorf("verdicts = %+v, want the verdict shown as not current", v)
	}
}

func TestARecordOpensWithItsStepsFactsAndProvenance(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := installCheck(t, runs)

	got, err := browse.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.RunID != "install-check-20261123T171533Z" || got.Status != string(record.StatusProbeFail) {
		t.Errorf("run = %+v", got.Run)
	}
	if got.Params["version"] != "1.2.3" || got.Tool.Version != "0.9.2" || got.Runner.User != "josh" {
		t.Errorf("provenance = params %v, tool %+v, runner %+v", got.Params, got.Tool, got.Runner)
	}
	if got.EventsHash != eventsHash(t, dir) {
		t.Errorf("events hash = %q", got.EventsHash)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].Host != "app" || got.Hosts[0].Resolved["ref"] != "ssh:192.0.2.10" {
		t.Errorf("hosts = %+v", got.Hosts)
	}

	if len(got.StepList) != 2 {
		t.Fatalf("steps = %+v", got.StepList)
	}
	first, second := got.StepList[0], got.StepList[1]
	if first.Seq != 1 || first.Label != "install package" || first.Host != "app" || first.Probe != "probes/install.sh" {
		t.Errorf("first step = %+v", first)
	}
	if first.Status != string(record.StatusMeasured) || first.RC == nil || *first.RC != 0 || first.DurationS != 3.5 {
		t.Errorf("first step outcome = %+v", first)
	}
	if want, _ := record.StepPaths(1, "install package"); first.Stdout != want {
		t.Errorf("stdout = %q, want %q", first.Stdout, want)
	}
	if len(first.Facts) != 1 || first.Facts[0].Key != "package.version" || *first.Facts[0].Value != "1.2.3-1.el9" {
		t.Errorf("first step facts = %+v", first.Facts)
	}
	// A missing reading is evidence too: the fact is there with its error.
	if len(second.Facts) != 1 || second.Facts[0].Value != nil || second.Facts[0].Error != "connection refused" {
		t.Errorf("second step facts = %+v", second.Facts)
	}
	if second.Status != string(record.StatusProbeFail) || *second.RC != 1 {
		t.Errorf("second step = %+v", second)
	}
}

// A step that started and never reported has no outcome to show, and must not
// borrow one.
func TestAStepThatNeverReportedHasNoOutcome(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := recorded(t, runs, "cut-20261101T000000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "cut-20261101T000000Z", Scenario: record.ScenarioRef{Name: "cut"}})
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: "long one", Host: "box"})
	})

	got, err := browse.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got.StepList) != 1 || got.StepList[0].RC != nil || got.StepList[0].Status != "" {
		t.Errorf("steps = %+v, want one with no outcome", got.StepList)
	}
	if got.Status != string(record.StatusUnfinished) {
		t.Errorf("status = %q", got.Status)
	}
}

// Read-time repairs are never hidden, and neither are the warnings the run
// itself wrote about what it had to repair at the parse boundary.
func TestARecordCarriesWhatItsReaderAndWriterHadToSay(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := recorded(t, runs, "noted-20261101T000000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "noted-20261101T000000Z", Scenario: record.ScenarioRef{Name: "noted"}})
		if err := w.Warn(1, 0, record.Warning{About: "rc", Message: "not a number", Recorded: "x"}); err != nil {
			t.Fatal(err)
		}
	})
	f, err := os.OpenFile(filepath.Join(dir, record.EventsFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema":1,"seq":3,"kind":"run-fin`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := browse.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got.Reports) != 1 {
		t.Errorf("reports = %+v, want the torn tail", got.Reports)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Message != "not a number" {
		t.Errorf("warnings = %+v", got.Warnings)
	}
}

// Whether a verdict passed is its judge's to say, by its pass set. A shipped
// judge's set is known here; an external judge's is not, and is not guessed.
func TestAVerdictSaysWhetherItIsInItsJudgesPassSet(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := installCheck(t, runs)
	events := eventsHash(t, dir)
	judgedBy(t, dir, "expect", "regressed", events)
	judgedBy(t, dir, "matrix", "pass", events)
	judgedBy(t, dir, "site-policy", "pass", events)

	got, err := browse.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	by := map[string]browse.Verdict{}
	for _, v := range got.Verdicts {
		by[v.Judge] = v
	}
	if v := by["expect"]; !v.Shipped || v.Passed {
		t.Errorf("expect regressed = %+v, want shipped and not passed", v)
	}
	if v := by["matrix"]; !v.Shipped || !v.Passed {
		t.Errorf("matrix pass = %+v, want shipped and passed", v)
	}
	if v := by["site-policy"]; v.Shipped || v.Passed {
		t.Errorf("external judge = %+v, want its pass set left unknown", v)
	}
}
