package main

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/record"
)

// Every other screen says when in UTC; Activity does too.
func TestActivitySaysWhenAJobStartedInUTC(t *testing.T) {
	test.NewTempApp(t)

	jv := newJobView("run hello", []string{"run", "hello"}, "r", func() {}, func() {}, func(string) {})
	jv.started = time.Date(2026, 11, 24, 5, 54, 24, 0, time.FixedZone("CST", -6*3600))
	a := newActivityView(func() []*jobView { return []*jobView{jv} }, func(*jobView) {})
	if got := a.cellText(0, acolStarted); got != "11:54:24 UTC" {
		t.Errorf("started = %q", got)
	}
}

// A picker never used opens at home, not wherever another picker was last.
func TestAPickerNeverUsedOpensAtHome(t *testing.T) {
	test.NewTempApp(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got := startFolder("never-used")
	if got == nil || filepath.Base(got.Path()) != filepath.Base(home) {
		t.Errorf("opens at %v, want %s", got, home)
	}
}

// An override that is not a flag is flagged while it is typed, not only once
// Run is pressed.
func TestAnOverrideThatIsNotAFlagIsFlaggedAsItIsTyped(t *testing.T) {
	test.NewTempApp(t)

	e := overridesEntry()
	var last error
	e.SetOnValidationChanged(func(err error) { last = err })
	w := test.NewWindow(e)
	defer w.Close()
	w.Canvas().Focus(e)
	test.Type(e, "repeat 2")
	if last == nil {
		t.Error("no error while typing a line that is not a flag")
	}
}

func viewsRecorded(t *testing.T, runs, id string, views map[string]record.ViewRef) string {
	t.Helper()
	return recorded(t, runs, id, func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: id, Scenario: record.ScenarioRef{Name: "install-check"}, Views: views})
		emit(t, w, record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
	})
}

// Render offers the views the scenario recorded first, then the built-in ones.
func TestRenderOffersTheRecordsOwnViewsFirst(t *testing.T) {
	runs := t.TempDir()
	dir := viewsRecorded(t, runs, "install-check-20261124T010000Z", map[string]record.ViewRef{
		"versions": {Kind: "rows", Facts: []string{"package.version"}},
		"brief":    {Kind: "table"},
	})
	got := renderChoices(dir)
	want := []string{"brief (recorded)", "versions (recorded)", "table", "tsv", "rows", "meta"}
	if !slices.Equal(got, want) {
		t.Errorf("choices = %q, want %q", got, want)
	}
	if args := renderArgs("/srv/runs", dir, "versions (recorded)"); !slices.Equal(args, []string{"--runs-dir", "/srv/runs", "render", dir, "--preset", "versions"}) {
		t.Errorf("preset args = %q", args)
	}
	if args := renderArgs("/srv/runs", dir, "table"); !slices.Equal(args, []string{"--runs-dir", "/srv/runs", "render", dir, "--view", "table"}) {
		t.Errorf("view args = %q", args)
	}
}

// Compare offers the other runs of the same scenario, newest first, and asks
// reproduce which stable facts differ.
func TestCompareOffersOtherRunsOfTheSameScenario(t *testing.T) {
	runs := t.TempDir()
	older := installCheck(t, runs, "install-check-20261123T171533Z-install-old")
	newer := installCheck(t, runs, "install-check-20261124T171533Z-install-new")
	viewsRecorded(t, runs, "install-check-20261122T000000Z", nil)
	recorded(t, runs, "hello-20261124T000000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "hello-20261124T000000Z", Scenario: record.ScenarioRef{Name: "hello"}})
	})

	got := compareCandidates(runs, newer)
	var ids []string
	for _, r := range got {
		ids = append(ids, r.RunID)
	}
	if want := []string{"install-check-20261123T171533Z-install-old", "install-check-20261122T000000Z"}; !slices.Equal(ids, want) {
		t.Errorf("candidates = %q, want %q", ids, want)
	}
	if args := compareArgs("/srv/runs", newer, []string{older}); !slices.Equal(args, []string{"--runs-dir", "/srv/runs", "reproduce", newer, older}) {
		t.Errorf("args = %q", args)
	}
}

func TestACampaignIsPackagedByItsID(t *testing.T) {
	if got := packageArgs("/srv/runs", "upgrade-1"); !slices.Equal(got, []string{"--runs-dir", "/srv/runs", "package", "upgrade-1"}) {
		t.Errorf("args = %q", got)
	}
}

// Render and Compare are on a record; Package is on a campaign.
func TestRenderCompareAndPackageStartJobs(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	older := installCheck(t, runs, "install-check-20261123T171533Z-install-old")
	rec := installCheck(t, runs, "install-check-20261124T171533Z-install-new")
	s := shellWithFake(t, runs)
	s.openRecord(rec, "Runs", s.showRuns)
	d := s.current.(*detailView)
	if d.render.Hidden || d.compare.Hidden {
		t.Fatal("no Render or Compare on a record")
	}

	s.renderRecord(rec, "table")
	if jv := waitJob(t, s); !slices.Equal(jv.args, renderArgs(runs, rec, "table")) || jv.record != rec {
		t.Errorf("render args %q, record %q: Open record should open what was rendered", jv.args, jv.record)
	}
	s.compareRecords(rec, []string{older})
	if jv := waitJob(t, s); !slices.Equal(jv.args, compareArgs(runs, rec, []string{older})) || jv.record != rec {
		t.Errorf("compare args %q, record %q", jv.args, jv.record)
	}

	upgradeCampaign(t, runs, true)
	id := upgradeID
	s.openCampaign(id)
	c := s.current.(*campaignView)
	test.Tap(c.pack.Button)
	if jv := waitJob(t, s); !slices.Equal(jv.args, packageArgs(runs, id)) {
		t.Errorf("package args %q", jv.args)
	}
}
