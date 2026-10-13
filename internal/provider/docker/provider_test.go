package docker_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	provider "github.com/jmurray2011/testease/internal/provider/docker"
	"github.com/jmurray2011/testease/internal/target"
)

// These need a daemon and an image already on it. They pull nothing: a test
// that reaches the network to decide whether it passes is not a test of this
// code. Set TESTEASE_TEST_IMAGE to something present locally with bash in it.

func image() string {
	if v := os.Getenv("TESTEASE_TEST_IMAGE"); v != "" {
		return v
	}
	return "almalinux:9"
}

func requireDocker(t *testing.T) backend.Client {
	t.Helper()

	client := backend.Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.Output(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Skipf("no docker daemon here: %v", err)
	}
	if _, err := client.Output(ctx, "image", "inspect", image()); err != nil {
		t.Skipf("image %s is not present locally; set TESTEASE_TEST_IMAGE to one that is", image())
	}
	return client
}

func runID(t *testing.T) string {
	t.Helper()
	// Container names must not collide between parallel tests, and must be
	// findable if one ever leaks.
	return fmt.Sprintf("gate-%s-%d", strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())), os.Getpid())
}

func boot(t *testing.T, opt provider.Options) (*provider.Provider, target.Target, target.Session) {
	t.Helper()

	if opt.Client == (backend.Client{}) {
		opt.Client = requireDocker(t)
	}
	if opt.RunID == "" {
		opt.RunID = runID(t)
	}
	opt.Runner = "testease-test"
	opt.Notices = io.Discard

	p := provider.New(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	tgt, err := p.Resolve(ctx, target.Spec{Host: "box", Provider: "docker", Image: image()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Release(context.Background(), tgt, false); err != nil {
			t.Errorf("Release: %v", err)
		}
		// And the network with it. A test that failed part way leaves one
		// behind, and enough of those exhaust docker's address pool - which
		// then fails every later test with an error about subnets rather than
		// about the test. Found the hard way.
		removeNetworks(t, opt.Client, opt.RunID)
	})

	session, err := tgt.(target.Sessioner).Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return p, tgt, session
}

func TestABootedTargetSaysWhatItIs(t *testing.T) {
	t.Parallel()

	_, tgt, _ := boot(t, provider.Options{})
	id := tgt.Identity()

	if id.Requested["image"] != image() {
		t.Errorf("requested image = %v, want %q", id.Requested["image"], image())
	}
	// A tag is a name someone can move. The digest is the artifact that
	// actually answered.
	if digest, _ := id.Resolved["image_digest"].(string); !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("resolved image_digest = %v, want a digest", id.Resolved["image_digest"])
	}
	if id.Facts["os"] == "" || id.Facts["arch"] == "" {
		t.Errorf("facts = %v, want them read off the box", id.Facts)
	}

	labels := tgt.Labels()
	for _, want := range []string{"testease.runner", "testease.run", "testease.pid", "testease.created", "testease.backend"} {
		if labels[want] == "" {
			t.Errorf("label %s is missing; reap can only trust labels", want)
		}
	}
}

func TestALeftoverContainerIsRefusedNotTalkedTo(t *testing.T) {
	t.Parallel()

	client := requireDocker(t)
	id := runID(t)

	// The measured failure: docker run fails on a name collision, and a
	// readiness loop that ignores that then talks happily to the OLD
	// container. The run would measure the previous run's machine and say
	// nothing about it.
	p := provider.New(provider.Options{Client: client, RunID: id, Runner: "testease-test", Notices: io.Discard})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	first, err := p.Resolve(ctx, target.Spec{Host: "box", Image: image()})
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	defer func() { _ = p.Release(context.Background(), first, false) }()

	second := provider.New(provider.Options{Client: client, RunID: id, Runner: "testease-test", Notices: io.Discard})
	if _, err := second.Resolve(ctx, target.Spec{Host: "box", Image: image()}); err == nil {
		t.Fatal("a second target booted onto a name already taken")
	}
}

func TestExitCodesComeBackVerbatim(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})
	ctx := context.Background()

	for _, want := range []int{0, 1, 42} {
		script := fmt.Appendf(nil, "exit %d\n", want)
		var out, errBuf bytes.Buffer
		result, err := session.RunScript(ctx, script, nil, target.RunOpts{Stdout: &out, Stderr: &errBuf})
		if err != nil {
			t.Fatalf("RunScript: %v", err)
		}
		if result.RC != want {
			t.Errorf("rc = %d, want %d", result.RC, want)
		}
	}
}

