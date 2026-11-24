package environment_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/environment"
)

// One table of every TESTEASE_* variable, so the CLI's help and the desktop
// app's settings cannot drift apart on what exists or on which ones are safe to
// leave set.

func TestEveryVariableIsNamedOnceAndSaysWhatItSets(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, v := range environment.Vars() {
		if !strings.HasPrefix(v.Name, "TESTEASE_") || v.Sets == "" {
			t.Errorf("variable %+v", v)
		}
		if seen[v.Name] {
			t.Errorf("%s is listed twice", v.Name)
		}
		seen[v.Name] = true
	}
}

// The split is provenance: a variable that changes where things are can sit in
// a profile, and one that changes what is measured cannot, because the record
// does not pin it.
func TestWhatChangesAMeasurementIsNotSafeToLeaveSet(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"TESTEASE_RUNS_DIR":       true,
		"TESTEASE_SCENARIO_ROOT":  true,
		"TESTEASE_AWS_PROFILE":    true,
		"TESTEASE_DOCKER_CONTEXT": false,
		"TESTEASE_ENV_FILE":       false,
		"TESTEASE_COMPARE":        false,
	}
	for _, v := range environment.Vars() {
		if safe, ok := want[v.Name]; ok {
			if v.Safe != safe {
				t.Errorf("%s safe = %v, want %v", v.Name, v.Safe, safe)
			}
			delete(want, v.Name)
		}
	}
	for name := range want {
		t.Errorf("%s is missing", name)
	}
}

func TestCurrentSaysWhatEachVariableIsSetTo(t *testing.T) {
	t.Parallel()

	env := map[string]string{"TESTEASE_RUNS_DIR": "/srv/runs"}
	got := environment.Current(func(k string) string { return env[k] })
	if len(got) != len(environment.Vars()) {
		t.Fatalf("Current listed %d of %d", len(got), len(environment.Vars()))
	}
	for _, s := range got {
		switch s.Name {
		case "TESTEASE_RUNS_DIR":
			if s.Value != "/srv/runs" || !s.Set {
				t.Errorf("%+v", s)
			}
		default:
			if s.Set {
				t.Errorf("%s reads as set: %+v", s.Name, s)
			}
		}
	}
}
