package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCampaignValidateDoesNotNeedAProvider(t *testing.T) {
	scenarioRoot := writeAuthoringScenario(t, `
schema: 1
name: sample
params:
  version: {default: one}
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)
	campaignRoot := t.TempDir()
	campaignDir := filepath.Join(campaignRoot, "sample-campaign")
	if err := os.MkdirAll(campaignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campaignDir, "campaign.yaml"), []byte("schema: 1\nname: sample-campaign\nscenario: sample\nparams: {version: two}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"campaign", "validate", "sample-campaign", "--campaign-root", campaignRoot, "--scenario-root", scenarioRoot})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "valid campaign sample-campaign") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCampaignValidateRejectsAnUnresolvedScenarioTemplate(t *testing.T) {
	scenarioRoot := writeAuthoringScenario(t, `
schema: 1
name: sample
hosts:
  box: {provider: docker, image: "{{.missing}}"}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)
	campaignRoot := t.TempDir()
	campaignDir := filepath.Join(campaignRoot, "sample-campaign")
	if err := os.MkdirAll(campaignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campaignDir, "campaign.yaml"), []byte("schema: 1\nname: sample-campaign\nscenario: sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"campaign", "validate", "sample-campaign", "--campaign-root", campaignRoot, "--scenario-root", scenarioRoot})

	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "did not resolve") {
		t.Fatalf("campaign validate error = %v, stdout = %q", err, stdout.String())
	}
}

func TestCampaignValidateResolvesUsingCampaignParameters(t *testing.T) {
	scenarioRoot := writeAuthoringScenario(t, `
schema: 1
name: sample
params:
  image: {}
hosts:
  box: {provider: docker, image: "{{.image}}"}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)
	campaignRoot := t.TempDir()
	campaignDir := filepath.Join(campaignRoot, "sample-campaign")
	if err := os.MkdirAll(campaignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campaignDir, "campaign.yaml"), []byte("schema: 1\nname: sample-campaign\nscenario: sample\nparams: {image: alpine:3.20}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"campaign", "validate", "sample-campaign", "--campaign-root", campaignRoot, "--scenario-root", scenarioRoot})

	if err := root.Execute(); err != nil {
		t.Fatalf("campaign validate: %v", err)
	}
}

func TestCampaignValidateRejectsAPartialMatrixOverride(t *testing.T) {
	scenarioRoot := writeAuthoringScenario(t, `
schema: 1
name: sample
params:
  from: {axis: true, values: "old,new"}
  to: {axis: true, values: "next,latest"}
hosts:
  box: {provider: docker, image: neutral-base:1}
steps:
  - label: check
    host: box
    probe: probes/check.sh
`)
	campaignRoot := t.TempDir()
	campaignDir := filepath.Join(campaignRoot, "sample-campaign")
	if err := os.MkdirAll(campaignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campaignDir, "campaign.yaml"), []byte("schema: 1\nname: sample-campaign\nscenario: sample\nmatrix: {from: [old]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	root.SetArgs([]string{"campaign", "validate", "sample-campaign", "--campaign-root", campaignRoot, "--scenario-root", scenarioRoot})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "to") {
		t.Fatalf("campaign validate error = %v, want the omitted axis", err)
	}
}

func TestCampaignValidateJSONReportsInvalidCampaign(t *testing.T) {
	campaignRoot := t.TempDir()
	campaignDir := filepath.Join(campaignRoot, "bad-campaign")
	if err := os.MkdirAll(campaignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campaignDir, "campaign.yaml"), []byte("schema: 1\nname: bad-campaign\nscenario: sample\nparallel: not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"campaign", "validate", "bad-campaign", "--campaign-root", campaignRoot, "--scenario-root", t.TempDir(), "-o", "json"})

	if err := root.Execute(); err == nil {
		t.Fatal("campaign validate succeeded for invalid campaign")
	}
	var result struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v; stdout = %q", err, stdout.String())
	}
	if result.Valid {
		t.Fatal("invalid campaign was reported as valid")
	}
	if !strings.Contains(result.Error, "parse campaign") {
		t.Fatalf("JSON error = %q, want campaign parse context", result.Error)
	}
}