func TestAProbeRunsWithItsDeclaredShell(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})
	var out, structured bytes.Buffer
	result, err := session.RunScript(context.Background(),
		[]byte("#!/bin/sh\nprintf 'shell=sh\\n'\nprintf 'fact\\tprobe.shell\\tsh\\n' >&3\n"), nil,
		target.RunOpts{Stdout: &out, Stderr: io.Discard, Structured: &structured})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if result.RC != 0 || strings.TrimSpace(out.String()) != "shell=sh" {
		t.Fatalf("result = %+v, stdout = %q", result, out.String())
	}
	if got := strings.TrimSpace(structured.String()); got != "fact\tprobe.shell\tsh" {
		t.Errorf("structured channel = %q", got)
	}
}

func TestStdoutAndStderrStayApart(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})

	var out, errBuf bytes.Buffer
	script := []byte("echo to stdout\necho to stderr >&2\n")
	if _, err := session.RunScript(context.Background(), script, nil,
		target.RunOpts{Stdout: &out, Stderr: &errBuf}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	if strings.TrimSpace(out.String()) != "to stdout" {
		t.Errorf("stdout = %q", out.String())
	}
	if strings.TrimSpace(errBuf.String()) != "to stderr" {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestArgumentsAndEnvironmentReachTheProbe(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})

	var out bytes.Buffer
	script := []byte(`echo "$1 $2 $VERSION"` + "\n")
	if _, err := session.RunScript(context.Background(), script, []string{"one", "two"},
		target.RunOpts{Stdout: &out, Stderr: io.Discard, Env: []string{"VERSION=3.0"}}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if strings.TrimSpace(out.String()) != "one two 3.0" {
		t.Errorf("output = %q, want %q", strings.TrimSpace(out.String()), "one two 3.0")
	}
}

func TestTheStructuredChannelIsNotStdout(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})

	var out, structured bytes.Buffer
	script := []byte("echo ordinary output\nprintf 'fact\\tpkg.version\\t2.0\\n' >&3\n")
	if _, err := session.RunScript(context.Background(), script, nil,
		target.RunOpts{Stdout: &out, Stderr: io.Discard, Structured: &structured}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	if got := strings.TrimSpace(structured.String()); got != "fact\tpkg.version\t2.0" {
		t.Errorf("structured channel = %q", got)
	}
	if strings.Contains(out.String(), "pkg.version") {
		t.Errorf("a reading leaked into stdout: %q", out.String())
	}
}

func TestATimeoutKillsTheProbeAndSaysSo(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})

	result, err := session.RunScript(context.Background(), []byte("sleep 30\n"), nil,
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if !result.TimedOut {
		t.Error("a probe that ran out of time did not say so")
	}
	// An exit code from something we killed is not a reading of what it would
	// have returned.
	if result.RC != -1 {
		t.Errorf("rc = %d, want -1", result.RC)
	}
}

func TestFetchWorksOnATmpfsPath(t *testing.T) {
	t.Parallel()

	// docker cp cannot read a tmpfs path - measured - and probes write where
	// they like, so retrieval goes through exec-cat instead.
	_, _, session := boot(t, provider.Options{})
	ctx := context.Background()

	script := []byte("printf 'written to tmpfs\\n' > /dev/shm/probe.out\n")
	if _, err := session.RunScript(ctx, script, nil, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	body, err := session.Fetch(ctx, "/dev/shm/probe.out")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer func() { _ = body.Close() }()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(got)) != "written to tmpfs" {
		t.Errorf("fetched %q", got)
	}
}

func TestPutPlacesAFile(t *testing.T) {
	t.Parallel()

	_, _, session := boot(t, provider.Options{})
	ctx := context.Background()

	if err := session.Put(ctx, strings.NewReader("#!/bin/bash\necho placed\n"), "/tmp/placed.sh", 0o755); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var out bytes.Buffer
	if _, err := session.Run(ctx, []string{"/tmp/placed.sh"}, target.RunOpts{Stdout: &out, Stderr: io.Discard}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out.String()) != "placed" {
		t.Errorf("output = %q", out.String())
	}
}

func TestWithoutAnInstrumentATargetIsHonestlyNotAFingerprinter(t *testing.T) {
	t.Parallel()

	_, tgt, _ := boot(t, provider.Options{})
	if _, ok := tgt.(target.Fingerprinter); ok {
		t.Error("a target with no instrument configured claims it can fingerprint")
	}
}

// fakeInstrument is a stand-in for a real fingerprinting tool: collect lists
// what is on the box, diff compares two listings with the exit codes the
// contract asks for. It exists so the pluggability is tested without pinning
// testease to any one instrument.
func fakeInstrument(t *testing.T) provider.Instrument {
	t.Helper()

	dir := t.TempDir()
	diff := filepath.Join(dir, "diff.sh")
	body := "#!/bin/bash\nif cmp -s \"$1\" \"$2\"; then exit 0; fi\ndiff \"$1\" \"$2\"\nexit 1\n"
	if err := os.WriteFile(diff, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake instrument: %v", err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("no bash here: %v", err)
	}
	return provider.Instrument{
		Name:    "fake@1",
		Collect: []string{"sh", "-c", "ls -1 /dev/shm 2>/dev/null | sort"},
		Diff:    []string{diff},
	}
}

