package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/browse"
	"github.com/jmurray2011/testease/internal/campaign"
)

// The Campaigns screen's columns.
const (
	ccolCampaign = iota
	ccolStatus
	ccolRuns
	ccolNow
	ccolStarted
)

var (
	campaignTitles = []string{"Campaign", "Status", "Runs", "Now", "Started"}
	campaignWidths = []float32{340, 120, 110, 170, 190}
)

// campaignsView lists the campaigns under a runs directory, newest first.
type campaignsView struct {
	dir    string
	onOpen func(browse.Campaign)

	all     []browse.Campaign
	count   *widget.Label
	message *widget.Label
	table   *widget.Table
	content fyne.CanvasObject
}

func newCampaignsView(dir string, onOpen func(browse.Campaign)) *campaignsView {
	v := &campaignsView{dir: dir, onOpen: onOpen, count: widget.NewLabel(""), message: message()}
	v.table = listing(func() int { return len(v.all) }, campaignTitles, campaignWidths, ccolCampaign, v.cell,
		func(row int) { v.onOpen(v.all[row]) })
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), v.reload)
	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Campaigns", v.count, refresh), widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), note("A campaign repeats one scenario with hooks around it. Each run it starts is a record of its own.")),
		nil, nil, v.table,
	)
	v.reload()
	return v
}

func (v *campaignsView) reload() {
	v.all = nil
	switch all, err := browse.Campaigns(v.dir); {
	case v.dir == "":
		showMessage(v.message, "Choose the runs directory to see its campaigns.")
	case err != nil:
		showMessage(v.message, fmt.Sprintf("Could not read the campaigns in %s: %v", v.dir, err))
	case len(all) == 0:
		showMessage(v.message, "No campaigns under "+v.dir+". A campaign is started with testease campaign run.")
	default:
		v.all = all
		v.message.Hide()
	}
	ids := make([]string, len(v.all))
	for i, c := range v.all {
		ids[i] = c.ID
	}
	fitColumn(v.table, ccolCampaign, campaignWidths[ccolCampaign], ids)
	v.count.SetText(plural(len(v.all), "campaign"))
	v.table.Refresh()
}

func (v *campaignsView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.all) {
		return "", pillNeutral, false
	}
	c := v.all[row]
	switch col {
	case ccolCampaign:
		return c.ID, pillNeutral, false
	case ccolStatus:
		return c.Status, groupStatusKind(c.Status), true
	case ccolRuns:
		if c.Repeat > 0 {
			return fmt.Sprintf("%d of %s", len(c.Runs), plural(c.Repeat, "run")), pillNeutral, false
		}
		return plural(len(c.Runs), "run"), pillNeutral, false
	case ccolNow:
		switch {
		case c.Current != "":
			return c.Current, pillNeutral, false
		case c.Cleanup != "":
			return cleanupText(c.Cleanup), pillNeutral, false
		default:
			return "—", pillNeutral, false
		}
	case ccolStarted:
		if c.Started.IsZero() {
			return "—", pillNeutral, false
		}
		// UTC, as the id beside it is.
		return c.Started.UTC().Format("2006-01-02 15:04") + " UTC", pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *campaignsView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}

// The campaign screen's columns.
const (
	acolWhat = iota
	acolStatus
	acolRun
	acolVerdict
)

var (
	activityTitles = []string{"Activity", "Status", "Run", "Verdict"}
	activityWidths = []float32{170, 100, 380, 150}
)

// campaignView is one campaign opened: what it did, in order, and a log - the
// campaign's own, or the one an activity kept.
type campaignView struct {
	runsDir, id string
	c           browse.Campaign
	selected    int
	onOpenRun   func(dir string)

	status   *pill
	meta     *widget.Label
	problem  *widget.Label
	table    *widget.Table
	logTitle *widget.Label
	log      *widget.TextGrid
	openRun  *widget.Button
	content  fyne.CanvasObject
}

func newCampaignView(runsDir, id string, back func(), openRun func(string)) *campaignView {
	v := &campaignView{runsDir: runsDir, id: id, selected: -1, onOpenRun: openRun, problem: message()}
	v.c = findCampaign(runsDir, id)

	v.status = newPill(v.c.Status, groupStatusKind(v.c.Status))
	top := backHeader("Campaigns", back, id, container.NewCenter(v.status), openFolderButton(v.c.Dir))
	v.meta = note("")

	v.table = listing(func() int { return len(v.c.Activities) }, activityTitles, activityWidths, acolRun, v.cell, nil)
	// Unlike a listing, the picked activity stays highlighted: its log is
	// what is showing below.
	v.table.OnSelected = func(id widget.TableCellID) { v.selectActivity(id.Row) }

	v.logTitle = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.logTitle.Truncation = fyne.TextTruncateEllipsis
	// As on a record, the log sits in a scroll of its own so a long line
	// scrolls rather than widening the window.
	v.log = widget.NewTextGrid()
	v.log.Scroll = fyne.ScrollNone
	v.openRun = widget.NewButtonWithIcon("Open run", theme.NavigateNextIcon(), v.openSelectedRun)
	campaignLog := widget.NewButton("Campaign log", v.showCampaignLog)
	campaignLog.Importance = widget.LowImportance
	logBar := container.NewBorder(nil, nil, nil, container.NewHBox(campaignLog, v.openRun), v.logTitle)
	logPane := container.NewBorder(container.NewVBox(widget.NewSeparator(), logBar), nil, nil, nil,
		container.NewScroll(container.NewPadded(v.log)))

	split := container.NewVSplit(v.table, logPane)
	split.Offset = 0.5
	v.content = container.NewBorder(
		container.NewVBox(container.NewPadded(container.NewVBox(top, v.meta)), widget.NewSeparator(), v.problem),
		nil, nil, nil, split,
	)
	v.refreshHeader()
	v.showCampaignLog()
	return v
}

