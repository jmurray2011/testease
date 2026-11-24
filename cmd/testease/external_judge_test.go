package main

import (
	"context"
	"fmt"
	"os"
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
	// The judge's command is this test binary, running only
	// TestExternalJudgeHelper, so it runs wherever the tests do.
	command := []string{os.Args[0], "-test.run=^TestExternalJudgeHelper$", "--", externalJudgeMarker}

	result, err := (externalJudge{
		Context: context.Background(), NameValue: "team-policy", VersionValue: "1", Command: command,
	}).Judge(&record.Record{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "pass" || result.Steps["check"].Verdict != "met" {
		t.Fatalf("result = %#v", result)
	}
}

const externalJudgeMarker = "testease-external-judge"

// TestExternalJudgeHelper is the judge the test above runs: it prints one
// passing result. Run as an ordinary test it finds no marker and does nothing.
func TestExternalJudgeHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == externalJudgeMarker {
			fmt.Println(`{"verdict":"pass","steps":{"check":{"verdict":"met"}}}`)
			os.Exit(0)
		}
	}
}
