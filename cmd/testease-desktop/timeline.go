package main

import (
	"fmt"
	"image/color"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/record"
)

// span is one step on a run's timeline, in seconds from the run's start.
type span struct {
	Seq        int
	Label      string
	Status     string
	Start, Dur float64
}

// timeline places the standing attempt's steps. A step is sized by the
// duration it measured; one with no outcome runs until the record's last
// event, which is as far as the record can say it went.
func timeline(events []record.Event) (spans []span, total float64) {
	start := 0
	for i, e := range events {
		if e.Kind == record.KindRunStarted {
			start = i
		}
	}
	events = events[start:]
	if len(events) == 0 {
		return nil, 0
	}
	t0 := events[0].TS
	end := events[len(events)-1].TS.Sub(t0).Seconds()
	at := map[int]int{}
	for _, e := range events {
		switch e.Kind {
		case record.KindStepStarted:
			s := span{Seq: e.Step, Start: e.TS.Sub(t0).Seconds(), Dur: -1}
			if started, err := record.Decode[record.StepStarted](e); err == nil {
				s.Label = started.Label
			}
			at[e.Step] = len(spans)
			spans = append(spans, s)
		case record.KindOperation:
			i, ok := at[e.Step]
			if !ok {
				continue
			}
			if op, err := record.Decode[record.Operation](e); err == nil {
				spans[i].Status = string(op.Status)
				spans[i].Dur = op.DurationS
			}
		}
	}
	total = end
	for i := range spans {
		if spans[i].Dur < 0 {
			spans[i].Dur = max(end-spans[i].Start, 0)
		}
		total = max(total, spans[i].Start+spans[i].Dur)
	}
	return spans, total
}

// slowest names the n longest steps, when there are enough steps for slow
// to mean something.
func slowest(spans []span, n int) map[int]bool {
	out := map[int]bool{}
	if len(spans) <= 3 {
		return out
	}
	ordered := append([]span(nil), spans...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Dur > ordered[j].Dur })
	for _, s := range ordered[:min(n, len(ordered))] {
		if s.Dur > 0 {
			out[s.Seq] = true
		}
	}
	return out
}

// timelineChart draws a run's steps as bars against the run's own clock,
// one row per step. Tapping a row opens that step.
type timelineChart struct {
	widget.BaseWidget
	spans    []span
	total    float64
	slow     map[int]bool // the three longest steps, in bold
	top      map[int]bool // the longest, named as such
	selected int
	onTap    func(seq int)
}

func newTimelineChart(spans []span, total float64, onTap func(int)) *timelineChart {
	c := &timelineChart{spans: spans, total: total, slow: slowest(spans, 3), top: slowest(spans, 1), selected: -1, onTap: onTap}
	c.ExtendBaseWidget(c)
	return c
}

// Select marks the step shown beside the timeline.
func (c *timelineChart) Select(seq int) {
	c.selected = seq
	c.Refresh()
}

const (
	timelineRow   = 26
	timelineLabel = 220
	timelineTail  = 90
)

func (c *timelineChart) Tapped(e *fyne.PointEvent) {
	row := int((e.Position.Y - timelineRow) / timelineRow)
	if row >= 0 && row < len(c.spans) && c.onTap != nil {
		c.onTap(c.spans[row].Seq)
	}
}

func (c *timelineChart) CreateRenderer() fyne.WidgetRenderer {
	r := &timelineRenderer{c: c}
	for range 3 {
		t := canvas.NewText("", nil)
		t.TextSize = theme.CaptionTextSize()
		r.ticks = append(r.ticks, t)
		r.grid = append(r.grid, canvas.NewLine(nil))
	}
	for range c.spans {
		label := canvas.NewText("", nil)
		label.TextStyle = fyne.TextStyle{Monospace: true}
		dur := canvas.NewText("", nil)
		dur.TextSize = theme.CaptionTextSize()
		bar := canvas.NewRectangle(nil)
		bar.CornerRadius = 3
		r.rows = append(r.rows, timelineRowParts{bg: canvas.NewRectangle(nil), label: label, bar: bar, dur: dur})
	}
	r.Refresh()
	return r
}

type timelineRowParts struct {
	bg         *canvas.Rectangle
	label, dur *canvas.Text
	bar        *canvas.Rectangle
}

