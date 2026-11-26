package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/scenario"
)

// paletteItem is one thing Ctrl+K can do: go to a screen, open a scenario's
// run form, open a record.
type paletteItem struct {
	Title, Kind string
	Do          func()
}

// paletteShown is how many matches the palette lists at once.
const paletteShown = 60

// matchItems is the items whose title or kind holds every word typed, any
// case. A title that starts with the first word comes first, then one that
// holds it, then one matched on its kind; a shorter title first among equals.
func matchItems(items []paletteItem, query string) []paletteItem {
	words := strings.Fields(strings.ToLower(query))
	type ranked struct {
		it   paletteItem
		rank int
	}
	var found []ranked
	for _, it := range items {
		title := strings.ToLower(it.Title)
		hay := title + " " + strings.ToLower(it.Kind)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		rank := 0
		if len(words) > 0 {
			switch {
			case strings.HasPrefix(title, words[0]):
			case strings.Contains(title, words[0]):
				rank = 1
			default:
				rank = 2
			}
		}
		found = append(found, ranked{it, rank})
	}
	if len(words) > 0 {
		sort.SliceStable(found, func(i, j int) bool {
			if found[i].rank != found[j].rank {
				return found[i].rank < found[j].rank
			}
			return len(found[i].it.Title) < len(found[j].it.Title)
		})
	}
	out := make([]paletteItem, len(found))
	for i, f := range found {
		out[i] = f.it
	}
	return out
}

// paletteItems is everything the palette offers now: each screen, each
// scenario under the root, and each record, newest first.
func (s *shell) paletteItems() []paletteItem {
	var items []paletteItem
	shows := s.sectionShows()
	for i := range sections {
		items = append(items, paletteItem{Title: sectionNames[i], Kind: "Go to", Do: shows[i]})
	}
	items = append(items, paletteItem{Title: "Compare runs…", Kind: "Runs", Do: func() {
		s.showRuns()
		s.chooseRunsToCompare()
	}})
	if root := s.tools.scenarioRoot; root != "" {
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				name := e.Name()
				if !e.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(root, name, scenario.File)); err != nil {
					continue
				}
				items = append(items, paletteItem{Title: name, Kind: "Run scenario", Do: func() { s.openScenario(name) }})
			}
		}
	}
	if runs, err := browse.List(s.dir); err == nil {
		sort.SliceStable(runs, func(i, j int) bool { return takenAt(runs[i].RunID) > takenAt(runs[j].RunID) })
		for _, r := range runs {
			dir := r.Dir
			items = append(items, paletteItem{Title: r.RunID, Kind: "Open run", Do: func() { s.openRecord(dir, "Runs", s.showRuns) }})
		}
	}
	return items
}

// paletteEntry is the palette's search line: up and down move through the
// matches, Enter takes the one picked, Escape closes.
type paletteEntry struct {
	widget.Entry
	onKey func(fyne.KeyName) bool
}

func newPaletteEntry() *paletteEntry {
	e := &paletteEntry{}
	e.ExtendBaseWidget(e)
	return e
}

func (e *paletteEntry) TypedKey(k *fyne.KeyEvent) {
	if e.onKey != nil && e.onKey(k.Name) {
		return
	}
	e.Entry.TypedKey(k)
}

// openPalette shows the palette over whatever screen is up.
func (s *shell) openPalette() {
	all := s.paletteItems()
	shown := matchItems(all, "")
	picked := 0
	entry := newPaletteEntry()
	entry.SetPlaceHolder("Go to a screen, run a scenario, open a run…")
	list := widget.NewList(
		func() int { return min(len(shown), paletteShown) },
		func() fyne.CanvasObject {
			kind := widget.NewLabel("")
			kind.Importance = widget.LowImportance
			title := widget.NewLabel("")
			title.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, nil, kind, title)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			row := o.(*fyne.Container)
			row.Objects[0].(*widget.Label).SetText(shown[i].Title)
			row.Objects[1].(*widget.Label).SetText(shown[i].Kind)
		},
	)
	var d dialog.Dialog
	take := func(i int) {
		if i < 0 || i >= len(shown) {
			return
		}
		d.Hide()
		shown[i].Do()
	}
	list.OnSelected = func(i widget.ListItemID) { take(i) }
	entry.OnChanged = func(q string) {
		shown = matchItems(all, q)
		picked = 0
		list.Refresh()
		list.ScrollToTop()
	}
	entry.OnSubmitted = func(string) { take(picked) }
	entry.onKey = func(k fyne.KeyName) bool {
		switch k {
		case fyne.KeyDown:
			picked = min(picked+1, min(len(shown), paletteShown)-1)
		case fyne.KeyUp:
			picked = max(picked-1, 0)
		case fyne.KeyEscape:
			d.Hide()
			return true
		default:
			return false
		}
		list.ScrollTo(picked)
		// Highlight without taking it: OnSelected takes a tap.
		was := list.OnSelected
		list.OnSelected = nil
		list.Select(picked)
		list.OnSelected = was
		return true
	}
	d = dialog.NewCustomWithoutButtons("Go to", container.NewBorder(entry, nil, nil, nil, list), s.window)
	d.Resize(fyne.NewSize(640, 460))
	d.Show()
	s.window.Canvas().Focus(entry)
}

// sectionShows is each section's screen, in the sidebar's order.
func (s *shell) sectionShows() [sections]func() {
	return [sections]func(){s.showHome, s.showRuns, s.showBatches, s.showCampaigns, s.showScenarios, s.showActivity, s.showTargets, s.showSettings}
}

// addShortcuts puts the palette on Ctrl+K (Cmd+K on a Mac) and each section
// on Ctrl+1 to Ctrl+8, in the sidebar's order.
func (s *shell) addShortcuts() {
	c := s.window.Canvas()
	c.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyK, Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) { s.openPalette() })
	shows := s.sectionShows()
	keys := []fyne.KeyName{fyne.Key1, fyne.Key2, fyne.Key3, fyne.Key4, fyne.Key5, fyne.Key6, fyne.Key7, fyne.Key8}
	for i := range sections {
		show := shows[i]
		c.AddShortcut(&desktop.CustomShortcut{KeyName: keys[i], Modifier: fyne.KeyModifierShortcutDefault}, func(fyne.Shortcut) { show() })
	}
}
