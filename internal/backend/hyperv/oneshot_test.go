package hyperv

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dockerbackend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/target"
)

// One shape for both daemons: the probe goes in as a file, the readings come
// back as a file. What differs is the shell, not the machine.

func TestTheProbeIsStagedWhereItSurvives(t *testing.T) {
	t.Parallel()

	// MEASURED. A file staged into C:\Windows\Temp lands in the layer and is
	// then discarded by start, because per-container writes under the system
	// directory do not survive into the running container. The root survives,
	// in both directions, on both daemons.
	for _, windows := range []bool{true, false} {
		opt := target.RunOpts{}
		_, stage, err := OneShot{}.plan([]byte("true\n"), nil, windows, &opt)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(stage) != 1 {
			t.Fatalf("stage = %+v, want the probe staged", stage)
		}
		if strings.Contains(strings.ToLower(stage[0].Remote), "windows") {
			t.Errorf("staged at %q, which start discards", stage[0].Remote)
		}
		if !strings.HasPrefix(stage[0].Remote, "/") {
			t.Errorf("staged at %q; docker cp wants the leading-slash form", stage[0].Remote)
		}
		if strings.Contains(stage[0].Remote, ":") {
			t.Errorf("staged at %q; docker cp rejects a drive letter", stage[0].Remote)
		}
	}
}

func TestEveryProbeIsToldWhereToPutAReading(t *testing.T) {
	t.Parallel()

	for _, windows := range []bool{true, false} {
		opt := target.RunOpts{}
		if _, _, err := (OneShot{}).plan(nil, nil, windows, &opt); err != nil {
			t.Fatalf("plan: %v", err)
		}
		if !strings.Contains(strings.Join(opt.Env, " "), "TESTEASE_FACTS="+factsAt) {
			t.Errorf("windows=%v: env = %v, want the readings path named", windows, opt.Env)
		}
	}
}

func TestAShellWithDescriptorsGetsFdThree(t *testing.T) {
	t.Parallel()

	// So a probe written for any other backend runs here unchanged.
	opt := target.RunOpts{}
	command, _, err := OneShot{}.plan(nil, []string{"3.0"}, false, &opt)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "exec 3>"+factsAt) {
		t.Errorf("command = %q, want fd 3 opened", joined)
	}
	if strings.Contains(joined, "powershell") {
		t.Errorf("a Linux cell runs %q", joined)
	}
	if !strings.HasSuffix(joined, "3.0") {
		t.Errorf("the step's arguments did not reach the probe: %q", joined)
	}
}

func TestAShellWithoutThemGetsThePathAndItsOwnSpelling(t *testing.T) {
	t.Parallel()

	// PowerShell has no third descriptor, and it will only run -File on
	// something named .ps1 - at a path spelled with a drive letter, which is
	// not the spelling docker cp accepts.
	opt := target.RunOpts{}
	command, stage, err := OneShot{}.plan(nil, []string{"3.0"}, true, &opt)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	joined := strings.Join(command, " ")

	if !strings.HasSuffix(stage[0].Remote, ".ps1") {
		t.Errorf("staged as %q; PowerShell will not run -File on that", stage[0].Remote)
	}
	if !strings.Contains(joined, `C:\testease-probe.ps1`) {
		t.Errorf("command = %q, want the path spelled the way the container reads it", joined)
	}
	if strings.Contains(joined, "exec 3>") {
		t.Errorf("PowerShell was given a descriptor it does not have: %q", joined)
	}
}

func TestTheCreateArgumentsCarryOwnershipAndBounds(t *testing.T) {
	t.Parallel()

	o := OneShot{
		Name:   "testease-run-cell-box-1",
		Image:  "neutral:1",
		Memory: "6g",
		Labels: map[string]string{"testease.run": "run-1", "testease.pid": "42"},
	}
	got := strings.Join(o.createArgs([]string{"/bin/sh", "-c", "true"}, []string{"K=V"}), " ")

	for _, want := range []string{
		"create", "--name testease-run-cell-box-1", "-m 6g",
		"--label testease.pid=42", "-e K=V", "neutral:1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("create args %q are missing %q", got, want)
		}
	}
	// The image goes last before the command, or docker reads the command's own
	// flags as its own.
	if strings.Index(got, "neutral:1") > strings.Index(got, "/bin/sh") {
		t.Errorf("the image must precede the command: %q", got)
	}
}

