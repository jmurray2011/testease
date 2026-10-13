package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// externalJudge lets a repository add a judge without rebuilding testease.
// The command receives the record directory as its final argument and writes a
// JSON judge.Result to stdout. It may use TESTEASE_RECORD_DIR instead if that
// is more convenient for a script.
type externalJudge struct {
	Context      context.Context
	NameValue    string
	VersionValue string
	Command      []string
}

func (j externalJudge) Name() string    { return j.NameValue }
func (j externalJudge) Version() string { return j.VersionValue }

func (j externalJudge) Judge(rec *record.Record) (judge.Result, error) {
	if len(j.Command) == 0 {
		return judge.Result{}, fmt.Errorf("external judge %q has no command", j.NameValue)
	}
	ctx := j.Context
	if ctx == nil {
		ctx = context.Background()
	}
	args := append([]string{}, j.Command[1:]...)
	args = append(args, rec.Dir)
	command := exec.CommandContext(ctx, j.Command[0], args...)
	command.Env = append(os.Environ(), "TESTEASE_RECORD_DIR="+rec.Dir)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return judge.Result{}, fmt.Errorf("external judge %s@%s failed: %s", j.NameValue, j.VersionValue, message)
	}
	var result judge.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return judge.Result{}, fmt.Errorf("external judge %s@%s returned invalid JSON: %w", j.NameValue, j.VersionValue, err)
	}
	if strings.TrimSpace(result.Verdict) == "" {
		return judge.Result{}, fmt.Errorf("external judge %s@%s returned no verdict", j.NameValue, j.VersionValue)
	}
	return result, nil
}
