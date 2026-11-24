package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// match is one place a search found, in the grid's rows and columns.
type match struct{ Row, Col, Len int }

// findMatches finds needle on every line, ignoring case. Columns count
// characters, the way a TextGrid lays them out.
func findMatches(text, needle string) []match {
	if needle == "" {
		return nil
	}
	lowNeedle := strings.ToLower(needle)
	n := utf8.RuneCountInString(needle)
	var out []match
	for row, line := range strings.Split(text, "\n") {
		low := strings.ToLower(line)
		from := 0
		for {
			i := strings.Index(low[from:], lowNeedle)
			if i < 0 {
				break
			}
			at := from + i
			out = append(out, match{Row: row, Col: utf8.RuneCountInString(low[:at]), Len: n})
			from = at + len(lowNeedle)
		}
	}
	return out
}

// trouble is what a line that went wrong usually says.
var trouble = regexp.MustCompile(`(?i)\b(error|failed|failure|fatal|panic|exception|denied|refused|timed out|timeout|traceback)\b`)

// firstTrouble is the first line that looks like something went wrong, or
// -1. A line that only counts errors ("errors: 0") is not trouble.
func firstTrouble(text string) int {
	for row, line := range strings.Split(text, "\n") {
		if trouble.MatchString(line) {
			return row
		}
	}
	return -1
}

// outputSearch is the bar over a step's output: a search, its count, the
// way through its matches, and a jump to the first line of trouble.
type outputSearch struct {
	entry      *widget.Entry
	found      *widget.Label
	prev, next *widget.Button
	trouble    *widget.Button
	bar        fyne.CanvasObject

	grid    func() (*widget.TextGrid, *container.Scroll)
	matches []match
	at      int
}

var (
	matchStyle   = &widget.CustomTextGridStyle{BGColor: nil}
	currentStyle = &widget.CustomTextGridStyle{BGColor: nil}
)

func newOutputSearch(grid func() (*widget.TextGrid, *container.Scroll)) *outputSearch {
	s := &outputSearch{grid: grid, entry: widget.NewEntry(), found: widget.NewLabel("")}
	s.entry.SetPlaceHolder("Search output")
	s.entry.OnChanged = func(string) { s.apply() }
	s.entry.OnSubmitted = func(string) { s.step(1) }
	s.found.Importance = widget.LowImportance
	s.found.Alignment = fyne.TextAlignTrailing
	s.prev = widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { s.step(-1) })
	s.next = widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { s.step(1) })
	s.trouble = widget.NewButtonWithIcon("First error", theme.ErrorIcon(), s.jumpToTrouble)
	s.bar = container.NewBorder(nil, nil, nil,
		container.NewHBox(container.NewGridWrap(fyne.NewSize(90, s.entry.MinSize().Height), s.found), s.prev, s.next, s.trouble), s.entry)
	return s
}

// apply searches the output showing now again: after typing, a new step or
// another tab.
func (s *outputSearch) apply() {
	g, _ := s.grid()
	if g == nil {
		s.found.SetText("")
		return
	}
	text := g.Text()
	s.matches = findMatches(text, s.entry.Text)
	s.at = 0
	if firstTrouble(text) < 0 {
		s.trouble.Disable()
	} else {
		s.trouble.Enable()
	}
	s.paint()
}

func (s *outputSearch) step(by int) {
	if len(s.matches) == 0 {
		return
	}
	s.at = (s.at + by + len(s.matches)) % len(s.matches)
	s.paint()
}

func (s *outputSearch) paint() {
	g, scroll := s.grid()
	if g == nil {
		return
	}
	matchStyle.BGColor = theme.Color(theme.ColorNameSelection)
	currentStyle.BGColor = theme.Color(colorPillBad)
	for i := range g.Rows {
		g.SetRowStyle(i, nil)
		for j := range g.Rows[i].Cells {
			g.Rows[i].Cells[j].Style = nil
		}
	}
	switch {
	case s.entry.Text == "":
		s.found.SetText("")
	case len(s.matches) == 0:
		s.found.SetText("no match")
	default:
		s.found.SetText(fmt.Sprintf("%d of %d", s.at+1, len(s.matches)))
		for i, m := range s.matches {
			style := matchStyle
			if i == s.at {
				style = currentStyle
			}
			g.SetStyleRange(m.Row, m.Col, m.Row, m.Col+m.Len-1, style)
		}
		s.show(g, scroll, s.matches[s.at].Row)
	}
	if len(s.matches) > 1 {
		s.prev.Enable()
		s.next.Enable()
	} else {
		s.prev.Disable()
		s.next.Disable()
	}
	g.Refresh()
}

func (s *outputSearch) jumpToTrouble() {
	g, scroll := s.grid()
	if g == nil {
		return
	}
	row := firstTrouble(g.Text())
	if row < 0 {
		return
	}
	currentStyle.BGColor = theme.Color(colorPillBad)
	g.SetRowStyle(row, currentStyle)
	s.show(g, scroll, row)
	g.Refresh()
}

// show scrolls a row into the middle of the view.
func (s *outputSearch) show(g *widget.TextGrid, scroll *container.Scroll, row int) {
	if scroll == nil || len(g.Rows) == 0 {
		return
	}
	line := g.MinSize().Height / float32(len(g.Rows))
	y := line*float32(row) - scroll.Size().Height/2
	scroll.Offset.Y = max(0, min(y, g.MinSize().Height-scroll.Size().Height))
	scroll.Refresh()
}
