package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/batch"
	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/campaign"
)

// Batches and campaigns are how runs are usually started: a set of tests run
// together, or one scenario repeated with hooks around it. Each screen lists
// them, opens one, and from there opens the records it started. Nothing here
// judges: a batch's verdicts are its judgment file's, as written.

const smokeID = "smoke-set-20261123T171533.000000000Z"

func saveJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// smokeSet writes a three-test batch: one that failed its probe, one that
// measured, and one skipped before it started a run.
func smokeSet(t *testing.T, runs string, judged bool) {
	t.Helper()

	dir := filepath.Join(runs, "batches", smokeID)
	saveJSON(t, filepath.Join(dir, "batch.json"), batch.Result{
		Schema: batch.Schema, ID: smokeID, Dir: dir, RunsDir: runs, Status: "failed", ExitCode: 2,
		Tests: []batch.TestResult{
			{Name: "install-old", Scenario: "install-check", Status: "probe-fail", RunID: "install-check-20261123T171533Z-install-old", ExitCode: 2},
			{Name: "install-new", Scenario: "install-check", Status: "measured", RunID: "install-check-20261123T171533Z-install-new"},
			{Scenario: "api-check", Status: "skipped"},
		},
	})
	if judged {
		saveJSON(t, filepath.Join(dir, "judgment.json"), batch.Judgment{
			Schema: 1, BatchID: smokeID, Status: "failed", ExitCode: 20,
			Tests: []batch.TestJudgment{
				{Name: "install-old", RunID: "install-check-20261123T171533Z-install-old", Status: "judged", Judge: "expect", Version: "3", Verdict: "regressed"},
				{Name: "install-new", RunID: "install-check-20261123T171533Z-install-new", Status: "judged", Judge: "expect", Version: "3", Verdict: "pass"},
				{Name: "api-check", Status: "not-measured"},
			},
		})
	}
}

func TestTheBatchesScreenListsEachBatchWithItsJudgment(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, true)

	v := newBatchesView(runs, func(browse.Batch) {})
	want := map[int]string{
		bcolBatch:    smokeID,
		bcolStatus:   "failed",
		bcolTests:    "3 tests",
		bcolJudgment: "failed",
	}
	for col, text := range want {
		if got := v.cellText(0, col); got != text {
			t.Errorf("column %d = %q, want %q", col, got, text)
		}
	}
	if v.count.Text != "1 batch" {
		t.Errorf("count = %q", v.count.Text)
	}
}

func TestABatchNotYetJudgedSaysSoInTheListing(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, false)

	v := newBatchesView(runs, func(browse.Batch) {})
	if got := v.cellText(0, bcolJudgment); got != "not judged" {
		t.Errorf("judgment = %q", got)
	}
}

func TestNoBatchesSaysSo(t *testing.T) {
	test.NewTempApp(t)

	v := newBatchesView(t.TempDir(), func(browse.Batch) {})
	if !v.message.Visible() || !strings.Contains(v.message.Text, "No batches") {
		t.Errorf("message = %q, visible %v", v.message.Text, v.message.Visible())
	}
}

func openSmokeSet(t *testing.T, judged bool, openRun func(string)) (*batchView, string) {
	t.Helper()

	runs := t.TempDir()
	smokeSet(t, runs, judged)
	bs, err := browse.Batches(runs)
	if err != nil || len(bs) != 1 {
		t.Fatalf("Batches = %v, %v", bs, err)
	}
	return newBatchView(runs, bs[0], func() {}, openRun), runs
}

func TestABatchOpensOnItsTestsAndTheirVerdicts(t *testing.T) {
	test.NewTempApp(t)

	v, _ := openSmokeSet(t, true, func(string) {})
	rows := [][4]string{
		{"install-old", "probe-fail", "install-check-20261123T171533Z-install-old", "expect@3 regressed"},
		{"install-new", "measured", "install-check-20261123T171533Z-install-new", "expect@3 pass"},
		{"api-check", "skipped", "—", "not-measured"},
	}
	for r, want := range rows {
		for c, text := range want {
			if got := v.cellText(r, c); got != text {
				t.Errorf("row %d column %d = %q, want %q", r, c, got, text)
			}
		}
	}
}

