package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/record"
)

// outputLimit caps how much of a step's output is put on screen. A step can
// print gigabytes; the tail is where the answer usually is, and the whole file
// is one "Open folder" away.
const outputLimit = 512 << 10

// detailView is one record opened.
type detailView struct {
	d        browse.Detail
	selected int

	steps     *widget.List
	stepTitle *widget.Label
	stepMeta  *widget.Label
	stdout    *widget.TextGrid
	stderr    *widget.TextGrid
	facts     *fyne.Container
	factLines []string
	tabs      *container.AppTabs
	timeline  *timelineChart
	// search looks through the output tab showing; outScroll and errScroll
	// hold stdout and stderr, so a match can be scrolled to.
	search *outputSearch
	// trends follow each fact back through the scenario's earlier runs.
	trends               map[factKey]factTrend
	outScroll, errScroll *container.Scroll

	meta, verdicts, targets, notes []string
	content                        fyne.CanvasObject

	// backLabel names the screen back goes to: the record was opened from
	// the Runs listing, a batch or a campaign.
	backLabel string
	back      func()

	// cells lists a matrix run's cells, and onOpenCell opens one: the steps
	// are in the cells' own records, not the parent's.
	cells      *widget.List
	onOpenCell func(dir string)
	// heat is a matrix run's cells as a grid, and matrix the scope the run
	// recorded.
	heat   *heatmapView
	matrix *record.Matrix

	// judge reads the record again with its judge; render shows it in one
	// of its views; compare asks which stable facts differ from other runs.
	judge, render, compare *action
	// again opens the record's scenario with the form filled from it.
	again *action
	// watch follows a record still being written, step output and all.
	watch *action
}

func newDetailView(dir string, back func()) (*detailView, error) {
	return newDetailViewFrom(dir, "Runs", back)
}

