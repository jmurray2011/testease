package main

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// Two or more runs side by side: what went in, what came out, and the
// differences said first.

func intp(n int) *int { return &n }

func sideOf(id, tool string, params map[string]string, steps ...browse.Step) side {
	return side{
		d: browse.Detail{
			Run:      browse.Run{RunID: id, Scenario: "api-smoke", Status: string(record.StatusMeasured)},
			Params:   params,
			Tool:     record.ToolRef{Version: tool},
			Runner:   record.HostRef{User: "josh", Hostname: "ws"},
			StepList: steps,
		},
		started: record.RunStarted{Scenario: record.ScenarioRef{Name: "api-smoke", Hash: "sha256:aaaa"}},
	}
}

func probe(status string, rc int, secs float64, facts ...record.Fact) browse.Step {
	return browse.Step{Label: "probe api", Status: status, RC: intp(rc), DurationS: secs, Facts: facts}
}

func fact(key, value string) record.Fact { return record.Fact{Key: key, Value: &value} }

func rowNamed(rows []diffRow, section, name string) (diffRow, bool) {
	for _, r := range rows {
		if r.Section == section && r.Name == name {
			return r, true
		}
	}
	return diffRow{}, false
}

func TestRunsAreComparedOnWhatWentInAndWhatCameOut(t *testing.T) {
	a := sideOf("api-smoke-20261120T100000Z", "0.3.0", map[string]string{"region": "us-east-1", "size": "m"},
		probe("measured", 0, 2.0, fact("http.status", "200"), fact("service.version", "2.4.0")))
	b := sideOf("api-smoke-20261121T100000Z", "0.4.0", map[string]string{"region": "us-east-2", "size": "m"},
		probe("probe-fail", 1, 2.1, fact("http.status", "503"), fact("service.version", "2.4.0")))
	b.d.Status = string(record.StatusProbeFail)
	rows := diffRuns([]side{a, b})

	for _, want := range []struct {
		section, name string
		values        []string
		differs       bool
	}{
		{secSetup, "Tool", []string{"0.3.0", "0.4.0"}, true},
		{secParams, "region", []string{"us-east-1", "us-east-2"}, true},
		{secParams, "size", []string{"m", "m"}, false},
		{secSteps, "probe api", []string{"measured · rc 0", "probe-fail · rc 1"}, true},
		{secFacts, "probe api · http.status", []string{"200", "503"}, true},
		{secFacts, "probe api · service.version", []string{"2.4.0", "2.4.0"}, false},
		{secOutcome, "Status", []string{"measured", "probe-fail"}, true},
	} {
		got, ok := rowNamed(rows, want.section, want.name)
		if !ok {
			t.Errorf("no row %s / %s", want.section, want.name)
			continue
		}
		if !slices.Equal(got.Values, want.values) || got.Differs != want.differs {
			t.Errorf("%s / %s = %v differs=%v, want %v differs=%v", want.section, want.name, got.Values, got.Differs, want.values, want.differs)
		}
	}
	// Two seconds against 2.1 is the same run, not a slower one.
	if got, ok := rowNamed(rows, secTimes, "probe api"); !ok || got.Differs {
		t.Errorf("step time %+v %v", got, ok)
	}
}

func TestWhatOneRunHasAndAnotherLacksIsADifference(t *testing.T) {
	a := sideOf("s-20261120T100000Z", "0.3.0", nil, probe("measured", 0, 1, fact("cache.hits", "40")))
	b := sideOf("s-20261121T100000Z", "0.3.0", map[string]string{"region": "eu-west-1"}, probe("measured", 0, 1))
	rows := diffRuns([]side{a, b})
	if got, _ := rowNamed(rows, secParams, "region"); !got.Differs || got.Values[0] != "" {
		t.Errorf("param %+v", got)
	}
	if got, _ := rowNamed(rows, secFacts, "probe api · cache.hits"); !got.Differs || got.Values[1] != "" {
		t.Errorf("fact %+v", got)
	}
}

