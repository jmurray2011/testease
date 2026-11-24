package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
)

// trendRuns is how many of a scenario's runs a fact's trend reaches back.
const trendRuns = 20

// factKey names a fact across runs: the step that took it, by label, and the
// fact's key. Step numbers move when a scenario changes; labels hold.
type factKey struct{ Step, Key string }

// factTrend is one fact's readings, oldest first, ending with this run's. A
// nil value is a step that tried and got no reading.
type factTrend struct{ Values []*string }

// factTrends follows every fact of a record back through earlier records of
// the same scenario, given oldest first.
func factTrends(now browse.Detail, earlier []browse.Detail) map[factKey]factTrend {
	out := map[factKey]factTrend{}
	for _, s := range now.StepList {
		for _, f := range s.Facts {
			out[factKey{s.Label, f.Key}] = factTrend{}
		}
	}
	for _, d := range append(append([]browse.Detail(nil), earlier...), now) {
		for _, s := range d.StepList {
			for _, f := range s.Facts {
				k := factKey{s.Label, f.Key}
				if tr, ok := out[k]; ok {
					tr.Values = append(tr.Values, f.Value)
					out[k] = tr
				}
			}
		}
	}
	return out
}

// numbers reads every reading as a number; ok is false if any is not one,
// or there are fewer than two to draw.
func (t factTrend) numbers() ([]float64, bool) {
	var out []float64
	for _, v := range t.Values {
		if v == nil {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(*v), 64)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) >= 2
}

// summary says how a fact that is not a number has moved.
func (t factTrend) summary() string {
	if len(t.Values) < 2 {
		return "first reading"
	}
	changes := 0
	for i := 1; i < len(t.Values); i++ {
		if text(t.Values[i]) != text(t.Values[i-1]) {
			changes++
		}
	}
	if changes == 0 {
		return fmt.Sprintf("the same in the last %d runs", len(t.Values))
	}
	return fmt.Sprintf("changed in %d of the last %d runs", changes, len(t.Values))
}

func text(v *string) string {
	if v == nil {
		return "\x00none"
	}
	return *v
}

// earlierRuns opens the records of the same scenario taken before this one,
// beside it in its runs directory, oldest first.
func earlierRuns(d browse.Detail) []browse.Detail {
	runs, err := browse.List(filepath.Dir(d.Dir))
	if err != nil {
		return nil
	}
	mine := takenAt(d.RunID)
	var same []browse.Run
	for _, r := range runs {
		if r.Scenario == d.Scenario && r.Dir != d.Dir && takenAt(r.RunID) < mine {
			same = append(same, r)
		}
	}
	// The listing is sorted by run id: one scenario's runs are in the order
	// they were taken.
	if len(same) > trendRuns-1 {
		same = same[len(same)-(trendRuns-1):]
	}
	var out []browse.Detail
	for _, r := range same {
		if o, err := browse.Open(r.Dir); err == nil {
			out = append(out, o)
		}
	}
	return out
}

// sparkline draws numbers as a line, the newest a dot at the right.
type sparkline struct {
	widget.BaseWidget
	values []float64
}

func newSparkline(values []float64) *sparkline {
	s := &sparkline{values: values}
	s.ExtendBaseWidget(s)
	return s
}

func (s *sparkline) CreateRenderer() fyne.WidgetRenderer {
	r := &sparkRenderer{s: s, dot: canvas.NewCircle(nil)}
	for i := 1; i < len(s.values); i++ {
		r.lines = append(r.lines, canvas.NewLine(nil))
	}
	r.Refresh()
	return r
}

type sparkRenderer struct {
	s     *sparkline
	lines []*canvas.Line
	dot   *canvas.Circle
}

const sparkWidth, sparkHeight = 120, 26

// trendColumn is the width the Facts tab gives every trend, so the lines
// start at the same place on every row.
const trendColumn = 300

func (r *sparkRenderer) Layout(size fyne.Size) {
	vs := r.s.values
	lo, hi := vs[0], vs[0]
	for _, v := range vs {
		lo, hi = min(lo, v), max(hi, v)
	}
	point := func(i int) fyne.Position {
		x := float32(i) / float32(len(vs)-1) * (size.Width - 6)
		y := size.Height / 2
		if hi > lo {
			y = 3 + (1-float32((vs[i]-lo)/(hi-lo)))*(size.Height-6)
		}
		return fyne.NewPos(x+1, y)
	}
	for i, l := range r.lines {
		l.Position1, l.Position2 = point(i), point(i+1)
	}
	last := point(len(vs) - 1)
	r.dot.Move(fyne.NewPos(last.X-3, last.Y-3))
	r.dot.Resize(fyne.NewSize(6, 6))
}

func (r *sparkRenderer) MinSize() fyne.Size { return fyne.NewSize(sparkWidth, sparkHeight) }

func (r *sparkRenderer) Refresh() {
	for _, l := range r.lines {
		l.StrokeColor = theme.Color(theme.ColorNamePrimary)
		l.StrokeWidth = 1.5
		l.Refresh()
	}
	r.dot.FillColor = theme.Color(theme.ColorNamePrimary)
	r.dot.Refresh()
}

func (r *sparkRenderer) Objects() []fyne.CanvasObject {
	out := []fyne.CanvasObject{r.dot}
	for _, l := range r.lines {
		out = append(out, l)
	}
	return out
}
func (r *sparkRenderer) Destroy() {}

// quantity says whether a fact's numbers measure an amount. A status or exit
// code names an outcome, and a line between two codes means nothing.
func quantity(key string) bool {
	words := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	if len(words) == 0 {
		return true
	}
	switch words[len(words)-1] {
	case "status", "code", "rc", "exit":
		return false
	}
	return true
}

// trendView is what the Facts tab shows beside a value: a line for amounts,
// otherwise how often it changed.
func trendView(key string, t factTrend) fyne.CanvasObject {
	if nums, ok := t.numbers(); ok && quantity(key) {
		lo, hi := nums[0], nums[0]
		for _, n := range nums {
			lo, hi = min(lo, n), max(hi, n)
		}
		if lo == hi {
			l := widget.NewLabel(t.summary())
			l.Importance = widget.LowImportance
			return l
		}
		r := widget.NewLabel(fmt.Sprintf("%s–%s over %d runs", trimNumber(lo), trimNumber(hi), len(nums)))
		r.Importance = widget.LowImportance
		return container.NewHBox(newSparkline(nums), r)
	}
	l := widget.NewLabel(t.summary())
	l.Importance = widget.LowImportance
	return l
}

func trimNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