func newDetailViewFrom(dir, backLabel string, back func()) (*detailView, error) {
	d, err := browse.Open(dir)
	if err != nil {
		return nil, err
	}
	v := &detailView{
		d: d, selected: -1, backLabel: backLabel, back: back, judge: newAction("Judge", theme.ConfirmIcon()),
		render: newAction("Render…", theme.DocumentIcon()), compare: newAction("Compare…", theme.ListIcon()),
		again: newAction("Run again…", theme.MediaReplayIcon()), watch: newAction("Watch live", theme.VisibilityIcon()),
	}

	// Header: where this is, what it measured, and how to get at the files.
	status := newPill(statusOf(d.Run), statusKind(statusOf(d.Run)))
	top := backHeader(backLabel, back, d.RunID, container.NewCenter(status), v.watch.Button, v.again.Button, v.judge.Button, v.render.Button, v.compare.Button, openFolderButton(d.Dir))

	v.meta = metaParts(d)
	metaLabel := widget.NewLabel(strings.Join(v.meta, "    "))
	metaLabel.Importance = widget.LowImportance
	metaLabel.Wrapping = fyne.TextWrapWord
	header := container.NewVBox(container.NewPadded(container.NewVBox(top, metaLabel)), widget.NewSeparator())

	// Steps, on the left.
	v.steps = widget.NewList(
		func() int { return len(d.StepList) },
		func() fyne.CanvasObject {
			seq := widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
			seq.Importance = widget.LowImportance
			label := widget.NewLabel("")
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(34, seq.MinSize().Height), seq), container.NewCenter(newPill("", pillNeutral)), label)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			s := d.StepList[i]
			row := o.(*fyne.Container)
			row.Objects[0].(*widget.Label).SetText(s.Label)
			row.Objects[1].(*fyne.Container).Objects[0].(*widget.Label).SetText(fmt.Sprint(s.Seq))
			p := row.Objects[2].(*fyne.Container).Objects[0].(*pill)
			p.Set(stepStatus(s), statusKind(s.Status))
		},
	)
	v.steps.OnSelected = v.selectStep
	stepsHeader := widget.NewLabel(stepsHeading(d))
	stepsHeader.Importance = widget.LowImportance
	var left fyne.CanvasObject = container.NewBorder(stepsHeader, nil, nil, nil, v.steps)
	if len(d.CellList) > 0 && len(d.StepList) == 0 {
		left = v.cellList(d)
	}

	// The selected step, in the middle.
	v.stepTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.stepTitle.Truncation = fyne.TextTruncateEllipsis
	v.stepMeta = widget.NewLabel("")
	v.stepMeta.Truncation = fyne.TextTruncateEllipsis
	v.stepMeta.Importance = widget.LowImportance
	// A TextGrid asks for the width of its longest line, and a drive step's
	// output can carry lines thousands of characters long. Each one sits in a
	// scroll of its own so output scrolls rather than widening the window.
	v.stdout = widget.NewTextGrid()
	v.stdout.Scroll = fyne.ScrollNone
	v.stderr = widget.NewTextGrid()
	v.stderr.Scroll = fyne.ScrollNone
	v.facts = container.NewVBox()
	v.outScroll = container.NewScroll(container.NewPadded(v.stdout))
	v.errScroll = container.NewScroll(container.NewPadded(v.stderr))
	v.tabs = container.NewAppTabs(
		container.NewTabItem("stdout", v.outScroll),
		container.NewTabItem("stderr", v.errScroll),
		container.NewTabItem("Facts", container.NewVScroll(container.NewPadded(v.facts))),
	)
	// The whole run against its own clock, from the record's event times.
	var matrix *record.Matrix
	if rec, err := record.Read(d.Dir); err == nil {
		for _, e := range rec.Latest().Events {
			if e.Kind == record.KindRunStarted {
				if started, err := record.Decode[record.RunStarted](e); err == nil {
					matrix, v.matrix = started.Matrix, started.Matrix
				}
				break
			}
		}
		if spans, total := timeline(rec.Events); len(spans) > 0 {
			v.timeline = newTimelineChart(spans, total, v.openStep)
			v.tabs.Append(container.NewTabItem("Timeline", container.NewVScroll(container.NewPadded(v.timeline))))
		}
	}
	v.trends = factTrends(d, earlierRuns(d))
	v.search = newOutputSearch(v.shownOutput)
	v.tabs.OnSelected = func(*container.TabItem) { v.search.apply() }
	var center fyne.CanvasObject = container.NewBorder(container.NewVBox(v.stepTitle, v.stepMeta, v.search.bar, widget.NewSeparator()), nil, nil, nil, v.tabs)

	// What judges, targets and readers said, on the right.
	v.verdicts = verdictParts(d)
	// A cell judged as part of its matrix run has its verdict in the run.
	if extra := cellVerdictLines(d.Dir); len(d.Verdicts) == 0 && len(extra) > 0 {
		v.verdicts = extra
	}
	v.targets = targetParts(d)
	v.notes = noteParts(d)
	right := container.NewVScroll(container.NewPadded(container.NewVBox(
		section("Verdicts", v.verdictsShown(d)),
		section("Target", lines(v.targets)),
		section("Read notes", lines(v.notes)),
	)))

	switch {
	case len(d.CellList) > 0 && len(d.StepList) == 0:
		v.stepTitle.SetText(fmt.Sprintf("This matrix run measured in %s. Pick one to open its record.", plural(len(d.CellList), "cell")))
		// The cells as a grid, by the axes the run recorded.
		v.heat = newHeatmapView(d, matrix, func(dir string) {
			if v.onOpenCell != nil {
				v.onOpenCell(dir)
			}
		})
		center = container.NewBorder(container.NewVBox(v.stepTitle, widget.NewSeparator()), nil, nil, nil, v.heat.content)
	case len(d.StepList) == 0:
		v.stepTitle.SetText("This record has no steps.")
	default:
		v.steps.Select(firstOfInterest(d.StepList))
	}

	inner := container.NewHSplit(center, right)
	inner.Offset = 0.7
	outer := container.NewHSplit(left, inner)
	outer.Offset = 0.3
	if v.heat != nil {
		// The grid is the matrix run's answer; the list beside it only
		// names the cells.
		outer.Offset = 0.2
	}
	v.content = container.NewBorder(header, nil, nil, nil, outer)
	return v, nil
}

