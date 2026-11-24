package main

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
)

// attentionLength is how many runs Home calls out.
const attentionLength = 8

// homeSummary is what Home says about a runs directory.
type homeSummary struct {
	Today, TodayJudged, TodayPassed int
	Running                         int
	// Attention is each scenario's latest finished run, where a judge failed
	// it or its measurement broke, newest first.
	Attention []browse.Run
}

// summarize reads the summary. Today is the UTC day, as run ids are stamped.
func summarize(runs []browse.Run, status func(browse.Run) string, now time.Time) homeSummary {
	var s homeSummary
	day := now.UTC().Format("20060102")
	latest := map[string]browse.Run{}
	for _, r := range runs {
		st := status(r)
		if st == statusRunning {
			s.Running++
		}
		if strings.HasPrefix(takenAt(r.RunID), day) {
			s.Today++
			switch outcomeOf(r) {
			case outcomePassed:
				s.TodayJudged++
				s.TodayPassed++
			case outcomeFailed:
				s.TodayJudged++
			}
		}
		if o := outcomeOf(r); o == outcomeIncomplete {
			continue
		}
		if prev, ok := latest[r.Scenario]; !ok || later(r, prev) {
			latest[r.Scenario] = r
		}
	}
	for _, r := range latest {
		if o := outcomeOf(r); o == outcomeFailed || o == outcomeBroken {
			s.Attention = append(s.Attention, r)
		}
	}
	sort.Slice(s.Attention, func(i, j int) bool { return later(s.Attention[i], s.Attention[j]) })
	if len(s.Attention) > attentionLength {
		s.Attention = s.Attention[:attentionLength]
	}
	return s
}

func later(a, b browse.Run) bool {
	ta, tb := takenAt(a.RunID), takenAt(b.RunID)
	if ta != tb {
		return ta > tb
	}
	return a.RunID > b.RunID
}

// homeView is the screen the app opens on.
type homeView struct {
	dir          string
	openRun      func(dir string)
	openScenario func(name string)
	now          func() time.Time

	summary   homeSummary
	scenarios []scenarioHistory

	tiles        [4]*tile
	chart        *daysChart
	attention    *widget.List
	allClear     *widget.Label
	scenarioList *widget.List
	empty        *fyne.Container
	board        *fyne.Container
	message      *widget.Label
	content      fyne.CanvasObject
}

