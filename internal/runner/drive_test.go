package runner_test

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/livebrowser"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

// A drive step runs a container here, pointed at the target's endpoint. The
// endpoint and the execution surface are different capabilities on purpose: a
// browser drives the product, a probe inspects the box, and neither does the
// other's job.

const driven = `
schema: 1
name: driven
question: does the app still work?
hosts:
  box: {provider: fake, image: neutral-base:1, port: 8080}
steps:
  - label: drive the app
    host: box
    probe: drive://suite-image:1
    env: {CI: "1"}
`

const drivenWithMedia = `
schema: 1
name: driven
question: does the app still work?
hosts:
  box: {provider: fake, image: neutral-base:1, port: 8080}
steps:
  - label: drive the app
    host: box
    probe: drive://suite-image:1
    media:
      - "/opt/suite:/suite:ro"
`

func TestADriveStepCarriesItsOwnMedia(t *testing.T) {
	t.Parallel()

	// A suite is usually a checkout of its own, too big and too separate to
	// live in the scenario directory. Without media a drive step can only be
	// handed the scenario and a scratch workspace, so the only way to give it a
	// real suite is to bake one into an image - which turns the suite's version
	// into a tag nobody can audit, and loses the provenance a fixture needs.
	driver := &fakeDriver{}
	if _, _, err := drove(t, drivenWithMedia, driver, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(driver.spec.Media) != 1 {
		t.Fatalf("media = %v, want the one the step declared", driver.spec.Media)
	}
	got := driver.spec.Media[0]
	if got.Source != "/opt/suite" || got.Dest != "/suite" || !got.ReadOnly {
		t.Errorf("media = %+v, want /opt/suite mounted read-only at /suite", got)
	}
}

func TestADriveStepTakesSecretsThatTheRecordDoesNotKeep(t *testing.T) {
	t.Parallel()

	// A suite that authenticates needs a credential, and the two places a
	// scenario could put one are both wrong: the manifest is committed, and
	// params are written into run.json. So the value is injected and never
	// recorded, which is the bargain --ssh-password-file already makes.
	driver := &fakeDriver{}
	out, err := runner.Run(context.Background(), runner.Options{
		Plan:         plan(t, driven),
		RunsDir:      t.TempDir(),
		Tool:         record.ToolRef{Version: "0.0.0-test"},
		Providers:    map[string]target.Provider{"fake": &endpointedProvider{target: &fakeTarget{session: &fakeSession{}}}},
		Driver:       driver,
		DriveSecrets: map[string]string{"API_KEY": "s3cret"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}

	if got := driver.spec.Env["API_KEY"]; got != "s3cret" {
		t.Errorf("API_KEY = %q, want it injected into the container", got)
	}
	if got := driver.spec.Env["CI"]; got != "1" {
		t.Errorf("CI = %q, want the scenario's own env kept alongside", got)
	}
	for _, e := range rec.Events {
		if strings.Contains(string(e.Data), "s3cret") {
			t.Fatalf("the secret reached the record in a %s event", e.Kind)
		}
	}
}

func TestADriveStepJoinsTheTargetsNetwork(t *testing.T) {
	t.Parallel()

	// A docker provider puts each cell's hosts on a network of their own, and
	// docker isolates user-defined bridges from the default one. A drive
	// container that does not join it cannot route to the target at all - which
	// is a browser reporting ERR_ABORTED against a box that is serving fine.
	driver := &fakeDriver{}
	prov := &networkedProvider{target: &fakeTarget{session: &fakeSession{}}, network: "testease-run-cell"}
	if _, _, err := drove(t, driven, driver, prov); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := driver.spec.Network; got != "testease-run-cell" {
		t.Errorf("network = %q, want the target's own", got)
	}
}

func TestADriveStepMountsTheScenarioByAnAbsolutePath(t *testing.T) {
	t.Parallel()

	// A path this process understands is not one the container runtime does.
	// Docker reads a bind source that is not absolute as a VOLUME NAME and
	// happily creates an empty volume under it, so a scenario reached by a
	// relative root - `--scenario-root .`, which is what the corpus's own
	// examples use - arrives inside the container as an empty directory. The
	// suite that travels with the question is then simply not there, and the
	// step fails for a reason that has nothing to do with the product.
	driver := &fakeDriver{}
	if _, _, err := drovePlan(t, relativePlan(t, driven), driver, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !filepath.IsAbs(driver.spec.Scenario) {
		t.Errorf("scenario mounted from %q, want an absolute path a container runtime can bind", driver.spec.Scenario)
	}
}

func TestADriveWorkspaceLivesBesideTheRun(t *testing.T) {
	t.Parallel()

	// Snap Docker has a private /tmp namespace. A workspace created with the
	// process default may therefore be an empty, root-owned directory when the
	// daemon binds it. The operator-selected runs directory is shared with the
	// daemon already and is the only reliable parent for drive scratch space.
	driver := &fakeDriver{}
	runsDir := t.TempDir()
	_, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, driven),
		RunsDir:   runsDir,
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": &endpointedProvider{target: &fakeTarget{session: &fakeSession{}}}},
		Driver:    driver,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantPrefix := runsDir + string(filepath.Separator)
	if !strings.HasPrefix(driver.spec.Workspace, wantPrefix) {
		t.Errorf("drive workspace = %q, want it beneath runs dir %q", driver.spec.Workspace, runsDir)
	}
}

// networkedTarget answers on a port AND lives on a network, as a docker one does.
type networkedTarget struct {
	*endpointedTarget
	network string
}

func (n *networkedTarget) Network() (string, bool) { return n.network, true }

type networkedProvider struct {
	target  *fakeTarget
	network string
}

func (p *networkedProvider) Resolve(context.Context, target.Spec) (target.Target, error) {
	return &networkedTarget{endpointedTarget: &endpointedTarget{p.target}, network: p.network}, nil
}
func (p *networkedProvider) Release(context.Context, target.Target, bool) error { return nil }

// fakeDriver stands in for a container runtime on this machine.
type fakeDriver struct {
	spec runner.DriveSpec
	run  func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error)
}

func (d *fakeDriver) Drive(_ context.Context, spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
	d.spec = spec
	if d.run != nil {
		return d.run(spec, opt)
	}
	return target.Result{RC: 0}, nil
}

// endpointedTarget is a target that answers on a port.
type endpointedTarget struct{ *fakeTarget }

func (t *endpointedTarget) Endpoint() (url.URL, bool) {
	return url.URL{Scheme: "http", Host: "172.17.0.4:8080"}, true
}

type endpointedProvider struct{ target *fakeTarget }

func (p *endpointedProvider) Resolve(context.Context, target.Spec) (target.Target, error) {
	return &endpointedTarget{p.target}, nil
}
func (p *endpointedProvider) Release(context.Context, target.Target, bool) error { return nil }

// relativePlan is a plan whose scenario directory is relative to this process's
// working directory, the way `--scenario-root .` produces one.
func relativePlan(t *testing.T, manifest string) *scenario.Plan {
	t.Helper()

	p := plan(t, manifest)
	// A copy under the working directory rather than a path relative to
	// t.TempDir: on a machine whose temporary directory is on another drive,
	// as GitHub's Windows runners have it, there is no relative path between
	// the two.
	rel, err := os.MkdirTemp(".", ".testease-test-scenario-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rel) })
	if err := os.CopyFS(rel, os.DirFS(p.Dir)); err != nil {
		t.Fatalf("copy the scenario: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("scenario directory %q is not relative, so this tests nothing", rel)
	}
	p.Dir = rel
	return p
}

func drove(t *testing.T, manifest string, driver *fakeDriver, prov target.Provider) (*runner.Outcome, *record.Record, error) {
	t.Helper()

	return drovePlan(t, plan(t, manifest), driver, prov)
}

func drovePlan(t *testing.T, p *scenario.Plan, driver *fakeDriver, prov target.Provider) (*runner.Outcome, *record.Record, error) {
	t.Helper()

	if prov == nil {
		prov = &endpointedProvider{target: &fakeTarget{session: &fakeSession{}}}
	}
	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      p,
		RunsDir:   t.TempDir(),
		Tool:      record.ToolRef{Version: "0.0.0-test"},
		Providers: map[string]target.Provider{"fake": prov},
		Driver:    driver,
	})
	if out == nil {
		return nil, nil, err
	}
	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	return out, rec, err
}

func TestADriveStepIsPointedAtTheEndpoint(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{}
	_, _, err := drove(t, driven, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if driver.spec.Image != "suite-image:1" {
		t.Errorf("image = %q, want the one the probe named", driver.spec.Image)
	}
	if got := driver.spec.Env[runner.EndpointEnv]; got != "http://172.17.0.4:8080" {
		t.Errorf("%s = %q, want the target's endpoint", runner.EndpointEnv, got)
	}
	if driver.spec.Env["CI"] != "1" {
		t.Errorf("the step's own environment did not reach the container: %v", driver.spec.Env)
	}
	if driver.spec.Env[runner.WorkspaceEnv] == "" || driver.spec.Workspace == "" {
		t.Errorf("the suite was given nowhere to write: %+v", driver.spec)
	}
}

func TestADriveStepNeedsNoSessionOnTheTarget(t *testing.T) {
	t.Parallel()

	// The two capabilities are separate, and a drive step must not open a
	// session it has no use for.
	session := &fakeSession{run: func([]byte, []string, target.RunOpts) (target.Result, error) {
		t.Error("a drive step ran a probe on the target")
		return target.Result{}, nil
	}}
	prov := &endpointedProvider{target: &fakeTarget{session: session}}

	if _, _, err := drove(t, driven, &fakeDriver{}, prov); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestATargetWithNoEndpointCannotBeDriven(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{target: &fakeTarget{session: &fakeSession{}}}
	_, _, err := drove(t, driven, &fakeDriver{}, prov)
	if err == nil {
		t.Fatal("a drive step ran against a target with no endpoint")
	}
	var precondition *runner.PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("error is %T (%v), want a precondition failure", err, err)
	}
	if precondition.Remedy == "" {
		t.Error("the refusal names no way to fix it")
	}
}

func TestTheReportIsTheOneTheSuiteNamed(t *testing.T) {
	t.Parallel()

	// Never the newest file. A suite that chains its projects re-runs a
	// dependency which writes its own report in the same millisecond, so only
	// the log says which file this run asked for.
	driver := &fakeDriver{run: func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
		for _, name := range []string{"wanted.json", "first-boot.json"} {
			if err := os.WriteFile(filepath.Join(spec.Workspace, name), []byte(`{"from":"`+name+`"}`), 0o644); err != nil {
				return target.Result{}, err
			}
		}
		// The dependency's report is named first; the one this run asked for
		// is named last, and last wins.
		_, _ = io.WriteString(opt.Stdout, runner.ReportMark+" /workspace/first-boot.json\n")
		_, _ = io.WriteString(opt.Stdout, runner.ReportMark+" /workspace/wanted.json\n")
		_, _ = io.WriteString(opt.Stdout, "all suites passed\n")
		return target.Result{RC: 0}, nil
	}}

	out, rec, err := drove(t, driven, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	report, ok := factIn(t, rec, record.ReportFact)
	if !ok {
		t.Fatal("the run recorded no report")
	}
	body, readErr := os.ReadFile(filepath.Join(out.Dir, filepath.FromSlash(report)))
	if readErr != nil {
		t.Fatalf("read the kept report: %v", readErr)
	}
	if !strings.Contains(string(body), "wanted.json") {
		t.Errorf("kept %q, want the report the suite named last", body)
	}
}

func TestASuiteThatNamedNoReportProducedNone(t *testing.T) {
	t.Parallel()

	// Nothing is guessed. Picking the newest file in the workspace is exactly
	// the mistake this contract exists to avoid.
	driver := &fakeDriver{run: func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
		if err := os.WriteFile(filepath.Join(spec.Workspace, "something.json"), []byte("{}"), 0o644); err != nil {
			return target.Result{}, err
		}
		_, _ = io.WriteString(opt.Stdout, "done\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := drove(t, driven, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := factIn(t, rec, record.ReportFact); ok {
		t.Error("a report nobody named was harvested anyway")
	}
}

func TestASuiteMayRecordReadingsOfItsOwn(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{run: func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
		facts := filepath.Base(spec.Env[runner.FactsEnv])
		if err := os.WriteFile(filepath.Join(spec.Workspace, facts),
			[]byte("fact\tsuite.tests\t42\n"), 0o644); err != nil {
			return target.Result{}, err
		}
		_, _ = io.WriteString(opt.Stdout, "done\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := drove(t, driven, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := factIn(t, rec, "suite.tests"); got != "42" {
		t.Errorf("suite.tests = %q, want the suite's own reading", got)
	}
}

func TestASuiteMayRecordAReadingItCouldNotTake(t *testing.T) {
	t.Parallel()

	// The facts FILE and fd 3 are two spellings of one channel, so a probe that
	// records an unread fact has to mean the same thing under either backend.
	// A verb the file reader silently dropped would make coverage depend on
	// which way the step happened to be driven.
	driver := &fakeDriver{run: func(spec runner.DriveSpec, opt target.RunOpts) (target.Result, error) {
		facts := filepath.Base(spec.Env[runner.FactsEnv])
		if err := os.WriteFile(filepath.Join(spec.Workspace, facts),
			[]byte("no-reading\tcoverage.machine\tbeta was present and not examined\n"), 0o644); err != nil {
			return target.Result{}, err
		}
		_, _ = io.WriteString(opt.Stdout, "done\n")
		return target.Result{RC: 0}, nil
	}}

	_, rec, err := drove(t, driven, driver, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found bool
	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		f, decodeErr := record.Decode[record.Fact](e)
		if decodeErr != nil {
			t.Fatalf("decode fact: %v", decodeErr)
		}
		if f.Key != "coverage.machine" {
			continue
		}
		found = true
		if f.Value != nil {
			t.Errorf("value = %q, want none", *f.Value)
		}
		if !strings.Contains(f.Error, "beta was present") {
			t.Errorf("error = %q, want the reason the reading is missing", f.Error)
		}
	}
	if !found {
		t.Error("the facts file dropped the unread fact")
	}
}

func TestARunWithNoDriverSaysSoRatherThanSkipping(t *testing.T) {
	t.Parallel()

	out, err := runner.Run(context.Background(), runner.Options{
		Plan:      plan(t, driven),
		RunsDir:   t.TempDir(),
		Providers: map[string]target.Provider{"fake": &endpointedProvider{target: &fakeTarget{session: &fakeSession{}}}},
	})
	if err == nil {
		t.Fatal("a drive step was skipped rather than refused")
	}
	if out == nil {
		t.Fatal("no record was left behind")
	}
}

func factIn(t *testing.T, rec *record.Record, key string) (string, bool) {
	t.Helper()

	value := ""
	found := false
	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		fact, err := record.Decode[record.Fact](e)
		if err != nil {
			t.Fatalf("decode fact: %v", err)
		}
		if fact.Key == key && fact.Value != nil {
			value, found = *fact.Value, true
		}
	}
	return value, found
}

func TestALiveBrowserStepSavesFramesThatTheRecordDoesNotKeep(t *testing.T) {
	t.Parallel()

	// While the step runs, the hook and its owner are in the workspace and the
	// suite's node is told to load it; a frame the suite saves is there for a
	// viewer. Once the step ends, the workspace and its frames are gone, and
	// the record holds none of them.
	var during []string
	runs := t.TempDir()
	driver := &fakeDriver{run: func(spec runner.DriveSpec, _ target.RunOpts) (target.Result, error) {
		live := filepath.Join(spec.Workspace, livebrowser.Dir)
		if err := os.WriteFile(filepath.Join(live, "frame-1.jpg"), []byte("jpeg"), 0o644); err != nil {
			return target.Result{RC: -1}, err
		}
		entries, _ := os.ReadDir(live)
		for _, e := range entries {
			during = append(during, e.Name())
		}
		return target.Result{RC: 0}, nil
	}}
	out, err := runner.Run(context.Background(), runner.Options{
		Plan:        plan(t, driven),
		RunsDir:     runs,
		Tool:        record.ToolRef{Version: "0.0.0-test"},
		Providers:   map[string]target.Provider{"fake": &endpointedProvider{target: &fakeTarget{session: &fakeSession{}}}},
		Driver:      driver,
		LiveBrowser: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := driver.spec.Env["NODE_OPTIONS"]; !strings.Contains(got, "--require /workspace/.testease-live/hook.cjs") {
		t.Errorf("NODE_OPTIONS = %q, want the hook required", got)
	}
	if strings.Join(during, ",") != "frame-1.jpg,hook.cjs,owner.json" {
		t.Errorf("live directory during the step = %v", during)
	}
	if left, _ := filepath.Glob(filepath.Join(runs, livebrowser.WorkspacePattern)); len(left) != 0 {
		t.Errorf("workspaces left after the step: %v", left)
	}
	_ = filepath.WalkDir(out.Dir, func(path string, d os.DirEntry, _ error) error {
		if d != nil && strings.HasPrefix(d.Name(), "frame-") {
			t.Errorf("a frame reached the record: %s", path)
		}
		return nil
	})
}
