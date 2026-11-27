package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// cellFunc is what one cell of a listing says, and whether it is a pill.
type cellFunc func(row, col int) (text string, kind pillKind, isPill bool)

// listing is a table of rows a person picks one of: a header row, cells that
// are text or a pill, and a monospace column for ids. Picking a row calls
// onPick and leaves nothing selected, so the same row can be picked again.
func listing(rows func() int, titles []string, widths []float32, mono int, cell cellFunc, onPick func(row int)) *widget.Table {
	t := widget.NewTableWithHeaders(
		func() (int, int) { return rows(), len(titles) },
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.Truncation = fyne.TextTruncateEllipsis
			return container.NewStack(label, container.NewHBox(newPill("", pillNeutral)))
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			stack := o.(*fyne.Container)
			label := stack.Objects[0].(*widget.Label)
			box := stack.Objects[1].(*fyne.Container)
			text, kind, isPill := cell(id.Row, id.Col)
			if isPill {
				label.Hide()
				box.Show()
				box.Objects[0].(*pill).Set(text, kind)
				// Lay the box out again for this pill's word: it was sized
				// for whatever the cell showed before.
				box.Refresh()
				return
			}
			box.Hide()
			label.Show()
			label.TextStyle = fyne.TextStyle{Monospace: id.Col == mono}
			label.SetText(text)
		},
	)
	t.ShowHeaderColumn = false
	t.CreateHeader = func() fyne.CanvasObject {
		l := widget.NewLabel("")
		l.Importance = widget.LowImportance
		return l
	}
	t.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < len(titles) {
			o.(*widget.Label).SetText(titles[id.Col])
		}
	}
	for i, w := range widths {
		t.SetColumnWidth(i, w)
	}
	t.OnSelected = func(id widget.TableCellID) {
		t.UnselectAll()
		if id.Row >= 0 && id.Row < rows() && onPick != nil {
			onPick(id.Row)
		}
	}
	return t
}

// fitColumn widens a monospace id column to its longest entry, up to fitCap.
func fitColumn(t *widget.Table, col int, base float32, ids []string) {
	pad := 2*theme.InnerPadding() + theme.Padding()
	w := base
	for _, id := range ids {
		if m := fyne.MeasureText(id, theme.TextSize(), fyne.TextStyle{Monospace: true}).Width + pad; m > w {
			w = m
		}
	}
	t.SetColumnWidth(col, min(w, fitCap))
}

// screenTitle is a list screen's heading: its name, a count beside it, and
// whatever buttons go on the right.
func screenTitle(name string, count *widget.Label, right ...fyne.CanvasObject) fyne.CanvasObject {
	title := widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameSubHeadingText
	objects := []fyne.CanvasObject{title}
	if count != nil {
		count.Importance = widget.LowImportance
		objects = append(objects, count)
	}
	objects = append(objects, layout.NewSpacer())
	objects = append(objects, right...)
	return container.NewPadded(container.NewHBox(objects...))
}

// note is a wrapped, low-importance line under a screen.
func note(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapWord
	return l
}

// message is a wrapped line that says why a screen is empty, hidden until
// there is something to say.
func message() *widget.Label {
	l := widget.NewLabel("")
	l.Wrapping = fyne.TextWrapWord
	l.Hide()
	return l
}

// backHeader is a detail screen's top line: a way back, what this is, and
// whatever goes on the right.
func backHeader(backLabel string, back func(), id string, right ...fyne.CanvasObject) fyne.CanvasObject {
	backButton := widget.NewButtonWithIcon(backLabel, theme.NavigateBackIcon(), back)
	backButton.Importance = widget.LowImportance
	name := widget.NewLabelWithStyle(id, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	name.Selectable = true
	name.Truncation = fyne.TextTruncateEllipsis
	return container.NewBorder(nil, nil,
		container.NewHBox(backButton, widget.NewLabel("/")),
		container.NewHBox(right...),
		name)
}

// action is a button for something the shell does from a screen. It stays
// hidden until the shell says what it does, so a screen built on its own
// offers nothing it cannot do.
type action struct {
	*widget.Button
	do func()
}

// primaryAction is the one action a screen is for, drawn in the primary
// colour so it is the first thing found. A screen has at most one.
func primaryAction(label string, icon fyne.Resource) *action {
	a := newAction(label, icon)
	a.Importance = widget.HighImportance
	return a
}

func newAction(label string, icon fyne.Resource) *action {
	a := &action{}
	a.Button = widget.NewButtonWithIcon(label, icon, func() {
		if a.do != nil {
			a.do()
		}
	})
	a.Hide()
	return a
}

func (a *action) set(do func()) {
	a.do = do
	a.Show()
}
