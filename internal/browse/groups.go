package browse

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmurray2011/testease/internal/batch"
	"github.com/jmurray2011/testease/internal/campaign"
)

// Batch is one batch under RUNS_DIR/batches, as its runner last saved it, with
// the judgment beside it when there is one that lines up.
type Batch struct {
	ID       string      `json:"batch_id"`
	Status   string      `json:"status"`
	ExitCode int         `json:"exit_code"`
	Dir      string      `json:"dir"`
	Tests    []BatchTest `json:"tests"`

	Judged         bool   `json:"judged"`
	JudgmentStatus string `json:"judgment_status,omitempty"`

	// Problem says why the batch, or its judgment, could not be read. A
	// judgment that does not line up with the measurement is refused rather
	// than merged, the same as the CLI refuses it.
	Problem string `json:"problem,omitempty"`
}

// BatchTest is one entry of a batch: the child run it started and, once the
// batch is judged, that run's verdict.
type BatchTest struct {
	Name     string `json:"name"`
	Scenario string `json:"scenario"`
	Status   string `json:"status"`
	RunID    string `json:"run_id,omitempty"`
	ExitCode int    `json:"exit_code"`

	Judge         string `json:"judge,omitempty"`
	Version       string `json:"version,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	JudgeStatus   string `json:"judge_status,omitempty"`
	JudgeError    string `json:"judge_error,omitempty"`
	VerdictPassed bool   `json:"verdict_passed,omitempty"`
}

// Batches reads every batch under runsDir, newest first by id. No batches
// directory is no batches, not an error.
func Batches(runsDir string) ([]Batch, error) {
	entries, err := os.ReadDir(filepath.Join(runsDir, "batches"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read batches: %w", err)
	}
	var found []Batch
	for _, e := range entries {
		if e.IsDir() {
			found = append(found, readBatch(runsDir, e.Name()))
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID > found[j].ID })
	return found, nil
}

func readBatch(runsDir, id string) Batch {
	b := Batch{ID: id, Dir: filepath.Join(runsDir, "batches", id), Status: Unreadable}
	result, err := batch.LoadResult(runsDir, id)
	if err != nil {
		b.Problem = err.Error()
		return b
	}
	b.Status, b.ExitCode = result.Status, result.ExitCode
	for _, t := range result.Tests {
		name := t.Name
		if name == "" {
			name = t.Scenario
		}
		b.Tests = append(b.Tests, BatchTest{Name: name, Scenario: t.Scenario, Status: t.Status, RunID: t.RunID, ExitCode: t.ExitCode})
	}

	judgment, err := batch.LoadJudgment(runsDir, id)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return b
	case err != nil:
		b.Problem = "judgment not read: " + err.Error()
		return b
	case len(judgment.Tests) != len(b.Tests):
		b.Problem = fmt.Sprintf("judgment has %d entries and the batch has %d; it is not shown", len(judgment.Tests), len(b.Tests))
		return b
	}
	for i, j := range judgment.Tests {
		if j.Name != b.Tests[i].Name || j.RunID != b.Tests[i].RunID {
			b.Problem = fmt.Sprintf("judgment entry %d does not match the batch's; it is not shown", i+1)
			return b
		}
	}
	b.Judged, b.JudgmentStatus = true, judgment.Status
	for i, j := range judgment.Tests {
		t := &b.Tests[i]
		t.Judge, t.Version, t.Verdict, t.JudgeStatus, t.JudgeError = j.Judge, j.Version, j.Verdict, j.Status, j.Error
		t.VerdictPassed = j.Verdict != "" && passedByShipped(j.Judge, j.Verdict)
	}
	return b
}

// Campaign is one campaign under RUNS_DIR/campaigns, read from its events.
type Campaign struct {
	ID       string    `json:"campaign_id"`
	Name     string    `json:"name"`
	Scenario string    `json:"scenario"`
	Repeat   int       `json:"repeat,omitempty"`
	Status   string    `json:"status"`
	ExitCode int       `json:"exit_code"`
	Runs     []string  `json:"runs"`
	Current  string    `json:"current,omitempty"`
	Cleanup  string    `json:"cleanup,omitempty"`
	Started  time.Time `json:"started"`
	Dir      string    `json:"dir"`
	// Log is the campaign's own running commentary, output.log.
	Log string `json:"log"`

	Activities []Activity `json:"activities,omitempty"`
	Problem    string     `json:"problem,omitempty"`
}

// Activity is one thing a campaign did: a hook, or an activity such as doctor,
// a run, a judgement or a reproduce. Finished is false while it is going.
type Activity struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Iteration int       `json:"iteration,omitempty"`
	At        time.Time `json:"at"`
	Finished  bool      `json:"finished"`
	ExitCode  int       `json:"exit_code"`
	RunID     string    `json:"run_id,omitempty"`
	Verdict   string    `json:"verdict,omitempty"`
	Log       string    `json:"log,omitempty"`
}

// Campaigns reads every campaign under runsDir, newest first by id.
func Campaigns(runsDir string) ([]Campaign, error) {
	entries, err := os.ReadDir(filepath.Join(runsDir, "campaigns"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read campaigns: %w", err)
	}
	var found []Campaign
	for _, e := range entries {
		if e.IsDir() {
			found = append(found, readCampaign(filepath.Join(runsDir, "campaigns", e.Name())))
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID > found[j].ID })
	return found, nil
}

// campaignData is the part of a campaign event's payload a browser reads. The
// field names are the campaign record's contract.
type campaignData struct {
	Name      string `json:"name"`
	Scenario  string `json:"scenario"`
	Repeat    int    `json:"repeat"`
	Activity  string `json:"activity"`
	Hook      string `json:"hook"`
	Iteration int    `json:"iteration"`
	ExitCode  int    `json:"exit_code"`
	RunID     string `json:"run_id"`
	Verdict   string `json:"verdict"`
	Log       string `json:"log"`
}

func readCampaign(dir string) Campaign {
	c := Campaign{ID: filepath.Base(dir), Dir: dir, Log: filepath.Join(dir, "output.log"), Status: Unreadable}
	status, err := campaign.ReadStatus(dir)
	if err != nil {
		c.Problem = err.Error()
		return c
	}
	if status.ID != "" {
		c.ID = status.ID
	}
	c.Status, c.ExitCode, c.Runs, c.Current, c.Cleanup = status.Status, status.ExitCode, status.Runs, status.Current, status.Cleanup

	events, err := campaign.ReadEvents(dir)
	if err != nil {
		c.Problem = err.Error()
		return c
	}
	open := map[string]int{}
	for _, e := range events {
		var d campaignData
		_ = json.Unmarshal(e.Data, &d)
		switch e.Kind {
		case "campaign-started":
			c.Name, c.Scenario, c.Repeat, c.Started = d.Name, d.Scenario, d.Repeat, e.TS
		case "hook-finished":
			c.Activities = append(c.Activities, Activity{Kind: "hook", Name: d.Hook, At: e.TS, Finished: true, ExitCode: d.ExitCode})
		case "activity-started":
			key := fmt.Sprintf("%s/%d", d.Activity, d.Iteration)
			open[key] = len(c.Activities)
			c.Activities = append(c.Activities, Activity{Kind: "activity", Name: d.Activity, Iteration: d.Iteration, At: e.TS, Log: d.Log})
		case "activity-finished":
			key := fmt.Sprintf("%s/%d", d.Activity, d.Iteration)
			i, ok := open[key]
			if !ok {
				i = len(c.Activities)
				c.Activities = append(c.Activities, Activity{Kind: "activity", Name: d.Activity, Iteration: d.Iteration, At: e.TS})
			}
			delete(open, key)
			a := &c.Activities[i]
			a.Finished, a.ExitCode, a.RunID, a.Verdict = true, d.ExitCode, d.RunID, d.Verdict
			if d.Log != "" {
				a.Log = d.Log
			}
		}
	}
	return c
}
