package main

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// A fact is read beside what the same step said of it in the scenario's
// earlier runs: a number as a line, anything else as how often it changed.

func withFact(id, label, key string, value *string) browse.Detail {
	return browse.Detail{Run: browse.Run{RunID: id, Scenario: "s"}, StepList: []browse.Step{{
		Label: label, Facts: []record.Fact{{Key: key, Value: value}},
	}}}
}

func str(s string) *string { return &s }

func TestAFactIsFollowedAcrossRunsByStepAndKey(t *testing.T) {
	earlier := []browse.Detail{
		withFact("s-20261120T100000Z", "probe", "latency_ms", str("120")),
		withFact("s-20261121T100000Z", "other step", "latency_ms", str("999")),
		withFact("s-20261122T100000Z", "probe", "latency_ms", str("95.5")),
	}
	now := withFact("s-20261123T100000Z", "probe", "latency_ms", str("101"))
	tr := factTrends(now, earlier)
	got := tr[factKey{"probe", "latency_ms"}]
	if len(got.Values) != 3 || *got.Values[0] != "120" || *got.Values[2] != "101" {
		t.Fatalf("values %v", got.Values)
	}
	nums, ok := got.numbers()
	if !ok || nums[1] != 95.5 {
		t.Errorf("numbers %v %v", nums, ok)
	}
}

func TestAWordFactSaysHowOftenItChanged(t *testing.T) {
	earlier := []browse.Detail{
		withFact("s-1", "probe", "version", str("1.2.0")),
		withFact("s-2", "probe", "version", str("1.2.0")),
		withFact("s-3", "probe", "version", nil),
	}
	now := withFact("s-4", "probe", "version", str("1.3.0"))
	tr := factTrends(now, earlier)[factKey{"probe", "version"}]
	if _, ok := tr.numbers(); ok {
		t.Error("versions read as numbers")
	}
	if got := tr.summary(); got != "changed in 2 of the last 4 runs" {
		t.Errorf("summary %q", got)
	}
	steady := factTrends(withFact("s-2", "probe", "version", str("1.2.0")), earlier[:1])[factKey{"probe", "version"}]
	if got := steady.summary(); got != "the same in the last 2 runs" {
		t.Errorf("summary %q", got)
	}
	first := factTrends(now, nil)[factKey{"probe", "version"}]
	if got := first.summary(); got != "first reading" {
		t.Errorf("summary %q", got)
	}
}

func TestTheFactsTabShowsATrendFromEarlierRuns(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261121T080000Z")
	dir := installCheck(t, runs, "install-check-20261122T080000Z")
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	v.steps.Select(0)
	tr, ok := v.trends[factKey{"install package", "package.version"}]
	if !ok || len(tr.Values) != 2 {
		t.Fatalf("trend %+v", tr)
	}
}

// A status or exit code is a name for an outcome, not a quantity: it is not
// drawn as a line.
func TestCodesAreNotDrawnAsQuantities(t *testing.T) {
	for _, key := range []string{"http.status", "probe.status_code", "exit_code", "step.rc", "upgrade.analyses_platform_key_http"} {
		if quantity(key) {
			t.Errorf("%s is drawn as a quantity", key)
		}
	}
	if !quantity("http.latency_ms") {
		t.Error("latency is not a quantity")
	}
}
