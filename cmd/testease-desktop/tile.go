package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// tileLook is how an outcome tile is drawn: a strong fill for a conclusion, a soft
// one for something that is not one, an outline for nothing yet.
type tileLook int

const (
	lookEmpty   tileLook = iota // nothing here yet: an outline
	lookNeutral                 // done, with nothing to conclude
	lookActive                  // going now
	lookPassed
	lookFailed
	lookBroken
)

func (l tileLook) colors() (fill, text color.Color) {
	switch l {
	case lookPassed:
		return outcomeColor(outcomePassed), theme.Color(theme.ColorNameBackground)
	case lookFailed:
		return outcomeColor(outcomeFailed), theme.Color(theme.ColorNameBackground)
	case lookBroken:
		return outcomeColor(outcomeBroken), theme.Color(theme.ColorNameBackground)
	case lookActive:
		return theme.Color(colorPillActive), theme.Color(colorPillActiveText)
	case lookNeutral:
		return theme.Color(colorPillNeutral), theme.Color(colorPillNeutralText)
	default:
		return color.Transparent, theme.Color(colorMuted)
	}
}

// lookOf is the look of a run's outcome.
func lookOf(o outcome) tileLook {
	switch o {
	case outcomePassed:
		return lookPassed
	case outcomeFailed:
		return lookFailed
	case outcomeBroken:
		return lookBroken
	default:
		return lookNeutral
	}
}

// outcomeTile is one square of a grid or a board: an optional name over a word,
// on a colour that says what it came to. Tapping it calls onTap, if set.
type outcomeTile struct {
	widget.BaseWidget
	title, word string
	look        tileLook
	onTap       func()
}

func newOutcomeTile(title, word string, look tileLook, onTap func()) *outcomeTile {
	t := &outcomeTile{title: title, word: word, look: look, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *outcomeTile) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *outcomeTile) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = 4
	r := &outcomeTileRenderer{t: t, bg: bg, title: canvas.NewText("", nil), word: canvas.NewText("", nil)}
	r.title.TextSize = theme.CaptionTextSize()
	r.word.TextStyle.Bold = true
	r.Refresh()
	return r
}

type outcomeTileRenderer struct {
	t           *outcomeTile
	bg          *canvas.Rectangle
	title, word *canvas.Text
}

func (r *outcomeTileRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	pad := theme.Padding()
	if r.t.title == "" {
		w := r.word.MinSize()
		r.word.Move(fyne.NewPos((size.Width-w.Width)/2, (size.Height-w.Height)/2))
		r.word.Resize(w)
		return
	}
	th, w := r.title.MinSize(), r.word.MinSize()
	top := (size.Height - th.Height - w.Height) / 2
	r.title.Move(fyne.NewPos(pad*2, top))
	r.title.Resize(fyne.NewSize(size.Width-pad*4, th.Height))
	r.word.Move(fyne.NewPos(pad*2, top+th.Height))
	r.word.Resize(fyne.NewSize(size.Width-pad*4, w.Height))
}

func (r *outcomeTileRenderer) MinSize() fyne.Size {
	w := max(r.title.MinSize().Width, r.word.MinSize().Width)
	return fyne.NewSize(w+theme.Padding()*4, r.title.MinSize().Height+r.word.MinSize().Height+theme.Padding()*2)
}

func (r *outcomeTileRenderer) Refresh() {
	fill, text := r.t.look.colors()
	r.bg.FillColor = fill
	r.bg.StrokeWidth = 0
	if r.t.look == lookEmpty {
		r.bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
		r.bg.StrokeWidth = 1
	}
	r.title.Text, r.title.Color = r.t.title, text
	r.word.Text, r.word.Color = r.t.word, text
	r.bg.Refresh()
	r.title.Refresh()
	r.word.Refresh()
	r.Layout(r.t.Size())
}

func (r *outcomeTileRenderer) Objects() []fyne.CanvasObject {
	if r.t.title == "" {
		return []fyne.CanvasObject{r.bg, r.word}
	}
	return []fyne.CanvasObject{r.bg, r.title, r.word}
}

func (r *outcomeTileRenderer) Destroy() {}

// sized holds an object at one size, whatever is around it.
func sized(w, h float32, o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWrap(fyne.NewSize(w, h), o)
}
