package doctor_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
	"github.com/jmurray2011/testease/internal/judge"
)

// a docker daemon that answers whatever the case needs.
type fakeDocker struct {
	version string
	images  map[string]bool
	err     error

	// elsewhere names the machine this daemon runs on when it is not this
	// one. Empty means the daemon is local, which is the ordinary case.
	elsewhere      string
	mountInvisible bool
	copyErr        error
}

func (d fakeDocker) IsLocal(context.Context) (bool, string, error) {
	if d.err != nil {
		return false, "", d.err
	}
	if d.elsewhere != "" {
		return false, d.elsewhere, nil
	}
	return true, "here", nil
}

func (d fakeDocker) ServerVersion(context.Context) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	return d.version, nil
}

func (d fakeDocker) HasImage(_ context.Context, ref string) (bool, error) {
	if d.err != nil {
		return false, d.err
	}
	return d.images[ref], nil
}

func (d fakeDocker) ReadMount(_ context.Context, source, _ string) ([]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	if d.mountInvisible {
		return nil, nil
	}
	return os.ReadFile(source)
}

func (d fakeDocker) MountType(_ context.Context, source, _ string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	if d.mountInvisible {
		return "", os.ErrNotExist
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "directory", nil
	}
	return "file", nil
}

func (d fakeDocker) CopyRoundTrip(context.Context, string) error { return d.copyErr }

const manifest = `
schema: 1
name: package-upgrade-leaves-it-running
question: what does an upgrade refusal leave behind?
params:
  store: {default: "%s"}
hosts:
  box:
    provider: docker
    image: neutral-base:1
    media: ["{{.store}}:/media:ro"]
steps:
  - label: install the old version
    host: box
    probe: probes/install.sh
    expect: {rc: 0}
judge: {name: %s, version: "%s"}
`

func scenarioDir(t *testing.T, store, judgeName, judgeVersion string) (root, name string) {
	t.Helper()

	root = t.TempDir()
	name = "package-upgrade-leaves-it-running"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := manifest
	for _, v := range []string{store, judgeName, judgeVersion} {
		yaml = strings.Replace(yaml, "%s", v, 1)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "install.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return root, name
}

func check(t *testing.T, r doctor.Report, name string) doctor.Check {
	t.Helper()

	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	var have []string
	for _, c := range r.Checks {
		have = append(have, c.Name)
	}
	t.Fatalf("no check named %q; report has %s", name, strings.Join(have, ", "))
	return doctor.Check{}
}

func hasCheck(r doctor.Report, name string) bool {
	for _, c := range r.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestAStaticOnlyScenarioDoesNotRequireAnUnrelatedDockerDaemon(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())
	path := filepath.Join(root, name, "scenario.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "provider: docker", "provider: static\n    backend: ssh\n    address: 127.0.0.1", 1))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{err: os.ErrPermission},
	})
	if hasCheck(r, "docker") {
		t.Fatal("static-only scenario checked an unrelated Docker daemon")
	}
}

func TestAStaticSSMLargeInputNeedsTheTransferStoreBeforeTheRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "ssm-media")
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema: 1
name: ssm-media
question: can exact large media reach this machine?
inputs:
  installer: {kind: file}
hosts:
  box:
    provider: static
    backend: ssm
    address: i-0123456789abcdef0
    image: ami-0123456789abcdef0
    media: ["@installer:/upgrade.exe:ro"]
steps:
  - {label: read it, host: box, probe: probes/read.ps1}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "read.ps1"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const inlineLimit = 16 * 1024
	payload := strings.Repeat("x", inlineLimit+1)
	media := filepath.Join(root, "upgrade.exe")
	if err := os.WriteFile(media, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	bundle := filepath.Join(root, "inputs.yaml")
	descriptor := fmt.Sprintf("schema: 1\ninputs:\n  installer: {path: %s, sha256: %x}\n", media, sum)
	if err := os.WriteFile(bundle, []byte(descriptor), 0o600); err != nil {
		t.Fatal(err)
	}

	opt := doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "ssm-media", InputBundle: bundle,
		SSMInlineMediaLimit: inlineLimit,
	}
	without := doctor.Run(context.Background(), opt)
	if got := check(t, without, "media"); got.Status != doctor.Fail || !strings.Contains(got.Found, "--ssm-transfer-store") {
		t.Fatalf("media check = %s %q, want transfer-store refusal", got.Status, got.Found)
	}
	if got := check(t, without, "images"); got.Status != doctor.OK {
		t.Errorf("static AMI was treated as a Docker image: %s %q", got.Status, got.Found)
	}

	opt.SSMTransferStore = "s3://lab/scratch"
	opt.SSMTransferStoreValid = true
	with := doctor.Run(context.Background(), opt)
	if got := check(t, with, "media"); got.Status != doctor.OK {
		t.Errorf("media with transfer store = %s %q", got.Status, got.Found)
	}
}

