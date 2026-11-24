package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// Targets are what runs left standing: a failed or interrupted run keeps its
// box for inspection. The app asks testease what is standing and, to clean up,
// asks testease reap, which removes only what it can prove nobody is using.
// It never removes a target by itself.

const standingJSON = `{
  "targets": [
    {"name": "testease-sleepy-20261124T052621Z-box", "where": "context default", "run": "sleepy-20261124T052621Z",
     "host": "box", "backend": "docker", "created": "2026-11-24T05:26:21Z", "fate": "orphaned",
     "shell": "docker exec -it testease-sleepy-20261124T052621Z-box bash"},
    {"name": "testease-hello-20261124T060000Z-box", "where": "context default", "run": "hello-20261124T060000Z",
     "host": "box", "backend": "docker", "created": "2026-11-24T06:00:00Z", "fate": "live",
     "shell": "docker exec -it testease-hello-20261124T060000Z-box bash"}
  ],
  "notices": ["skipped context remote: daemon not answering"]
}`

const reapPreviewJSON = `{
  "targets": [
    {"kind": "container", "fate": "orphaned", "where": "context default", "name": "testease-sleepy-20261124T052621Z-box",
     "run": "sleepy-20261124T052621Z", "reason": "its run (pid 4242) is gone"},
    {"kind": "network", "fate": "orphaned", "where": "context default", "name": "testease-sleepy-20261124T052621Z",
     "run": "sleepy-20261124T052621Z", "reason": "its run (pid 4242) is gone"},
    {"kind": "container", "fate": "live", "where": "context default", "name": "testease-hello-20261124T060000Z-box",
     "run": "hello-20261124T060000Z", "reason": "its run (pid 777) is still going"}
  ],
  "removable": 2, "removed": [], "notices": []
}`

const reapForceJSON = `{
  "targets": [], "removable": 2,
  "removed": ["testease-sleepy-20261124T052621Z-box", "testease-sleepy-20261124T052621Z"],
  "notices": []
}`

// fakeCLI answers testease commands from canned output and remembers what it
// was asked.
type fakeCLI struct {
	asked   [][]string
	answers map[string]string
}

func (f *fakeCLI) call(_ context.Context, args []string) ([]byte, error) {
	f.asked = append(f.asked, args)
	key := strings.Join(args, " ")
	// The longest matching key wins, so "reap --force" beats "reap" whatever
	// order the map gives.
	best, found := "", false
	for k := range f.answers {
		if strings.Contains(key, k) && len(k) >= len(best) {
			best, found = k, true
		}
	}
	if found {
		return []byte(f.answers[best]), nil
	}
	return []byte(`{"targets": [], "notices": []}`), nil
}

func newFakeTargets(t *testing.T, cli *fakeCLI) *targetsView {
	t.Helper()
	v := newTargetsView("/srv/runs", cli.call, func(string) {})
	v.async = func(f func()) { f() }
	v.onUI = func(f func()) { f() }
	v.reload()
	return v
}

func TestTheTargetsScreenListsWhatTesteaseSaysIsStanding(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)

	if want := []string{"--runs-dir", "/srv/runs", "targets", "--output", "json"}; !slices.Equal(cli.asked[0], want) {
		t.Errorf("asked %q, want %q", cli.asked[0], want)
	}
	want := map[int]string{
		tcolTarget: "testease-sleepy-20261124T052621Z-box",
		tcolHost:   "box",
		tcolRunID:  "sleepy-20261124T052621Z",
		tcolFate:   "kept, run gone",
	}
	for col, text := range want {
		if got := v.cellText(0, col); got != text {
			t.Errorf("column %d = %q, want %q", col, got, text)
		}
	}
	if got := v.cellText(1, tcolFate); got != "run still going" {
		t.Errorf("live fate = %q", got)
	}
	if !v.message.Visible() || !strings.Contains(v.message.Text, "daemon not answering") {
		t.Errorf("a daemon that did not answer is not shown as empty: %q", v.message.Text)
	}
}

func TestPickingATargetShowsHowToGetOntoIt(t *testing.T) {
	test.NewTempApp(t)

	v := newFakeTargets(t, &fakeCLI{answers: map[string]string{"targets": standingJSON}})
	v.pick(0)
	if got := v.shell.Text; got != "docker exec -it testease-sleepy-20261124T052621Z-box bash" {
		t.Errorf("shell = %q", got)
	}
}