func TestAMuchSlowerStepIsCalledOut(t *testing.T) {
	a := sideOf("s-20261120T100000Z", "0.3.0", nil, probe("measured", 0, 4))
	b := sideOf("s-20261121T100000Z", "0.3.0", nil, probe("measured", 0, 31))
	got, _ := rowNamed(diffRuns([]side{a, b}), secTimes, "probe api")
	if !got.Differs || got.Values[1] != "31 s" {
		t.Errorf("time %+v", got)
	}
}

func TestASecretIsComparedButNotShown(t *testing.T) {
	a := sideOf("s-20261120T100000Z", "0.3.0", map[string]string{"api_token": "abc"})
	b := sideOf("s-20261121T100000Z", "0.3.0", map[string]string{"api_token": "xyz"})
	got, _ := rowNamed(diffRuns([]side{a, b}), secParams, "api_token")
	if !got.Differs || !got.Changed[1] {
		t.Error("two different secrets read as the same")
	}
	for _, v := range got.Values {
		if v == "abc" || v == "xyz" {
			t.Errorf("secret shown: %v", got.Values)
		}
	}
}

func TestARepeatedStepLabelIsMatchedByItsTurn(t *testing.T) {
	step := func(rc int) browse.Step { return browse.Step{Label: "poll", Status: "measured", RC: intp(rc)} }
	a := sideOf("s-20261120T100000Z", "0.3.0", nil, step(0), step(0))
	b := sideOf("s-20261121T100000Z", "0.3.0", nil, step(0), step(1))
	rows := diffRuns([]side{a, b})
	if got, _ := rowNamed(rows, secSteps, "poll"); got.Differs {
		t.Errorf("first poll %+v", got)
	}
	if got, ok := rowNamed(rows, secSteps, "poll (2)"); !ok || !got.Differs {
		t.Errorf("second poll %+v", got)
	}
}

func TestTheExplanationSaysWhatWentInBeforeWhatCameOut(t *testing.T) {
	a := sideOf("s-20261120T100000Z", "0.3.0", map[string]string{"region": "us-east-1"},
		probe("measured", 0, 2, fact("http.status", "200")))
	b := sideOf("s-20261121T100000Z", "0.4.0", map[string]string{"region": "us-east-2"},
		probe("probe-fail", 1, 2, fact("http.status", "503")))
	b.d.Status = string(record.StatusProbeFail)
	in, out := explain(diffRuns([]side{a, b}), 2)
	if want := []string{"Tool: 0.3.0, then 0.4.0", "1 param differs: region"}; !slices.Equal(in, want) {
		t.Errorf("went in %q, want %q", in, want)
	}
	if want := []string{"1 step result differs: probe api", "1 fact differs: probe api · http.status", "Status: measured, then probe-fail"}; !slices.Equal(out, want) {
		t.Errorf("came out %q, want %q", out, want)
	}
}

func TestSameInputsWithDifferentResultsSaySo(t *testing.T) {
	a := sideOf("s-20261120T100000Z", "0.3.0", nil, probe("measured", 0, 2))
	b := sideOf("s-20261121T100000Z", "0.3.0", nil, probe("probe-fail", 1, 2))
	in, out := explain(diffRuns([]side{a, b}), 2)
	if len(in) != 1 || !strings.Contains(in[0], "Nothing recorded about what went in differs") {
		t.Errorf("went in %q", in)
	}
	if len(out) == 0 {
		t.Error("no difference in what came out")
	}
	same, _ := explain(diffRuns([]side{a, a}), 2)
	if len(same) != 1 || !strings.Contains(same[0], "Nothing differs") {
		t.Errorf("identical runs %q", same)
	}
}

func TestManyDifferencesAreNamedAFewAtATime(t *testing.T) {
	pa, pb := map[string]string{}, map[string]string{}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		pa[k], pb[k] = "1", "2"
	}
	in, _ := explain(diffRuns([]side{sideOf("s-1", "1", pa), sideOf("s-2", "1", pb)}), 2)
	if want := "5 params differ: a, b, c and 2 more"; len(in) != 1 || in[0] != want {
		t.Errorf("got %q, want %q", in, want)
	}
}