func TestARunsDirThatCanHoldARecordPasses(t *testing.T) {
	t.Parallel()

	r := doctor.Run(context.Background(), doctor.Options{RunsDir: t.TempDir()})

	if got := check(t, r, "runs-dir").Status; got != doctor.OK {
		t.Errorf("status = %q, want %q", got, doctor.OK)
	}
}

func TestARunsDirThatCannotBeCreatedFails(t *testing.T) {
	t.Parallel()

	// A file where a directory has to go. The runner would discover this after
	// resolving targets, which is minutes and a container too late.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := doctor.Run(context.Background(), doctor.Options{RunsDir: filepath.Join(blocker, "runs")})

	got := check(t, r, "runs-dir")
	if got.Status != doctor.Fail {
		t.Errorf("status = %q, want %q", got.Status, doctor.Fail)
	}
	if got.Remedy == "" {
		t.Error("a failure with no remedy is the thing this command exists to stop")
	}
}

func TestTheLockIsTakenNotInferred(t *testing.T) {
	t.Parallel()

	// The record refuses to write without an exclusive lock, so the question is
	// whether one can be taken here - not what the filesystem is called.
	dir := t.TempDir()
	r := doctor.Run(context.Background(), doctor.Options{RunsDir: dir})

	if got := check(t, r, "runs-dir").Status; got != doctor.OK {
		t.Fatalf("status = %q, want %q", got, doctor.OK)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("doctor left %d file(s) behind", len(entries))
	}
}

func TestAJudgeThisBinaryCannotRunIsAFailureBeforeTheRunNotAfterIt(t *testing.T) {
	t.Parallel()

	// A scenario pinned to a judge version this binary no longer ships measures
	// fine and then cannot be judged at all.
	root, name := scenarioDir(t, t.TempDir(), "expect", "1")

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	got := check(t, r, "judge")
	if got.Status != doctor.Fail {
		t.Errorf("status = %q, want %q", got.Status, doctor.Fail)
	}
	if !strings.Contains(got.Found, "expect") {
		t.Errorf("found = %q, want it to name the judge", got.Found)
	}
}

func TestAJudgeThisBinaryShipsPasses(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	if got := check(t, r, "judge").Status; got != doctor.OK {
		t.Errorf("status = %q, want %q", got, doctor.OK)
	}
}

func TestMediaThatIsNotOnThisMachineFails(t *testing.T) {
	t.Parallel()

	// Scenarios pin absolute host paths for their artifact stores. On another
	// machine the run boots, mounts nothing useful, and the probe fails
	// somewhere unrelated.
	root, name := scenarioDir(t, "/no/such/store", "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	got := check(t, r, "media")
	if got.Status != doctor.Fail {
		t.Errorf("status = %q, want %q", got.Status, doctor.Fail)
	}
	if !strings.Contains(got.Found, "/no/such/store") {
		t.Errorf("found = %q, want it to name the path", got.Found)
	}
}

func TestMediaThatIsHerePasses(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	if got := check(t, r, "media").Status; got != doctor.OK {
		t.Errorf("status = %q, want %q", got, doctor.OK)
	}
}

func TestDockerMediaMustBeVisibleToTheSelectedDaemon(t *testing.T) {
	t.Parallel()

	// Host-side stat is not evidence that a remote daemon can resolve the
	// same bind source. This was a strict-doctor false green before a run on a
	// remote context mounted six empty directories in place of six files.
	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{
			version:        "29.6.1",
			images:         map[string]bool{"neutral-base:1": true},
			elsewhere:      "remote-daemon",
			mountInvisible: true,
		},
	})

	got := check(t, r, "media")
	if got.Status != doctor.Fail {
		t.Fatalf("status = %q, want %q", got.Status, doctor.Fail)
	}
	if !strings.Contains(got.Found, "Docker daemon") {
		t.Errorf("found = %q, want the failing boundary named", got.Found)
	}
}