// findCampaign reads one campaign by id. One that is gone is shown as gone,
// not as an error dialog over a screen that was open a moment ago.
func findCampaign(runsDir, id string) browse.Campaign {
	all, err := browse.Campaigns(runsDir)
	if err == nil {
		for _, c := range all {
			if c.ID == id {
				return c
			}
		}
	}
	dir := filepath.Join(runsDir, "campaigns", id)
	return browse.Campaign{ID: id, Dir: dir, Log: filepath.Join(dir, "output.log"), Status: browse.Unreadable, Problem: "this campaign is no longer in the runs directory"}
}

func (v *campaignView) refreshHeader() {
	v.status.Set(v.c.Status, groupStatusKind(v.c.Status))
	parts := []string{"Scenario " + orDash(v.c.Scenario)}
	if v.c.Repeat > 0 {
		parts = append(parts, fmt.Sprintf("%d of %s", len(v.c.Runs), plural(v.c.Repeat, "run")))
	}
	if v.c.Cleanup != "" {
		parts = append(parts, cleanupText(v.c.Cleanup))
	}
	if v.c.Status != campaign.StatusRunning && v.c.Status != browse.Unreadable {
		parts = append(parts, fmt.Sprintf("exit %d", v.c.ExitCode))
	}
	v.meta.SetText(strings.Join(parts, "    "))
	if v.c.Problem != "" {
		showMessage(v.problem, v.c.Problem)
	} else {
		v.problem.Hide()
	}
}

// reload reads the campaign again, keeping the activity the person picked.
func (v *campaignView) reload() {
	v.c = findCampaign(v.runsDir, v.id)
	v.refreshHeader()
	v.table.Refresh()
	if v.selected >= len(v.c.Activities) {
		v.selected = -1
	}
	if v.selected >= 0 {
		v.selectActivity(v.selected)
	} else {
		v.showCampaignLog()
	}
}

// returned re-reads the campaign when a person comes back to it from a record,
// and highlights the activity whose log is showing. The periodic refresh does
// not: selecting a row scrolls to it, which would fight a person scrolling.
func (v *campaignView) returned() {
	v.reload()
	if v.selected >= 0 {
		v.table.Select(widget.TableCellID{Row: v.selected, Col: acolWhat})
	}
}

func (v *campaignView) showCampaignLog() {
	v.selected = -1
	v.table.UnselectAll()
	v.logTitle.SetText("output.log")
	v.log.SetText(v.readLog("output.log"))
	v.openRun.Disable()
}

// selectActivity shows the log an activity kept, and offers the record it
// started when it started one.
func (v *campaignView) selectActivity(i int) {
	if i < 0 || i >= len(v.c.Activities) {
		return
	}
	v.selected = i
	a := v.c.Activities[i]
	if a.Log == "" {
		v.logTitle.SetText(activityName(a))
		v.log.SetText("(this activity kept no log of its own; the campaign log has its output)")
	} else {
		v.logTitle.SetText(activityName(a) + " · " + a.Log)
		v.log.SetText(v.readLog(a.Log))
	}
	if a.RunID != "" && filepath.IsLocal(a.RunID) {
		v.openRun.Enable()
	} else {
		v.openRun.Disable()
	}
}

func (v *campaignView) openSelectedRun() {
	if v.selected < 0 || v.selected >= len(v.c.Activities) {
		return
	}
	if id := v.c.Activities[v.selected].RunID; id != "" && filepath.IsLocal(id) {
		v.onOpenRun(filepath.Join(v.runsDir, id))
	}
}

// readLog reads a log the campaign keeps. As with a record, a path that
// leaves the campaign is not followed, whatever the campaign says.
func (v *campaignView) readLog(rel string) string {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return fmt.Sprintf("(the campaign names its log as %q, which is outside the campaign; it is not followed)", rel)
	}
	path := filepath.Join(v.c.Dir, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Sprintf("(%s is not in the campaign yet)", rel)
	case err != nil:
		return fmt.Sprintf("(could not read %s: %v)", rel, err)
	case info.Size() == 0:
		return "(empty)"
	}
	return readTail(path, outputLimit)
}

// cleanupText is how a campaign's cleanup went, in words.
func cleanupText(cleanup string) string {
	if cleanup == "not-configured" {
		return "no cleanup hook"
	}
	return "cleanup " + cleanup
}

func activityName(a browse.Activity) string {
	switch {
	case a.Kind == "hook":
		return "hook " + a.Name
	case a.Iteration > 0:
		return fmt.Sprintf("%s %d", a.Name, a.Iteration)
	default:
		return a.Name
	}
}

func (v *campaignView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.c.Activities) {
		return "", pillNeutral, false
	}
	a := v.c.Activities[row]
	switch col {
	case acolWhat:
		return activityName(a), pillNeutral, false
	case acolStatus:
		switch {
		case !a.Finished:
			return "running", pillActive, true
		case a.ExitCode != 0:
			return fmt.Sprintf("exit %d", a.ExitCode), pillBad, true
		default:
			return "exit 0", pillNeutral, true
		}
	case acolRun:
		return orDash(a.RunID), pillNeutral, false
	case acolVerdict:
		return a.Verdict, pillNeutral, false
	}
	return "", pillNeutral, false
}

func (v *campaignView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}