func TestMediaIsStagedBesideTheProbe(t *testing.T) {
	t.Parallel()

	// A one-shot cell has no session, so there is no separate call in which to
	// put a file on it: whatever a probe needs has to arrive in the same window
	// the probe does, while the container is merely created. A helper library a
	// scenario ships is exactly that.
	dir := t.TempDir()
	helper := filepath.Join(dir, "lib.ps1")
	if err := os.WriteFile(helper, []byte("function Get-Thing { 1 }\n"), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	shot := OneShot{
		Image: "neutral:1",
		Media: []Stage{{Local: helper, Remote: `C:\lib.ps1`}},
	}

	var opt target.RunOpts
	_, stage, err := shot.plan([]byte("Write-Output ok\n"), nil, true, &opt)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	var found bool
	for _, s := range stage {
		if s.Remote == `C:\lib.ps1` && s.Local == helper {
			found = true
		}
	}
	if !found {
		t.Errorf("media did not reach the staging list: %+v", stage)
	}
}

// fakeDocker writes a docker stand-in that answers create/cp/start/rm/stop the
// way the daemon would, so Run's whole choreography can be exercised without
// one. `cp <cell>:/wanted.txt <local>` hands back a fixed body; /missing.txt
// is refused the way a file that is not there is.
func fakeDocker(t *testing.T, startExit int) string {
	t.Helper()
	// A container that ran and reported that code, which is the ordinary case.
	return fakeDockerIn(t, startExit, "exited", startExit)
}

// fakeDockerIn is a docker whose `start -a` exits with one code and whose
// container is left in a given state. The two are separate because they are
// separate on a real daemon: `docker start -a` exits with the CONTAINER's
// status when the container ran, and with its own when the container never
// started, and only the state says which happened.
func fakeDockerIn(t *testing.T, startExit int, state string, containerExit int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "docker")
	body := `#!/bin/bash
cmd="$1"; shift
case "$cmd" in
  rm|stop|create) exit 0;;
  inspect)
    printf '` + state + ` ` + fmt.Sprint(containerExit) + `'
    exit 0;;
  cp)
    src="$1"; dst="$2"
    case "$src" in
      *:/wanted.txt) printf 'kept bytes' > "$dst"; exit 0;;
      *:*/testease-facts) exit 1;;
      *:*) echo "no such file" >&2; exit 1;;
      *) exit 0;;
    esac;;
  start) exit ` + fmt.Sprint(startExit) + `;;
  *) exit 0;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	return path
}

func TestDeclaredFetchesTravelWithTheCommand(t *testing.T) {
	t.Parallel()

	shot := OneShot{
		Client: dockerbackend.Client{Bin: fakeDocker(t, 0)},
		OS:     Linux,
		Name:   "cell",
		Image:  "neutral:1",
	}
	result, err := shot.Run(t.Context(), []byte("true\n"), nil, target.RunOpts{
		Stdout: io.Discard, Stderr: io.Discard,
		Fetch: []string{"/wanted.txt", "/missing.txt"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(result.Fetched["/wanted.txt"]); got != "kept bytes" {
		t.Errorf("fetched %q, want the file the cell left behind", got)
	}
	if _, there := result.Fetched["/missing.txt"]; there {
		t.Error("a file that was not there must be absent, not present and empty")
	}
}

func TestDockersOwnFailureIsNotACellExitCode(t *testing.T) {
	t.Parallel()

	// The daemon refusing to start the container: `docker start -a` reports
	// its own failure and the container is left in `created`, never having
	// run. MEASURED - the code beside that state is meaningless, because no
	// process of the cell's produced it.
	shot := OneShot{
		Client: dockerbackend.Client{Bin: fakeDockerIn(t, 1, "created", 127)},
		OS:     Linux,
		Name:   "cell",
		Image:  "neutral:1",
	}
	result, err := shot.Run(t.Context(), []byte("true\n"), nil, target.RunOpts{
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("docker's own failure was recorded as a measurement")
	}
	if result.RC != -1 {
		t.Errorf("RC = %d, want -1: docker's code is not the cell's", result.RC)
	}
}

func TestACellsRealExitCodeIsKept(t *testing.T) {
	t.Parallel()

	shot := OneShot{
		Client: dockerbackend.Client{Bin: fakeDocker(t, 3)},
		OS:     Linux,
		Name:   "cell",
		Image:  "neutral:1",
	}
	result, err := shot.Run(t.Context(), []byte("exit 3\n"), nil, target.RunOpts{
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.RC != 3 {
		t.Errorf("RC = %d, want the cell's own 3", result.RC)
	}
}

// THE 125/126/127 CONVENTIONS ARE `docker run`'s, AND THIS IS `docker start -a`.
//
// Measured against a live daemon on a plain image:
//
//	docker create ... sh -c 'nosuchtool'  -> start -a exits 127, state `exited 127`
//	docker create ... /nosuchbinary       -> start -a exits   1, state `created 127`
//	docker create ... sh -c 'exit 126'    -> start -a exits 126, state `exited 126`
//
// So a code cannot say whose it is and the container's STATE can: a cell that
// ran reports its own status through start, whatever the number. 127 is the
// no-curl-on-the-appliance case this lab has already met - a probe that shells
// out to a tool the image does not carry - and discarding it as infrastructure
// throws away a real reading AND, through the early return, that cell's fd3
// facts and declared artifacts with it.
func TestAProbeThatCouldNotFindItsToolKeepsItsOwnExitCode(t *testing.T) {
	t.Parallel()

	for _, rc := range []int{125, 126, 127} {
		shot := OneShot{
			Client: dockerbackend.Client{Bin: fakeDockerIn(t, rc, "exited", rc)},
			OS:     Linux,
			Name:   "cell",
			Image:  "neutral:1",
		}
		result, err := shot.Run(t.Context(), []byte("nosuchtool\n"), nil, target.RunOpts{
			Stdout: io.Discard, Stderr: io.Discard,
			Fetch: []string{"/wanted.txt"},
		})
		if err != nil {
			t.Fatalf("rc %d: Run: %v", rc, err)
		}
		if result.RC != rc {
			t.Errorf("rc %d: RC = %d, want the cell's own", rc, result.RC)
		}
		// The early return did not just lose the code. It ran before the
		// readings were taken, so the cell lost its evidence too.
		if got := string(result.Fetched["/wanted.txt"]); got != "kept bytes" {
			t.Errorf("rc %d: fetched %q, want the artifact the cell left behind", rc, got)
		}
	}
}
