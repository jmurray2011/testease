package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"fyne.io/fyne/v2/test"
)

// Judging reads a saved record with the judge the record names; it measures
// nothing and moves no baseline.
func TestJudgingAsksForTheRecordsOwnJudge(t *testing.T) {
	got := judgeArgs("/srv/runs", "/srv/runs/hello-1")
	want := []string{"--runs-dir", "/srv/runs", "judge", "/srv/runs/hello-1"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestABatchRunsFromItsFileAndIsJudgedByItsID(t *testing.T) {
	got := batchRunArgs("/srv/runs", "/srv/scenarios", "/srv/smoke.yaml")
	want := []string{"--runs-dir", "/srv/runs", "--scenario-root", "/srv/scenarios", "batch", "run", "/srv/smoke.yaml"}
	if !slices.Equal(got, want) {
		t.Errorf("run args = %q, want %q", got, want)
	}
	if got := batchRunArgs("/srv/runs", "", "smoke.yaml"); slices.Contains(got, "--scenario-root") {
		t.Errorf("run args = %q pass an empty scenario root", got)
	}
	got = batchJudgeArgs("/srv/runs", "smoke-20261124T000000Z")
	want = []string{"--runs-dir", "/srv/runs", "batch", "judge", "smoke-20261124T000000Z"}
	if !slices.Equal(got, want) {
		t.Errorf("judge args = %q, want %q", got, want)
	}
}

// A campaign is the folder the person picks: its parent is the campaign root
// and its name the campaign. Overrides go one flag a line, as for a run.
func TestACampaignRunsFromTheFolderPicked(t *testing.T) {
	dir := filepath.Join("srv", "campaigns", "upgrade")
	got, err := campaignRunArgs("/srv/runs", "/srv/scenarios", dir, "--repeat=3\n\n--keep")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--runs-dir", "/srv/runs", "--scenario-root", "/srv/scenarios",
		"campaign", "--campaign-root", filepath.Join("srv", "campaigns"), "run", "upgrade", "--repeat=3", "--keep",
	}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
	if _, err := campaignRunArgs("/srv/runs", "", dir, "repeat 3"); err == nil {
		t.Error("a line that is not a flag was taken")
	}
}

// Only a scenario run reports steps; a batch or campaign run is its output.
func TestOnlyAScenarioRunIsShownAsSteps(t *testing.T) {
	test.NewTempApp(t)

	for _, c := range []struct {
		args []string
		run  bool
	}{
		{[]string{"--runs-dir", "r", "run", "hello"}, true},
		{batchRunArgs("r", "s", "run.yaml"), false},
		{[]string{"--runs-dir", "r", "campaign", "run", "upgrade"}, false},
		{judgeArgs("r", "run"), false},
	} {
		v := newJobView("x", c.args, "r", func() {}, func() {}, func(string) {})
		if v.isRun != c.run {
			t.Errorf("%q: shown as a run = %v, want %v", c.args, v.isRun, c.run)
		}
	}
}

// A judge job is about a record already on disk: Open record opens it, with
// the verdict just written.
func TestAJudgeJobOpensTheRecordItJudged(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	rec := filepath.Join(runs, "hello-1")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var opened string
	v := newJobView("judge hello-1", judgeArgs(runs, rec), runs, func() {}, func() {}, func(d string) { opened = d })
	v.record = rec
	v.s = jobState{Done: true}
	v.render()
	if v.open.Disabled() {
		t.Fatal("Open record disabled once judged")
	}
	v.openRecord()
	if opened != rec {
		t.Errorf("opened %q, want %q", opened, rec)
	}
}

func shellWithFake(t *testing.T, runs string) *shell {
	t.Helper()
	t.Setenv(fakeEnv, "finish")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	s := newShell(w, runs, "--runs-dir", func(string) {})
	s.useTools(tools{testease: self, testeaseFrom: "--testease", scenarioRoot: "/srv/scenarios"})
	s.uiCall = func(func()) {}
	w.SetContent(s.content)
	return s
}

func waitJob(t *testing.T, s *shell) *jobView {
	t.Helper()
	jv, ok := s.current.(*jobView)
	if !ok {
		t.Fatalf("showing %T, want a job", s.current)
	}
	if jv.j != nil {
		jv.j.wait()
	}
	return jv
}

// Judge on an open record judges it, and the job opens that record again.
func TestJudgeOnARecordJudgesIt(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	rec := installCheck(t, runs, "install-check-20261123T171533Z-install-old")
	s := shellWithFake(t, runs)
	s.openRecord(rec, "Runs", s.showRuns)
	d := s.current.(*detailView)
	if d.judge.Hidden {
		t.Fatal("no Judge button on a record")
	}
	test.Tap(d.judge.Button)

	jv := waitJob(t, s)
	if !slices.Equal(jv.args, judgeArgs(runs, rec)) || jv.record != rec {
		t.Errorf("args %q record %q", jv.args, jv.record)
	}
}

func TestABatchIsJudgedFromItsScreen(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, false)
	s := shellWithFake(t, runs)
	s.showBatches()
	s.batches.onOpen(s.batches.all[0])
	b := s.current.(*batchView)
	test.Tap(b.judge.Button)

	jv := waitJob(t, s)
	if !slices.Equal(jv.args, batchJudgeArgs(runs, b.b.ID)) {
		t.Errorf("args %q", jv.args)
	}
}

