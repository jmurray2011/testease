package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/scenario"
)

// runRequest is what a person asked a run or a doctor for. It lives as long
// as the form it was typed into: nothing in it is remembered, because what
// changes a measurement is typed each time, as on the command line.
type runRequest struct {
	RunsDir, ScenarioRoot, Scenario string
	// Params are k=v; Matrix is k=v1,v2 for an axis. An empty value is the
	// scenario's own default and is not passed.
	Params map[string]string
	Matrix map[string]string
	Target string
	Keep   bool
	// Extra is more flags, one a line, passed as typed.
	Extra string
}

func (r runRequest) common(verb string) ([]string, error) {
	args := []string{"--runs-dir", r.RunsDir, "--scenario-root", r.ScenarioRoot, verb, r.Scenario}
	if verb == "run" {
		args = append(args, "--output", "json", "--progress", "json")
	}
	for _, k := range sortedKeys(r.Params) {
		if v := strings.TrimSpace(r.Params[k]); v != "" {
			args = append(args, "--param", k+"="+v)
		}
	}
	for _, k := range sortedKeys(r.Matrix) {
		if v := strings.TrimSpace(r.Matrix[k]); v != "" {
			args = append(args, "--matrix", k+"="+v)
		}
	}
	if t := strings.TrimSpace(r.Target); t != "" {
		args = append(args, "--target", t)
	}
	return args, nil
}

// extra is the extra flags, one a line. There is no shell: a line is one
// argument exactly as typed, and one that is not a flag is refused rather
// than guessed at.
func (r runRequest) extra() ([]string, error) {
	var out []string
	for _, line := range strings.Split(r.Extra, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "-") {
			return nil, fmt.Errorf("%q is not a flag: put one flag a line, like --docker-context=remote", line)
		}
		out = append(out, line)
	}
	return out, nil
}

func runArgs(r runRequest) ([]string, error) {
	args, err := r.common("run")
	if err != nil {
		return nil, err
	}
	if r.Keep {
		args = append(args, "--keep")
	}
	extra, err := r.extra()
	if err != nil {
		return nil, err
	}
	return append(args, extra...), nil
}

// doctorArgs asks doctor about the run the form describes. --keep is a run's
// alone, and doctor reports in text.
func doctorArgs(r runRequest) ([]string, error) {
	args, err := r.common("doctor")
	if err != nil {
		return nil, err
	}
	extra, err := r.extra()
	if err != nil {
		return nil, err
	}
	return append(args, extra...), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// The Scenarios screen's columns.
const (
	scolName = iota
	scolQuestion
	scolParams
)

var (
	scenarioTitles = []string{"Scenario", "Question", "Params"}
	scenarioWidths = []float32{260, 620, 80}
)

// scenarioRow is one scenario under the root, loaded far enough to list.
type scenarioRow struct {
	Name     string
	Question string
	Params   int
	Problem  string
}

// scenariosView lists the scenarios under the scenario root.
type scenariosView struct {
	root, from string
	onOpen     func(name string)
	onChoose   func()

	all     []scenarioRow
	count   *widget.Label
	where   *widget.Label
	message *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
}

func newScenariosView(root, from string, onOpen func(string)) *scenariosView {
	v := &scenariosView{root: root, from: from, onOpen: onOpen, count: widget.NewLabel(""), message: message()}
	v.table = listing(func() int { return len(v.all) }, scenarioTitles, scenarioWidths, scolName, v.cell,
		func(row int) { v.onOpen(v.all[row].Name) })
	v.where = note("")
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), v.reload)
	choose := widget.NewButtonWithIcon("Choose…", theme.FolderOpenIcon(), func() {
		if v.onChoose != nil {
			v.onChoose()
		}
	})
	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Scenarios", v.count, choose, refresh), v.where, widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), note("Pick a scenario to check it with doctor or start a run. A run is testease run, called with the flags you give it.")),
		nil, nil, v.table,
	)
	v.reload()
	return v
}

func (v *scenariosView) reload() {
	v.all = nil
	if v.root == "" {
		v.where.SetText("")
		showMessage(v.message, "Choose the scenario root: the folder whose subfolders each hold a scenario.yaml (--scenario-root, TESTEASE_SCENARIO_ROOT).")
	} else {
		v.where.SetText("From " + v.root + " (" + v.from + ")")
		names, err := scenario.Names(v.root)
		switch {
		case errors.Is(err, os.ErrNotExist):
			showMessage(v.message, "No scenario root at "+v.root)
		case err != nil:
			showMessage(v.message, fmt.Sprintf("Could not read %s: %v", v.root, err))
		case len(names) == 0:
			showMessage(v.message, "No scenarios under "+v.root+". Each one is a folder with a scenario.yaml in it.")
		default:
			v.message.Hide()
			for _, n := range names {
				v.all = append(v.all, loadRow(v.root, n))
			}
		}
	}
	v.count.SetText(plural(len(v.all), "scenario"))
	v.table.Refresh()
}

func loadRow(root, name string) scenarioRow {
	m, err := scenario.Load(scenarioDir(root, name))
	if err != nil {
		return scenarioRow{Name: name, Problem: err.Error()}
	}
	return scenarioRow{Name: name, Question: oneLine(m.Question), Params: len(m.Params)}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

func (v *scenariosView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.all) {
		return "", pillNeutral, false
	}
	s := v.all[row]
	switch col {
	case scolName:
		return s.Name, pillNeutral, false
	case scolQuestion:
		if s.Problem != "" {
			return "will not load: " + s.Problem, pillBad, false
		}
		return s.Question, pillNeutral, false
	case scolParams:
		if s.Problem != "" {
			return "—", pillNeutral, false
		}
		return fmt.Sprint(s.Params), pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *scenariosView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}
