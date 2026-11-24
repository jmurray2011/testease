package main

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
)

// Runs narrows by scenario, outcome and age as well as by text, and keeps
// those choices for next time: they say what to look at, not what to measure.

func TestAFilterMatchesOnEveryPartSet(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	pass := judgedRun("web-20261124T090000Z", "web", true)
	fail := judgedRun("web-20261120T090000Z", "web", false)
	old := judgedRun("db-20261001T090000Z", "db", true)
	same := func(r browse.Run) string { return r.Status }

	cases := []struct {
		name string
		f    runFilter
		r    browse.Run
		want bool
	}{
		{"nothing set", runFilter{}, old, true},
		{"scenario", runFilter{Scenario: "web"}, pass, true},
		{"other scenario", runFilter{Scenario: "web"}, old, false},
		{"scenario is exact", runFilter{Scenario: "we"}, pass, false},
		{"outcome", runFilter{Outcome: outcomeFailed.String()}, fail, true},
		{"other outcome", runFilter{Outcome: outcomeFailed.String()}, pass, false},
		{"today", runFilter{Since: sinceToday}, pass, true},
		{"not today", runFilter{Since: sinceToday}, fail, false},
		{"week", runFilter{Since: sinceWeek}, fail, true},
		{"month", runFilter{Since: sinceMonth}, old, false},
		{"text", runFilter{Text: "1120"}, fail, true},
		{"all of them", runFilter{Scenario: "web", Outcome: outcomePassed.String(), Since: sinceToday, Text: "web"}, pass, true},
	}
	for _, c := range cases {
		if got := c.f.matches(c.r, same, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunsKeepsTheChosenFiltersForNextTime(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261123T171533Z")

	v := newRunsView(runs, func(string) {})
	v.scenario.SetSelected("install-check")
	v.outcome.SetSelected(outcomeBroken.String())
	if got := shownRunIDs(v); len(got) != 1 {
		t.Errorf("shown %v", got)
	}

	again := newRunsView(runs, func(string) {})
	if again.scenario.Selected != "install-check" || again.outcome.Selected != outcomeBroken.String() {
		t.Errorf("a new screen starts with %q, %q", again.scenario.Selected, again.outcome.Selected)
	}
	again.clear()
	if again.scenario.Selected != anyScenario || again.outcome.Selected != anyOutcome || again.since.Selected != anyTime {
		t.Errorf("clear left %q %q %q", again.scenario.Selected, again.outcome.Selected, again.since.Selected)
	}
	if fyne.CurrentApp().Preferences().String(prefRunsScenario) != "" {
		t.Error("clearing did not forget the scenario")
	}
}

// Home's scenario opens Runs on that scenario alone, not on every scenario
// whose name contains it.
func TestAScenarioFromHomeListsOnlyItsRuns(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261123T171533Z")
	installCheck(t, runs, "install-check-extra-20261123T171533Z")

	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	s := newShell(w, runs, "--runs-dir", func(string) {})
	s.showScenarioRuns("install-check")
	if s.runs.scenario.Selected != "install-check" {
		t.Errorf("scenario %q", s.runs.scenario.Selected)
	}
}

// Choosing "every scenario" forgets the scenario rather than remembering the
// words.
func TestChoosingAnyForgetsTheChoice(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261123T171533Z")
	v := newRunsView(runs, func(string) {})
	v.scenario.SetSelected("install-check")
	v.scenario.SetSelected(anyScenario)
	if got := fyne.CurrentApp().Preferences().String(prefRunsScenario); got != "" {
		t.Errorf("remembered %q", got)
	}
}