func TestAnImageThisDaemonDoesNotHaveFails(t *testing.T) {
	t.Parallel()

	// A missing image used to produce a full table of unbooted rows at exit 0,
	// which reads as a completed sweep over nothing.
	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{version: "27.0.0", images: map[string]bool{}},
	})

	got := check(t, r, "images")
	if got.Status != doctor.Fail {
		t.Errorf("status = %q, want %q", got.Status, doctor.Fail)
	}
	if !strings.Contains(got.Found, "neutral-base:1") {
		t.Errorf("found = %q, want it to name the image", got.Found)
	}
}

func TestAnImageInspectionErrorNamesDockerRecovery(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{version: "27.0.0", err: fmt.Errorf("permission denied")},
	})

	got := check(t, r, "images")
	if got.Status != doctor.Unchecked {
		t.Fatalf("status = %q, want %q", got.Status, doctor.Unchecked)
	}
	if !strings.Contains(got.Remedy, "docker context ls") {
		t.Errorf("remedy = %q, want Docker daemon recovery", got.Remedy)
	}
}

func TestMissingScenarioNamesTheScenarioRootInItsRemedy(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "scenarios")
	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "missing",
	})

	got := check(t, r, "scenario")
	if !strings.Contains(got.Remedy, "--scenario-root "+root) {
		t.Errorf("remedy = %q, want the selected scenario root", got.Remedy)
	}
}

func TestAnImageThisDaemonHasPasses(t *testing.T) {
	t.Parallel()

	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Docker: fakeDocker{version: "27.0.0", images: map[string]bool{"neutral-base:1": true}},
	})

	if got := check(t, r, "images").Status; got != doctor.OK {
		t.Errorf("status = %q, want %q", got, doctor.OK)
	}
}

func TestCrossPlatformImagesAreCheckedOnTheirOwnDaemons(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "cross-platform")
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema: 1
name: cross-platform
question: can bytes cross daemons?
hosts:
  source: {provider: docker, image: linux-product:1}
  destination: {provider: hyperv, image: windows-product:1}
