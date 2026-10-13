package docker

import (
	"context"
	"encoding/base64"
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
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := exec.CommandContext(ctx, "sleep", "5").Run()
	if err == nil {
		t.Fatal("the process was not killed, so this tests nothing")
	}

	rc, reported := codeOf(err)
	if reported {
		t.Errorf("a killed process reported a code of %d", rc)
	}
}

func TestAnOrdinaryFailureKeepsItsCode(t *testing.T) {
	t.Parallel()

	err := exec.Command("sh", "-c", "exit 3").Run()
	rc, reported := codeOf(err)
	if !reported || rc != 3 {
		t.Errorf("rc = %d reported = %v, want 3 and true", rc, reported)
	}
}

// fakeDocker writes a docker stand-in whose exec and inspect answers are the
// script's, so the transport-vs-probe split can be exercised without a daemon.
func fakeDocker(t *testing.T, body string) Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	return Client{Bin: path}
}

func TestDockersOwnExitCodeIsNotAMeasurement(t *testing.T) {
	t.Parallel()

	// 125 is the docker CLI's own failure code: the daemon refused or never
	// answered, and the probe never ran.
	s := &Session{Client: fakeDocker(t, `[ "$1" = exec ] && exit 125; exit 0`), Container: "box"}
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
	script := `case "$1" in
exec) echo "container is not running" >&2; exit 1;;
inspect) echo false; exit 0;;
*) exit 0;;
esac`
	s := &Session{Client: fakeDocker(t, script), Container: "box"}
	result, err := s.Run(t.Context(), []string{"true"}, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("a dead container's docker error was recorded as the probe's exit code")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1", result.RC)
	}
}

func TestAProbesExitCodeSurvivesWhileTheContainerLives(t *testing.T) {
	t.Parallel()

	script := `case "$1" in
exec) exit 3;;
inspect) echo true; exit 0;;
*) exit 0;;
esac`
	s := &Session{Client: fakeDocker(t, script), Container: "box"}
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
	script := `case "$1" in
exec)
  case " $* " in *" bash "*) exit 127;; esac
  case " $* " in *" /bin/sh "*) exit 0;; esac
  exit 126;;
inspect) echo true; exit 0;;
*) exit 0;;
esac`
	s := &Session{Client: fakeDocker(t, script), Container: "box"}
	result, err := s.RunScript(t.Context(), []byte("#!/bin/sh\nprintf 'ok\\n'\n"), nil,
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if result.RC != 0 {
		t.Errorf("RC = %d, want the /bin/sh probe to run without bash", result.RC)
	}
}

func TestDriveDoesNotReportDockersFailureAsTheSuites(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, `[ "$1" = run ] && { echo "no such image" >&2; exit 125; }; exit 0`)
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

	c := fakeDocker(t, `[ "$1" = run ] && exit 7; exit 0`)
	result, err := c.Drive(t.Context(), DriveSpec{Image: "suite:1"},
		target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if result.RC != 7 {
		t.Errorf("RC = %d, want the suite's own 7", result.RC)
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
	script := `printf '%s\n' "$*" >> "` + log + `"
if [ "$1" = cp ] && [ "$2" = - ]; then
  tar -tf - >> "` + tarLog + `"
  exit 0
fi
if [ "$1" = cp ] && [[ "$2" == *:/workspace/. ]]; then
  printf 'fact\tremote.drive\treturned\n' > "$3/.testease-facts"
  exit 0
fi
if [ "$1" = start ]; then exit 0; fi
exit 0`
	c := fakeDocker(t, script)
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

func TestRemoteDriveDoesNotMeasureASuiteWhoseEvidenceCannotReturn(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	c := fakeDocker(t, `
if [ "$1" = cp ] && [ "$2" = - ]; then cat >/dev/null; exit 0; fi
if [ "$1" = cp ] && [[ "$2" == *:/workspace/. ]]; then exit 1; fi
exit 0`)
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

func TestSHA256ReadsTheFileSeenByTheContainer(t *testing.T) {
	t.Parallel()

	want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	script := `case "$3" in
sha256sum) echo "` + want + `  /mounted/input"; exit 0;;
inspect) echo true; exit 0;;
*) exit 1;;
esac`
	s := &Session{Client: fakeDocker(t, script), Container: "box"}
	got, err := s.SHA256(t.Context(), "/mounted/input")
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}
	if got != "sha256:"+want {
		t.Errorf("SHA256 = %q, want sha256:%s", got, want)
	}
}

func TestPutOnAWindowsContainerSpeaksPowerShell(t *testing.T) {
	t.Parallel()

	// A Windows container has no sh and no cat: a Put spelled through them
	// fails on exactly the containers the OS field exists to describe.
	argFile := filepath.Join(t.TempDir(), "args")
	c := fakeDocker(t, `echo "$@" > `+argFile+`; exit 0`)

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
func TestAKeptContainerBlockingItsOwnResumeSaysHowToClearIt(t *testing.T) {
	t.Parallel()

	conflict := `echo 'docker: Error response from daemon: Conflict. The container name "/testease-run-box" is already in use by container "2ee35e39b1b5". You have to remove (or rename) that container to be able to reuse that name.' >&2; exit 125`
	c := fakeDocker(t, conflict)

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

	conflict := `echo 'docker: Error response from daemon: Conflict. The container name "/testease-run-box" is already in use by container "2ee35e39b1b5".' >&2; exit 125`
	c := fakeDocker(t, conflict)
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
func TestAnInspectThatDescribedNothingIsNotAnEmptyDaemon(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, `
case "$1" in
  ps)      printf 'testease-a-box\ntestease-b-box\ntestease-c-box\n'; exit 0;;
  inspect) echo 'Cannot connect to the Docker daemon' >&2; exit 1;;
esac
exit 0`)

	found, err := c.Labelled(t.Context(), "testease.run")
	if err == nil {
		t.Fatalf("three containers were listed and none described, reported as %d found and no error", len(found))
	}
}

// And a listing whose inspect described SOME of them still prints those: a
// container removed between the listing and the inspect makes the whole call
// non-zero, and losing the survivors to it is the reason the error is
// tolerated in the first place.
func TestAPartlyDescribedListingKeepsTheSurvivors(t *testing.T) {
	t.Parallel()

	c := fakeDocker(t, `
case "$1" in
  ps)      printf 'testease-a-box\ntestease-b-box\n'; exit 0;;
  inspect) printf 'testease-a-box\t{"testease.run":"r1"}\n'; echo 'No such object: testease-b-box' >&2; exit 1;;
esac
exit 0`)

	found, err := c.Labelled(t.Context(), "testease.run")
	if err != nil {
		t.Fatalf("Labelled: %v", err)
	}
	if len(found) != 1 || found["testease-a-box"]["testease.run"] != "r1" {
		t.Errorf("found = %v, want the one container the inspect described", found)
	}
}
