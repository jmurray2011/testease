package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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
	window              fyne.Window
	// backLabel names where back goes: Scenarios, or the record a run again
	// came from.
	backLabel string
}

// axisPick is an axis the scenario declares values for: each value a box,
// and a line for values it does not list.
type axisPick struct {
	declared []string
	checks   *widget.CheckGroup
	other    *widget.Entry
}

// value is the axis as --matrix takes it: empty for the scenario's own
// values, all of them, as declared.
func (a *axisPick) value() (string, bool) {
	var picked []string
	for _, d := range a.declared {
		if slices.Contains(a.checks.Selected, d) {
			picked = append(picked, d)
		}
	}
	extra := splitList(a.other.Text)
	if len(picked) == len(a.declared) && len(extra) == 0 {
		return "", true
	}
	all := append(picked, extra...)
	return strings.Join(all, ","), len(all) > 0
}

// set picks an axis's values back out of a --matrix value.
func (a *axisPick) set(value string) {
	values := splitList(value)
	if len(values) == 0 {
		a.checks.SetSelected(a.declared)
		a.other.SetText("")
		return
	}
	var picked, extra []string
	for _, v := range values {
		if slices.Contains(a.declared, v) {
			picked = append(picked, v)
		} else {
			extra = append(extra, v)
		}
	}
	a.checks.SetSelected(picked)
	a.other.SetText(strings.Join(extra, ","))
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// scenarioView is one scenario opened: what it asks, and a form for a run of
// it. The form starts empty every time; a preset or a record fills it only
// when a person asks.
type scenarioView struct {
	ctx      scenarioContext
	manifest *scenario.Manifest

	params  map[string]*widget.Entry
	axes    map[string]bool
	picks   map[string]*axisPick
	target  *widget.SelectEntry
	keep    *widget.Check
	extra   *widget.Entry
	preset  *widget.Select
	filled  *widget.Label
	problem *widget.Label
	content fyne.CanvasObject
}

func newScenarioView(ctx scenarioContext, back func()) (*scenarioView, error) {
	m, err := scenario.Load(scenarioDir(ctx.root, ctx.name))
	if err != nil {
		return nil, err
	}
	v := &scenarioView{
		ctx: ctx, manifest: m, params: map[string]*widget.Entry{}, axes: map[string]bool{},
		picks: map[string]*axisPick{}, problem: message(), filled: message(),
	}

	top := backHeader(orText(ctx.backLabel, "Scenarios"), back, m.Name, openFolderButton(m.Dir))
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
		// An axis that lists its values is picked from them.
		if declared := splitList(p.Values); p.Axis && !strings.HasPrefix(p.Values, "@") && len(declared) > 0 {
			pick := &axisPick{declared: declared, checks: widget.NewCheckGroup(declared, nil), other: widget.NewEntry()}
			pick.checks.Horizontal = true
			pick.checks.SetSelected(declared)
			pick.other.SetPlaceHolder("other values, comma-separated")
			v.picks[k] = pick
			form.Append(k+" (axis)", container.NewVBox(pick.checks, pick.other))
			continue
		}
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

	// Presets are named on purpose and fill the form only when picked.
	v.preset = widget.NewSelect(nil, func(name string) {
		if r, ok := loadPresets(m.Name)[name]; ok {
			v.fill(r.request())
			v.say(v.filled, "Filled from the preset "+name+". Secrets are never kept in a preset; type them again.")
		}
	})
	v.preset.PlaceHolder = "Fill from a preset…"
	v.refreshPresets()
	saveAs := widget.NewButtonWithIcon("Save as preset…", theme.DocumentSaveIcon(), v.askPresetName)
	remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		if v.preset.Selected != "" {
			v.deletePreset(v.preset.Selected)
		}
	})
	presets := container.NewBorder(nil, nil, nil, container.NewHBox(saveAs, remove), v.preset)

	doctor := widget.NewButtonWithIcon("Doctor", theme.SearchIcon(), v.doctor)
	run := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), v.run)
	run.Importance = widget.HighImportance

	v.content = container.NewVBox(
		container.NewPadded(container.NewVBox(top, question)),
		widget.NewSeparator(),
		container.NewPadded(presets),
		v.filled,
		container.NewPadded(form),
		v.problem,
		container.NewPadded(container.NewHBox(doctor, run)),
		widget.NewSeparator(),
		note("Nothing typed here is kept unless you save it as a preset: params, matrix and flags change what is measured, so each run asks for them. Flags are passed as typed, with no shell in between."),
	)
	return v, nil
}

