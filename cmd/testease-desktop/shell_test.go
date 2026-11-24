package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

// Fyne grows a window to fit whatever its content asks for. Nothing on a
// screen - a record's output, a long label from someone's scenario - may push
// the window past the screen; a screen that asks for more scrolls instead.
func TestAScreenThatAsksForMoreRoomScrollsRatherThanGrowingTheWindow(t *testing.T) {
	test.NewTempApp(t)

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	w.SetContent(s.content)

	huge := canvas.NewRectangle(nil)
	huge.SetMinSize(fyne.NewSize(5000, 3000))
	s.show(huge)

	if got := s.content.MinSize(); got.Width > openingSize.Width || got.Height > openingSize.Height {
		t.Errorf("the window is asked for %v, want no more than %v", got, openingSize)
	}
}
