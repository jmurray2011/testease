package main

import (
	"path/filepath"
	"strings"
)

// judgeArgs reads a saved record again. With no --judge, testease uses the
// judge the record names, and judging alone never moves a baseline.
func judgeArgs(runsDir, record string) []string {
	return []string{"--runs-dir", runsDir, "judge", record}
}

func withScenarioRoot(runsDir, scenarioRoot string) []string {
	args := []string{"--runs-dir", runsDir}
	if scenarioRoot != "" {
		args = append(args, "--scenario-root", scenarioRoot)
	}
	return args
}

// batchRunArgs runs every scenario a batch file lists, up to its own limit.
func batchRunArgs(runsDir, scenarioRoot, file string) []string {
	return append(withScenarioRoot(runsDir, scenarioRoot), "batch", "run", file)
}

// batchJudgeArgs judges every measured run of a batch and saves the verdicts.
func batchJudgeArgs(runsDir, id string) []string {
	return []string{"--runs-dir", runsDir, "batch", "judge", id}
}

// campaignRunArgs runs the campaign in dir: its parent is the campaign root
// and its folder name the campaign. extra is more flags, one a line, as on
// a scenario's run form.
func campaignRunArgs(runsDir, scenarioRoot, dir, extra string) ([]string, error) {
	more, err := runRequest{Extra: extra}.extra()
	if err != nil {
		return nil, err
	}
	args := append(withScenarioRoot(runsDir, scenarioRoot),
		"campaign", "--campaign-root", filepath.Dir(dir), "run", filepath.Base(dir))
	return append(args, more...), nil
}

// globalsWithValue are testease's global flags that take the next argument.
var globalsWithValue = map[string]bool{"--runs-dir": true, "--scenario-root": true, "--log-level": true, "--log-format": true}

// command is the testease command args name: the first argument that is not
// a global flag or a global flag's value.
func command(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a
		}
		if globalsWithValue[a] {
			i++
		}
	}
	return ""
}
