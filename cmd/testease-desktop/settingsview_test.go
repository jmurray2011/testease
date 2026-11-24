package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/environment"
)

// Settings shows what this app was started with, and changes none of it.
// There is no config file: where records are is the one thing remembered, and
// every TESTEASE_* variable is listed with whether it is safe to leave set.

func settingsRow(t *testing.T, v *settingsView, name string) int {
	t.Helper()
	for i, s := range v.rows {
		if s.Name == name {
			return i
		}
	}
	t.Fatalf("%s is not listed", name)
	return -1
}

func TestSettingsListsEveryEnvironmentVariable(t *testing.T) {
	test.NewTempApp(t)

	v := newSettingsView("/srv/runs", "--runs-dir", func(string) string { return "" })
	if len(v.rows) != len(environment.Vars()) {
		t.Errorf("listed %d of %d", len(v.rows), len(environment.Vars()))
	}
	if v.where.Text != "/srv/runs (from --runs-dir)" {
		t.Errorf("where = %q", v.where.Text)
	}
}

func TestAVariableThatChangesWhatIsMeasuredIsMarkedWhenItIsSet(t *testing.T) {
	test.NewTempApp(t)

	env := map[string]string{
		"TESTEASE_RUNS_DIR":       "/srv/runs",
		"TESTEASE_DOCKER_CONTEXT": "remote-box",
	}
	v := newSettingsView("/srv/runs", "TESTEASE_RUNS_DIR", func(k string) string { return env[k] })

	cases := map[string][3]string{
		"TESTEASE_RUNS_DIR":       {"TESTEASE_RUNS_DIR", "/srv/runs", "safe to leave set"},
		"TESTEASE_DOCKER_CONTEXT": {"TESTEASE_DOCKER_CONTEXT", "remote-box", "set; pass it as a flag"},
		"TESTEASE_COMPARE":        {"TESTEASE_COMPARE", "not set", "pass as a flag"},
	}
	for name, want := range cases {
		r := settingsRow(t, v, name)
		for c, text := range want {
			if got := v.cellText(r, c); got != text {
				t.Errorf("%s column %d = %q, want %q", name, c, got, text)
			}
		}
	}
	if _, kind, _ := v.cell(settingsRow(t, v, "TESTEASE_DOCKER_CONTEXT"), ecolUse); kind != pillBad {
		t.Error("a measurement-changing variable left set is not marked")
	}
}

// Settings is read, not picked from: nothing on it can be selected.
func TestTheEnvironmentListHasNothingToSelect(t *testing.T) {
	test.NewTempApp(t)

	v := newSettingsView("", "", func(string) string { return "" })
	if _, ok := any(v.list).(interface{ Select(widget.TableCellID) }); ok {
		t.Error("the environment is listed in a selectable table")
	}
	if got := len(v.list.Objects); got != len(environment.Vars())+1 {
		t.Errorf("list has %d rows, want a header and one per variable", got)
	}
}