// Removing is reap's call: the app shows what reap would remove and why, asks,
// and only then runs reap --force.
func TestRemovingKeptTargetsShowsWhatReapWouldRemoveThenAsks(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{
		"json --force": reapForceJSON,
		"reap":         reapPreviewJSON,
		"targets":      standingJSON,
	}}
	v := newFakeTargets(t, cli)
	var question string
	v.ask = func(_, msg string, yes func()) { question = msg; yes() }

	v.removeKept()

	if !strings.Contains(question, "testease-sleepy-20261124T052621Z-box") || !strings.Contains(question, "its run (pid 4242) is gone") {
		t.Errorf("question = %q, want each target and why reap may remove it", question)
	}
	if strings.Contains(question, "testease-hello") {
		t.Errorf("question = %q offers a target whose run is still going", question)
	}
	var reaps [][]string
	for _, a := range cli.asked {
		if slices.Contains(a, "reap") {
			reaps = append(reaps, a)
		}
	}
	if len(reaps) != 2 || slices.Contains(reaps[0], "--force") || !slices.Contains(reaps[1], "--force") {
		t.Fatalf("reap asked %q, want a look then --force", reaps)
	}
	if !strings.Contains(v.message.Text, "Removed 2") {
		t.Errorf("message = %q", v.message.Text)
	}
}

func TestNothingRemovableIsSaidWithoutAsking(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"reap": `{"targets": [], "removable": 0, "removed": [], "notices": []}`}}
	v := newFakeTargets(t, cli)
	v.ask = func(string, string, func()) { t.Error("asked to remove nothing") }

	v.removeKept()
	for _, a := range cli.asked {
		if slices.Contains(a, "--force") {
			t.Errorf("ran %q", a)
		}
	}
	if !strings.Contains(v.message.Text, "Nothing") {
		t.Errorf("message = %q", v.message.Text)
	}
}

// Saying no puts the screen back as it was, not stuck on the question.
func TestSayingNoToRemovingLeavesNoQuestionShowing(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"reap": reapPreviewJSON, "targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.ask = func(string, string, func()) {}

	v.removeKept()
	if strings.Contains(v.message.Text, "Asking") {
		t.Errorf("message = %q after no", v.message.Text)
	}
}

// Once the list is asked again, a target picked before may be gone, so the
// pick is let go with it.
func TestReloadingLetsGoOfThePickedTarget(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.pick(0)
	cli.answers["targets"] = `{"targets": [], "notices": []}`
	v.reload()

	if strings.Contains(v.shell.Text, "docker exec") {
		t.Errorf("shell = %q after the target went", v.shell.Text)
	}
	if !v.copy.Disabled() || !v.openRec.Disabled() {
		t.Error("Copy or Open run still enabled with nothing picked")
	}
}

const ec2PreviewJSON = `{
  "targets": [
    {"kind": "ec2-instance", "fate": "orphaned", "where": "ec2", "name": "i-0abc", "run": "fleet-20261124T010000Z",
     "created": "2026-11-24T01:00:00Z", "reason": "the record released it and no run holds it"},
    {"kind": "ec2-instance", "fate": "unprovable", "where": "ec2", "name": "i-0def",
     "reason": "resource lacks a complete testease identity"}
  ],
  "removable": 1, "removed": [], "notices": []
}`

func ec2Targets(t *testing.T, cli *fakeCLI) *targetsView {
	t.Helper()
	v := newFakeTargets(t, cli)
	v.profile.SetText("lab")
	v.region.SetText("us-east-2")
	v.runner.SetText("josh@box")
	v.choose(placeEC2)
	return v
}

// EC2 and ECS targets are listed by reap's own look, which says of each
// whether it may be removed and why; nothing is removed by looking.
func TestEC2TargetsAreListedByReapsLook(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON, "--platform ec2": ec2PreviewJSON}}
	v := ec2Targets(t, cli)

	last := cli.asked[len(cli.asked)-1]
	want := []string{"--runs-dir", "/srv/runs", "reap", "--platform", "ec2", "--aws-profile", "lab", "--aws-region", "us-east-2", "--runner", "josh@box", "--output", "json"}
	if !slices.Equal(last, want) {
		t.Errorf("asked %q, want %q", last, want)
	}
	if len(v.all) != 2 || v.cellText(0, tcolTarget) != "i-0abc" || v.cellText(0, tcolHost) != "ec2-instance" {
		t.Fatalf("listed %+v", v.all)
	}
	if got := v.cellText(1, tcolFate); got != "owner unknown" {
		t.Errorf("fate = %q", got)
	}
	v.pick(1)
	if !strings.Contains(v.shell.Text, "lacks a complete testease identity") || !v.copy.Disabled() {
		t.Errorf("picked shows %q, copy disabled %v; want reap's reason and nothing to copy", v.shell.Text, v.copy.Disabled())
	}
}

