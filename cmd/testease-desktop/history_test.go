package main

import (
	"testing"

	"github.com/jmurray2011/testease/internal/browse"
)

// A scenario's history is what its records say, newest last. A verdict is the
// only thing that says pass or fail: a run nobody judged is not counted as
// either, and a run whose measurement broke is shown as broken, not failed.

func judgedRun(id, scenario string, passed bool) browse.Run {
	verdict := "pass"
	if !passed {
		verdict = "fail"
	}
	return browse.Run{RunID: id, Scenario: scenario, Status: "measured", Verdicts: []browse.Verdict{
		{Judge: "steps", Version: "1", Verdict: verdict, Current: true, Shipped: true, Passed: passed},
	}}
}

func TestEachRunHasOneOutcome(t *testing.T) {
	cases := map[string]struct {
		run  browse.Run
		want outcome
	}{
		"passed":            {judgedRun("a", "s", true), outcomePassed},
		"failed":            {judgedRun("a", "s", false), outcomeFailed},
		"not judged":        {browse.Run{Status: "measured"}, outcomeUnjudged},
		"external judge":    {browse.Run{Status: "measured", Verdicts: []browse.Verdict{{Verdict: "ok", Current: true}}}, outcomeUnjudged},
		"over old events":   {browse.Run{Status: "measured", Verdicts: []browse.Verdict{{Verdict: "pass", Shipped: true, Passed: true}}}, outcomeUnjudged},
		"probe broke":       {browse.Run{Status: "probe-fail"}, outcomeBroken},
		"infra broke":       {browse.Run{Status: "infra-fail"}, outcomeBroken},
		"unreadable":        {browse.Run{Status: browse.Unreadable}, outcomeBroken},
		"interrupted":       {browse.Run{Status: "interrupted"}, outcomeIncomplete},
		"unfinished":        {browse.Run{Status: "unfinished"}, outcomeIncomplete},
		"any failing judge": {withVerdict(judgedRun("a", "s", true), false), outcomeFailed},
	}
	for name, c := range cases {
		if got := outcomeOf(c.run); got != c.want {
			t.Errorf("%s: outcome %v, want %v", name, got, c.want)
		}
	}
}

func withVerdict(r browse.Run, passed bool) browse.Run {
	r.Verdicts = append(r.Verdicts, judgedRun(r.RunID, r.Scenario, passed).Verdicts[0])
	return r
}

func TestHistoryGroupsRunsByScenarioInTheOrderTheyWereTaken(t *testing.T) {
	runs := []browse.Run{
		judgedRun("web-20261101T100000Z", "web", true),
		judgedRun("db-20261102T100000Z", "db", false),
		judgedRun("web-20261103T100000Z", "web", false),
		{RunID: "web-20261104T100000Z", Scenario: "web", Status: "measured"},
		judgedRun("web-20261105T100000Z", "web", true),
	}
	hs := histories(runs)
	if len(hs) != 2 {
		t.Fatalf("%d scenarios, want 2", len(hs))
	}
	// Newest activity first: web ran last.
	web := hs[0]
	if web.Scenario != "web" || len(web.Points) != 4 {
		t.Fatalf("first is %q with %d points", web.Scenario, len(web.Points))
	}
	if web.Points[0].RunID != "web-20261101T100000Z" || web.Points[3].RunID != "web-20261105T100000Z" {
		t.Errorf("points are not oldest first: %v", web.Points)
	}
	if web.Judged != 3 || web.Passed != 2 {
		t.Errorf("judged %d passed %d, want 3 and 2", web.Judged, web.Passed)
	}
	if web.Last != "20261105T100000Z" {
		t.Errorf("last = %q", web.Last)
	}
}

// Flakiness is how often a judged outcome changed from one judged run to the
// next: pass, fail, pass is two changes in two chances.
func TestFlakinessCountsChangesBetweenJudgedRuns(t *testing.T) {
	steady := histories([]browse.Run{judgedRun("s-1", "s", true), judgedRun("s-2", "s", true), judgedRun("s-3", "s", true)})[0]
	if steady.Flakiness() != 0 {
		t.Errorf("steady flakiness = %v", steady.Flakiness())
	}
	flapping := histories([]browse.Run{
		judgedRun("s-1", "s", true), {RunID: "s-2", Scenario: "s", Status: "measured"},
		judgedRun("s-3", "s", false), judgedRun("s-4", "s", true),
	})[0]
	if flapping.Flakiness() != 1 {
		t.Errorf("flapping flakiness = %v, want 1", flapping.Flakiness())
	}
	single := histories([]browse.Run{judgedRun("s-1", "s", false)})[0]
	if single.Flakiness() != 0 {
		t.Errorf("one run's flakiness = %v", single.Flakiness())
	}
}

func TestPassRateIsOfJudgedRunsOnly(t *testing.T) {
	h := histories([]browse.Run{
		judgedRun("s-1", "s", true), {RunID: "s-2", Scenario: "s", Status: "probe-fail"},
		judgedRun("s-3", "s", false), {RunID: "s-4", Scenario: "s", Status: "measured"},
	})[0]
	rate, ok := h.PassRate()
	if !ok || rate != 0.5 {
		t.Errorf("pass rate %v %v, want 0.5", rate, ok)
	}
	if _, ok := histories([]browse.Run{{RunID: "s-1", Scenario: "s", Status: "measured"}})[0].PassRate(); ok {
		t.Error("a scenario nobody judged has a pass rate")
	}
}

// A long history keeps its newest runs: the strip is for what is happening
// now, and the counts are over every run.
func TestAStripKeepsTheNewestRuns(t *testing.T) {
	var runs []browse.Run
	for i := 0; i < historyLength+5; i++ {
		runs = append(runs, judgedRun("s-"+string(rune('a'+i)), "s", true))
	}
	h := histories(runs)[0]
	if len(h.Points) != historyLength {
		t.Errorf("%d points, want %d", len(h.Points), historyLength)
	}
	if h.Judged != historyLength+5 {
		t.Errorf("judged %d, want every run", h.Judged)
	}
	if h.Points[len(h.Points)-1].RunID != runs[len(runs)-1].RunID {
		t.Error("the strip lost the newest run")
	}
}