func newHomeView(dir string, openRun, openScenario func(string)) *homeView {
	v := &homeView{dir: dir, openRun: openRun, openScenario: openScenario, now: time.Now}

	v.tiles = [4]*tile{newTile("Runs today"), newTile("Passed today"), newTile("Running now"), newTile("Need a look")}
	tiles := container.NewGridWithColumns(4, v.tiles[0], v.tiles[1], v.tiles[2], v.tiles[3])

	v.chart = newDaysChart()
	chartBox := container.NewBorder(
		container.NewHBox(widget.NewLabelWithStyle("Runs per day, last 14 UTC days", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), layout.NewSpacer(), legend()),
		nil, nil, nil, v.chart,
	)

	v.attention = widget.NewList(
		func() int { return len(v.summary.Attention) },
		func() fyne.CanvasObject {
			id := widget.NewLabel("")
			id.TextStyle = fyne.TextStyle{Monospace: true}
			id.Truncation = fyne.TextTruncateEllipsis
			return container.New(&fixedColumns{widths: []float32{170, 200}}, container.NewHBox(newPill("", pillNeutral)), id)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			r := v.summary.Attention[i]
			c := o.(*fyne.Container)
			c.Objects[0].(*fyne.Container).Objects[0].(*pill).Set(outcomeOf(r).String(), pillBad)
			c.Objects[1].(*widget.Label).SetText(r.RunID)
		},
	)
	v.attention.OnSelected = func(i widget.ListItemID) {
		v.attention.UnselectAll()
		v.openRun(v.summary.Attention[i].Dir)
	}

	v.scenarioList = widget.NewList(
		func() int { return len(v.scenarios) },
		func() fyne.CanvasObject {
			name := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			name.Truncation = fyne.TextTruncateEllipsis
			rate := widget.NewLabel("")
			last := widget.NewLabel("")
			last.Importance = widget.LowImportance
			return container.New(&fixedColumns{widths: []float32{220, historyLength*stripCell + 16, 190, 120, 160}},
				name, container.NewCenter(newStrip()), rate, container.NewHBox(newPill("", pillNeutral)), last)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			h := v.scenarios[i]
			c := o.(*fyne.Container)
			c.Objects[0].(*widget.Label).SetText(h.Scenario)
			c.Objects[1].(*fyne.Container).Objects[0].(*strip).Set(h.Points)
			c.Objects[2].(*widget.Label).SetText(rateText(h))
			flaky := c.Objects[3].(*fyne.Container).Objects[0].(*pill)
			if text, kind := flakinessText(h); text != "" {
				flaky.Set(text, kind)
				flaky.Show()
			} else {
				flaky.Hide()
			}
			c.Objects[4].(*widget.Label).SetText(shortStamp(h.Last))
		},
	)
	v.scenarioList.OnSelected = func(i widget.ListItemID) {
		v.scenarioList.UnselectAll()
		v.openScenario(v.scenarios[i].Scenario)
	}

	v.allClear = note("Nothing needs a look: every scenario's latest finished run passed, or has not been judged.")
	v.allClear.Hide()
	needs := heading("Needs a look")
	scen := heading("Scenarios")
	v.board = container.NewBorder(
		container.NewVBox(tiles, container.NewPadded(chartBox), widget.NewSeparator()),
		nil, nil, nil,
		container.NewVSplit(
			container.NewBorder(needs, nil, nil, nil, container.NewStack(v.attention, container.NewPadded(v.allClear))),
			container.NewBorder(scen, nil, nil, nil, v.scenarioList),
		),
	)
	v.board.Objects[0].(*container.Split).Offset = 0.4

	v.message = widget.NewLabel("")
	v.message.Wrapping = fyne.TextWrapWord
	v.message.Hide()
	v.empty = container.NewCenter(container.NewVBox(
		widget.NewLabelWithStyle("No runs here yet", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle("Start one from Scenarios, or choose another runs directory below the sections.", fyne.TextAlignCenter, fyne.TextStyle{}),
	))
	v.empty.Hide()

	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Home", nil, widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), v.reload)), v.message),
		nil, nil, nil,
		container.NewStack(v.board, v.empty),
	)
	return v
}

func heading(text string) fyne.CanvasObject {
	return container.NewPadded(widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
}

// reload reads the runs directory again.
func (v *homeView) reload() {
	runs, err := browse.List(v.dir)
	if err != nil && v.dir != "" {
		v.message.SetText(err.Error())
		v.message.Show()
	} else {
		v.message.Hide()
	}
	v.summary = summarize(runs, statusOf, v.now())
	v.scenarios = histories(runs)

	s := v.summary
	v.tiles[0].Set(fmt.Sprint(s.Today), "UTC day")
	if s.TodayJudged == 0 {
		v.tiles[1].Set("—", "nothing judged today")
	} else {
		v.tiles[1].Set(fmt.Sprintf("%d of %d", s.TodayPassed, s.TodayJudged), "judged runs")
	}
	v.tiles[2].Set(fmt.Sprint(s.Running), "records being written")
	v.tiles[3].Set(fmt.Sprint(len(s.Attention)), "latest run failed or broke")
	v.chart.Set(lastDays(runs, v.now(), chartDays))

	if len(runs) == 0 {
		v.board.Hide()
		v.empty.Show()
	} else {
		v.empty.Hide()
		v.board.Show()
	}
	if len(s.Attention) == 0 {
		v.allClear.Show()
	} else {
		v.allClear.Hide()
	}
	v.attention.Refresh()
	v.scenarioList.Refresh()
}

func rateText(h scenarioHistory) string {
	rate, ok := h.PassRate()
	if !ok {
		return fmt.Sprintf("none of %s judged", plural(h.Runs, "run"))
	}
	return fmt.Sprintf("%.0f%% of %d judged", rate*100, h.Judged)
}

// flakinessText names a scenario that changes its mind. Fewer than four
// judged runs is too few to call.
func flakinessText(h scenarioHistory) (string, pillKind) {
	if h.Judged < 4 {
		return "", pillNeutral
	}
	switch f := h.Flakiness(); {
	case f >= 0.3:
		return "flaky", pillBad
	case f > 0:
		return "wavers", pillNeutral
	}
	return "steady", pillGood
}

// shortStamp shows a run id's stamp as a date and time.
func shortStamp(stamp string) string {
	t, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return stamp
	}
	return t.Format("2 Jan 15:04") + " UTC"
}

