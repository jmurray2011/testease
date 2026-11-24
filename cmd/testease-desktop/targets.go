package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runnerid"
)

// cliCall runs one testease command and returns what it wrote to stdout.
type cliCall func(ctx context.Context, args []string) ([]byte, error)

// cliTimeout bounds a command that only asks: listing targets asks every
// docker daemon in turn, and one that never answers must not hold the screen.
const cliTimeout = 2 * time.Minute

// execCall calls the testease binary at exe. Its stderr comes back in the
// error when it fails, because that is where testease says why.
func execCall(exe string) cliCall {
	return func(ctx context.Context, args []string) ([]byte, error) {
		if exe == "" {
			return nil, fmt.Errorf("no testease binary found: put testease beside this app or on PATH, or start this app with --testease <path>")
		}
		ctx, cancel := context.WithTimeout(ctx, cliTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // the testease binary the person chose
		prepare(cmd)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil && len(out) == 0 {
			return nil, fmt.Errorf("testease %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}

// The Targets screen's columns.
const (
	tcolTarget = iota
	tcolHost
	tcolRunID
	tcolFate
	tcolCreated
)

var (
	targetTitles = []string{"Target", "Host", "Run", "State", "Created"}
	targetWidths = []float32{380, 100, 300, 150, 180}
)

// standing is one target as testease targets -o json reports it.
type standing struct {
	Name    string `json:"name"`
	Where   string `json:"where"`
	RunID   string `json:"run"`
	Host    string `json:"host"`
	Cell    string `json:"cell"`
	Created string `json:"created"`
	Fate    string `json:"fate"`
	Shell   string `json:"shell"`
	// Kind and Reason come from reap, for targets only reap can see.
	Kind   string `json:"-"`
	Reason string `json:"-"`
}

// reaped is testease reap -o json.
type reaped struct {
	Targets []struct {
		Kind    string `json:"kind"`
		Fate    string `json:"fate"`
		Name    string `json:"name"`
		RunID   string `json:"run"`
		Created string `json:"created"`
		Reason  string `json:"reason"`
	} `json:"targets"`
	Removable int      `json:"removable"`
	Removed   []string `json:"removed"`
	Notices   []string `json:"notices"`
}

// targetsView lists the targets standing right now. It never removes one by
// itself: removal is testease reap's, which removes only what it can prove
// nobody is using, and the app asks before it runs it.
type targetsView struct {
	runsDir string
	call    cliCall
	openRun func(dir string)
	// async runs slow work off the app's goroutine and onUI brings the answer
	// back; tests run both inline.
	async func(func())
	onUI  func(func())
	ask   func(title, message string, yes func())

	all      []standing
	picked   int
	count    *widget.Label
	message  *widget.Label
	shell    *widget.Label
	copy     *widget.Button
	openRec  *widget.Button
	remove   *widget.Button
	table    *widget.Table
	content  fyne.CanvasObject
	busy     bool
	looks    int
	notices  []string
	lastNote string

	// place is where to look: docker here or everywhere, or a cloud
	// platform in the AWS account the entries name.
	place                    int
	places                   *widget.Select
	choosing                 bool
	profile, region, cluster *widget.Entry
	runner                   *widget.Entry
	account                  *fyne.Container
}

// The runner and account a cloud look was last made for.
const (
	prefCloudRunner  = "cloud-runner"
	prefCloudProfile = "cloud-profile"
	prefCloudRegion  = "cloud-region"
	prefCloudCluster = "cloud-cluster"
)

// Where the Targets screen looks.
const (
	placeDocker = iota
	placeDockerAll
	placeEC2
	placeECS
)

var placeNames = []string{"Docker: this context", "Docker: every context", "EC2", "ECS"}

func newTargetsView(runsDir string, call cliCall, openRun func(string)) *targetsView {
	v := &targetsView{
		runsDir: runsDir, call: call, openRun: openRun, picked: -1,
		async: func(f func()) { go f() }, onUI: fyne.Do,
		count: widget.NewLabel(""), message: message(),
		profile: widget.NewEntry(), region: widget.NewEntry(), cluster: widget.NewEntry(),
		runner: widget.NewEntry(),
	}
	// Cloud targets are labelled with the runner that started them, and a
	// person can run under more than one name - Windows' user and WSL's on
	// one machine. The name last looked for is remembered; it narrows the
	// look, and removal still needs the run's own record.
	v.runner.SetText(runnerid.Runner())
	if last := fyne.CurrentApp().Preferences().String(prefCloudRunner); last != "" {
		v.runner.SetText(last)
	}
	v.runner.SetPlaceHolder("runner, user@host")
	// The account is connection setup, like a path: the same variables the
	// CLI reads fill it in, then the account last looked in.
	prefs := fyne.CurrentApp().Preferences()
	v.profile.SetText(firstSet(os.Getenv("TESTEASE_AWS_PROFILE"), prefs.String(prefCloudProfile)))
	v.region.SetText(firstSet(os.Getenv("TESTEASE_AWS_REGION"), prefs.String(prefCloudRegion)))
	v.cluster.SetText(prefs.String(prefCloudCluster))
	v.profile.SetPlaceHolder("AWS profile")
	v.region.SetPlaceHolder("region")
	v.cluster.SetPlaceHolder("ECS cluster")
	for _, e := range []*widget.Entry{v.profile, v.region, v.cluster, v.runner} {
		e.OnSubmitted = func(string) { v.reload() }
	}
	v.places = widget.NewSelect(placeNames, func(name string) {
		for i, n := range placeNames {
			if n == name && !v.choosing {
				v.choose(i)
			}
		}
	})
	v.choosing = true
	v.places.SetSelectedIndex(placeDocker)
	v.choosing = false
	v.account = container.NewGridWithColumns(5, v.profile, v.region, v.runner, v.cluster,
		widget.NewButtonWithIcon("Look", theme.SearchIcon(), v.reload))
	v.account.Hide()
	v.table = listing(func() int { return len(v.all) }, targetTitles, targetWidths, tcolTarget, v.cell, nil)
	v.table.OnSelected = func(id widget.TableCellID) { v.pick(id.Row) }

	v.shell = widget.NewLabelWithStyle(pickHint, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	v.shell.Selectable = true
	v.shell.Truncation = fyne.TextTruncateEllipsis
	v.copy = widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if v.picked >= 0 && v.picked < len(v.all) {
			fyne.CurrentApp().Clipboard().SetContent(v.all[v.picked].Shell)
		}
	})
	v.copy.Disable()
	v.openRec = widget.NewButtonWithIcon("Open run", theme.NavigateNextIcon(), func() {
		if dir := v.recordOf(v.picked); dir != "" {
			v.openRun(dir)
		}
	})
	v.openRec.Disable()

	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), v.reload)
	v.remove = widget.NewButtonWithIcon("Remove kept targets…", theme.DeleteIcon(), v.removeKept)
	picked := container.NewBorder(nil, nil, nil, container.NewHBox(v.copy, v.openRec), v.shell)
	v.content = container.NewBorder(
		container.NewVBox(screenTitle("Targets", v.count,
			container.NewGridWrap(fyne.NewSize(230, v.places.MinSize().Height), v.places), v.remove, refresh),
			container.NewPadded(v.account), widget.NewSeparator(), v.message),
		container.NewVBox(widget.NewSeparator(), container.NewPadded(picked),
			note("A run that did not finish cleanly keeps its targets, because a half-changed box answers questions the record does not. Remove kept targets runs testease reap, which removes only targets whose run is gone, along with the networks and secret workspaces those runs left, and asks first.")),
		nil, nil, v.table,
	)
	return v
}

const pickHint = "Pick a target to see how to get onto it."

func (v *targetsView) base() []string { return []string{"--runs-dir", v.runsDir} }

// choose looks somewhere else.
func (v *targetsView) choose(place int) {
	v.place = place
	v.choosing = true
	v.places.SetSelectedIndex(place)
	v.choosing = false
	if v.cloud() {
		v.account.Show()
		if place == placeECS {
			v.cluster.Show()
		} else {
			v.cluster.Hide()
		}
	} else {
		v.account.Hide()
	}
	v.lastNote = ""
	v.reload()
}

func (v *targetsView) cloud() bool { return v.place == placeEC2 || v.place == placeECS }

// missing says what a cloud look needs that is not filled in.
func (v *targetsView) missing() string {
	blank := func(e *widget.Entry) bool { return strings.TrimSpace(e.Text) == "" }
	switch {
	case v.place == placeEC2 && (blank(v.profile) || blank(v.region)):
		return "EC2 needs an AWS profile and a region to look in."
	case v.place == placeECS && (blank(v.profile) || blank(v.region) || blank(v.cluster)):
		return "ECS needs an AWS profile, a region and a cluster to look in."
	}
	return ""
}

// reapArgs asks reap about the place chosen. Docker targets are listed by
// testease targets; cloud ones only reap can see, and its look removes nothing.
func (v *targetsView) reapArgs(force bool) []string {
	args := append(v.base(), "reap")
	switch v.place {
	case placeDockerAll:
		args = append(args, "--all-contexts")
	case placeEC2, placeECS:
		prefs := fyne.CurrentApp().Preferences()
		platform := map[int]string{placeEC2: "ec2", placeECS: "ecs"}[v.place]
		profile, region := strings.TrimSpace(v.profile.Text), strings.TrimSpace(v.region.Text)
		args = append(args, "--platform", platform, "--aws-profile", profile, "--aws-region", region)
		prefs.SetString(prefCloudProfile, profile)
		prefs.SetString(prefCloudRegion, region)
		if r := strings.TrimSpace(v.runner.Text); r != "" {
			args = append(args, "--runner", r)
			prefs.SetString(prefCloudRunner, r)
		}
		if v.place == placeECS {
			cluster := strings.TrimSpace(v.cluster.Text)
			args = append(args, "--ecs-cluster", cluster)
			prefs.SetString(prefCloudCluster, cluster)
		}
	}
	args = append(args, "--output", "json")
	if force {
		args = append(args, "--force")
	}
	return args
}

func (v *targetsView) listArgs() []string {
	if v.cloud() {
		return v.reapArgs(false)
	}
	args := append(v.base(), "targets", "--output", "json")
	if v.place == placeDockerAll {
		args = append(args, "--all-contexts")
	}
	return args
}

// fromReap is reap's look as rows: what each is and why reap would or would
// not remove it.
func fromReap(r reaped) []standing {
	out := make([]standing, 0, len(r.Targets))
	for _, t := range r.Targets {
		out = append(out, standing{Name: t.Name, RunID: t.RunID, Created: t.Created, Fate: t.Fate, Kind: t.Kind, Reason: t.Reason})
	}
	return out
}

// reload asks testease what is standing. Docker can be slow to answer, so the
// asking happens off the app's goroutine.
func (v *targetsView) reload() {
	// Each look supersedes the last: an answer from a place no longer
	// chosen, or one overtaken by a newer look, is dropped.
	v.looks++
	look := v.looks
	v.all = nil
	v.count.SetText("")
	v.table.Refresh()
	v.clearPick()
	if need := v.missing(); need != "" {
		v.all, v.notices = nil, nil
		showMessage(v.message, need)
		v.count.SetText(plural(0, "target"))
		v.table.Refresh()
		return
	}
	showMessage(v.message, "Asking testease what is standing…")
	args, cloud := v.listArgs(), v.cloud()
	v.async(func() {
		out, err := v.call(context.Background(), args)
		var got struct {
			Targets []standing `json:"targets"`
			Notices []string   `json:"notices"`
		}
		if err == nil && cloud {
			var r reaped
			if err = json.Unmarshal(out, &r); err == nil {
				got.Targets, got.Notices = fromReap(r), r.Notices
			}
		} else if err == nil {
			err = json.Unmarshal(out, &got)
		}
		v.onUI(func() {
			if look != v.looks {
				return
			}
			if err != nil {
				v.all = nil
				showMessage(v.message, "Could not list targets: "+err.Error())
			} else {
				v.all, v.notices = got.Targets, got.Notices
				v.showNotes()
			}
			v.count.SetText(plural(len(v.all), "target"))
			v.clearPick()
			v.table.UnselectAll()
			v.table.Refresh()
		})
	})
}

// showNotes says what the listing could not see, and anything the last
// removal said. A daemon that did not answer is not an empty daemon.
func (v *targetsView) showNotes() {
	var lines []string
	if v.lastNote != "" {
		lines = append(lines, v.lastNote)
	}
	switch {
	case len(v.all) == 0 && len(v.notices) == 0:
		lines = append(lines, "No targets standing.")
	case len(v.all) == 0 && v.place == placeDockerAll:
		// The contexts that answered had nothing; the rest are said below.
		lines = append(lines, "No targets standing in the contexts that answered.")
	case len(v.all) == 0:
		// The one place there was could not be looked at, so nothing found
		// is not nothing standing.
		lines = append(lines, "testease could not look:")
	}
	lines = append(lines, v.notices...)
	if len(lines) == 0 {
		v.message.Hide()
		return
	}
	showMessage(v.message, strings.Join(lines, "\n"))
}

// clearPick lets go of the picked target: after a new look it may be gone.
func (v *targetsView) clearPick() {
	v.picked = -1
	v.shell.SetText(pickHint)
	v.copy.Disable()
	v.openRec.Disable()
}

func (v *targetsView) pick(row int) {
	if row < 0 || row >= len(v.all) {
		return
	}
	v.picked = row
	t := v.all[row]
	if t.Shell == "" {
		// A cloud target has no shell to print; what there is to know is
		// why reap would or would not remove it.
		v.shell.SetText(orDash(t.Reason))
		v.copy.Disable()
	} else {
		v.shell.SetText(t.Shell)
		v.copy.Enable()
	}
	if v.recordOf(row) != "" {
		v.openRec.Enable()
	} else {
		v.openRec.Disable()
	}
}

func (v *targetsView) recordOf(row int) string {
	if row < 0 || row >= len(v.all) {
		return ""
	}
	id := v.all[row].RunID
	if id == "" || !filepath.IsLocal(id) {
		return ""
	}
	dir := filepath.Join(v.runsDir, id)
	if _, err := os.Stat(filepath.Join(dir, record.EventsFile)); err != nil {
		return ""
	}
	return dir
}

// removeKept asks reap what it would remove and why, shows that, and runs
// reap --force only on a yes.
func (v *targetsView) removeKept() {
	if v.busy {
		return
	}
	if need := v.missing(); need != "" {
		showMessage(v.message, need)
		return
	}
	v.busy = true
	v.lastNote = ""
	showMessage(v.message, "Asking testease reap what it may remove…")
	args := v.reapArgs(false)
	v.async(func() {
		out, err := v.call(context.Background(), args)
		var preview reaped
		if err == nil {
			err = json.Unmarshal(out, &preview)
		}
		v.onUI(func() {
			v.busy = false
			if err != nil {
				showMessage(v.message, "Could not ask reap: "+err.Error())
				return
			}
			var lines []string
			for _, t := range preview.Targets {
				if t.Fate == "orphaned" {
					lines = append(lines, fmt.Sprintf("%s %s: %s", t.Kind, t.Name, t.Reason))
				}
			}
			if len(lines) == 0 {
				v.lastNote = "Nothing is safe to remove: reap removes a target only when its run is gone."
				v.showNotes()
				return
			}
			msg := fmt.Sprintf("testease reap would remove %s:\n\n%s\n\nRemove %s?",
				plural(len(lines), "item"), strings.Join(lines, "\n"), map[bool]string{true: "it", false: "them"}[len(lines) == 1])
			v.showNotes()
			v.ask("Remove kept targets?", msg, v.reap)
		})
	})
}

func (v *targetsView) reap() {
	v.busy = true
	showMessage(v.message, "Removing…")
	args := v.reapArgs(true)
	v.async(func() {
		out, err := v.call(context.Background(), args)
		var done reaped
		if err == nil {
			err = json.Unmarshal(out, &done)
		}
		v.onUI(func() {
			v.busy = false
			switch {
			case err != nil:
				v.lastNote = "Reap failed: " + err.Error()
			default:
				v.lastNote = fmt.Sprintf("Removed %d: %s", len(done.Removed), strings.Join(done.Removed, ", "))
				if len(done.Notices) > 0 {
					v.lastNote += "\n" + strings.Join(done.Notices, "\n")
				}
			}
			v.reload()
		})
	})
}

func (v *targetsView) cell(row, col int) (string, pillKind, bool) {
	if row < 0 || row >= len(v.all) {
		return "", pillNeutral, false
	}
	t := v.all[row]
	switch col {
	case tcolTarget:
		return t.Name, pillNeutral, false
	case tcolHost:
		host := t.Host
		if host == "" {
			host = t.Kind
		}
		if t.Cell != "" {
			host = t.Cell + " · " + host
		}
		return orDash(host), pillNeutral, false
	case tcolRunID:
		return orDash(t.RunID), pillNeutral, false
	case tcolFate:
		return fateWords(t.Fate)
	case tcolCreated:
		return orDash(t.Created), pillNeutral, false
	}
	return "", pillNeutral, false
}

// fateWords says what reap's word for a target means to a person deciding
// whether to clean up.
func fateWords(fate string) (string, pillKind, bool) {
	switch fate {
	case "orphaned":
		return "kept, run gone", pillNeutral, true
	case "live":
		return "run still going", pillActive, true
	case "foreign":
		return "another user's", pillNeutral, true
	case "unprovable":
		return "owner unknown", pillBad, true
	default:
		return orDash(fate), pillNeutral, true
	}
}

func (v *targetsView) cellText(row, col int) string {
	text, _, _ := v.cell(row, col)
	return text
}

// firstSet is the first of values that is not blank.
func firstSet(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
