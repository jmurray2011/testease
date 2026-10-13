package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/testease/internal/doctor"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/reap"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
)

// The exit codes are the integration. Jenkins mode is nothing else, so a code
// that means one thing on Tuesday and another after a refactor is the whole
// contract quietly changing under whoever automated against it.

func TestHelpIncludesFirstRunPath(t *testing.T) {
	root := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"Quick start:",
		"make build",
		"./testease doctor hello --scenario-root examples",
		"./testease run hello --scenario-root examples",
		"./testease judge",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help does not contain %q", want)
		}
	}
}

func TestUnknownOutputFormatIsRejected(t *testing.T) {
	root := newRoot()
	root.SetArgs([]string{"backends", "--output", "yaml"})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--output must be one of") {
		t.Fatalf("error = %v, want an output-format usage error", err)
	}
}

func TestRenderAcceptsHTMLOutput(t *testing.T) {
	cmd := renderCmd()
	if err := cmd.Flags().Set("output", "html"); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputFormat(cmd); err != nil {
		t.Fatal(err)
	}
}

func TestHelpListsEveryOperatorEnvironmentDefault(t *testing.T) {
	root := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"TESTEASE_SCENARIO_ROOT",
		"TESTEASE_RUNS_DIR",
		"TESTEASE_BASELINES",
		"TESTEASE_FINDINGS",
		"TESTEASE_RUNS_REPO",
		"TESTEASE_RUNS_URL",
		"TESTEASE_STATEFILE_DIR",
		"TESTEASE_TARGET_CATALOG",
		"TESTEASE_CAMPAIGN_ROOT",
		"TESTEASE_INPUT_BUNDLE",
		"TESTEASE_LOG_LEVEL",
		"TESTEASE_LOG_FORMAT",
		"TESTEASE_STORE",
		"TESTEASE_DOCKER_CONTEXT",
		"TESTEASE_WIN_DOCKER_CONTEXT",
		"TESTEASE_INSTRUMENT_NAME",
		"TESTEASE_INSTRUMENT_COLLECT",
		"TESTEASE_INSTRUMENT_DIFF",
		"TESTEASE_WIN_MEMORY",
		"TESTEASE_AWS_PROFILE",
		"TESTEASE_AWS_REGION",
		"TESTEASE_SSM_TRANSFER_STORE",
		"TESTEASE_WINRM_USER",
		"TESTEASE_WINRM_PASSWORD_FILE",
		"TESTEASE_SSH_USER",
		"TESTEASE_SSH_PASSWORD_FILE",
		"TESTEASE_SSH_KEY",
		"TESTEASE_SSH_KNOWN_HOSTS",
		"TESTEASE_ENV_FILE",
		"TESTEASE_COMPARE",
	} {
		if !strings.Contains(output.String(), name) {
			t.Errorf("root help does not list %s", name)
		}
	}
}

func TestHelpSeparatesCoreAndAdvancedCommands(t *testing.T) {
	root := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	help := output.String()
	core := strings.Index(help, "Common commands:")
	advanced := strings.Index(help, "Other commands:")
	if core < 0 || advanced < 0 || core >= advanced {
		t.Fatalf("help group headings are missing or out of order:\n%s", help)
	}
	for _, command := range []string{"validate", "plan", "doctor", "run", "judge", "render", "scenarios"} {
		if index := strings.Index(help[core:advanced], "  "+command); index < 0 {
			t.Errorf("core command %q is not in the core group", command)
		}
	}
	for _, command := range []string{"campaign", "package", "publish", "reproduce"} {
		if index := strings.Index(help[advanced:], "  "+command); index < 0 {
			t.Errorf("advanced command %q is not in the advanced group", command)
		}
	}
}

func TestEveryFailureHasItsOwnCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nothing wrong", nil, exitOK},
		{
			"a precondition, which the person running it fixes",
			&runner.PreconditionError{Message: "no image", Remedy: "docker pull"},
			exitPrecondition,
		},
		{
			"a record another writer holds",
			&record.LockedError{Dir: "/runs/x", PID: 41922},
			exitIntegrity,
		},
		{
			"a judge disagreeing with what it said last time",
			&judge.DisagreesError{Path: "verdicts/expect@1.json", Judge: "expect", Version: "1"},
			exitIntegrity,
		},
		{
			"a corpus citing what nobody can reach",
			&corpusError{missing: 1},
			exitIntegrity,
		},
		{
			"a verdict outside the judge's pass set",
			&verdictError{verdict: judge.Regressed, judge: "expect", version: "1"},
			exitVerdict,
		},
		{
			// Nobody could check it, which is not the product failing.
			"a verdict nobody could reach",
			&verdictError{verdict: judge.Unverified, judge: "expect", version: "1"},
			exitUnjudgeable,
		},
		{
			"a judge with nothing to judge",
			&judge.NothingToJudgeError{Judge: "expect"},
			exitUnjudgeable,
		},
		{
			"a run whose probe died",
			&outcomeError{status: record.StatusProbeFail, runID: "r"},
			exitProbeFail,
		},
		{
			"a run whose substrate died",
			&outcomeError{status: record.StatusInfraFail, runID: "r"},
			exitInfraFail,
		},
		{
			"a run somebody stopped",
			&outcomeError{status: record.StatusInterrupted, runID: "r"},
			exitInterrupted,
		},
		{"anything else is the caller's mistake", errors.New("what"), exitUsage},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := codeFor(c.err); got != c.want {
				t.Errorf("codeFor(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

func TestAWrappedErrorStillFindsItsCode(t *testing.T) {
	t.Parallel()

	// Errors travel up through layers that add context. A code that only
	// worked on a bare error would be a code that stopped working the first
	// time somebody wrapped one.
	wrapped := fmt.Errorf("resolve host %q: %w", "box",
		&runner.PreconditionError{Message: "no image"})

	if got := codeFor(wrapped); got != exitPrecondition {
		t.Errorf("codeFor(wrapped) = %d, want %d", got, exitPrecondition)
	}
}

func TestARecordedInfrastructureFailureGetsAnInfrastructureCode(t *testing.T) {
	t.Parallel()

	cause := errors.New("docker daemon unavailable")
	err := runError(&runner.Outcome{
		RunID:  "hello-20260926T201934Z",
		Status: record.StatusInfraFail,
	}, cause)

	if got := codeFor(err); got != exitInfraFail {
		t.Fatalf("codeFor(%v) = %d, want %d", err, got, exitInfraFail)
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("error %q does not retain cause %q", err, cause)
	}
}

func TestInfrastructureFailureSuggestsRunningDoctor(t *testing.T) {
	err := runErrorWithRemedy(&runner.Outcome{
		RunID:  "hello-20260926T201934Z",
		Status: record.StatusInfraFail,
	}, errors.New("docker daemon unavailable"), "testease doctor hello --scenario-root examples")

	if !strings.Contains(err.Error(), "try: testease doctor hello --scenario-root examples") {
		t.Fatalf("error %q does not suggest doctor", err)
	}
}

func TestRunReportUsesTheRecordedPathForJudging(t *testing.T) {
	cmd := runCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := report(cmd, &scenario.Manifest{Judge: &scenario.JudgeRef{Name: "expect", Version: "3"}}, &runner.Outcome{
		RunID:    "hello-20260926T201934Z",
		Dir:      "/tmp/custom-runs/hello-20260926T201934Z",
		Status:   record.StatusMeasured,
		Total:    1,
		Measured: 1,
	}, nil); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stderr.String(), "testease judge /tmp/custom-runs/hello-20260926T201934Z") {
		t.Fatalf("report does not suggest the recorded path: %q", stderr.String())
	}
}

func TestRunReportDoesNotSuggestJudgingAnUnmeasuredFailure(t *testing.T) {
	cmd := runCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := report(cmd, &scenario.Manifest{Judge: &scenario.JudgeRef{Name: "expect", Version: "3"}}, &runner.Outcome{
		RunID:  "hello-20260926T201934Z",
		Dir:    "/tmp/custom-runs/hello-20260926T201934Z",
		Status: record.StatusInfraFail,
	}, errors.New("docker unavailable")); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(stderr.String(), "testease judge") {
		t.Fatalf("report suggested judging an unmeasured failure: %q", stderr.String())
	}
}

func TestEveryCodeIsItsOwnNumber(t *testing.T) {
	t.Parallel()

	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok": exitOK, "usage": exitUsage, "precondition": exitPrecondition,
		"integrity": exitIntegrity, "infra-fail": exitInfraFail,
		"probe-fail": exitProbeFail, "interrupted": exitInterrupted,
		"verdict": exitVerdict, "unjudgeable": exitUnjudgeable,
	} {
		if other, clash := seen[code]; clash {
			t.Errorf("%s and %s are both %d", name, other, code)
		}
		seen[code] = name
	}
}

func TestOnlyAPassIsAPass(t *testing.T) {
	t.Parallel()

	// Never invent success. Anything that is not one of the words a judge uses
	// for "no worse than before" has to fail the check, including words no
	// judge has yet.
	for _, verdict := range []string{judge.Pass, judge.Promote, judge.Established, judge.Restored, judge.ReleaseReady} {
		if !passed(verdict) {
			t.Errorf("%q is a pass and did not read as one", verdict)
		}
	}
	for _, verdict := range []string{
		judge.Regressed, judge.Hold, judge.Unverified, judge.Unmet, "something new", "",
	} {
		if passed(verdict) {
			t.Errorf("%q read as a pass", verdict)
		}
	}
}