type timelineRenderer struct {
	c     *timelineChart
	rows  []timelineRowParts
	ticks []*canvas.Text
	grid  []*canvas.Line
	size  fyne.Size
}

func (r *timelineRenderer) Layout(size fyne.Size) {
	r.size = size
	plot := max(size.Width-timelineLabel-timelineTail, 40)
	scale := func(sec float64) float32 {
		if r.c.total <= 0 {
			return 0
		}
		return float32(sec/r.c.total) * plot
	}
	for i, t := range r.ticks {
		x := timelineLabel + plot*float32(i)/2
		t.Move(fyne.NewPos(x-4, 4))
		r.grid[i].Position1 = fyne.NewPos(x, timelineRow-4)
		r.grid[i].Position2 = fyne.NewPos(x, float32(timelineRow*(len(r.rows)+1)))
	}
	for i, row := range r.rows {
		s := r.c.spans[i]
		y := float32(timelineRow * (i + 1))
		row.bg.Move(fyne.NewPos(0, y))
		row.bg.Resize(fyne.NewSize(size.Width, timelineRow))
		row.label.Move(fyne.NewPos(6, y+4))
		x := timelineLabel + scale(s.Start)
		w := max(scale(s.Dur), 2)
		row.bar.Move(fyne.NewPos(x, y+6))
		row.bar.Resize(fyne.NewSize(w, timelineRow-12))
		row.dur.Move(fyne.NewPos(x+w+6, y+6))
	}
}

func (r *timelineRenderer) MinSize() fyne.Size {
	return fyne.NewSize(timelineLabel+timelineTail+200, float32(timelineRow*(len(r.rows)+1)))
}

func (r *timelineRenderer) Refresh() {
	muted := theme.Color(theme.ColorNameDisabled)
	for i, t := range r.ticks {
		t.Text = seconds(r.c.total * float64(i) / 2)
		t.Color = muted
		t.Refresh()
		r.grid[i].StrokeColor = theme.Color(theme.ColorNameSeparator)
		r.grid[i].StrokeWidth = 1
		r.grid[i].Refresh()
	}
	for i, row := range r.rows {
		s := r.c.spans[i]
		row.bg.FillColor = color.Transparent
		if s.Seq == r.c.selected {
			row.bg.FillColor = theme.Color(colorSelected)
		}
		row.bg.Refresh()
		row.label.Text = shorten(fmt.Sprintf("%d %s", s.Seq, s.Label), 26)
		row.label.Color = theme.Color(theme.ColorNameForeground)
		row.label.Refresh()
		row.bar.FillColor = spanColor(s.Status)
		row.bar.Refresh()
		row.dur.Text = seconds(s.Dur)
		row.dur.TextStyle = fyne.TextStyle{Bold: r.c.slow[s.Seq]}
		row.dur.Color = muted
		if r.c.slow[s.Seq] {
			row.dur.Color = theme.Color(theme.ColorNameForeground)
		}
		if r.c.top[s.Seq] {
			row.dur.Text += " · slowest"
		}
		row.dur.Refresh()
	}
	if r.size.Width > 0 {
		r.Layout(r.size)
	}
}

func (r *timelineRenderer) Objects() []fyne.CanvasObject {
	var out []fyne.CanvasObject
	for _, row := range r.rows {
		out = append(out, row.bg)
	}
	for i := range r.ticks {
		out = append(out, r.grid[i], r.ticks[i])
	}
	for _, row := range r.rows {
		out = append(out, row.label, row.bar, row.dur)
	}
	return out
}
func (r *timelineRenderer) Destroy() {}

// spanColor keeps the app's pairing: a step that measured cleanly in blue,
// one whose measurement went wrong in orange, one with no outcome in grey.
func spanColor(status string) color.Color {
	switch status {
	case string(record.StatusMeasured):
		return theme.Color(theme.ColorNamePrimary)
	case "":
		return theme.Color(colorMuted)
	case string(record.StatusSkipped):
		return theme.Color(colorPillNeutral)
	default:
		return theme.Color(colorPillBadText)
	}
}

// seconds is a duration as a person reads it.
func seconds(s float64) string {
	switch {
	case s < 10:
		return fmt.Sprintf("%.1f s", s)
	case s < 120:
		return fmt.Sprintf("%.0f s", s)
	default:
		return fmt.Sprintf("%dm%02ds", int(s)/60, int(s)%60)
	}
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