func TestRemovingEC2TargetsAsksThenForcesWithTheSameAccount(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{
		"targets": standingJSON, "--platform ec2": ec2PreviewJSON,
		"--platform ec2 --aws-profile lab --aws-region us-east-2 --runner josh@box --output json --force": `{"targets": [], "removable": 1, "removed": ["i-0abc"], "notices": []}`,
	}}
	v := ec2Targets(t, cli)
	var question string
	v.ask = func(_, msg string, yes func()) { question = msg; yes() }
	v.removeKept()

	if !strings.Contains(question, "i-0abc") || strings.Contains(question, "i-0def") {
		t.Errorf("question = %q, want only the instance reap may remove", question)
	}
	force := cli.asked[len(cli.asked)-2]
	if !slices.Contains(force, "--force") || !slices.Contains(force, "lab") || !slices.Contains(force, "ec2") {
		t.Errorf("forced with %q", force)
	}
	if !strings.Contains(v.message.Text, "Removed 1: i-0abc") {
		t.Errorf("message = %q", v.message.Text)
	}
}

// Without an account to look in, nothing is asked: reap would refuse anyway.
func TestEC2WithoutAnAccountSaysWhatItNeeds(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	before := len(cli.asked)
	v.profile.SetText("")
	v.region.SetText("")
	v.choose(placeECS)
	if len(cli.asked) != before {
		t.Errorf("asked %q without an account", cli.asked[before:])
	}
	if !strings.Contains(v.message.Text, "AWS profile") || !strings.Contains(v.message.Text, "cluster") {
		t.Errorf("message = %q", v.message.Text)
	}
	if len(v.all) != 0 {
		t.Errorf("still listing %d docker targets under ECS", len(v.all))
	}
}

func TestEveryDockerContextIsAskedOfBothTargetsAndReap(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON, "reap": reapPreviewJSON}}
	v := newFakeTargets(t, cli)
	v.choose(placeDockerAll)
	v.ask = func(string, string, func()) {}
	v.removeKept()
	for _, verb := range []string{"targets", "reap"} {
		found := false
		for _, a := range cli.asked {
			if slices.Contains(a, verb) && slices.Contains(a, "--all-contexts") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not asked about every context: %q", verb, cli.asked)
		}
	}
}

func TestECSAsksForItsCluster(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.profile.SetText("lab")
	v.region.SetText("us-east-2")
	v.cluster.SetText("fleet")
	v.choose(placeECS)
	last := cli.asked[len(cli.asked)-1]
	if !slices.Contains(last, "ecs") || !slices.Contains(last, "--ecs-cluster") || !slices.Contains(last, "fleet") {
		t.Errorf("asked %q", last)
	}
}

// A look that failed is not a look that found nothing.
func TestALookThatFailedDoesNotClaimNothingIsStanding(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{
		"targets":        standingJSON,
		"--platform ec2": `{"targets": [], "removable": 0, "removed": [], "notices": ["could not list ec2 targets: Token has expired"]}`,
	}}
	v := ec2Targets(t, cli)
	if strings.Contains(v.message.Text, "No targets standing") {
		t.Errorf("message = %q claims nothing is standing", v.message.Text)
	}
	if !strings.Contains(v.message.Text, "Token has expired") {
		t.Errorf("message = %q, want why the look failed", v.message.Text)
	}
}

// Changing where to look while a slow look is still out drops its answer:
// docker's targets must not show up under EC2.
func TestAnAnswerFromWhereYouNoLongerLookIsDropped(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON, "--platform ec2": ec2PreviewJSON}}
	v := newTargetsView("/srv/runs", cli.call, func(string) {})
	var pending []func()
	v.async = func(f func()) { pending = append(pending, f) }
	v.onUI = func(f func()) { f() }
	v.profile.SetText("lab")
	v.region.SetText("us-east-2")

	v.reload()
	v.choose(placeEC2)
	if len(v.all) != 0 {
		t.Fatalf("still showing %d rows from the last place", len(v.all))
	}
	for _, f := range pending {
		f()
	}
	if len(v.all) != 2 || v.all[0].Name != "i-0abc" {
		t.Errorf("showing %+v, want only EC2's answer", v.all)
	}
}