func TestARunIdIsNotAPath(t *testing.T) {
	t.Parallel()

	// Both are what somebody has to hand, and a run id must not be mistaken
	// for a directory that happens to share its name.
	dir := t.TempDir()
	cmd := runCmd()
	cmd.Flags().String("runs-dir", dir, "")

	if got := recordDir(cmd, "some-run-20260812T140322Z"); got == "some-run-20260812T140322Z" {
		t.Errorf("recordDir = %q, want it resolved under the runs directory", got)
	}
}

func TestAWindowsOneShotCanUseADifferentDockerContext(t *testing.T) {
	t.Parallel()

	cmd := runCmd()
	if err := cmd.Flags().Set("docker-context", "local-linux"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("win-docker-context", "winhost"); err != nil {
		t.Fatal(err)
	}
	ordinary, windows := dockerClients(cmd, nil)
	if ordinary.Context != "local-linux" || windows.Context != "winhost" {
		t.Fatalf("docker contexts = ordinary %q, windows %q", ordinary.Context, windows.Context)
	}
	conditions := measurementConditions(cmd, nil)
	if conditions["docker.context"] != "local-linux" || conditions["win.docker_context"] != "winhost" {
		t.Fatalf("measurement conditions = %#v", conditions)
	}
}

// A remedy that names a command this tool does not have sends the reader to a
// second failure. The tree is walked rather than listed so the check cannot go
// stale.
func TestEveryRemedyNamesACommandThisToolHas(t *testing.T) {
	t.Parallel()

	root := newRoot()
	known := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			known[sub.Name()] = true
			walk(sub)
		}
	}
	walk(root)

	if len(known) == 0 {
		t.Fatal("no commands found: this test would pass on anything")
	}

	for _, r := range append(runner.Remedies(), doctor.Remedies()...) {
		fields := strings.Fields(r)
		if len(fields) == 0 || fields[0] != "testease" {
			// Remedies may name any command; only this tool's are checkable here.
			continue
		}
		if len(fields) < 2 {
			t.Errorf("remedy %q says to run testease and not what", r)
			continue
		}
		if !known[fields[1]] {
			t.Errorf("remedy %q names %q, which is not a testease command; it has %s",
				r, fields[1], strings.Join(sortedNames(known), ", "))
		}
	}
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func TestAnEnvFileIsReadTheWayASuiteWroteIt(t *testing.T) {
	t.Parallel()

	// The point of the file is to aim testease at a suite's own .env rather than
	// transcribing it, so the shapes that file already has - comments, blanks,
	// quoted values, an `=` inside a value - have to survive the trip.
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(
		"# a comment\n\nAZURE_ENDPOINT=\"https://x.example/openai\"\n"+
			"AZURE_API_KEY='k=ey=with=equals'\n  SPACED = value \n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := runCmd()
	if err := cmd.Flags().Set("env-file", path); err != nil {
		t.Fatal(err)
	}
	got, err := driveSecrets(cmd)
	if err != nil {
		t.Fatalf("driveSecrets: %v", err)
	}

	for key, want := range map[string]string{
		"AZURE_ENDPOINT": "https://x.example/openai",
		"AZURE_API_KEY":  "k=ey=with=equals",
		"SPACED":         "value",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("read %d values, want 3: comments and blanks are not settings", len(got))
	}
}

func TestTargetProfileParamsAreDefaultsAndSelectionIsRecorded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	catalog := filepath.Join(dir, "targets.yaml")
	if err := os.WriteFile(catalog, []byte(`
schema: 1
targets:
  - name: lab-clone
    tags: [os:rhel9, posture:fips-stig]
    params: {backend: ssh, address: 192.0.2.10}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := runCmd()
	if err := cmd.ParseFlags([]string{"--target", "lab-clone", "--target-tag", "posture:fips-stig", "--target-catalog", catalog, "--param", "address=192.0.2.20"}); err != nil {
		t.Fatal(err)
	}
	params, selection, err := selectedParams(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if params["backend"] != "ssh" || params["address"] != "192.0.2.20" {
		t.Fatalf("params = %#v", params)
	}
	conditions := measurementConditions(cmd, selection)
	if conditions["target.name"] != "lab-clone" || len(conditions["target.catalog_sha256"]) != 64 || conditions["target.failure_policy"] != "retain" {
		t.Fatalf("conditions = %#v", conditions)
	}
	if err := cmd.Flags().Set("release-failed-targets", "true"); err != nil {
		t.Fatal(err)
	}
	if got := measurementConditions(cmd, selection)["target.failure_policy"]; got != "release" {
		t.Fatalf("target failure policy = %q, want release", got)
	}
}

func TestPinningAnAxisCollapsesItRatherThanContradictingIt(t *testing.T) {
	t.Parallel()

	// Pinning is a legitimate way to run or preflight one cell, so it collapses
	// the axis. A census cannot honour a param that is pinned and swept at once.
	m := &scenario.Manifest{Params: map[string]scenario.Param{
		"crossing": {Values: "a-to-b,b-to-c", Axis: true},
		"other":    {Values: "1,2", Axis: true},
	}}

	scope, err := axesToSweep(m, map[string]string{"crossing": "a-to-b"})
	if err != nil {
		t.Fatalf("axesToSweep: %v", err)
	}
	if _, still := scope["crossing"]; still {
		t.Error("a pinned axis is still swept, which the census refuses as a contradiction")
	}
	if got := scope["other"]; len(got) != 2 {
		t.Errorf("other = %v, want the axes nobody pinned to keep sweeping", got)
	}

	// Pinning every axis leaves nothing to sweep, which is a single run.
	scope, err = axesToSweep(m, map[string]string{"crossing": "a-to-b", "other": "1"})
	if err != nil {
		t.Fatalf("axesToSweep: %v", err)
	}
	if len(scope) != 0 {
		t.Errorf("axes = %v, want none: a fully pinned census is one run", scope)
	}
}

// Every read command takes -o json, emitting the same objects the record
// holds. A read command without it is a surface CI cannot use, discovered by
// whoever automated against it.
func TestEveryReadCommandSpeaksJSON(t *testing.T) {
	t.Parallel()

	// Commands that only mutate. Named rather than inferred, so adding one is a
	// decision somebody made on purpose.
	writesOnly := map[string]bool{"import": true}

	for _, c := range newRoot().Commands() {
		if writesOnly[c.Name()] || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		if c.Flags().Lookup("output") == nil {
			t.Errorf("%s has no -o json", c.Name())
		}
	}
}

func TestTheShellCommandCarriesTheContextItWasFoundIn(t *testing.T) {
	t.Parallel()

	// A target found on a remote daemon is not reachable by a bare docker exec,
	// and a command that does not work is worse than none.
	cases := []struct {
		where string
		want  string
	}{
		{"docker", "docker exec -it box bash"},
		{"docker:winhost", "docker --context winhost exec -it box bash"},
	}

	for _, c := range cases {
		got := shellFor(reap.Target{Name: "box", Where: c.where})
		if got != c.want {
			t.Errorf("shell for %q = %q, want %q", c.where, got, c.want)
		}
	}
}

// Only the facts view reads across records. Rendering the first run id and
// dropping the rest would answer a different question than the one asked, and
// silently: the output looks exactly like a correct render of one record.
func TestOnlyTheFactsViewTakesMoreThanOneRun(t *testing.T) {
	t.Parallel()

	for _, view := range []string{"tsv", "table", "rows", "meta"} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"render", "--view", view, "one", "two"})

		err := root.Execute()
		if err == nil {
			t.Errorf("%s accepted two run ids; rendered %q", view, out.String())
			continue
		}
		if !strings.Contains(err.Error(), view) {
			t.Errorf("%s: error = %v, want the view named", view, err)
		}
	}
}

// A sweep passed on the command line is the only way a rung list reaches a
// census, so the guard against sweeping an undiscovered @-value has to hold
// there. Swept literally it becomes a cell whose axis value is a pattern, and
// the record says "measured" about a glob nobody resolved.
func TestAnUndiscoveredAxisValueIsRefusedWhereverItArrives(t *testing.T) {
	t.Parallel()

	for _, from := range []struct {
		name string
		args []string
	}{
		{"the scenario's own axis", nil},
		{"--matrix", []string{"--matrix", "from=@store:app-*.pkg"}},
	} {
		cmd := runCmd()
		if err := cmd.ParseFlags(from.args); err != nil {
			t.Fatalf("%s: parse flags: %v", from.name, err)
		}
		m := &scenario.Manifest{Params: map[string]scenario.Param{
			"from": {Values: "@store:app-*.pkg", Axis: true},
		}}

		scope, err := scopeOfPinned(cmd, m, nil)
		if err == nil {
			t.Errorf("%s: swept %v, want a refusal: no store was given to discover it from", from.name, scope)
			continue
		}
		if !strings.Contains(err.Error(), "@store:") && !strings.Contains(err.Error(), "artifact store") {
			t.Errorf("%s: error = %v, want the discovery named", from.name, err)
		}
	}
}

// meta rebuilds run.json. Handing that file to somebody who asked for a page is
// a different answer than the one requested, and a silent one.
func TestTheMetaViewHasNoPage(t *testing.T) {
	t.Parallel()

	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"render", "--view", "meta", "-o", "html", "some-run"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("meta accepted -o html; wrote %q", out.String())
	}
	if !strings.Contains(err.Error(), "run.json") {
		t.Fatalf("error = %v, want the rebuild named", err)
	}
}
