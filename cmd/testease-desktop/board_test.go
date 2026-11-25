package main

import (
	"slices"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
)

// A campaign reads as a board: each thing it did in order, each run
// coloured by what it came to, and the runs still to come.

func TestABoardShowsWhatACampaignDidAndWhatIsLeft(t *testing.T) {
	c := browse.Campaign{Status: "running", Repeat: 4, Activities: []browse.Activity{
		{Kind: "hook", Name: "prepare", Finished: true},
		{Kind: "activity", Name: "doctor", Finished: true, ExitCode: 3},
		{Kind: "activity", Name: "run", Iteration: 1, Finished: true, RunID: "s-1"},
		{Kind: "activity", Name: "run", Iteration: 2, Finished: true, RunID: "s-2"},
		{Kind: "activity", Name: "run", Iteration: 3, At: time.Now()},
	}}
	runs := map[string]browse.Run{
		"s-1": judgedRun("s-1", "s", true),
		"s-2": judgedRun("s-2", "s", false),
	}
	items := campaignBoard(c, func(id string) (browse.Run, bool) { r, ok := runs[id]; return r, ok })
	var labels []string
	var states []boardState
	for _, it := range items {
		labels = append(labels, it.Label)
		states = append(states, it.State)
	}
	if want := []string{"hook prepare", "doctor", "run 1", "run 2", "run 3", "run 4"}; !slices.Equal(labels, want) {
		t.Fatalf("labels %q, want %q", labels, want)
	}
	if want := []boardState{boardDone, boardBad, boardPassed, boardFailed, boardRunning, boardWaiting}; !slices.Equal(states, want) {
		t.Errorf("states %v, want %v", states, want)
	}
	// A failed run's tile says what its judge said.
	if items[3].Word != "fail" {
		t.Errorf("failed run says %q", items[3].Word)
	}
	if items[2].RunID != "s-1" || items[5].RunID != "" {
		t.Errorf("run ids %q %q", items[2].RunID, items[5].RunID)
	}
	if got, want := boardSummary(c, items), "2 of 4 runs done: 1 passed, 1 failed · run 3 going"; got != want {
		t.Errorf("summary %q, want %q", got, want)
	}
}

func TestAFinishedCampaignDoesNotWaitForRunsItNeverStarted(t *testing.T) {
	c := browse.Campaign{Status: "failed", Repeat: 3, Activities: []browse.Activity{
		{Kind: "activity", Name: "run", Iteration: 1, Finished: true, ExitCode: 2, RunID: "s-1"},
	}}
	items := campaignBoard(c, func(string) (browse.Run, bool) { return browse.Run{}, false })
	if len(items) != 1 || items[0].State != boardBad {
		t.Errorf("items %+v", items)
	}
}

func TestTheCampaignScreenShowsItsBoardAndKeepsUp(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	upgradeCampaign(t, runs, false)
	v := newCampaignView(runs, upgradeID, func() {}, func(string) {})
	if n := len(v.board.items); n != 4 {
		t.Fatalf("%d items on the board", n)
	}
	if got := v.board.items[len(v.board.items)-1]; got.Label != "run 2" || got.State != boardRunning {
		t.Errorf("last item %+v", got)
	}
	// The same campaign, finished since.
	later := t.TempDir()
	upgradeCampaign(t, later, true)
	v.runsDir = later
	v.reload()
	if got := v.board.items[len(v.board.items)-1]; got.Label != "hook cleanup" || got.State != boardDone {
		t.Errorf("after it finished, last item %+v", got)
	}
}