func TestATestInABatchOpensTheRecordItStarted(t *testing.T) {
	test.NewTempApp(t)

	var opened string
	v, runs := openSmokeSet(t, true, func(dir string) { opened = dir })
	v.open(0)
	if want := filepath.Join(runs, "install-check-20261123T171533Z-install-old"); opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}
	opened = ""
	v.open(2)
	if opened != "" {
		t.Errorf("a test that started no run opened %q", opened)
	}
}

// A judgment that does not line up with its batch is refused, and the screen
// says why instead of showing the measurement with someone else's verdicts.
func TestARefusedJudgmentIsShownWithItsReason(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, true)
	saveJSON(t, filepath.Join(runs, "batches", smokeID, "judgment.json"), batch.Judgment{
		Schema: 1, BatchID: smokeID, Status: "passed",
		Tests: []batch.TestJudgment{{Name: "something-else", Status: "judged", Verdict: "pass"}},
	})
	bs, err := browse.Batches(runs)
	if err != nil {
		t.Fatal(err)
	}

	list := newBatchesView(runs, func(browse.Batch) {})
	if got := list.cellText(0, bcolJudgment); got != "judgment refused" {
		t.Errorf("listing judgment = %q", got)
	}
	v := newBatchView(runs, bs[0], func() {}, func(string) {})
	if !v.problem.Visible() || !strings.Contains(v.problem.Text, "not shown") {
		t.Errorf("problem = %q, visible %v", v.problem.Text, v.problem.Visible())
	}
	if got := v.cellText(1, tcolVerdict); got != "not judged" {
		t.Errorf("verdict = %q, want the refused judgment left out", got)
	}
}

const upgradeID = "upgrade-campaign-20261124T010000Z"

func upgradeCampaign(t *testing.T, runs string, finished bool) string {
	t.Helper()

	dir := filepath.Join(runs, "campaigns", upgradeID)
	w, err := campaign.OpenRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	emit := func(kind string, data any) {
		if err := w.Emit(kind, data); err != nil {
			t.Fatal(err)
		}
	}
	emit("campaign-started", map[string]any{"id": upgradeID, "name": "upgrade-campaign", "scenario": "upgrade-scenario", "repeat": 2})
	emit("activity-started", map[string]any{"activity": "prepare", "log": "hooks/prepare.err"})
	emit("hook-finished", map[string]any{"hook": "prepare", "exit_code": 0, "stderr": "hooks/prepare.err"})
	emit("activity-started", map[string]any{"activity": "doctor", "log": "doctor.log"})
	emit("activity-finished", map[string]any{"activity": "doctor", "exit_code": 3, "log": "doctor.log"})
	emit("activity-started", map[string]any{"activity": "run", "iteration": 1, "log": "runs/01-run.log"})
	emit("activity-finished", map[string]any{"activity": "run", "iteration": 1, "exit_code": 0, "run_id": "upgrade-scenario-20261124T010100Z", "verdict": "pass", "log": "runs/01-run.log"})
	emit("activity-started", map[string]any{"activity": "run", "iteration": 2, "log": "../../elsewhere.log"})
	if finished {
		emit("activity-finished", map[string]any{"activity": "run", "iteration": 2, "exit_code": 0, "run_id": "upgrade-scenario-20261124T011500Z"})
		emit("activity-started", map[string]any{"activity": "cleanup", "log": "hooks/cleanup.err"})
		emit("hook-finished", map[string]any{"hook": "cleanup", "exit_code": 0, "stderr": "hooks/cleanup.err"})
		emit("campaign-finished", campaign.Result{
			ID: upgradeID, Status: campaign.StatusPassed,
			Runs: []string{"upgrade-scenario-20261124T010100Z", "upgrade-scenario-20261124T011500Z"}, Cleanup: "passed",
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte("run 2 of 2 started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doctor.log"), []byte("docker context not found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTheCampaignsScreenSaysWhereEachCampaignHasGot(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, false)

	v := newCampaignsView(runs, func(browse.Campaign) {})
	want := map[int]string{
		ccolCampaign: upgradeID,
		ccolStatus:   "running",
		ccolRuns:     "1 of 2 runs",
		ccolNow:      "run",
	}
	for col, text := range want {
		if got := v.cellText(0, col); got != text {
			t.Errorf("column %d = %q, want %q", col, got, text)
		}
	}
}

func TestAFinishedCampaignSaysHowItsCleanupWent(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, true)

	v := newCampaignsView(runs, func(browse.Campaign) {})
	if got := v.cellText(0, ccolStatus); got != "passed" {
		t.Errorf("status = %q", got)
	}
	if got := v.cellText(0, ccolNow); got != "cleanup passed" {
		t.Errorf("now = %q", got)
	}
	if got := v.cellText(0, ccolRuns); got != "2 of 2 runs" {
		t.Errorf("runs = %q", got)
	}
}

func TestACampaignOpensOnWhatItDidAndItsLog(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, false)

	v := newCampaignView(runs, upgradeID, func() {}, func(string) {})
	rows := [][4]string{
		{"hook prepare", "exit 0", "—", ""},
		{"doctor", "exit 3", "—", ""},
		{"run 1", "exit 0", "upgrade-scenario-20261124T010100Z", "pass"},
		{"run 2", "running", "—", ""},
	}
	for r, want := range rows {
		for c, text := range want {
			if got := v.cellText(r, c); got != text {
				t.Errorf("row %d column %d = %q, want %q", r, c, got, text)
			}
		}
	}
	if got := v.log.Text(); got != "run 2 of 2 started\n" {
		t.Errorf("log = %q, want the campaign's output.log", got)
	}
}

func TestSelectingAnActivityShowsItsLogAndOffersItsRun(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, false)
	var opened string
	v := newCampaignView(runs, upgradeID, func() {}, func(dir string) { opened = dir })

	v.selectActivity(1)
	if got := v.log.Text(); got != "docker context not found\n" {
		t.Errorf("doctor's log = %q", got)
	}
	if !v.openRun.Disabled() {
		t.Error("an activity that started no run offers one")
	}

	v.selectActivity(2)
	if v.openRun.Disabled() {
		t.Fatal("run 1 does not offer its record")
	}
	v.openRun.OnTapped()
	if want := filepath.Join(runs, "upgrade-scenario-20261124T010100Z"); opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}

	// The campaign names run 2's log outside itself. It is not followed.
	v.selectActivity(3)
	if got := v.log.Text(); !strings.Contains(got, "not followed") {
		t.Errorf("log outside the campaign = %q", got)
	}
}

