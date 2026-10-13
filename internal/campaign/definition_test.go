package campaign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/scenario"
)

func writeDefinition(t *testing.T, body string, hooks ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "campaign.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, hook := range hooks {
		if err := os.WriteFile(filepath.Join(dir, hook), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDefinitionIsStrictAndPinsItsBytes(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, `
schema: 1
name: sample-saml
scenario: saml-e2e-live
params: {to: "7.2.3"}
matrix: {from: ["6.2.4", "7.2.1"]}
parallel: 1
release_failed_targets: true
target:
  name: lab-clone
  tags: [posture:fips-stig, capacity:exclusive]
repeat: 2
judge: expect@3
reproduce: true
lifecycle:
  prepare: prepare.sh
  cleanup: cleanup.sh
`, "prepare.sh", "cleanup.sh")
	definition, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "sample-saml" || definition.Repeat != 2 || definition.Parallel != 1 || !definition.ReleaseFailedTargets || len(definition.Matrix["from"]) != 2 || len(definition.SHA256) != 64 {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestLoadRefusesUnknownFieldsAndNonExecutableHooks(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"schema: 1\nname: x\nscenario: y\nsurprise: true\n",
		"schema: 1\nname: x\nscenario: y\nlifecycle: {prepare: missing.sh}\n",
		"schema: 1\nname: x\nscenario: y\nparams: {from: old}\nmatrix: {from: [older]}\n",
		"schema: 1\nname: x\nscenario: y\nmatrix: {from: []}\n",
		"schema: 1\nname: x\nscenario: y\nparallel: -1\n",
	} {
		if _, err := Load(writeDefinition(t, body)); err == nil {
			t.Fatalf("Load accepted:\n%s", body)
		}
	}
}

func TestReproductionRequiresTwoIndependentRuns(t *testing.T) {
	t.Parallel()
	dir := writeDefinition(t, "schema: 1\nname: x\nscenario: y\nrepeat: 1\nreproduce: true\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted a one-run reproduction campaign")
	}
}

func TestValidateAgainstScenarioRefusesUnknownScopeNames(t *testing.T) {
	t.Parallel()
	definition := &Definition{
		Scenario: "sample",
		Params:   map[string]string{"typo": "value"},
		Matrix:   map[string][]string{"fixed": {"one"}},
	}
	manifest := &scenario.Manifest{
		Name: "sample",
		Params: map[string]scenario.Param{
			"version": {Axis: true, Values: "one,two"},
		},
	}

	err := definition.ValidateAgainstScenario(manifest)
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("err = %v, want the unknown parameter", err)
	}
}

func TestValidateAgainstScenarioRefusesUnknownJudgeAndRender(t *testing.T) {
	t.Parallel()
	manifest := &scenario.Manifest{
		Name:  "sample",
		Views: map[string]scenario.View{"team": {Kind: "facts"}},
	}

	unknownJudge := &Definition{Scenario: "sample", Judge: "typo@1"}
	if err := unknownJudge.ValidateAgainstScenario(manifest); err == nil || !strings.Contains(err.Error(), "judge") {
		t.Fatalf("unknown judge error = %v", err)
	}

	unknownRender := &Definition{Scenario: "sample", Render: "typo"}
	if err := unknownRender.ValidateAgainstScenario(manifest); err == nil || !strings.Contains(err.Error(), "render") {
		t.Fatalf("unknown render error = %v", err)
	}
}
