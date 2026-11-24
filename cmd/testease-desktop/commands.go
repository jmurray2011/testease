package main

import (
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
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

// flagsWithValue are the flags this app puts before a command's own
// arguments that take the next argument.
var flagsWithValue = map[string]bool{
	"--runs-dir": true, "--scenario-root": true, "--log-level": true, "--log-format": true, "--campaign-root": true,
}

// commandWords is the command args name, like [run hello] or [batch run
// smoke.yaml]: the arguments that are not those flags or their values.
func commandWords(args []string) []string {
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !strings.HasPrefix(a, "-"):
			words = append(words, a)
		case flagsWithValue[a]:
			i++
		}
	}
	return words
}

// command is the testease command args name.
func command(args []string) string {
	if w := commandWords(args); len(w) > 0 {
		return w[0]
	}
	return ""
}

// startsTargets is whether the command stands targets up: a run, or a batch
// or campaign of runs. A judge or doctor starts none.
func startsTargets(args []string) bool {
	w := commandWords(args)
	switch {
	case len(w) > 0 && w[0] == "run":
		return true
	case len(w) > 1 && (w[0] == "batch" || w[0] == "campaign") && w[1] == "run":
		return true
	}
	return false
}

// pickedPath is a path a dialog gave, written the platform's way.
func pickedPath(p string) string { return filepath.FromSlash(p) }

// Where each picker opens: the folder it was last used in. This is only
// where to browse from; it changes nothing that is measured.
const (
	prefBatchFolder    = "batch-folder"
	prefCampaignFolder = "campaign-folder"
)

// rememberFolder keeps the folder holding what was picked: a batch file's
// folder, or the folder a campaign's folder sits in.
func rememberFolder(key, picked string) {
	fyne.CurrentApp().Preferences().SetString(key, filepath.Dir(picked))
}

func lastFolder(key string) fyne.ListableURI {
	dir := fyne.CurrentApp().Preferences().String(key)
	if dir == "" {
		return nil
	}
	l, err := storage.ListerForURI(storage.NewFileURI(dir))
	if err != nil {
		return nil
	}
	return l
}

// overridesValid refuses, on the form, a line that is not a flag.
func overridesValid(text string) error {
	_, err := runRequest{Extra: text}.extra()
	return err
}