// tile is one number on Home, with what it counts under it.
type tile struct {
	widget.BaseWidget
	title, value, detail string
}

func newTile(title string) *tile {
	t := &tile{title: title}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tile) Set(value, detail string) {
	t.value, t.detail = value, detail
	t.Refresh()
}

func (t *tile) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(nil)
	bg.CornerRadius = 8
	title := widget.NewLabel("")
	title.Importance = widget.LowImportance
	value := canvas.NewText("", nil)
	value.TextSize = theme.TextSize() * 2
	value.TextStyle = fyne.TextStyle{Bold: true}
	detail := widget.NewLabel("")
	detail.Importance = widget.LowImportance
	detail.Truncation = fyne.TextTruncateEllipsis
	r := &tileRenderer{
		t: t, bg: bg, title: title, value: value, detail: detail,
		box: container.NewPadded(container.NewVBox(title, container.NewPadded(value), detail)),
	}
	r.Refresh()
	return r
}

type tileRenderer struct {
	t             *tile
	bg            *canvas.Rectangle
	title, detail *widget.Label
	value         *canvas.Text
	box           *fyne.Container
}

func (r *tileRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.box.Resize(size)
}
func (r *tileRenderer) MinSize() fyne.Size { return r.box.MinSize() }
func (r *tileRenderer) Refresh() {
	r.bg.FillColor = theme.Color(colorNav)
	r.title.SetText(r.t.title)
	r.value.Text = r.t.value
	r.value.Color = theme.Color(theme.ColorNameForeground)
	r.detail.SetText(r.t.detail)
	r.bg.Refresh()
	r.value.Refresh()
}
func (r *tileRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.box} }
func (r *tileRenderer) Destroy()                     {}

// strip is a scenario's recent runs as a row of squares, oldest left.
type strip struct {
	widget.BaseWidget
	points []historyPoint
}

const stripCell = 12

func newStrip() *strip {
	s := &strip{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *strip) Set(points []historyPoint) {
	s.points = points
	s.Refresh()
}

func (s *strip) CreateRenderer() fyne.WidgetRenderer {
	r := &stripRenderer{s: s}
	for i := 0; i < historyLength; i++ {
		c := canvas.NewRectangle(nil)
		c.CornerRadius = 2
		r.cells = append(r.cells, c)
	}
	r.Refresh()
	return r
}

type stripRenderer struct {
	s     *strip
	cells []*canvas.Rectangle
}

func (r *stripRenderer) Layout(fyne.Size) {
	for i, c := range r.cells {
		c.Move(fyne.NewPos(float32(i*stripCell), 2))
		c.Resize(fyne.NewSize(stripCell-3, stripCell-3))
	}
}

func (r *stripRenderer) MinSize() fyne.Size {
	return fyne.NewSize(historyLength*stripCell, stripCell+2)
}

func (r *stripRenderer) Refresh() {
	for i, c := range r.cells {
		if i < len(r.s.points) {
			c.FillColor = outcomeColor(r.s.points[i].Outcome)
			c.Show()
		} else {
			c.Hide()
		}
		c.Refresh()
	}
}

func (r *stripRenderer) Objects() []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, len(r.cells))
	for i, c := range r.cells {
		out[i] = c
	}
	return out
}
func (r *stripRenderer) Destroy() {}

