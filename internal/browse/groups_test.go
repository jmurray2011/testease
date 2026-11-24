package browse_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/batch"
	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/campaign"
)

// Batches and campaigns are read from the files their runners keep beside the
// run records: batch.json and judgment.json for a batch, the events file for a
// campaign. Neither is a record of measurement; the runs they started are.

func writeJSON(t *testing.T, path string, v any) {
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

func smokeSet(t *testing.T, runs string, judged bool) string {
	t.Helper()

	id := "smoke-set-20261123T171533.000000000Z"
	dir := filepath.Join(runs, "batches", id)
	writeJSON(t, filepath.Join(dir, "batch.json"), batch.Result{
		Schema: batch.Schema, ID: id, Dir: dir, RunsDir: runs, Status: "failed", ExitCode: 2,
		Tests: []batch.TestResult{
			{Name: "install-old", Scenario: "install-check", Status: "probe-fail", RunID: "install-check-20261123T171533Z-install-old", ExitCode: 2},
			{Name: "install-new", Scenario: "install-check", Status: "measured", RunID: "install-check-20261123T171533Z-install-new"},
			{Scenario: "api-check", Status: "skipped"},
		},
	})
	if judged {
		writeJSON(t, filepath.Join(dir, "judgment.json"), batch.Judgment{
			Schema: 1, BatchID: id, Status: "failed", ExitCode: 20,
			Tests: []batch.TestJudgment{
				{Name: "install-old", Scenario: "install-check", RunID: "install-check-20261123T171533Z-install-old", Status: "judged", Judge: "expect", Version: "3", Verdict: "regressed"},
				{Name: "install-new", Scenario: "install-check", RunID: "install-check-20261123T171533Z-install-new", Status: "judged", Judge: "expect", Version: "3", Verdict: "pass"},
				{Name: "api-check", Scenario: "api-check", Status: "not-measured"},
			},
		})
	}
	return dir
}

func TestBatchesAreListedWithTheirTestsAndJudgment(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := smokeSet(t, runs, true)

	got, err := browse.Batches(runs)
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("batches = %+v", got)
	}
	b := got[0]
	if b.ID != "smoke-set-20261123T171533.000000000Z" || b.Status != "failed" || b.Dir != dir || !b.Judged || b.JudgmentStatus != "failed" {
		t.Errorf("batch = %+v", b)
	}
	if len(b.Tests) != 3 {
		t.Fatalf("tests = %+v", b.Tests)
	}
	old := b.Tests[0]
	if old.Name != "install-old" || old.Status != "probe-fail" || old.RunID != "install-check-20261123T171533Z-install-old" ||
		old.Judge != "expect" || old.Version != "3" || old.Verdict != "regressed" {
		t.Errorf("first test = %+v", old)
	}
	if b.Tests[2].Name != "api-check" {
		t.Errorf("an unnamed test is named by its scenario: %+v", b.Tests[2])
	}
}

func TestABatchNotYetJudgedSaysSo(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	smokeSet(t, runs, false)

	got, err := browse.Batches(runs)
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if got[0].Judged || got[0].Tests[0].Verdict != "" {
		t.Errorf("batch = %+v, want it unjudged", got[0])
	}
}

// A judgment that does not line up with the measurement is not merged into
// it. The CLI refuses it; a browser says why and shows the measurement alone.
func TestAJudgmentThatDoesNotMatchItsBatchIsNotMerged(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := smokeSet(t, runs, true)
	writeJSON(t, filepath.Join(dir, "judgment.json"), batch.Judgment{
		Schema: 1, BatchID: "smoke-set-20261123T171533.000000000Z", Status: "passed",
		Tests: []batch.TestJudgment{{Name: "something-else", Status: "judged", Verdict: "pass"}},
	})

	got, err := browse.Batches(runs)
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if got[0].Judged || got[0].Problem == "" || got[0].Tests[1].Verdict != "" {
		t.Errorf("batch = %+v, want the judgment refused with a reason", got[0])
	}
}

