package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type pillKind int

const (
	pillNeutral pillKind = iota
	pillGood
	pillBad
	pillActive
)

func (k pillKind) colors() (bg, fg fyne.ThemeColorName) {
	switch k {
	case pillGood:
		return colorPillGood, colorPillGoodText
	case pillBad:
		return colorPillBad, colorPillBadText
	case pillActive:
		return colorPillActive, colorPillActiveText
	default:
		return colorPillNeutral, colorPillNeutralText
	}
}

// pill is a short status word on a rounded background. Its colours are looked
// up on every refresh, so it follows a light/dark switch like Fyne's own
// widgets do.
type pill struct {
	widget.BaseWidget
	Text string
	Kind pillKind
}

func newPill(text string, kind pillKind) *pill {
	p := &pill{Text: text, Kind: kind}
	p.ExtendBaseWidget(p)
	return p
}

func (p *pill) Set(text string, kind pillKind) {
	p.Text, p.Kind = text, kind
	p.Refresh()
}

func (p *pill) CreateRenderer() fyne.WidgetRenderer {
	r := &pillRenderer{p: p, bg: canvas.NewRectangle(nil), text: canvas.NewText("", nil)}
	r.text.TextSize = theme.CaptionTextSize()
	r.text.TextStyle = fyne.TextStyle{Bold: true}
	r.Refresh()
	return r
}

type pillRenderer struct {
	p    *pill
	bg   *canvas.Rectangle
	text *canvas.Text
}

const pillPadX, pillPadY = 8, 2

func (r *pillRenderer) Layout(size fyne.Size) {
	textSize := r.text.MinSize()
	box := fyne.NewSize(textSize.Width+2*pillPadX, textSize.Height+2*pillPadY)
	top := (size.Height - box.Height) / 2
	r.bg.Move(fyne.NewPos(0, top))
	r.bg.Resize(box)
	r.bg.CornerRadius = box.Height / 2
	r.text.Move(fyne.NewPos(pillPadX, top+pillPadY))
	r.text.Resize(textSize)
}

func (r *pillRenderer) MinSize() fyne.Size {
	s := r.text.MinSize()
	return fyne.NewSize(s.Width+2*pillPadX, s.Height+2*pillPadY)
}

func (r *pillRenderer) Refresh() {
	bg, fg := r.p.Kind.colors()
	r.bg.FillColor = theme.Color(bg)
	r.text.Color = theme.Color(fg)
	r.text.Text = r.p.Text
	r.bg.Refresh()
	r.text.Refresh()
	r.Layout(r.p.Size())
}

func (r *pillRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.text} }
func (r *pillRenderer) Destroy()                     {}