// outcomeColor keeps the app's pairing: blue for a judge's pass, orange for a
// fail, and quieter shades for what no judge decided.
func outcomeColor(o outcome) color.Color {
	switch o {
	case outcomePassed:
		return theme.Color(theme.ColorNamePrimary)
	case outcomeFailed:
		return theme.Color(colorPillBadText)
	case outcomeBroken:
		return theme.Color(colorMuted)
	default:
		return theme.Color(colorPillNeutral)
	}
}

func legend() fyne.CanvasObject {
	item := func(o outcome, text string) fyne.CanvasObject {
		sw := canvas.NewRectangle(outcomeColor(o))
		sw.CornerRadius = 2
		sw.SetMinSize(fyne.NewSize(stripCell-3, stripCell-3))
		l := widget.NewLabel(text)
		l.Importance = widget.LowImportance
		return container.NewHBox(container.NewCenter(sw), l)
	}
	return container.NewHBox(
		item(outcomePassed, "passed"), item(outcomeFailed, "failed"),
		item(outcomeBroken, "measurement broke"), item(outcomeUnjudged, "not judged or unfinished"),
	)
}

// dayCount is one UTC day's runs by what came of them.
type dayCount struct {
	Day                           string
	Passed, Failed, Broken, Other int
}

func (d dayCount) total() int { return d.Passed + d.Failed + d.Broken + d.Other }

// lastDays counts the n UTC days ending today, oldest first.
func lastDays(runs []browse.Run, now time.Time, n int) []dayCount {
	days := make([]dayCount, n)
	index := map[string]int{}
	today := now.UTC().Truncate(24 * time.Hour)
	for i := range days {
		d := today.AddDate(0, 0, i-n+1).Format("20060102")
		days[i].Day = d
		index[d] = i
	}
	for _, r := range runs {
		at := takenAt(r.RunID)
		if len(at) < 8 {
			continue
		}
		i, ok := index[at[:8]]
		if !ok {
			continue
		}
		switch outcomeOf(r) {
		case outcomePassed:
			days[i].Passed++
		case outcomeFailed:
			days[i].Failed++
		case outcomeBroken:
			days[i].Broken++
		default:
			days[i].Other++
		}
	}
	return days
}

// chartDays is how many days Home's chart covers.
const chartDays = 14

// daysChart is runs per day as stacked bars: passed at the bottom, then
// failed, then broken measurements, then everything no judge decided.
type daysChart struct {
	widget.BaseWidget
	days []dayCount
}

func newDaysChart() *daysChart {
	c := &daysChart{}
	c.ExtendBaseWidget(c)
	return c
}

func (c *daysChart) Set(days []dayCount) {
	c.days = days
	c.Refresh()
}

func (c *daysChart) CreateRenderer() fyne.WidgetRenderer {
	r := &daysRenderer{c: c, base: canvas.NewLine(nil), peak: canvas.NewText("", nil)}
	r.peak.TextSize = theme.CaptionTextSize()
	for i := 0; i < chartDays; i++ {
		r.bars = append(r.bars, [4]*canvas.Rectangle{canvas.NewRectangle(nil), canvas.NewRectangle(nil), canvas.NewRectangle(nil), canvas.NewRectangle(nil)})
		l := canvas.NewText("", nil)
		l.TextSize = theme.CaptionTextSize()
		l.Alignment = fyne.TextAlignCenter
		r.labels = append(r.labels, l)
		n := canvas.NewText("", nil)
		n.TextSize = theme.CaptionTextSize()
		n.Alignment = fyne.TextAlignCenter
		r.counts = append(r.counts, n)
	}
	r.Refresh()
	return r
}

