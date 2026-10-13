package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
)

func TestParseJudgeCommandPreservesQuotedArguments(t *testing.T) {
	got, err := parseJudgeCommand(`"/tmp/judge scripts/policy.sh" --strict "two words"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/tmp/judge scripts/policy.sh", "--strict", "two words"}
	if len(got) != len(want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExternalJudgeReadsAResultFromItsCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "judge.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"verdict\":\"pass\",\"steps\":{\"check\":{\"verdict\":\"met\"}}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := (externalJudge{
		Context: context.Background(), NameValue: "team-policy", VersionValue: "1", Command: []string{script},
	}).Judge(&record.Record{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "pass" || result.Steps["check"].Verdict != "met" {
		t.Fatalf("result = %#v", result)
	}
}