// firstOfInterest is the step a person opening a record wants first: the first
// one that did not measure cleanly, or the first step when all of them did.
// cellList is a matrix run's cells, on the left where a run's steps go.
func (v *detailView) cellList(d browse.Detail) fyne.CanvasObject {
	v.cells = widget.NewList(
		func() int { return len(d.CellList) },
		func() fyne.CanvasObject {
			label := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, nil, container.NewCenter(newPill("", pillNeutral)), label)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			c := d.CellList[i]
			row := o.(*fyne.Container)
			row.Objects[0].(*widget.Label).SetText(c.Key)
			status := c.Status
			if status == "" {
				status = "no outcome"
			}
			row.Objects[1].(*fyne.Container).Objects[0].(*pill).Set(status, statusKind(c.Status))
		},
	)
	v.cells.OnSelected = func(i widget.ListItemID) {
		v.cells.UnselectAll()
		if i < 0 || i >= len(d.CellList) || v.onOpenCell == nil {
			return
		}
		// A cell's key names its folder under the parent; one that would
		// leave the record is not followed.
		if rel, err := filepath.Rel(d.Dir, d.CellList[i].Dir); err == nil && filepath.IsLocal(rel) {
			v.onOpenCell(d.CellList[i].Dir)
		}
	}
	heading := widget.NewLabel("Cells")
	heading.Importance = widget.LowImportance
	return container.NewBorder(heading, nil, nil, nil, v.cells)
}

// openFolderButton opens a folder in the OS's file manager.
func openFolderButton(dir string) *widget.Button {
	return widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
		if u, err := url.Parse(storage.NewFileURI(dir).String()); err == nil {
			_ = fyne.CurrentApp().OpenURL(u)
		}
	})
}

func firstOfInterest(steps []browse.Step) int {
	for i, s := range steps {
		if s.Status != string(record.StatusMeasured) {
			return i
		}
	}
	return 0
}

func stepStatus(s browse.Step) string {
	if s.Status == "" {
		return "no outcome"
	}
	return s.Status
}

// shownOutput is the output tab showing, if one is.
func (v *detailView) shownOutput() (*widget.TextGrid, *container.Scroll) {
	switch v.tabs.SelectedIndex() {
	case 0:
		return v.stdout, v.outScroll
	case 1:
		return v.stderr, v.errScroll
	}
	return nil, nil
}

// openStep shows the step a timeline row names, with its output.
func (v *detailView) openStep(seq int) {
	for i, s := range v.d.StepList {
		if s.Seq == seq {
			v.steps.Select(i)
			v.tabs.SelectIndex(0)
			return
		}
	}
}