type daysRenderer struct {
	c      *daysChart
	bars   [][4]*canvas.Rectangle
	labels []*canvas.Text
	counts []*canvas.Text
	base   *canvas.Line
	peak   *canvas.Text
	size   fyne.Size
}

const chartLabelHeight = 18

func (r *daysRenderer) Layout(size fyne.Size) {
	r.size = size
	r.place()
}

func (r *daysRenderer) place() {
	size := r.size
	if size.Width == 0 || len(r.c.days) == 0 {
		return
	}
	peak := 1
	for _, d := range r.c.days {
		peak = max(peak, d.total())
	}
	plot := size.Height - 2*chartLabelHeight
	slot := size.Width / float32(len(r.bars))
	bar := min(slot*0.6, 36)
	for i, stack := range r.bars {
		x := slot*float32(i) + (slot-bar)/2
		y := chartLabelHeight + plot
		var d dayCount
		if i < len(r.c.days) {
			d = r.c.days[i]
		}
		for j, n := range []int{d.Passed, d.Failed, d.Broken, d.Other} {
			h := plot * float32(n) / float32(peak)
			y -= h
			stack[j].Move(fyne.NewPos(x, y))
			stack[j].Resize(fyne.NewSize(bar, h))
		}
		r.counts[i].Move(fyne.NewPos(slot*float32(i), y-chartLabelHeight))
		r.counts[i].Resize(fyne.NewSize(slot, chartLabelHeight))
		r.labels[i].Move(fyne.NewPos(slot*float32(i), chartLabelHeight+plot+2))
		r.labels[i].Resize(fyne.NewSize(slot, chartLabelHeight))
	}
	r.base.Position1 = fyne.NewPos(0, chartLabelHeight+plot)
	r.base.Position2 = fyne.NewPos(size.Width, chartLabelHeight+plot)
}

func (r *daysRenderer) MinSize() fyne.Size { return fyne.NewSize(chartDays*24, 130) }

func (r *daysRenderer) Refresh() {
	colors := [4]color.Color{outcomeColor(outcomePassed), outcomeColor(outcomeFailed), outcomeColor(outcomeBroken), outcomeColor(outcomeUnjudged)}
	muted := theme.Color(theme.ColorNameDisabled)
	for i, stack := range r.bars {
		var d dayCount
		if i < len(r.c.days) {
			d = r.c.days[i]
		}
		for j := range stack {
			stack[j].FillColor = colors[j]
			stack[j].Refresh()
		}
		r.counts[i].Text = ""
		if d.total() > 0 {
			r.counts[i].Text = fmt.Sprint(d.total())
		}
		r.counts[i].Color = muted
		r.counts[i].Refresh()
		r.labels[i].Text = dayLabel(d.Day, i, len(r.bars))
		r.labels[i].Color = muted
		r.labels[i].Refresh()
	}
	r.base.StrokeColor = theme.Color(theme.ColorNameSeparator)
	r.base.StrokeWidth = 1
	r.base.Refresh()
	r.place()
}

// dayLabel names every other day, and today always.
func dayLabel(day string, i, n int) string {
	t, err := time.Parse("20060102", day)
	if err != nil {
		return ""
	}
	if i == n-1 {
		return "today"
	}
	if (n-1-i)%2 != 0 {
		return ""
	}
	return t.Format("2 Jan")
}

func (r *daysRenderer) Objects() []fyne.CanvasObject {
	out := []fyne.CanvasObject{r.base}
	for i, stack := range r.bars {
		out = append(out, stack[0], stack[1], stack[2], stack[3], r.counts[i], r.labels[i])
	}
	return out
}
func (r *daysRenderer) Destroy() {}