// A running campaign moves on while it is watched. Reloading keeps up with it
// without losing which activity the person picked.
func TestACampaignKeepsUpAsItRuns(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, false)
	v := newCampaignView(runs, upgradeID, func() {}, func(string) {})
	v.selectActivity(1)

	if err := os.RemoveAll(filepath.Join(runs, "campaigns", upgradeID)); err != nil {
		t.Fatal(err)
	}
	upgradeCampaign(t, runs, true)
	v.reload()

	if got := v.cellText(3, 1); got != "exit 0" {
		t.Errorf("run 2 after reload = %q", got)
	}
	if v.selected != 1 {
		t.Errorf("selected = %d, want the doctor still picked", v.selected)
	}
}

func TestCountsArePluralisedAsWords(t *testing.T) {
	t.Parallel()

	for n, want := range map[int]string{0: "0 batches", 1: "1 batch", 2: "2 batches"} {
		if got := plural(n, "batch"); got != want {
			t.Errorf("plural(%d, batch) = %q, want %q", n, got, want)
		}
	}
	if got := plural(2, "campaign"); got != "2 campaigns" {
		t.Errorf("plural(2, campaign) = %q", got)
	}
}

// Every id carries UTC by contract, so a time shown beside one is UTC too;
// otherwise one campaign reads as starting on two different dates.
func TestACampaignsStartIsShownInTheTimeItsIDCarries(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	upgradeCampaign(t, runs, false)
	v := newCampaignsView(runs, func(browse.Campaign) {})
	want := v.all[0].Started.UTC().Format("2006-01-02 15:04") + " UTC"
	if got := v.cellText(0, ccolStarted); got != want {
		t.Errorf("started = %q, want %q", got, want)
	}
}

// A campaign with no cleanup hook says so in words that fit its column.
func TestACampaignWithNoCleanupHookSaysSoPlainly(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := filepath.Join(runs, "campaigns", "docs-20261106T010314Z")
	w, err := campaign.OpenRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("campaign-started", map[string]any{"id": "docs-20261106T010314Z", "name": "docs", "repeat": 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("campaign-finished", campaign.Result{ID: "docs-20261106T010314Z", Status: campaign.StatusInterrupted, Cleanup: "not-configured"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	v := newCampaignsView(runs, func(browse.Campaign) {})
	if got := v.cellText(0, ccolNow); got != "no cleanup hook" {
		t.Errorf("now = %q", got)
	}
}
