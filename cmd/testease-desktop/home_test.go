package main

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
)

// Home opens on an answer: what ran today, what judges said of it, what is
// still going, what needs a look, and how each scenario has been doing.

func TestTodayIsCountedFromRunIDsInUTC(t *testing.T) {
	runs := []browse.Run{
		judgedRun("web-20261124T090000Z", "web", true),
		judgedRun("web-20261124T100000Z", "web", false),
		{RunID: "db-20261124T110000Z", Scenario: "db", Status: "measured"},
		{RunID: "db-20261124T120000Z", Scenario: "db", Status: "probe-fail"},
		{RunID: "db-20261124T130000Z", Scenario: "db", Status: "unfinished"},
		judgedRun("web-20261123T235959Z", "web", false),
	}
	status := func(r browse.Run) string {
		if r.RunID == "db-20261124T130000Z" {
			return statusRunning
		}
		return r.Status
	}
	s := summarize(runs, status, time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC))
	if s.Today != 5 || s.TodayJudged != 2 || s.TodayPassed != 1 {
		t.Errorf("today %d, judged %d, passed %d; want 5, 2, 1", s.Today, s.TodayJudged, s.TodayPassed)
	}
	if s.Running != 1 {
		t.Errorf("running %d, want 1", s.Running)
	}
	// Each scenario's latest finished run, newest first: failed and broken
	// both need a look, whatever day it was.
	var ids []string
	for _, r := range s.Attention {
		ids = append(ids, r.RunID)
	}
	want := []string{"db-20261124T120000Z", "web-20261124T100000Z"}
	if len(ids) != len(want) {
		t.Fatalf("attention %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("attention %v, want %v", ids, want)
			break
		}
	}
}

// A scenario whose latest run passed no longer needs a look for an older
// failure: what needs a look is what is broken now.
func TestAFailureFixedSinceIsNotCalledOut(t *testing.T) {
	runs := []browse.Run{
		judgedRun("web-20261123T100000Z", "web", false),
		judgedRun("web-20261124T100000Z", "web", true),
	}
	s := summarize(runs, func(r browse.Run) string { return r.Status }, time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC))
	if len(s.Attention) != 0 {
		t.Errorf("attention %v, want none", s.Attention)
	}
}

func TestAttentionIsCapped(t *testing.T) {
	var runs []browse.Run
	for i := 0; i < attentionLength+3; i++ {
		runs = append(runs, judgedRun("s"+string(rune('a'+i))+"-20261124T100000Z", "s"+string(rune('a'+i)), false))
	}
	s := summarize(runs, func(r browse.Run) string { return r.Status }, time.Now())
	if len(s.Attention) != attentionLength {
		t.Errorf("%d called out, want %d", len(s.Attention), attentionLength)
	}
}

func TestHomeShowsARecordThatNeedsALookAndOpensIt(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	dir := installCheck(t, runs, "install-check-20261124T100000Z")

	var opened, filtered string
	v := newHomeView(runs, func(d string) { opened = d }, func(s string) { filtered = s })
	v.reload()

	if v.attention.Length() != 1 {
		t.Fatalf("%d need a look, want 1", v.attention.Length())
	}
	v.attention.Select(0)
	if opened != dir {
		t.Errorf("opened %q, want %q", opened, dir)
	}
	if len(v.scenarios) != 1 || v.scenarios[0].Scenario != "install-check" {
		t.Fatalf("scenarios %v", v.scenarios)
	}
	v.scenarioList.Select(0)
	if filtered != "install-check" {
		t.Errorf("filtered for %q", filtered)
	}
}

func TestHomeWithNoRunsSaysWhatToDo(t *testing.T) {
	test.NewTempApp(t)
	v := newHomeView(t.TempDir(), func(string) {}, func(string) {})
	v.reload()
	if !v.empty.Visible() {
		t.Error("an empty runs directory shows an empty dashboard")
	}
}

func TestTheLastDaysAreCountedByOutcome(t *testing.T) {
	runs := []browse.Run{
		judgedRun("a-20261124T090000Z", "a", true),
		judgedRun("a-20261124T100000Z", "a", false),
		{RunID: "a-20261122T100000Z", Scenario: "a", Status: "measured"},
		judgedRun("a-20261101T100000Z", "a", true), // too old
	}
	days := lastDays(runs, time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC), 3)
	if len(days) != 3 {
		t.Fatalf("%d days", len(days))
	}
	want := []dayCount{
		{Day: "20261122", Other: 1},
		{Day: "20261123"},
		{Day: "20261124", Passed: 1, Failed: 1},
	}
	for i := range want {
		if days[i] != want[i] {
			t.Errorf("day %d = %+v, want %+v", i, days[i], want[i])
		}
	}
}

func TestHomeSaysWhenNothingNeedsALook(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261124T100000Z")
	v := newHomeView(runs, func(string) {}, func(string) {})
	v.reload()
	if v.allClear.Visible() {
		t.Error("all clear shown beside a broken run")
	}
	v2 := newHomeView(t.TempDir(), func(string) {}, func(string) {})
	v2.reload()
	if !v2.allClear.Visible() {
		t.Error("all clear not shown when nothing needs a look")
	}
}
