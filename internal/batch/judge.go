package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Judgment is the derived verdict over a batch's child run records.
// It lives beside batch.json so judging never changes the measurement.
type Judgment struct {
	Schema   int            `json:"schema"`
	BatchID  string         `json:"batch_id"`
	Status   string         `json:"status"`
	ExitCode int            `json:"exit_code"`
	Tests    []TestJudgment `json:"tests"`
}

type TestJudgment struct {
	Name     string `json:"name"`
	Scenario string `json:"scenario"`
	RunID    string `json:"run_id,omitempty"`
	Status   string `json:"status"`
	Judge    string `json:"judge,omitempty"`
	Version  string `json:"version,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
	Log      string `json:"log,omitempty"`
}

func batchDir(runsDir, id string) (string, error) {
	if !safeName.MatchString(id) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid batch id %q", id)
	}
	return filepath.Join(runsDir, "batches", id), nil
}

func LoadResult(runsDir, id string) (Result, error) {
	dir, err := batchDir(runsDir, id)
	if err != nil {
		return Result{}, err
	}
	body, err := os.ReadFile(filepath.Join(dir, "batch.json"))
	if err != nil {
		return Result{}, err
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return Result{}, err
	}
	if result.ID != id {
		return Result{}, fmt.Errorf("batch record %s names %q", dir, result.ID)
	}
	return result, nil
}

func LoadJudgment(runsDir, id string) (Judgment, error) {
	dir, err := batchDir(runsDir, id)
	if err != nil {
		return Judgment{}, err
	}
	body, err := os.ReadFile(filepath.Join(dir, "judgment.json"))
	if err != nil {
		return Judgment{}, err
	}
	var result Judgment
	if err := json.Unmarshal(body, &result); err != nil {
		return Judgment{}, err
	}
	if result.BatchID != id {
		return Judgment{}, fmt.Errorf("batch judgment %s names %q", dir, result.BatchID)
	}
	return result, nil
}

// Judge invokes the normal judge command for each measured child, then saves
// their verdicts as a derived batch summary. A failed child does not prevent
// the remaining measured children from being judged.
func Judge(ctx context.Context, runsDir, id string, invoker Invoker) (Judgment, error) {
	if invoker == nil {
		return Judgment{}, fmt.Errorf("batch judge needs an invoker")
	}
	measurement, err := LoadResult(runsDir, id)
	if err != nil {
		return Judgment{}, err
	}
	if measurement.Status == "running" {
		return Judgment{}, fmt.Errorf("batch %s is still running", id)
	}
	for _, test := range measurement.Tests {
		name := test.Name
		if name == "" {
			name = test.Scenario
		}
		if !safeName.MatchString(name) || name == "." || name == ".." || test.RunID != "" && (!safeName.MatchString(test.RunID) || test.RunID == "." || test.RunID == "..") {
			return Judgment{}, fmt.Errorf("batch %s has unsafe saved child name or run id", id)
		}
	}
	dir, err := batchDir(runsDir, id)
	if err != nil {
		return Judgment{}, err
	}
	result := Judgment{Schema: Schema, BatchID: id, Status: "passed", Tests: make([]TestJudgment, len(measurement.Tests))}
	for index, test := range measurement.Tests {
		entry := TestJudgment{Name: test.Name, Scenario: test.Scenario, RunID: test.RunID}
		if entry.Name == "" {
			entry.Name = test.Scenario
		}
		if test.Status != "measured" || test.RunID == "" {
			entry.Status, entry.ExitCode = "unjudgeable", 21
			entry.Error = fmt.Sprintf("child run status is %s; no measured record to judge", test.Status)
		} else if ctx.Err() != nil {
			entry.Status, entry.ExitCode = "interrupted", 12
			entry.Error = ctx.Err().Error()
		} else {
			entry = judgeChild(ctx, runsDir, dir, entry, invoker)
		}
		result.Tests[index] = entry
		if entry.ExitCode != 0 && result.ExitCode == 0 {
			result.Status, result.ExitCode = "failed", entry.ExitCode
		}
	}
	if err := saveJudgment(dir, result); err != nil {
		return result, err
	}
	return result, nil
}

func judgeChild(ctx context.Context, runsDir, dir string, entry TestJudgment, invoker Invoker) TestJudgment {
	entry.Log = entry.Name + ".judge.log"
	log, err := os.Create(filepath.Join(dir, entry.Log))
	if err != nil {
		entry.Status, entry.ExitCode, entry.Error = "unjudgeable", 10, err.Error()
		return entry
	}
	defer func() { _ = log.Close() }()
	var stdout bytes.Buffer
	entry.ExitCode = invoker.Invoke(ctx, &stdout, log, "--runs-dir", runsDir, "judge", entry.RunID, "--output", "json")
	_, _ = log.Write(stdout.Bytes())
	var response struct {
		Judge   string `json:"judge"`
		Version string `json:"version"`
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err != nil || response.Judge == "" || response.Version == "" || response.Verdict == "" {
		entry.Status = "unjudgeable"
		if entry.ExitCode == 0 || entry.ExitCode == 20 {
			entry.ExitCode = 10
		}
		entry.Error = "judge did not return a complete JSON verdict"
		return entry
	}
	entry.Judge, entry.Version, entry.Verdict = response.Judge, response.Version, response.Verdict
	switch entry.ExitCode {
	case 0:
		entry.Status = "passed"
	case 20:
		entry.Status = "failed"
	default:
		entry.Status = "unjudgeable"
	}
	return entry
}

func saveJudgment(dir string, result Judgment) error {
	file, err := os.CreateTemp(dir, ".judgment-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "judgment.json"))
}
