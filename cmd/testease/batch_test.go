package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchValidateChecksEveryScenarioBeforeProvisioning(t *testing.T) {
	rootDir := t.TempDir()
	scenarios := filepath.Join(rootDir, "scenarios")
	for _, name := range []string{"alpha", "bravo"} {
		dir := filepath.Join(scenarios, name)
		if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "schema: 1\nname: " + name + "\nquestion: does it answer?\nhosts:\n  box: {provider: docker, image: neutral-base:1}\nsteps:\n  - label: check\n    host: box\n    probe: probes/check.sh\n"
		if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "probes", "check.sh"), []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	batchFile := filepath.Join(rootDir, "batch.yaml")
	if err := os.WriteFile(batchFile, []byte("schema: 1\nname: two-tests\ntests:\n  - scenario: alpha\n  - scenario: bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		root := newRoot()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"batch", "validate", batchFile, "--scenario-root", scenarios})
		err := root.Execute()
		return output.String(), err
	}
	if output, err := run(); err != nil || !strings.Contains(output, "valid batch two-tests (2 tests)") {
		t.Fatalf("valid batch: output %q, error %v", output, err)
	}
	if err := os.Remove(filepath.Join(scenarios, "bravo", "probes", "check.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "bravo") {
		t.Fatalf("missing bravo probe was accepted: %v", err)
	}
}

func TestBatchStatusDistinguishesNamedRunsOfOneScenario(t *testing.T) {
	runs := t.TempDir()
	dir := filepath.Join(runs, "batches", "two-variants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schema":1,"batch_id":"two-variants","status":"measured","tests":[{"name":"writer","scenario":"alpha","status":"measured","run_id":"alpha-writer","exit_code":0},{"name":"clean","scenario":"alpha","status":"measured","run_id":"alpha-clean","exit_code":0}]}`
	if err := os.WriteFile(filepath.Join(dir, "batch.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--runs-dir", runs, "batch", "status", "two-variants"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"writer (alpha)\tmeasured\talpha-writer", "clean (alpha)\tmeasured\talpha-clean"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status %q lacks %q", output.String(), want)
		}
	}
}

func TestRunSuffixRefusesUnsafeNamesAndResume(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"path traversal", []string{"run", "missing", "--run-suffix", "../escape"}, "invalid run suffix"},
		{"resume conflict", []string{"run", "missing", "--run-suffix", "variant", "--resume", "old-run"}, "--run-suffix cannot be used with --resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(tc.args)
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run error = %v, output = %q, want %q", err, output.String(), tc.want)
			}
		})
	}
}