func orText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// request is the run the form describes. An axis picked down to no values
// is refused: a sweep over nothing measures nothing.
func (v *scenarioView) request() (runRequest, error) {
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
	for k, a := range v.picks {
		value, ok := a.value()
		if !ok {
			return r, fmt.Errorf("pick at least one value of %s to sweep", k)
		}
		r.Matrix[k] = value
	}
	return r, nil
}

// fill puts a run's params, axes and flags in the form.
func (v *scenarioView) fill(r runRequest) {
	for k, e := range v.params {
		if v.axes[k] {
			e.SetText(r.Matrix[k])
		} else {
			e.SetText(r.Params[k])
		}
	}
	for k, a := range v.picks {
		value := r.Matrix[k]
		if value == "" {
			value = r.Params[k]
		}
		a.set(value)
	}
	v.target.SetText(r.Target)
	v.keep.SetChecked(r.Keep)
	v.extra.SetText(r.Extra)
}

func (v *scenarioView) run() {
	r, err := v.request()
	if err == nil {
		var args []string
		if args, err = runArgs(r); err == nil {
			v.problem.Hide()
			v.ctx.start(fmt.Sprintf("run %s", v.ctx.name), args)
			return
		}
	}
	v.say(v.problem, err.Error())
}

func (v *scenarioView) doctor() {
	r, err := v.request()
	if err == nil {
		var args []string
		if args, err = doctorArgs(r); err == nil {
			v.problem.Hide()
			v.ctx.start(fmt.Sprintf("doctor %s", v.ctx.name), args)
			return
		}
	}
	v.say(v.problem, err.Error())
}

func (v *scenarioView) askPresetName() {
	if v.ctx.window == nil {
		return
	}
	name := widget.NewEntry()
	name.SetPlaceHolder("e.g. nightly on 7.3")
	dialog.ShowForm("Save as preset", "Save", "Cancel", []*widget.FormItem{
		{Text: "Name", Widget: name, HintText: "Saving under a name already used replaces it. Secrets are not saved."},
	}, func(ok bool) {
		if ok && strings.TrimSpace(name.Text) != "" {
			v.savePreset(strings.TrimSpace(name.Text))
		}
	}, v.ctx.window)
}

// savePreset keeps the form under a name, secrets left out.
func (v *scenarioView) savePreset(name string) {
	r, err := v.request()
	if err != nil {
		v.say(v.problem, err.Error())
		return
	}
	all := loadPresets(v.manifest.Name)
	all[name] = presetOf(r)
	savePresets(v.manifest.Name, all)
	v.refreshPresets()
	v.say(v.filled, "Saved as the preset "+name+".")
}

func (v *scenarioView) deletePreset(name string) {
	all := loadPresets(v.manifest.Name)
	delete(all, name)
	savePresets(v.manifest.Name, all)
	v.preset.ClearSelected()
	v.refreshPresets()
}

func (v *scenarioView) refreshPresets() {
	names := make([]string, 0)
	for n := range loadPresets(v.manifest.Name) {
		names = append(names, n)
	}
	sort.Strings(names)
	v.preset.SetOptions(names)
}

// say shows a line on the form and lays the form out again, so the line
// takes its own room rather than drawing over what was there.
func (v *scenarioView) say(l *widget.Label, text string) {
	showMessage(l, text)
	if c, ok := v.content.(*fyne.Container); ok {
		c.Refresh()
	}
}
