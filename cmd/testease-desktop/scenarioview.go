package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/scenario"
)

func scenarioDir(root, name string) string { return filepath.Join(root, name) }

// scenarioContext is what a scenario screen needs from the window: where
// records and scenarios are, which targets the catalog names, and how to
// start a testease command.
type scenarioContext struct {
	runsDir, root, name string
	targets             []string
	start               func(title string, args []string)
}

// scenarioView is one scenario opened: what it asks, and a form for a run of
// it. The form starts empty every time.
type scenarioView struct {
	ctx      scenarioContext
	manifest *scenario.Manifest

	params  map[string]*widget.Entry
	axes    map[string]bool
	target  *widget.SelectEntry
	keep    *widget.Check
	extra   *widget.Entry
	problem *widget.Label
	content fyne.CanvasObject
}

func newScenarioView(ctx scenarioContext, back func()) (*scenarioView, error) {
	m, err := scenario.Load(scenarioDir(ctx.root, ctx.name))
	if err != nil {
		return nil, err
	}
	v := &scenarioView{ctx: ctx, manifest: m, params: map[string]*widget.Entry{}, axes: map[string]bool{}, problem: message()}

	top := backHeader("Scenarios", back, m.Name, openFolderButton(m.Dir))
	question := widget.NewLabel(strings.TrimSpace(m.Question))
	question.Wrapping = fyne.TextWrapWord

	form := widget.NewForm()
	names := make([]string, 0, len(m.Params))
	for k := range m.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		p := m.Params[k]
		e := widget.NewEntry()
		label := k
		switch {
		case p.Axis:
			v.axes[k] = true
			label = k + " (axis)"
			e.SetPlaceHolder(orText(p.Values, "the scenario's own values") + " — comma-separated to sweep")
		default:
			e.SetPlaceHolder(orText(p.Default, "no default"))
		}
		if looksSecret(k) {
			e.Password = true
		}
		v.params[k] = e
		form.Append(label, e)
	}

	v.target = widget.NewSelectEntry(ctx.targets)
	v.target.SetPlaceHolder("none: the scenario's own hosts")
	form.Append("Target", v.target)
	v.keep = widget.NewCheck("Leave targets standing after a clean run (--keep)", nil)
	form.Append("", v.keep)
	v.extra = widget.NewMultiLineEntry()
	v.extra.SetPlaceHolder("More flags, one a line, e.g.\n--docker-context=remote\n--env-file=C:\\secrets\\hello.env")
	v.extra.SetMinRowsVisible(3)
	form.Append("More flags", v.extra)

	doctor := widget.NewButtonWithIcon("Doctor", theme.SearchIcon(), v.doctor)
	run := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), v.run)
	run.Importance = widget.HighImportance

	v.content = container.NewVBox(
		container.NewPadded(container.NewVBox(top, question)),
		widget.NewSeparator(),
		container.NewPadded(form),
		v.problem,
		container.NewPadded(container.NewHBox(doctor, run)),
		widget.NewSeparator(),
		note("Nothing typed here is kept: params, matrix and flags change what is measured, so each run asks for them again. Flags are passed as typed, with no shell in between."),
	)
	return v, nil
}

func orText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (v *scenarioView) request() runRequest {
	r := runRequest{
		RunsDir: v.ctx.runsDir, ScenarioRoot: v.ctx.root, Scenario: v.ctx.name,
		Params: map[string]string{}, Matrix: map[string]string{},
		Target: v.target.Text, Keep: v.keep.Checked, Extra: v.extra.Text,
	}
	for k, e := range v.params {
		if v.axes[k] {
			r.Matrix[k] = e.Text
		} else {
			r.Params[k] = e.Text
		}
	}
	return r
}

func (v *scenarioView) run() {
	args, err := runArgs(v.request())
	if err != nil {
		showMessage(v.problem, err.Error())
		return
	}
	v.problem.Hide()
	v.ctx.start(fmt.Sprintf("run %s", v.ctx.name), args)
}

func (v *scenarioView) doctor() {
	args, err := doctorArgs(v.request())
	if err != nil {
		showMessage(v.problem, err.Error())
		return
	}
	v.problem.Hide()
	v.ctx.start(fmt.Sprintf("doctor %s", v.ctx.name), args)
}