func (v *detailView) selectStep(i widget.ListItemID) {
	if i < 0 || i >= len(v.d.StepList) {
		return
	}
	v.selected = i
	s := v.d.StepList[i]
	if v.timeline != nil {
		v.timeline.Select(s.Seq)
	}
	v.stepTitle.SetText(fmt.Sprintf("%d · %s", s.Seq, s.Label))

	var parts []string
	if s.RC != nil {
		parts = append(parts, fmt.Sprintf("rc %d", *s.RC), fmt.Sprintf("%.1f s", s.DurationS))
	} else {
		parts = append(parts, "no outcome recorded")
	}
	parts = append(parts, "host "+s.Host)
	if s.Probe != "" {
		parts = append(parts, s.Probe)
	}
	v.stepMeta.SetText(strings.Join(parts, " · "))

	v.stdout.SetText(v.output(s.Stdout, "stdout"))
	v.stderr.SetText(v.output(s.Stderr, "stderr"))
	if v.search != nil {
		v.search.apply()
	}

	v.factLines = v.factLines[:0]
	v.facts.RemoveAll()
	if len(s.Facts) == 0 {
		v.facts.Add(widget.NewLabel("This step recorded no facts."))
	}
	for _, f := range s.Facts {
		value := ""
		switch {
		case f.Value != nil:
			value = shown(f.Key, *f.Value)
		case f.Error != "":
			value = "no reading: " + f.Error
		default:
			value = "no reading"
		}
		v.factLines = append(v.factLines, f.Key+" "+value)
		key := widget.NewLabelWithStyle(f.Key, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		key.Importance = widget.LowImportance
		val := widget.NewLabelWithStyle(value, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		val.Selectable = true
		val.Wrapping = fyne.TextWrapWord
		// The name and its trend on one line, the value under them across the
		// whole width, so a long name or value never crowds the other.
		var trend fyne.CanvasObject = widget.NewLabel("")
		if tr, ok := v.trends[factKey{s.Label, f.Key}]; ok && !looksSecret(f.Key) {
			trend = trendView(f.Key, tr)
		}
		key.Wrapping = fyne.TextWrapBreak
		v.facts.Add(container.NewVBox(
			container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(trendColumn, sparkHeight+12), trend), key),
			val,
		))
		v.facts.Add(widget.NewSeparator())
	}
	v.facts.Refresh()
}

// output reads one of a step's output files for display. Records travel - they
// are published and read on machines that did not write them - so a path that
// leaves the record is not followed, whatever the record says.
func (v *detailView) output(rel, stream string) string {
	if rel == "" {
		return fmt.Sprintf("(this step recorded no %s)", stream)
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return fmt.Sprintf("(the record names %s as %q, which is outside the record; it is not followed)", stream, rel)
	}
	path := filepath.Join(v.d.Dir, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Sprintf("(the record names %s, but that file is not in the record)", rel)
	case err != nil:
		return fmt.Sprintf("(could not read %s: %v)", rel, err)
	case info.Size() == 0:
		return "(empty)"
	}
	return readTail(path, outputLimit)
}

// readTail reads the end of a file, from the first whole line inside the last
// limit bytes, and says when it has left the start out.
func readTail(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("(could not read: %v)", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Sprintf("(could not read: %v)", err)
	}
	size := info.Size()
	if size <= limit {
		body, err := io.ReadAll(f)
		if err != nil {
			return fmt.Sprintf("(could not read: %v)", err)
		}
		return string(body)
	}
	if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
		return fmt.Sprintf("(could not read: %v)", err)
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("(could not read: %v)", err)
	}
	if nl := strings.IndexByte(string(body), '\n'); nl >= 0 && nl+1 < len(body) {
		body = body[nl+1:]
	}
	return fmt.Sprintf("… showing the last %s of %s; the whole file is in the record\n%s",
		byteSize(int64(len(body))), byteSize(size), body)
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func stepsHeading(d browse.Detail) string {
	hosts := map[string]bool{}
	for _, s := range d.StepList {
		hosts[s.Host] = true
	}
	if len(hosts) == 1 {
		for h := range hosts {
			return "Steps on host " + h
		}
	}
	return "Steps"
}

func metaParts(d browse.Detail) []string {
	parts := []string{"Scenario " + orDash(d.Scenario)}
	if len(d.Params) > 0 {
		keys := make([]string, 0, len(d.Params))
		for k := range d.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		params := make([]string, len(keys))
		for i, k := range keys {
			params[i] = k + "=" + shown(k, d.Params[k])
		}
		parts = append(parts, "Params "+strings.Join(params, " "))
	}
	if d.Runner.User != "" || d.Runner.Hostname != "" {
		parts = append(parts, "Runner "+d.Runner.User+"@"+d.Runner.Hostname)
	}
	if d.Tool.Version != "" {
		parts = append(parts, "Tool "+d.Tool.Version)
	}
	parts = append(parts, "Events "+shortHash(d.EventsHash))
	return parts
}