func TestAConfiguredInstrumentMakesTheTargetAFingerprinter(t *testing.T) {
	t.Parallel()

	instrument := fakeInstrument(t)
	_, tgt, session := boot(t, provider.Options{Instrument: &instrument})
	ctx := context.Background()

	printer, ok := tgt.(target.Fingerprinter)
	if !ok {
		t.Fatal("a configured instrument did not make the target a Fingerprinter")
	}

	before, err := printer.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if before.Instrument != "fake@1" {
		t.Errorf("instrument = %q, want it named on the reading", before.Instrument)
	}

	// Nothing happened between the readings.
	unchanged, err := printer.Diff(ctx, before)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if unchanged.Result != target.Unchanged {
		t.Errorf("result = %q, want %q", unchanged.Result, target.Unchanged)
	}

	// Now something does.
	again, err := printer.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := session.RunScript(ctx, []byte("touch /dev/shm/new-file\n"), nil,
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	changed, err := printer.Diff(ctx, again)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if changed.Result != target.Changed {
		t.Errorf("result = %q, want %q", changed.Result, target.Changed)
	}
	if !bytes.Contains(changed.Detail, []byte("new-file")) {
		t.Errorf("diff detail = %q, want the instrument's own output kept", changed.Detail)
	}
}

func TestKeepLeavesTheTargetStandingAndSaysHowToClearIt(t *testing.T) {
	t.Parallel()

	client := requireDocker(t)
	var notices bytes.Buffer
	id := runID(t)
	p := provider.New(provider.Options{
		Client: client, RunID: id, Runner: "testease-test", Notices: &notices,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tgt, err := p.Resolve(ctx, target.Spec{Host: "box", Image: image()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := p.Release(ctx, tgt, true); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !strings.Contains(notices.String(), "docker rm -f") {
		t.Errorf("keeping a target printed no way to clear it: %q", notices.String())
	}

	// Still there, which is the point: a half-mutated box answers questions
	// the record does not.
	name := strings.TrimPrefix(tgt.Identity().Resolved["ref"].(string), "container:")
	if _, err := client.Output(ctx, "inspect", "-f", "{{.Id}}", name); err != nil {
		t.Errorf("a kept target was removed anyway: %v", err)
	}
	if _, err := client.Output(ctx, "rm", "-f", name); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	// The network outlives a kept target on purpose - it is still attached -
	// so removing the container by hand is what orphans it. That is reap's
	// job in a real run and this test's job here. Found by label rather than
	// by name, because the name is shortened to fit docker's limit.
	nets, err := client.Output(ctx, "network", "ls", "--filter", "label=testease.run="+id, "--format", "{{.Name}}")
	if err == nil {
		for _, n := range strings.Fields(nets) {
			if _, err := client.Output(ctx, "network", "rm", n); err != nil {
				t.Logf("network cleanup: %v", err)
			}
		}
	}
}

// removeNetworks clears the networks a run created, found by label because the
// name is shortened to fit docker's limit.
func removeNetworks(t *testing.T, client backend.Client, runID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := client.Output(ctx, "network", "ls", "--filter", "label=testease.run="+runID, "--format", "{{.Name}}")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(out) {
		_, _ = client.Output(ctx, "network", "rm", name)
	}
}

func TestADeclaredSchemeIsWhatTheEndpointSpeaks(t *testing.T) {
	t.Parallel()

	// The scheme was hardcoded to http regardless of port, so a host serving TLS
	// handed its suite an http:// URL and the browser aborted the navigation
	// against a box that was serving fine. It is declared rather than inferred
	// from the number: this lab serves TLS on 8443 as readily as on 443, so a
	// port-based guess is wrong on the box most likely to be driven.
	client := requireDocker(t)
	id := runID(t)
	p := provider.New(provider.Options{
		Client: client, RunID: id, Runner: "testease-test", Notices: io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	tgt, err := p.Resolve(ctx, target.Spec{
		Host: "box", Provider: "docker", Image: image(), Port: 443, Scheme: "https",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Release(context.Background(), tgt, false)
		removeNetworks(t, client, id)
	})

	e, ok := tgt.(interface{ Endpoint() (url.URL, bool) })
	if !ok {
		t.Fatal("a host declaring a port has no endpoint")
	}
	got, _ := e.Endpoint()
	if got.Scheme != "https" {
		t.Errorf("scheme = %q, want the one the host declared", got.Scheme)
	}

	// And the network it is on, so a drive container can join it.
	n, ok := tgt.(interface{ Network() (string, bool) })
	if !ok {
		t.Fatal("a docker target does not say what network it is on")
	}
	if name, on := n.Network(); !on || name == "" {
		t.Errorf("network = %q,%v want the cell's own", name, on)
	}
}
