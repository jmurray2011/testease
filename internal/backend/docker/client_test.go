package docker

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

func TestAKilledProcessReportsNoCode(t *testing.T) {
	t.Parallel()

	// exec.ExitError.ExitCode() returns -1 when a process was KILLED rather
	// than exited: there is no exit code, and -1 is how the standard library
	// says so. Reading that as the probe's status and returning no error tells
	// the runner the step was measured - so a probe the backstop killed lands
	// in the record as `rc=-1 status=measured`, which is the tool claiming a
	// measurement nobody took.
	//
	// Windows has no -1: a process its context killed exits 1, which is also
	// the commonest code a real probe returns. Only the caller knows it killed
	// it, so the caller says so.
	sleeper := fakeExecutable(t, "sleep", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := exec.CommandContext(ctx, sleeper).Run()
	if err == nil {
		t.Fatal("the process was not killed, so this tests nothing")
	}

	rc, reported := codeOf(err, ctx.Err() != nil)
	if reported {
		t.Errorf("a killed process reported a code of %d", rc)
	}
}

func TestAnOrdinaryFailureKeepsItsCode(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	err := exec.CommandContext(ctx, fakeExecutable(t, "exit-3", nil)).Run()
	rc, reported := codeOf(err, ctx.Err() != nil)
	if !reported || rc != 3 {
		t.Errorf("rc = %d reported = %v, want 3 and true", rc, reported)
	}
}

// The code a killed process leaves on Windows is a real reading when nothing
// killed it: a probe that exits 1 under a live context is measured as 1.
func TestAnExitOfOneUnderALiveContextIsMeasured(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	err := exec.CommandContext(ctx, fakeExecutable(t, "exit-1", nil)).Run()
	rc, reported := codeOf(err, ctx.Err() != nil)
	if !reported || rc != 1 {
		t.Errorf("rc = %d reported = %v, want 1 and true", rc, reported)
	}
}

// fakeBehaviours are the docker stand-ins the tests run, each answering exec,
// inspect and the rest as the test needs, so the transport-vs-probe split can
// be exercised without a daemon. A fake runs in its own process: whatever it
// needs from its test arrives as a parameter.
var fakeBehaviours = map[string]fakeBehaviour{
	"sleep":                    func(fakeCall) int { time.Sleep(5 * time.Second); return 0 },
	"exit-3":                   func(fakeCall) int { return 3 },
	"exit-1":                   func(fakeCall) int { return 1 },
	"exec-fails-in-docker":     fakeExecFailsInDocker,
	"exec-on-dead-container":   fakeExecOnDeadContainer,
	"exec-on-live-container":   fakeExecOnLiveContainer,
	"alpine-shells":            fakeAlpineShells,
	"structured-channel":       fakeStructuredChannel,
	"run-no-such-image":        fakeRunNoSuchImage,
	"run-exits-7":              fakeRunExits7,
	"remote-drive":             fakeRemoteDrive,
	"remote-drive-loses-it":    fakeRemoteDriveLosesIt,
	"sha256sum":                fakeSHA256Sum,
	"record-args":              fakeRecordArgs,
	"name-conflict":            fakeNameConflict,
	"inspect-describes-none":   fakeInspectDescribesNone,
	"inspect-describes-some":   fakeInspectDescribesSome,
	"exec-streams-then-sleeps": fakeExecStreamsThenSleeps,
	"exec-fails-late":          fakeExecFailsLate,
}

// 125 is the docker CLI's own failure code: the daemon refused or never
// answered, and the probe never ran.
func fakeExecFailsInDocker(c fakeCall) int {
	if c.arg(1) == "exec" {
		return 125
	}
	return 0
}

func TestDockersOwnExitCodeIsNotAMeasurement(t *testing.T) {
	t.Parallel()

	s := &Session{Client: fakeDocker(t, "exec-fails-in-docker", nil), Container: "box"}
	result, err := s.Run(t.Context(), []string{"true"}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("docker's own failure was recorded as a measurement")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1", result.RC)
	}
}

func TestAnExitCodeFromADeadContainerIsNotAMeasurement(t *testing.T) {
	t.Parallel()

	// Confirmed live: docker exec against a stopped or missing container exits
	// 1 with its own message, and that 1 would pass a negative control for a
	// probe that never executed.
	s := &Session{Client: fakeDocker(t, "exec-on-dead-container", nil), Container: "box"}
	result, err := s.Run(t.Context(), []string{"true"}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("a dead container's docker error was recorded as the probe's exit code")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1", result.RC)
	}
}

func fakeExecOnDeadContainer(c fakeCall) int {
	switch c.arg(1) {
	case "exec":
		_, _ = fmt.Fprintln(c.stderr, "container is not running")
		return 1
	case "inspect":
		_, _ = fmt.Fprintln(c.stdout, "false")
	}
	return 0
}

func fakeExecOnLiveContainer(c fakeCall) int {
	switch c.arg(1) {
	case "exec":
		return 3
	case "inspect":
		_, _ = fmt.Fprintln(c.stdout, "true")
	}
	return 0
}

func TestAProbesExitCodeSurvivesWhileTheContainerLives(t *testing.T) {
	t.Parallel()

	s := &Session{Client: fakeDocker(t, "exec-on-live-container", nil), Container: "box"}
	result, err := s.Run(t.Context(), []string{"false"}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.RC != 3 {
		t.Errorf("RC = %d, want the probe's own 3", result.RC)
	}
}

func TestRunScriptHonorsTheProbeShebangWhenBashIsAbsent(t *testing.T) {
	t.Parallel()

	// Alpine supplies /bin/sh but no bash. A probe that explicitly names sh is
	// executable there; the Docker transport must not replace that contract
	// with its own stronger shell requirement.
	s := &Session{Client: fakeDocker(t, "alpine-shells", nil), Container: "box"}
	result, err := s.RunScript(t.Context(), []byte("#!/bin/sh\nprintf 'ok\\n'\n"), nil,
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if result.RC != 0 {
		t.Errorf("RC = %d, want the /bin/sh probe to run without bash", result.RC)
	}
}

// fakeAlpineShells has /bin/sh and no bash.
func fakeAlpineShells(c fakeCall) int {
	switch c.arg(1) {
	case "exec":
		words := c.words()
		if slices.Contains(words, "bash") {
			return 127
		}
		if slices.Contains(words, "/bin/sh") {
			return 0
		}
		return 126
	case "inspect":
		_, _ = fmt.Fprintln(c.stdout, "true")
	}
	return 0
}

type observedStructured chan []byte

func (w observedStructured) Write(p []byte) (int, error) {
	w <- append([]byte(nil), p...)
	return len(p), nil
}

// fakeStructuredChannel writes a progress reading to the probe's fd 3 file,
// holds the probe open until the test releases it, then writes a final fact.
// The other execs read the file, as the poll and the drain do.
func fakeStructuredChannel(c fakeCall) int {
	channel := ""
	for _, arg := range c.args {
		if strings.HasPrefix(arg, "/tmp/testease-fd3-") {
			channel = c.inContainer(arg)
		}
	}
	joined := c.joined()
	switch {
	case strings.Contains(joined, " -i "):
		if err := os.MkdirAll(filepath.Dir(channel), 0o755); err != nil {
			return 1
		}
		if err := os.WriteFile(channel, []byte("progress\thttp.status\t404\n"), 0o644); err != nil {
			return 1
		}
		for {
			if _, err := os.Stat(c.params["release"]); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		f, err := os.OpenFile(channel, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 1
		}
		_, _ = f.WriteString("fact\thttp.final\t404\n")
		_ = f.Close()
		return 0
	case strings.Contains(joined, "rm -f"):
		body, _ := os.ReadFile(channel)
		_, _ = c.stdout.Write(body)
		_ = os.Remove(channel)
		return 0
	default:
		body, err := os.ReadFile(channel)
		if err != nil {
			return 1
		}
		_, _ = c.stdout.Write(body)
		return 0
	}
}

func TestRunScriptStreamsStructuredReadingsBeforeProbeExit(t *testing.T) {
	t.Parallel()

	release := filepath.Join(t.TempDir(), "release")
	session := &Session{Client: fakeDocker(t, "structured-channel", map[string]string{"release": release}), Container: "box"}
	readings := make(observedStructured, 16)
	done := make(chan error, 1)
	go func() {
		_, err := session.RunScript(t.Context(), []byte("#!/bin/sh\ntrue\n"), nil,
			target.RunOpts{Stdout: io.Discard, Stderr: io.Discard, Structured: readings})
		done <- err
	}()

	var first []byte
	select {
	case first = <-readings:
	case <-time.After(5 * time.Second):
		_ = os.WriteFile(release, nil, 0o600)
		t.Fatal("structured progress did not arrive before the probe exited")
	}
	if string(first) != "progress\thttp.status\t404\n" {
		t.Errorf("first reading = %q, want live HTTP status", first)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not finish after release")
	}
	var remaining string
	for len(readings) > 0 {
		remaining += string(<-readings)
	}
	if remaining != "fact\thttp.final\t404\n" {
		t.Errorf("remaining readings = %q, want the final fact exactly once", remaining)
	}
}

func fakeRunNoSuchImage(c fakeCall) int {
	if c.arg(1) == "run" {
		_, _ = fmt.Fprintln(c.stderr, "no such image")
		return 125
	}
	return 0
}

func fakeRunExits7(c fakeCall) int {
	if c.arg(1) == "run" {
		return 7
	}
	return 0
}

func TestDriveDoesNotReportDockersFailureAsTheSuites(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, "run-no-such-image", nil)
	result, err := c.Drive(t.Context(), DriveSpec{Image: "gone:1"},
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("docker run's own failure was recorded as the suite's RC")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1", result.RC)
	}
}

func TestDriveKeepsTheSuitesOwnExitCode(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, "run-exits-7", nil)
	result, err := c.Drive(t.Context(), DriveSpec{Image: "suite:1"},
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if result.RC != 7 {
		t.Errorf("RC = %d, want the suite's own 7", result.RC)
	}
}

// fakeRemoteDrive logs every command. Inbound docker cp consumes the archive
// and lists what it carried, as tar -t does. Outbound docker cp stands in for
// the daemon by leaving the evidence where the runner asked for it.
func fakeRemoteDrive(c fakeCall) int {
	if err := appendLine(c.params["log"], strings.Join(c.args, " ")); err != nil {
		return 1
	}
	if c.arg(1) == "cp" && c.arg(2) == "-" {
		names, err := tarNames(c.stdin)
		if err != nil {
			_, _ = fmt.Fprintln(c.stderr, err)
		}
		for _, name := range names {
			if err := appendLine(c.params["tar-log"], name); err != nil {
				return 1
			}
		}
		return 0
	}
	if c.arg(1) == "cp" && strings.HasSuffix(c.arg(2), ":/workspace/.") {
		facts := filepath.Join(c.arg(3), ".testease-facts")
		if err := os.WriteFile(facts, []byte("fact\tremote.drive\treturned\n"), 0o644); err != nil {
			return 1
		}
	}
	return 0
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func tarNames(r io.Reader) ([]string, error) {
	var names []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return names, err
		}
		names = append(names, h.Name)
	}
}

func TestRemoteDriveCarriesItsScenarioAndEvidenceAcrossTheContext(t *testing.T) {
	t.Parallel()

	scenario := t.TempDir()
	if err := os.WriteFile(filepath.Join(scenario, "suite.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	mediaDir := t.TempDir()
	media := filepath.Join(mediaDir, "suite.tar")
	if err := os.WriteFile(media, []byte("exact suite bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "docker.log")
	tarLog := filepath.Join(t.TempDir(), "tar.log")

	// Inbound docker cp consumes the archive. Outbound docker cp stands in for
	// the daemon by leaving the evidence where the runner asked for it.
	c := fakeDocker(t, "remote-drive", map[string]string{"log": log, "tar-log": tarLog})
	result, err := c.DriveRemote(t.Context(), DriveSpec{
		Image:         "suite:1",
		Workspace:     workspace,
		Mount:         "/workspace",
		Scenario:      scenario,
		ScenarioMount: "/scenario",
		Media: []target.Media{{
			Source: media, Dest: "/inputs/suite.tar", ReadOnly: true,
		}},
		Network: "cell-network",
	}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("DriveRemote: %v", err)
	}
	if result.RC != 0 {
		t.Errorf("RC = %d, want 0", result.RC)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".testease-facts")); err != nil {
		t.Fatalf("the returned workspace has no evidence: %v", err)
	}

	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(commands)
	for _, want := range []string{
		"create --name testease-drive-",
		"--network cell-network",
		"cp - testease-drive-",
		"start -a testease-drive-",
		":/workspace/. " + workspace,
		"rm -f testease-drive-",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docker commands do not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-v "+media+":/inputs/suite.tar") {
		t.Errorf("remote drive tried to bind a runner-local media path:\n%s", got)
	}
	packed, err := os.ReadFile(tarLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scenario/suite.sh", "workspace", "inputs/suite.tar"} {
		if !strings.Contains(string(packed), want) {
			t.Errorf("transferred archives do not contain %q:\n%s", want, packed)
		}
	}
}

// fakeRemoteDriveLosesIt takes the inbound archive and cannot return the
// workspace.
func fakeRemoteDriveLosesIt(c fakeCall) int {
	if c.arg(1) == "cp" && c.arg(2) == "-" {
		_, _ = io.Copy(io.Discard, c.stdin)
		return 0
	}
	if c.arg(1) == "cp" && strings.HasSuffix(c.arg(2), ":/workspace/.") {
		return 1
	}
	return 0
}

func TestRemoteDriveDoesNotMeasureASuiteWhoseEvidenceCannotReturn(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	c := fakeDocker(t, "remote-drive-loses-it", nil)
	result, err := c.DriveRemote(t.Context(), DriveSpec{
		Image: "suite:1", Workspace: workspace, Mount: "/workspace",
	}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("a lost remote workspace was recorded as a measurement")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1", result.RC)
	}
}

func TestRemoteDriveArchiveRefusesARelativeContainerDestination(t *testing.T) {
	t.Parallel()

	if _, err := directoryArchive(t.TempDir(), "workspace", 1000, 1000); err == nil {
		t.Fatal("a relative destination could escape the exact remote-drive layout")
	}
}

func TestTheWindowsExistsProbeKeepsThePathInOneArgument(t *testing.T) {
	t.Parallel()

	// A path with a space has to reach cmd as one token, or `if exist C:\Program`
	// becomes the condition and the rest the command to run when it holds - a
	// false condition runs nothing, cmd exits 0, and Stage certifies media that
	// never arrived.
	//
	// It reaches cmd as one token by BEING one argument, not by carrying quotes
	// of its own: quotes written into the argument do not survive the round trip
	// into a Windows command line, and the probe then answers "absent" about
	// every path there is. This asserts the shape; that the probe actually
	// answers is TestTheWindowsExistsProbeAnswersForAPathWithASpace, which needs
	// a real daemon because nothing on Linux evaluates a cmd expression.
	path := `C:\Program Files\vendor\setup.msi`
	probe := windowsExistsProbe(path)
	if !slices.Contains(probe, path) {
		t.Errorf("probe %q does not carry the path as a single argument", probe)
	}
	for _, arg := range probe {
		if strings.Contains(arg, `"`) {
			t.Errorf("probe %q quotes an argument; the command-line joiner does that", probe)
		}
	}
}

// fakeSHA256Sum answers the container's sha256sum, the third argument of
// `docker exec box sha256sum <path>`.
func fakeSHA256Sum(c fakeCall) int {
	switch c.arg(3) {
	case "sha256sum":
		_, _ = fmt.Fprintln(c.stdout, c.params["sum"]+"  /mounted/input")
		return 0
	case "inspect":
		_, _ = fmt.Fprintln(c.stdout, "true")
		return 0
	}
	return 1
}

func TestSHA256ReadsTheFileSeenByTheContainer(t *testing.T) {
	t.Parallel()

	want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	s := &Session{Client: fakeDocker(t, "sha256sum", map[string]string{"sum": want}), Container: "box"}
	got, err := s.SHA256(t.Context(), "/mounted/input")
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}
	if got != "sha256:"+want {
		t.Errorf("SHA256 = %q, want sha256:%s", got, want)
	}
}

func fakeRecordArgs(c fakeCall) int {
	if err := os.WriteFile(c.params["args"], []byte(strings.Join(c.args, " ")+"\n"), 0o644); err != nil {
		return 1
	}
	return 0
}

func TestPutOnAWindowsContainerSpeaksPowerShell(t *testing.T) {
	t.Parallel()

	// A Windows container has no sh and no cat: a Put spelled through them
	// fails on exactly the containers the OS field exists to describe.
	argFile := filepath.Join(t.TempDir(), "args")
	c := fakeDocker(t, "record-args", map[string]string{"args": argFile})

	s := &Session{Client: c, Container: "box", OS: Windows}
	if err := s.Put(t.Context(), strings.NewReader("hello"), `C:\dest.txt`, 0o644); err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("the fake docker was never invoked: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "powershell") {
		t.Errorf("Put ran %q, want PowerShell on a Windows container", got)
	}
	if strings.Contains(got, "sh -c") {
		t.Errorf("Put ran %q through sh, which a Windows container does not have", got)
	}
	if !strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("hello"))) {
		t.Errorf("Put ran %q without carrying the bytes", got)
	}
}

// Interrupting a run keeps its targets standing on purpose, and a container's
// name is a function of the run, the cell and the host - so resuming that run
// asks the daemon for a name the kept box already holds. Measured live: docker
// exits 125 with "Conflict. The container name ... is already in use". The
// refusal is right; without the remedy it reads as a daemon problem rather than
// as the earlier attempt's box waiting to be collected.
// fakeNameConflict refuses every command as docker refuses a taken name.
func fakeNameConflict(c fakeCall) int {
	_, _ = fmt.Fprintln(c.stderr, c.params["message"])
	return 125
}

func TestAKeptContainerBlockingItsOwnResumeSaysHowToClearIt(t *testing.T) {
	t.Parallel()

	conflict := `docker: Error response from daemon: Conflict. The container name "/testease-run-box" is already in use by container "2ee35e39b1b5". You have to remove (or rename) that container to be able to reuse that name.`
	c := fakeDocker(t, "name-conflict", map[string]string{"message": conflict})

	err := c.Up(t.Context(), Launch{Name: "testease-run-box", Image: "neutral:1", Network: "net", Alias: "box"})
	if err == nil {
		t.Fatal("a name already in use was not reported")
	}
	if !strings.Contains(err.Error(), "testease reap") {
		t.Errorf("error = %v, want the command that collects the earlier attempt's target", err)
	}
	if !strings.Contains(err.Error(), "testease-run-box") {
		t.Errorf("error = %v, want the container standing in the way named", err)
	}
}

// And it names the daemon it was talking to. `testease reap --force` looks in
// the current docker context; the box standing in the way is on whichever
// context this run dialled, so a remedy that omits it sends the operator to
// the wrong daemon, which reports nothing to reap and reads like a clean one.
func TestTheRemedyNamesTheContextTheKeptBoxIsOn(t *testing.T) {
	t.Parallel()

	conflict := `docker: Error response from daemon: Conflict. The container name "/testease-run-box" is already in use by container "2ee35e39b1b5".`
	c := fakeDocker(t, "name-conflict", map[string]string{"message": conflict})
	c.Context = "winhost"

	err := c.Up(t.Context(), Launch{Name: "testease-run-box", Image: "neutral:1", Network: "net", Alias: "box"})
	if err == nil {
		t.Fatal("a name already in use was not reported")
	}
	if !strings.Contains(err.Error(), "--docker-contexts winhost") {
		t.Errorf("error = %v, want the context the box is on named", err)
	}
}

// REAP'S WORST FAILURE IS A QUIET ONE. The listing says a daemon holds three
// labelled containers and the batched inspect that describes them fails
// outright - the daemon stopped answering between the two calls, or the argv
// went past E2BIG. The error is deliberately tolerated so that one container
// removed mid-listing does not hide the survivors, but tolerating a TOTAL
// failure hands back an empty map and a nil error, and reap then prints
// "nothing to reap" over leftovers that are still standing.
func fakeInspectDescribesNone(c fakeCall) int {
	switch c.arg(1) {
	case "ps":
		_, _ = fmt.Fprint(c.stdout, "testease-a-box\ntestease-b-box\ntestease-c-box\n")
		return 0
	case "inspect":
		_, _ = fmt.Fprintln(c.stderr, "Cannot connect to the Docker daemon")
		return 1
	}
	return 0
}

func TestAnInspectThatDescribedNothingIsNotAnEmptyDaemon(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, "inspect-describes-none", nil)

	found, err := c.Labelled(t.Context(), "testease.run")
	if err == nil {
		t.Fatalf("three containers were listed and none described, reported as %d found and no error", len(found))
	}
}

// And a listing whose inspect described SOME of them still prints those: a
// container removed between the listing and the inspect makes the whole call
// non-zero, and losing the survivors to it is the reason the error is
// tolerated in the first place.
func fakeInspectDescribesSome(c fakeCall) int {
	switch c.arg(1) {
	case "ps":
		_, _ = fmt.Fprint(c.stdout, "testease-a-box\ntestease-b-box\n")
		return 0
	case "inspect":
		_, _ = fmt.Fprint(c.stdout, "testease-a-box\t{\"testease.run\":\"r1\"}\n")
		_, _ = fmt.Fprintln(c.stderr, "No such object: testease-b-box")
		return 1
	}
	return 0
}

func TestAPartlyDescribedListingKeepsTheSurvivors(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, "inspect-describes-some", nil)

	found, err := c.Labelled(t.Context(), "testease.run")
	if err != nil {
		t.Fatalf("Labelled: %v", err)
	}
	if len(found) != 1 || found["testease-a-box"]["testease.run"] != "r1" {
		t.Errorf("found = %v, want the one container the inspect described", found)
	}
}