func shortHash(h string) string {
	const prefix = "sha256:"
	if !strings.HasPrefix(h, prefix) || len(h) < len(prefix)+12 {
		return orDash(h)
	}
	digest := h[len(prefix):]
	return prefix + digest[:4] + "…" + digest[len(digest)-4:]
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func verdictParts(d browse.Detail) []string {
	if len(d.Verdicts) == 0 {
		return []string{"Not judged yet. A run has no verdict until a judge reads it."}
	}
	var out []string
	for _, v := range d.Verdicts {
		out = append(out, verdictName(v)+" "+v.Verdict+" "+verdictProvenance(v))
	}
	return out
}

func verdictProvenance(v browse.Verdict) string {
	when := v.JudgedAt.UTC().Format("2 Jan 2006 15:04 UTC")
	if v.Current {
		return "Judged " + when + " against these events."
	}
	return "Judged " + when + " over earlier events; the record's events have changed since, so this is not its verdict."
}

// verdictsShown is the Verdicts section: the record's own, or, for a cell
// with none, what its matrix run's judge said of it.
func (v *detailView) verdictsShown(d browse.Detail) fyne.CanvasObject {
	if len(d.Verdicts) == 0 {
		return lines(v.verdicts)
	}
	return verdictCards(d)
}

func verdictCards(d browse.Detail) fyne.CanvasObject {
	if len(d.Verdicts) == 0 {
		return lines(verdictParts(d))
	}
	box := container.NewVBox()
	for _, v := range d.Verdicts {
		name := widget.NewLabelWithStyle(verdictName(v), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		when := widget.NewLabel(verdictProvenance(v))
		when.Importance = widget.LowImportance
		when.Wrapping = fyne.TextWrapWord
		box.Add(container.NewVBox(
			container.NewBorder(nil, nil, name, container.NewCenter(newPill(v.Verdict, verdictKind(v)))),
			when,
		))
	}
	return box
}

func targetParts(d browse.Detail) []string {
	fates := map[string]string{}
	for _, t := range d.Targets {
		fates[t.Host] = t.Fate
	}
	var out []string
	for _, h := range d.Hosts {
		ref := ""
		if r, ok := h.Resolved["ref"]; ok {
			ref = fmt.Sprint(r)
		}
		out = append(out, strings.TrimSpace(h.Host+" "+ref)+" · "+fateText(fates[h.Host]))
	}
	switch {
	case len(out) == 0 && len(d.CellList) > 0:
		return []string{"Targets are resolved in each cell. Open a cell to see its targets and what became of them."}
	case len(out) == 0:
		return []string{"No target was resolved."}
	}
	return out
}

func noteParts(d browse.Detail) []string {
	var out []string
	for _, r := range d.Reports {
		out = append(out, r.About+": "+r.Message)
	}
	for _, w := range d.Warnings {
		out = append(out, w.About+": "+w.Message+" (recorded "+fmt.Sprintf("%q", w.Recorded)+")")
	}
	if len(out) == 0 {
		return []string{"None. The record read cleanly; no repairs were needed."}
	}
	return out
}

func section(title string, body fyne.CanvasObject) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewVBox(heading, body, widget.NewSeparator())
}

func lines(texts []string) fyne.CanvasObject {
	box := container.NewVBox()
	for _, t := range texts {
		l := widget.NewLabel(t)
		l.Wrapping = fyne.TextWrapWord
		l.Selectable = true
		box.Add(l)
	}
	return box
}

func (v *detailView) metaText() string     { return strings.Join(v.meta, "\n") }
func (v *detailView) verdictsText() string { return strings.Join(v.verdicts, "\n") }
func (v *detailView) targetsText() string  { return strings.Join(v.targets, "\n") }
func (v *detailView) notesText() string    { return strings.Join(v.notes, "\n") }
func (v *detailView) factsText() string    { return strings.Join(v.factLines, "\n") }