steps:
  - {label: produce, host: source, probe: probes/source.sh}
  - {label: inspect, host: destination, probe: probes/destination.ps1}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{"source.sh", "destination.ps1"} {
		if err := os.WriteFile(filepath.Join(dir, "probes", probe), []byte("exit 0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	opt := doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "cross-platform",
		Docker:        fakeDocker{version: "27", images: map[string]bool{"linux-product:1": true}},
		OneShotDocker: fakeDocker{version: "27", images: map[string]bool{"windows-product:1": true}},
	}
	if got := check(t, doctor.Run(context.Background(), opt), "images"); got.Status != doctor.OK {
		t.Fatalf("images = %s (%s), want each image found on its own daemon", got.Status, got.Found)
	}

	opt.OneShotDocker = fakeDocker{version: "27", images: map[string]bool{}}
	if got := check(t, doctor.Run(context.Background(), opt), "images"); got.Status != doctor.Fail || !strings.Contains(got.Found, "windows-product:1") {
		t.Fatalf("images = %s (%s), want missing one-shot image", got.Status, got.Found)
	}
}

func TestDoctorChecksEveryResolvedMatrixCell(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := "windows-restore-matrix"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `
schema: 1
name: windows-restore-matrix
question: does every forward crossing work?
params:
  crossing: {values: "5.0.2-to-6.0.3,5.0.3-to-6.2.4,6.0.3-to-6.2.4", axis: true}
hosts:
  box:
    provider: docker
    image: "neutral-base:{{crossingTarget .crossing}}"
steps:
  - label: read it
    host: box
    probe: probes/read.sh
    expect: {rc: 0}
judge: {name: matrix, version: "3"}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "read.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	opt := doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Cells: []map[string]string{
			{"crossing": "5.0.2-to-6.0.3"},
			{"crossing": "5.0.3-to-6.2.4"},
			{"crossing": "6.0.3-to-6.2.4"},
		},
		Docker: fakeDocker{version: "27.0.0", images: map[string]bool{
			"neutral-base:6.0.3": true,
			"neutral-base:6.2.4": true,
		}},
		Backends: testBackends(),
	}
	r := doctor.Run(context.Background(), opt)
	if got := check(t, r, "scenario"); got.Status != doctor.OK || !strings.Contains(got.Found, "3 matrix cells") {
		t.Errorf("scenario = %q (%s), want every cell resolved", got.Status, got.Found)
	}
	if got := check(t, r, "images"); got.Status != doctor.OK ||
		!strings.Contains(got.Found, "neutral-base:6.0.3") || !strings.Contains(got.Found, "neutral-base:6.2.4") ||
		strings.Count(got.Found, "neutral-base:6.2.4") != 1 {
		t.Errorf("images = %q (%s), want both cell images checked", got.Status, got.Found)
	}
	if got := check(t, r, "capabilities"); got.Status != doctor.OK {
		t.Errorf("capabilities = %q (%s), want every cell's scoped docker host checked", got.Status, got.Found)
	}

	opt.Docker = fakeDocker{version: "27.0.0", images: map[string]bool{"neutral-base:6.0.3": true}}
	r = doctor.Run(context.Background(), opt)
	if got := check(t, r, "images"); got.Status != doctor.Fail || !strings.Contains(got.Found, "neutral-base:6.2.4") {
		t.Errorf("images = %q (%s), want the missing cell image named", got.Status, got.Found)
	}
}

func TestDoctorChecksEveryResolvedMatrixCarryDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := "carry-matrix"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `
schema: 1
name: carry-matrix
question: can every cell carry evidence to its destination?
params:
  crossing: {values: "one,two", axis: true}
hosts:
  source: {provider: docker, image: "source:{{.crossing}}"}
  destination: {provider: docker, image: "destination:{{.crossing}}"}
steps:
  - label: carry it
    host: source
    carry: {from: /out.bin, to_host: destination, to: /in.bin}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Cells: []map[string]string{{"crossing": "one"}, {"crossing": "two"}},
		Docker: fakeDocker{version: "27.0.0", images: map[string]bool{
			"source:one": true, "source:two": true,
			"destination:one": true, "destination:two": true,
		}},
		Backends: testBackends(),
	})
	if got := check(t, r, "capabilities"); got.Status != doctor.OK {
		t.Errorf("capabilities = %q (%s), want every cell's scoped carry destination checked", got.Status, got.Found)
	}
}

func TestWithoutADaemonTheImageCheckIsUncheckedNotPassed(t *testing.T) {
	t.Parallel()

	// Reporting ok for something nobody looked at is the failure this whole
	// tool is built against, aimed at itself.
	root, name := scenarioDir(t, t.TempDir(), "expect", (judge.Expect{}).Version())

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	if got := check(t, r, "images").Status; got != doctor.Unchecked {
		t.Errorf("status = %q, want %q", got, doctor.Unchecked)
	}
}

func TestACapabilityThatNeedsMoreThanTheManifestIsUncheckedNotPassed(t *testing.T) {
	t.Parallel()

	// This case used to assert that the whole capability class was unanswerable
	// before a target was dialled. That premise was wrong: which capabilities a
	// KIND has is static, and the manifest names each host's kind, so most of
	// the class is decided before anything boots. What survives is the
	// invariant - a check nobody could make says unchecked and says why - and
	// it belongs on the capabilities that really do need more than the
	// manifest. docker fingerprints only when the run was given an instrument.
	root, name := requiringScenario(t, "fingerprint")

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Backends: testBackends(),
	})

	got := check(t, r, "capabilities")
	if got.Status != doctor.Unchecked {
		t.Errorf("status = %q, want %q", got.Status, doctor.Unchecked)
	}
	if !strings.Contains(got.Found, "instrument") {
		t.Errorf("found = %q, want the condition named", got.Found)
	}
}