// In every context, the ones that answered with nothing are nothing standing;
// the ones that did not answer are said beside it.
func TestEveryContextSaysTheOnesThatAnsweredHaveNothingStanding(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": `{"targets": [], "notices": ["skipped docker:dockersrv: no answer within 30s"]}`}}
	v := newFakeTargets(t, cli)
	v.choose(placeDockerAll)
	if !strings.Contains(v.message.Text, "No targets standing in the contexts that answered") || !strings.Contains(v.message.Text, "dockersrv") {
		t.Errorf("message = %q", v.message.Text)
	}
}

// While a look is out, the count of the last one is not shown as this one's.
func TestTheCountIsClearedWhileLooking(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.async = func(func()) {}
	v.reload()
	if v.count.Text != "" {
		t.Errorf("count = %q while looking", v.count.Text)
	}
}

// A cloud look is of one runner's targets. It starts as this one, and can be
// pointed at another name the same person runs under - WSL's user beside
// Windows' - which is remembered for next time. Proof of a run still comes
// from its record, so naming a runner offers nothing reap could not prove.
func TestTheCloudRunnerStartsAsThisOneAndIsRemembered(t *testing.T) {
	test.NewTempApp(t)

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	if v.runner.Text == "" || !strings.Contains(v.runner.Text, "@") {
		t.Errorf("runner starts as %q, want this runner's user@host", v.runner.Text)
	}
	v.profile.SetText("lab")
	v.region.SetText("us-east-2")
	v.runner.SetText("jmd9qs@atreides-ghola1")
	v.choose(placeEC2)

	again := newFakeTargets(t, cli)
	if again.runner.Text != "jmd9qs@atreides-ghola1" {
		t.Errorf("a new screen starts with runner %q", again.runner.Text)
	}
}

// The account a cloud look was made in is remembered like a chosen folder:
// TESTEASE_AWS_* still come first, as they do for the CLI.
func TestTheCloudAccountIsRememberedUnlessTheEnvironmentNamesOne(t *testing.T) {
	test.NewTempApp(t)
	t.Setenv("TESTEASE_AWS_PROFILE", "")
	t.Setenv("TESTEASE_AWS_REGION", "")

	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.profile.SetText("lab")
	v.region.SetText("us-east-2")
	v.cluster.SetText("runs")
	v.choose(placeECS)

	again := newFakeTargets(t, cli)
	if again.profile.Text != "lab" || again.region.Text != "us-east-2" || again.cluster.Text != "runs" {
		t.Errorf("a new screen starts with %q %q %q", again.profile.Text, again.region.Text, again.cluster.Text)
	}

	t.Setenv("TESTEASE_AWS_PROFILE", "from-env")
	t.Setenv("TESTEASE_AWS_REGION", "eu-west-1")
	fromEnv := newFakeTargets(t, cli)
	if fromEnv.profile.Text != "from-env" || fromEnv.region.Text != "eu-west-1" {
		t.Errorf("with the environment set, the screen starts with %q %q", fromEnv.profile.Text, fromEnv.region.Text)
	}
}

// A value the environment supplied is the environment's to keep: it is not
// remembered as though it had been typed.
func TestAnAccountFromTheEnvironmentIsNotRemembered(t *testing.T) {
	test.NewTempApp(t)
	t.Setenv("TESTEASE_AWS_PROFILE", "")
	t.Setenv("TESTEASE_AWS_REGION", "eu-west-1")
	cli := &fakeCLI{answers: map[string]string{"targets": standingJSON}}
	v := newFakeTargets(t, cli)
	v.profile.SetText("lab")
	v.choose(placeEC2)
	if got := fyne.CurrentApp().Preferences().String(prefCloudRegion); got != "" {
		t.Errorf("remembered region %q from the environment", got)
	}
	if got := fyne.CurrentApp().Preferences().String(prefCloudProfile); got != "lab" {
		t.Errorf("remembered profile %q", got)
	}
}