func TestThreeRunsListEachValueOnce(t *testing.T) {
	in, _ := explain(diffRuns([]side{sideOf("s-1", "0.3.0", nil), sideOf("s-2", "0.4.0", nil), sideOf("s-3", "0.3.0", nil)}), 3)
	if want := "Tool differs: 0.3.0, 0.4.0"; len(in) != 1 || in[0] != want {
		t.Errorf("got %q, want %q", in, want)
	}
}

func TestComparedRunsReadOldestFirst(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	older := installCheck(t, runs, "install-check-20261121T080000Z")
	newer := installCheck(t, runs, "install-check-20261122T080000Z")
	v, err := newCompareView([]string{newer, older}, "Runs", func() {})
	if err != nil {
		t.Fatal(err)
	}
	if v.sides[0].d.Dir != older || v.sides[1].d.Dir != newer {
		t.Errorf("order %s, %s", v.sides[0].d.RunID, v.sides[1].d.RunID)
	}
	if v.sides[0].started.Scenario.Name != "install-check" {
		t.Errorf("run started not read: %+v", v.sides[0].started)
	}
}

func TestOnlyWhatDiffersIsShownUntilAskedForEverything(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	a := installCheck(t, runs, "install-check-20261121T080000Z")
	b := installCheck(t, runs, "install-check-20261122T080000Z")
	v, err := newCompareView([]string{a, b}, "Runs", func() {})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.shown {
		if !r.Differs && !r.heading {
			t.Errorf("row %s / %s shown though it does not differ", r.Section, r.Name)
		}
	}
	v.onlyDiff.SetChecked(false)
	if len(v.shown) <= len(v.rows) {
		t.Errorf("everything: %d rows shown of %d", len(v.shown), len(v.rows))
	}
}

func TestRunsOfDifferentScenariosAreComparedWithoutReproduce(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	a := installCheck(t, runs, "install-check-20261121T080000Z")
	b := recorded(t, runs, "hello-20261122T000000Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{RunID: "hello-20261122T000000Z", Scenario: record.ScenarioRef{Name: "hello"}})
	})
	s := shellWithFake(t, runs)
	s.compareRecords([]string{a, b}, "Runs", s.showRuns)
	cv, ok := s.current.(*compareView)
	if !ok {
		t.Fatalf("opened %T", s.current)
	}
	if !cv.reproduce.Hidden {
		t.Error("reproduce offered for runs of two scenarios")
	}
	if got, _ := rowNamed(cv.rows, secSetup, "Scenario"); !got.Differs {
		t.Errorf("scenario row %+v", got)
	}
}

func TestTheRunsScreenComparesTheRunsPicked(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261121T080000Z")
	installCheck(t, runs, "install-check-20261122T080000Z")
	s := shellWithFake(t, runs)
	s.showRuns()
	if s.runs.compare.Hidden {
		t.Fatal("no Compare on the Runs screen")
	}
	if got := len(s.runs.shown); got != 2 {
		t.Fatalf("%d runs shown", got)
	}
}

func TestThePickerShowsManyRunsAtOnce(t *testing.T) {
	test.NewTempApp(t)
	ids := make([]string, 14)
	for i := range ids {
		ids[i] = "vt7-operations-202611" + string(rune('a'+i))
	}
	list := pickerList(widget.NewCheckGroup(ids, nil))
	if h := list.MinSize().Height; h < 280 {
		t.Errorf("picker is %v tall", h)
	}
}

func TestNothingDifferingSaysSoAndEachRunIsNamedInFull(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	a := installCheck(t, runs, "install-check-20261121T080000Z-a-rather-long-suffix")
	b := installCheck(t, runs, "install-check-20261122T080000Z-a-rather-long-suffix")
	v, err := newCompareView([]string{a, b}, "Runs", func() {})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.shown) != 0 || v.empty.Hidden {
		t.Errorf("%d rows shown, empty note hidden: %v", len(v.shown), v.empty.Hidden)
	}
	v.onlyDiff.SetChecked(false)
	if !v.empty.Hidden {
		t.Error("empty note shown over every row")
	}
	id := fyne.MeasureText(v.sides[0].d.RunID, theme.TextSize(), fyne.TextStyle{Monospace: true}).Width
	if w := v.runWidth; w < id {
		t.Errorf("run column %v, id %v", w, id)
	}
}
