package campaign

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeInvoker struct {
	calls []string
}

func (f *fakeInvoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case slicesContain(args, "run"):
		iteration := 0
		for _, call := range f.calls {
			if strings.Contains(call, " run ") {
				iteration++
			}
		}
		_, _ = fmt.Fprintf(stdout, `{"run_id":"run-%d"}`, iteration)
		return 0
	case slicesContain(args, "judge"):
		_, _ = io.WriteString(stdout, `{"verdict":"pass"}`)
		return 0
	case slicesContain(args, "reproduce"):
		_, _ = io.WriteString(stdout, `{"verdict":"pass"}`)
		return 0
	case slicesContain(args, "doctor"):
		_, _ = io.WriteString(stdout, `{"checks":[]}`)
		return 0
	case slicesContain(args, "render"):
		_, _ = io.WriteString(stdout, "facts\n")
		return 0
	default:
		return 2
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRunRepeatsJudgesReproducesAndAlwaysCleansUp(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, `
schema: 1
name: demo
scenario: scenario-a
params: {to: "7.2.3"}
matrix: {from: ["6.2.4", "7.2.1"]}
parallel: 1
release_failed_targets: true
repeat: 2
judge: expect@3
render: team
reproduce: true
lifecycle: {prepare: prepare.sh, cleanup: cleanup.sh}
`, "prepare.sh", "cleanup.sh")
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(t.TempDir(), "campaign-record")
	invoker := &fakeInvoker{}
	result, err := Run(context.Background(), Options{
		Definition: definition, CampaignDir: recordDir, RunsDir: t.TempDir(), ScenarioRoot: t.TempDir(),
		TargetName: "lab-clone", TargetTags: []string{"capacity:exclusive"}, Invoker: invoker,
		Now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusPassed || result.Cleanup != "released" || !result.Reproduced {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(invoker.calls, "\n"); strings.Count(got, " run ") != 2 || strings.Count(got, " judge ") != 2 || strings.Count(got, " render ") != 2 || strings.Count(got, " reproduce ") != 1 {
		t.Fatalf("calls:\n%s", got)
	} else if !strings.Contains(got, "--param to=7.2.3") || !strings.Contains(got, "--matrix from=6.2.4,7.2.1") || !strings.Contains(got, "--par 1") || !strings.Contains(got, "--release-failed-targets") {
		t.Fatalf("campaign scope was not passed to child commands:\n%s", got)
	}
	status, err := ReadStatus(recordDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusPassed || len(status.Runs) != 2 {
		t.Fatalf("status = %#v", status)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "hooks", "cleanup.out")); err != nil {
		t.Fatalf("cleanup evidence: %v", err)
	}
}

type failingInvoker struct{}

func (failingInvoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	if slicesContain(args, "run") {
		_, _ = io.WriteString(stdout, `{"run_id":"failed-run"}`)
		return 10
	}
	return 0
}

func TestRunFailureStillCleansUp(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, "schema: 1\nname: demo\nscenario: scenario-a\nlifecycle: {cleanup: cleanup.sh}\n", "cleanup.sh")
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Options{Definition: definition, CampaignDir: filepath.Join(t.TempDir(), "record"), RunsDir: t.TempDir(), ScenarioRoot: t.TempDir(), Invoker: failingInvoker{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusInfraFail || result.Cleanup != "released" || result.ExitCode != 10 {
		t.Fatalf("result = %#v", result)
	}
}

func TestKeepIsExplicitInTheRecord(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, "schema: 1\nname: demo\nscenario: scenario-a\nlifecycle: {cleanup: cleanup.sh}\n", "cleanup.sh")
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(t.TempDir(), "record")
	result, err := Run(context.Background(), Options{Definition: definition, CampaignDir: recordDir, RunsDir: t.TempDir(), ScenarioRoot: t.TempDir(), Invoker: &fakeInvoker{}, Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleanup != "kept" {
		t.Fatalf("cleanup = %q", result.Cleanup)
	}
	events, err := ReadEvents(recordDir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		found = found || event.Kind == "cleanup-skipped"
	}
	if !found {
		t.Fatal("record has no cleanup-skipped event")
	}
}

func TestOutputLogContainsLiveActivity(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, "schema: 1\nname: demo\nscenario: scenario-a\n")
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(t.TempDir(), "record")
	_, err = Run(context.Background(), Options{Definition: definition, CampaignDir: recordDir, RunsDir: t.TempDir(), ScenarioRoot: t.TempDir(), Invoker: &fakeInvoker{}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(recordDir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("run 1 of 1 started")) || !bytes.Contains(body, []byte("finished passed")) {
		t.Fatalf("output:\n%s", body)
	}
}

func TestPrepareReceiptSuppliesTheGeneratedInputBundle(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, "schema: 1\nname: demo\nscenario: scenario-a\nlifecycle: {prepare: prepare.sh}\n", "prepare.sh")
	prepare := filepath.Join(dir, "prepare.sh")
	if err := os.WriteFile(prepare, []byte("#!/bin/sh\nprintf '{\"schema\":1,\"input_bundle\":\"/staged/inputs.yaml\"}\\n' >\"$TESTEASE_HOOK_RECEIPT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &fakeInvoker{}
	_, err = Run(context.Background(), Options{Definition: definition, CampaignDir: filepath.Join(t.TempDir(), "record"), RunsDir: t.TempDir(), ScenarioRoot: t.TempDir(), Invoker: invoker})
	if err != nil {
		t.Fatal(err)
	}
	var runCall string
	for _, call := range invoker.calls {
		if strings.Contains(call, " run ") {
			runCall = call
		}
	}
	if !strings.Contains(runCall, "--input-bundle /staged/inputs.yaml") {
		t.Fatalf("run call did not consume prepare receipt: %s", runCall)
	}
}