// A scenario asking a kind for something it does not have is decided from the
// manifest. The runner refuses this too - after it has booted a box.
func TestACapabilityTheHostsKindDoesNotHaveFailsBeforeAnythingBoots(t *testing.T) {
	t.Parallel()

	root, name := requiringScenario(t, "oneshot")

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Backends: testBackends(),
	})

	got := check(t, r, "capabilities")
	if got.Status != doctor.Fail {
		t.Errorf("status = %q, want %q: docker has no oneshot", got.Status, doctor.Fail)
	}
	if !strings.Contains(got.Found, "oneshot") {
		t.Errorf("found = %q, want the capability named", got.Found)
	}
}

func TestACapabilityTheHostsKindHasPasses(t *testing.T) {
	t.Parallel()

	root, name := requiringScenario(t, "session")

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
		Backends: testBackends(),
	})

	if got := check(t, r, "capabilities"); got.Status != doctor.OK {
		t.Errorf("status = %q (%s), want ok: docker has a session", got.Status, got.Found)
	}
}

// Without the table there is nothing to check against, and saying so is not
// the same as saying the scenario is fine.
func TestWithoutTheTableTheCapabilityCheckIsUncheckedNotPassed(t *testing.T) {
	t.Parallel()

	root, name := requiringScenario(t, "session")

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: name,
	})

	if got := check(t, r, "capabilities"); got.Status != doctor.Unchecked {
		t.Errorf("status = %q, want %q", got.Status, doctor.Unchecked)
	}
}

// testBackends is `testease backends` as doctor is handed it.
func testBackends() []doctor.Backend {
	return []doctor.Backend{
		{
			Provider: "docker",
			Offers:   []string{"session", "media"},
			Conditional: map[string]string{
				"fingerprint": "--instrument-collect and --instrument-diff",
				"endpoint":    "the host to declare a port",
			},
		},
		{Provider: "hyperv", Offers: []string{"oneshot"}},
	}
}

// requiringScenario writes a docker-hosted scenario that requires one thing.
func requiringScenario(t *testing.T, need string) (root, name string) {
	t.Helper()

	root = t.TempDir()
	name = "needs-" + need
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `
schema: 1
name: ` + name + `
question: what does this host have to be able to do?
requires: [` + need + `]
hosts:
  box:
    provider: docker
    image: neutral-base:1
steps:
  - label: read it
    host: box
    probe: probes/read.sh
    expect: {rc: 0}
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes", "read.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return root, name
}

func TestAScenarioThatDoesNotLoadStopsTheChecksThatDependOnIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "scenario.yaml"), []byte("schema: 1\nname: broken\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "broken",
	})

	if got := check(t, r, "scenario").Status; got != doctor.Fail {
		t.Errorf("status = %q, want %q", got, doctor.Fail)
	}
	for _, name := range []string{"media", "images", "judge"} {
		if got := check(t, r, name).Status; got != doctor.Unchecked {
			t.Errorf("%s = %q, want %q: it depends on a scenario that did not load", name, got, doctor.Unchecked)
		}
	}
}

func TestWorstIsTheStatusACallerActsOn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		statuses []doctor.Status
		strict   bool
		want     doctor.Status
	}{
		{"all clear", []doctor.Status{doctor.OK, doctor.OK}, false, doctor.OK},
		{"a warning is not a failure", []doctor.Status{doctor.OK, doctor.Warn}, false, doctor.Warn},
		{"a failure outranks a warning", []doctor.Status{doctor.Warn, doctor.Fail}, false, doctor.Fail},
		{"unchecked is not a pass", []doctor.Status{doctor.OK, doctor.Unchecked}, false, doctor.Warn},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var r doctor.Report
			for i, s := range c.statuses {
				r.Checks = append(r.Checks, doctor.Check{Name: string(rune('a' + i)), Status: s})
			}
			if got := r.Worst(); got != c.want {
				t.Errorf("worst = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStrictMakesAWarningCountBecauseAScriptCannotReadProse(t *testing.T) {
	t.Parallel()

	r := doctor.Report{Checks: []doctor.Check{{Name: "a", Status: doctor.Warn}}}

	if r.Failed(false) {
		t.Error("a warning failed the default run")
	}
	if !r.Failed(true) {
		t.Error("a warning passed under --strict")
	}
}
