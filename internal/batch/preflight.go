package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmurray2011/testease/internal/doctor"
)

// PreflightResult reports what the ordinary doctor command found for each
// batch entry before any child run was scheduled.
type PreflightResult struct {
	Schema   int             `json:"schema"`
	Batch    string          `json:"batch"`
	Strict   bool            `json:"strict"`
	Status   string          `json:"status"`
	ExitCode int             `json:"exit_code"`
	Tests    []PreflightTest `json:"tests"`
}

type PreflightTest struct {
	Name     string         `json:"name"`
	Scenario string         `json:"scenario"`
	Status   string         `json:"status"`
	ExitCode int            `json:"exit_code"`
	Checks   []doctor.Check `json:"checks,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// Preflight checks every entry, even when an earlier entry fails. It reuses
// the same CLI doctor used for a standalone run and the run's entry bindings.
func Preflight(ctx context.Context, opt Options, strict bool) PreflightResult {
	result := PreflightResult{Schema: Schema, Strict: strict, Status: "passed"}
	if opt.Definition == nil || opt.Invoker == nil {
		result.Status, result.ExitCode = "failed", 10
		return result
	}
	result.Batch = opt.Definition.Name
	result.Tests = make([]PreflightTest, len(opt.Definition.Tests))
	for index, test := range opt.Definition.Tests {
		entry := PreflightTest{Name: testName(test), Scenario: test.Scenario}
		if ctx.Err() != nil {
			entry.Status, entry.ExitCode, entry.Error = "interrupted", 12, ctx.Err().Error()
		} else {
			args := []string{"--runs-dir", opt.RunsDir, "--scenario-root", opt.ScenarioRoot, "doctor", test.Scenario, "--output", "json"}
			args = append(args, entryArgs(test)...)
			if strict {
				args = append(args, "--strict")
			}
			var stdout, stderr bytes.Buffer
			entry.ExitCode = opt.Invoker.Invoke(ctx, &stdout, &stderr, args...)
			var report doctor.Report
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
				if entry.ExitCode == 0 {
					entry.ExitCode = 10
				}
				entry.Error = fmt.Sprintf("doctor did not return a JSON report: %s", bytes.TrimSpace(stderr.Bytes()))
			} else {
				entry.Checks = report.Checks
			}
			if entry.ExitCode == 0 {
				entry.Status = "passed"
			} else {
				entry.Status = "failed"
			}
		}
		result.Tests[index] = entry
		if entry.ExitCode != 0 && result.ExitCode == 0 {
			result.Status, result.ExitCode = "failed", entry.ExitCode
		}
	}
	return result
}
