package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateChecksAScenarioWithoutAProvider(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
question: does it work?
params:
  image: {default: alpine:3.20}
hosts:
  box:
    provider: docker
    image: "{{.image}}"
steps:
  - label: check it
    host: box
    probe: probes/check.sh
    expect: {rc: 0}
`)
	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"validate", "sample", "--scenario-root", rootDir})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "valid scenario sample") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestValidateRejectsAnUnresolvedTemplate(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
hosts:
  box:
    provider: docker
    image: "{{.typo}}"
steps:
  - label: check it
    host: box
    probe: probes/check.sh
`)

	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"validate", "sample", "--scenario-root", rootDir})

	if err := root.Execute(); err == nil || !strings.Contains(stdout.String(), "did not resolve") {
		t.Fatalf("validate error = %v, stdout = %q, want unresolved template error", err, stdout.String())
	}
}

func TestValidateAllRejectsAnEmptyScenarioRoot(t *testing.T) {
	root := newRoot()
	root.SetArgs([]string{"validate", "--all", "--scenario-root", t.TempDir()})

	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "no scenarios") {
		t.Fatalf("validate --all error = %v, want an empty-root error", err)
	}
}

func TestPlanShowsResolvedStepsWithoutRunningThem(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
question: does it work?
params:
  image: {default: alpine:3.20}
hosts:
  box:
    provider: docker
    image: "{{.image}}"
steps:
  - label: check it
    host: box
    probe: probes/check.sh
    expect: {rc: 0}
`)

	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"plan", "sample", "--scenario-root", rootDir, "--output", "json"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name  string `json:"name"`
		Cells []struct {
			Hosts []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"hosts"`
			Steps []struct {
				Label string `json:"label"`
				Probe string `json:"probe"`
			} `json:"steps"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json = %q: %v", stdout.String(), err)
	}
	if got.Name != "sample" || len(got.Cells) != 1 || len(got.Cells[0].Hosts) != 1 || got.Cells[0].Hosts[0].Name != "box" || got.Cells[0].Hosts[0].Image != "alpine:3.20" {
		t.Fatalf("plan = %#v", got)
	}
	if len(got.Cells[0].Steps) != 1 || got.Cells[0].Steps[0].Probe != "probes/check.sh" {
		t.Fatalf("steps = %#v", got.Cells[0].Steps)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPlanOmitsExcludedMatrixCells(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
params:
  from: {axis: true, values: "old,new"}
  to: {axis: true, values: "next,latest"}
matrix:
  exclude:
    - {from: old, to: latest}
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)

	root := newRoot()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"plan", "sample", "--scenario-root", rootDir, "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Cells []struct {
			Params map[string]string `json:"params"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json = %q: %v", stdout.String(), err)
	}
	if len(got.Cells) != 3 {
		t.Fatalf("cells = %#v, want 3", got.Cells)
	}
}

func TestPlanJSONIncludesCarryDetails(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
hosts:
  source: {provider: docker, image: neutral-linux:1}
  destination: {provider: hyperv, image: neutral-windows:1}
steps:
  - label: move it
    host: source
    carry: {from: /out/item, to_host: destination, to: /item}
`)

	root := newRoot()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"plan", "sample", "--scenario-root", rootDir, "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Cells []struct {
			Steps []struct {
				Carry *struct {
					From   string `json:"from"`
					ToHost string `json:"to_host"`
					To     string `json:"to"`
				} `json:"carry"`
			} `json:"steps"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json = %q: %v", stdout.String(), err)
	}
	if got.Cells[0].Steps[0].Carry == nil || got.Cells[0].Steps[0].Carry.From != "/out/item" || got.Cells[0].Steps[0].Carry.ToHost != "destination" || got.Cells[0].Steps[0].Carry.To != "/item" {
		t.Fatalf("carry = %#v", got.Cells[0].Steps[0].Carry)
	}
}

func TestPlanTextExplainsCarryDetails(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
hosts:
  source: {provider: docker, image: neutral-linux:1}
  destination: {provider: hyperv, image: neutral-windows:1}
steps:
  - label: move it
    host: source
    carry: {from: /out/item, to_host: destination, to: /item}
`)

	root := newRoot()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"plan", "sample", "--scenario-root", rootDir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "carry /out/item -> destination:/item") {
		t.Fatalf("plan = %q, want carry source and destination", stdout.String())
	}
}

func TestPlanRefusesExplicitDiscoveryValues(t *testing.T) {
	rootDir := writeAuthoringScenario(t, `
schema: 1
name: sample
params:
  version: {axis: true, values: "@store:packages-*"}
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)

	root := newRoot()
	root.SetArgs([]string{"plan", "sample", "--scenario-root", rootDir, "--matrix", "version=@store:packages-*"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Fatalf("plan error = %v, want discovery precondition", err)
	}
}

func writeAuthoringScenario(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sample")
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(strings.TrimSpace(manifest)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "check.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
