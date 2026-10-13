package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

// A RECORD MUST BE ABLE TO SAY WHAT PRODUCED IT.
//
// os and arch were the whole of env_fingerprint, and several values that
// change WHAT IS MEASURED reach the runner from the environment: the artifact
// store an axis discovers from, the fingerprint instrument's own commands, the
// memory a one-shot Windows cell gets. None of them appeared anywhere in the
// record, so two runs that measured different things over the same scenario
// were indistinguishable afterwards - which is the record lying by omission
// about its own conditions.
func TestTheRecordSaysWhatWasMeasuringIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opt := runner.Options{
		RunsDir: dir,
		Tool:    record.ToolRef{Version: "1.4.0", Commit: "abc1234"},
		Measurement: map[string]string{
			"store":              "/legacy",
			"instrument.collect": "siglae collect --profile sample",
			"instrument.diff":    "siglae diff",
			"instrument.name":    "siglae@2",
			"docker.context":     "winhost",
			"win.memory":         "6g",
		},
	}

	started := runStartedOf(t, opt)

	if started.Tool.Version != "1.4.0" || started.Tool.Commit != "abc1234" {
		t.Errorf("tool = %+v, want the binary that took the measurement", started.Tool)
	}
	for key, want := range map[string]string{
		"store":              "/legacy",
		"instrument.collect": "siglae collect --profile sample",
		"docker.context":     "winhost",
	} {
		if got := started.EnvFingerprint[key]; got != want {
			t.Errorf("env_fingerprint[%q] = %q, want %q", key, got, want)
		}
	}
	// The machine is still in there; this widens the field rather than
	// replacing what it held.
	if started.EnvFingerprint["os"] == "" || started.EnvFingerprint["arch"] == "" {
		t.Errorf("env_fingerprint = %v, want the machine still named", started.EnvFingerprint)
	}
}

// A run given none of them says none of them, rather than carrying empty keys
// that read as "measured with no instrument" instead of "nobody said".
func TestAnUnconfiguredRunClaimsNoMeasurementConditions(t *testing.T) {
	t.Parallel()

	started := runStartedOf(t, runner.Options{RunsDir: t.TempDir()})

	for key := range started.EnvFingerprint {
		if key != "os" && key != "arch" {
			t.Errorf("env_fingerprint carries %q on a run that declared nothing", key)
		}
	}
}

// runStartedOf runs a scenario far enough to write its run-started event and
// reads it back off the record.
func runStartedOf(t *testing.T, opt runner.Options) record.RunStarted {
	t.Helper()

	if opt.Plan == nil {
		opt.Plan = plan(t, oneStep)
	}
	if opt.Providers == nil {
		opt.Providers = map[string]target.Provider{
			"fake": &fakeProvider{target: &fakeTarget{session: &fakeSession{run: exits(0, "x\n")}}},
		}
	}
	_, _ = runner.Run(t.Context(), opt)

	entries, err := os.ReadDir(opt.RunsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no record was written under %s (%v)", opt.RunsDir, err)
	}
	body, err := os.ReadFile(filepath.Join(opt.RunsDir, entries[0].Name(), record.EventsFile))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, `"kind":"run-started"`) {
			continue
		}
		var e record.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse run-started: %v", err)
		}
		started, err := record.Decode[record.RunStarted](e)
		if err != nil {
			t.Fatalf("decode run-started: %v", err)
		}
		return started
	}
	t.Fatalf("no run-started in %s", body)
	return record.RunStarted{}
}