func TestABatchFileStartsABatchRun(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	s := shellWithFake(t, runs)
	s.showBatches()
	if s.batches.run.Hidden {
		t.Fatal("no Run batch button")
	}
	s.runBatch("/srv/smoke.yaml")

	jv := waitJob(t, s)
	if !slices.Equal(jv.args, batchRunArgs(runs, "/srv/scenarios", "/srv/smoke.yaml")) {
		t.Errorf("args %q", jv.args)
	}
}

func TestACampaignFolderStartsACampaignRun(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	s := shellWithFake(t, runs)
	s.showCampaigns()
	if s.campaigns.run.Hidden {
		t.Fatal("no Run campaign button")
	}
	dir := filepath.Join("srv", "campaigns", "upgrade")
	if err := s.runCampaign(dir, "not a flag"); err == nil {
		t.Error("took a line that is not a flag")
	}
	if len(s.jobs) != 0 {
		t.Fatalf("started %d jobs on a refused form", len(s.jobs))
	}
	if err := s.runCampaign(dir, "--repeat=2"); err != nil {
		t.Fatal(err)
	}
	jv := waitJob(t, s)
	want, _ := campaignRunArgs(runs, "/srv/scenarios", dir, "--repeat=2")
	if !slices.Equal(jv.args, want) {
		t.Errorf("args %q", jv.args)
	}
}

// A path picked in a dialog is written the platform's way in the command.
func TestAPickedPathIsWrittenThePlatformsWay(t *testing.T) {
	if got := pickedPath("C:/Users/josh/two-hellos.yaml"); got != filepath.FromSlash("C:/Users/josh/two-hellos.yaml") {
		t.Errorf("path = %q", got)
	}
}

// Batch files and campaign folders live in different places; each picker
// opens where it was last used, not where the other one was.
func TestEachPickerOpensWhereItWasLastUsed(t *testing.T) {
	test.NewTempApp(t)

	batches, campaigns := t.TempDir(), t.TempDir()
	rememberFolder(prefBatchFolder, filepath.Join(batches, "smoke.yaml"))
	rememberFolder(prefCampaignFolder, filepath.Join(campaigns, "upgrade"))

	if got := lastFolder(prefBatchFolder); got == nil || filepath.Base(got.Path()) != filepath.Base(batches) {
		t.Errorf("batch picker opens at %v, want %s", got, batches)
	}
	if got := lastFolder(prefCampaignFolder); got == nil || filepath.Base(got.Path()) != filepath.Base(campaigns) {
		t.Errorf("campaign picker opens at %v, want %s", got, campaigns)
	}
	if lastFolder("nothing-yet") != nil {
		t.Error("a picker never used opens somewhere chosen for it")
	}
}

// Overrides that are not flags are refused on the form, before it closes.
func TestTheCampaignFormRefusesALineThatIsNotAFlag(t *testing.T) {
	if overridesValid("--repeat=2\n\n--keep") != nil {
		t.Error("refused good flags")
	}
	if overridesValid("repeat 2") == nil {
		t.Error("took a line that is not a flag")
	}
}

// A batch or campaign that did not finish cleanly can keep its runs' targets
// too, so its job offers the way to them.
func TestAStoppedBatchOffersItsKeptTargets(t *testing.T) {
	test.NewTempApp(t)

	v := newJobView("batch sleepy", batchRunArgs("r", "s", "sleepy.yaml"), "r", func() {}, func() {}, func(string) {})
	v.s = jobState{Done: true, Exit: 12}
	v.render()
	if v.targets.Hidden {
		t.Error("no Kept targets on a stopped batch")
	}

	ok := newJobView("batch hello", batchRunArgs("r", "s", "hello.yaml"), "r", func() {}, func() {}, func(string) {})
	ok.s = jobState{Done: true}
	ok.render()
	if !ok.targets.Hidden {
		t.Error("Kept targets on a batch that finished cleanly")
	}

	judged := newJobView("judge batch", batchJudgeArgs("r", "b"), "r", func() {}, func() {}, func(string) {})
	judged.s = jobState{Done: true, Exit: 1}
	judged.render()
	if !judged.targets.Hidden {
		t.Error("Kept targets on a judge, which starts no targets")
	}
}