func TestABatchThatWillNotReadIsListed(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := filepath.Join(runs, "batches", "torn-20261101T000000.000000000Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "batch.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := browse.Batches(runs)
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if len(got) != 1 || got[0].Status != browse.Unreadable || got[0].Problem == "" {
		t.Errorf("batches = %+v", got)
	}
}

func TestNoBatchesIsAnEmptyListNotAnError(t *testing.T) {
	t.Parallel()

	got, err := browse.Batches(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Errorf("Batches = %+v, %v", got, err)
	}
}

func upgradeCampaign(t *testing.T, runs string, finished bool) string {
	t.Helper()

	dir := filepath.Join(runs, "campaigns", "upgrade-campaign-20261124T010000Z")
	w, err := campaign.OpenRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	emit := func(kind string, data any) {
		if err := w.Emit(kind, data); err != nil {
			t.Fatal(err)
		}
	}
	emit("campaign-started", map[string]any{"id": "upgrade-campaign-20261124T010000Z", "name": "upgrade-campaign", "scenario": "upgrade-scenario", "repeat": 2})
	emit("hook-finished", map[string]any{"hook": "prepare", "exit_code": 0, "duration_ms": 1200})
	emit("activity-started", map[string]any{"activity": "doctor", "log": "doctor.log"})
	emit("activity-finished", map[string]any{"activity": "doctor", "exit_code": 0, "log": "doctor.log"})
	emit("activity-started", map[string]any{"activity": "run", "iteration": 1, "log": "runs/01-run.log"})
	emit("activity-finished", map[string]any{"activity": "run", "iteration": 1, "exit_code": 0, "run_id": "upgrade-scenario-20261124T010100Z", "log": "runs/01-run.log"})
	emit("activity-started", map[string]any{"activity": "run", "iteration": 2, "log": "runs/02-run.log"})
	if finished {
		emit("activity-finished", map[string]any{"activity": "run", "iteration": 2, "exit_code": 0, "run_id": "upgrade-scenario-20261124T011500Z", "log": "runs/02-run.log"})
		emit("hook-finished", map[string]any{"hook": "cleanup", "exit_code": 0})
		emit("campaign-finished", campaign.Result{
			ID: "upgrade-campaign-20261124T010000Z", Status: campaign.StatusPassed,
			Runs: []string{"upgrade-scenario-20261124T010100Z", "upgrade-scenario-20261124T011500Z"}, Cleanup: "passed",
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte("run 2 of 2 started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestACampaignIsReadFromItsEvents(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	dir := upgradeCampaign(t, runs, false)

	got, err := browse.Campaigns(runs)
	if err != nil {
		t.Fatalf("Campaigns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("campaigns = %+v", got)
	}
	c := got[0]
	if c.ID != "upgrade-campaign-20261124T010000Z" || c.Name != "upgrade-campaign" || c.Scenario != "upgrade-scenario" || c.Dir != dir {
		t.Errorf("campaign = %+v", c)
	}
	if c.Status != campaign.StatusRunning || c.Current != "run" || c.Repeat != 2 {
		t.Errorf("campaign = %+v, want run 2 in progress", c)
	}
	if len(c.Runs) != 1 || c.Runs[0] != "upgrade-scenario-20261124T010100Z" {
		t.Errorf("runs = %v", c.Runs)
	}
	if c.Log != filepath.Join(dir, "output.log") {
		t.Errorf("log = %q", c.Log)
	}

	var kinds []string
	for _, a := range c.Activities {
		kinds = append(kinds, a.Kind+":"+a.Name)
	}
	want := []string{"hook:prepare", "activity:doctor", "activity:run", "activity:run"}
	if len(kinds) != len(want) {
		t.Fatalf("activities = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("activities = %v, want %v", kinds, want)
		}
	}
	if last := c.Activities[3]; last.Finished || last.Iteration != 2 {
		t.Errorf("last activity = %+v, want run 2 still going", last)
	}
	if first := c.Activities[2]; !first.Finished || first.RunID != "upgrade-scenario-20261124T010100Z" {
		t.Errorf("run 1 = %+v", first)
	}
}

func TestAFinishedCampaignSaysHowItFinished(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	upgradeCampaign(t, runs, true)

	got, err := browse.Campaigns(runs)
	if err != nil {
		t.Fatalf("Campaigns: %v", err)
	}
	c := got[0]
	if c.Status != campaign.StatusPassed || c.Cleanup != "passed" || len(c.Runs) != 2 || c.Current != "" {
		t.Errorf("campaign = %+v", c)
	}
}

func TestACampaignThatWillNotReadIsListed(t *testing.T) {
	t.Parallel()

	runs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runs, "campaigns", "empty-20261101T000000Z"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := browse.Campaigns(runs)
	if err != nil {
		t.Fatalf("Campaigns: %v", err)
	}
	if len(got) != 1 || got[0].Status != browse.Unreadable || got[0].Problem == "" {
		t.Errorf("campaigns = %+v", got)
	}
}
