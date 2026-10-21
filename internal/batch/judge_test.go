package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type judgeInvoker struct {
	calls [][]string
}

func (i *judgeInvoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	i.calls = append(i.calls, slices.Clone(args))
	runID := args[slices.Index(args, "judge")+1]
	verdict := "pass"
	code := 0
	if runID == "bravo-run" {
		verdict = "hold"
		code = 20
	}
	_, _ = fmt.Fprintf(stdout, `{"judge":"expect","version":"3","verdict":%q}`, verdict)
	return code
}

func TestJudgeAggregatesEachMeasuredChildAndSavesSeparateSummary(t *testing.T) {
	runs := t.TempDir()
	id := "two-tests"
	dir := filepath.Join(runs, "batches", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	measurement := Result{Schema: Schema, ID: id, Status: "measured", Tests: []TestResult{
		{Name: "alpha", Scenario: "scenario", RunID: "alpha-run", Status: "measured"},
		{Name: "bravo", Scenario: "scenario", RunID: "bravo-run", Status: "measured"},
		{Name: "skipped", Scenario: "other", Status: "skipped"},
	}}
	if err := save(dir, measurement); err != nil {
		t.Fatal(err)
	}
	invoker := &judgeInvoker{}
	got, err := Judge(context.Background(), runs, id, invoker)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.ExitCode != 20 || len(got.Tests) != 3 {
		t.Fatalf("judgment = %+v", got)
	}
	if got.Tests[0].Status != "passed" || got.Tests[1].Verdict != "hold" || got.Tests[1].Status != "failed" || got.Tests[2].Status != "unjudgeable" {
		t.Fatalf("child judgments = %+v", got.Tests)
	}
	if len(invoker.calls) != 2 {
		t.Fatalf("judge calls = %v, want both measured runs only", invoker.calls)
	}
	for _, call := range invoker.calls {
		if !slices.Contains(call, "--runs-dir") || !slices.Contains(call, "--output") || !slices.Contains(call, "json") {
			t.Errorf("judge call missing record or JSON flags: %v", call)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "judgment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Judgment
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "failed" || saved.Tests[1].Verdict != "hold" {
		t.Fatalf("saved judgment = %+v", saved)
	}
	body, err = os.ReadFile(filepath.Join(dir, "batch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var unchanged Result
	if err := json.Unmarshal(body, &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "measured" {
		t.Fatalf("measurement was changed: %+v", unchanged)
	}
}

func TestJudgeKeepsDistinctLogsForOlderUnnamedBatchEntries(t *testing.T) {
	runs := t.TempDir()
	id := "old-batch"
	dir := filepath.Join(runs, "batches", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := save(dir, Result{Schema: Schema, ID: id, Status: "measured", Tests: []TestResult{
		{Scenario: "alpha", RunID: "alpha-run", Status: "measured"},
		{Scenario: "bravo", RunID: "bravo-run", Status: "measured"},
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := Judge(context.Background(), runs, id, &judgeInvoker{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tests[0].Log != "alpha.judge.log" || got.Tests[1].Log != "bravo.judge.log" {
		t.Fatalf("legacy child logs = %q and %q", got.Tests[0].Log, got.Tests[1].Log)
	}
}
