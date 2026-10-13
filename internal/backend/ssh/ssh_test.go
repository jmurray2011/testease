package ssh

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	cssh "golang.org/x/crypto/ssh"
)

// The two things a remote backend has to get right before it is worth dialling
// anything: it must not invent an exit code it never learned, and it must give
// probes a third descriptor without folding facts into testimony.

func TestATransportFailureIsNotAnExitCode(t *testing.T) {
	t.Parallel()

	// An *ssh.ExitError carries a code the far side actually reported.
	got, known := exitCode(&cssh.ExitError{Waitmsg: cssh.Waitmsg{}})
	if !known {
		t.Error("an ExitError carries a real code and must be reported as known")
	}
	if got != 0 {
		t.Errorf("code = %d, want 0 for a zero Waitmsg", got)
	}

	// Anything else is the connection failing, not the command exiting. It
	// must come back unknown so the runner records infra-fail rather than
	// writing down a number nobody measured. Invariant 2: never invent
	// success, and -1 means "there is no exit code" rather than "it failed".
	for _, err := range []error{
		errors.New("connection reset by peer"),
		&cssh.ExitMissingError{},
	} {
		code, known := exitCode(err)
		if known {
			t.Errorf("%T reported a known exit code", err)
		}
		if code != -1 {
			t.Errorf("%T code = %d, want -1", err, code)
		}
	}

	// And a clean run is zero, known.
	if code, known := exitCode(nil); !known || code != 0 {
		t.Errorf("nil error = (%d,%v), want (0,true)", code, known)
	}
}

func TestFactsGetTheirOwnDescriptor(t *testing.T) {
	t.Parallel()

	// fd 3 is the structured channel. SSH gives a session stdout and stderr
	// and nothing else, so the wrapper has to open the third descriptor on the
	// far side and leave it somewhere fetchable. What must NOT happen is fd 3
	// being folded into stdout, which would let a judge recover a fact by
	// parsing testimony.
	cmd := wrapScript(nil, "/tmp/probe.sh", "/tmp/facts.3", []string{"a b", "c"})

	if !strings.Contains(cmd, "3>") {
		t.Errorf("wrapper opens no fd 3: %s", cmd)
	}
	if strings.Contains(cmd, "3>&1") || strings.Contains(cmd, "2>&1") {
		t.Errorf("wrapper merges channels that must stay apart: %s", cmd)
	}
	if !strings.Contains(cmd, "/tmp/facts.3") {
		t.Errorf("wrapper does not route fd 3 to the fact file: %s", cmd)
	}

	// Arguments are quoted, or an argument with a space becomes two arguments
	// and the probe measures something nobody asked for.
	if !strings.Contains(cmd, `'a b'`) {
		t.Errorf("argument with a space is not quoted: %s", cmd)
	}
}

func TestAProbeRunsThroughItsInterpreterOnNoexecTmp(t *testing.T) {
	t.Parallel()

	// A DISA STIG box mounts /tmp noexec, so the shipped probe cannot be
	// executed directly - it has to be handed to the interpreter its shebang
	// names, which reads the file rather than execve-ing it. The interpreter
	// therefore has to come FIRST on the command line and the fd 3 redirect has
	// to survive.
	interp := interpreter([]byte("#!/bin/bash\nprintf 'fact\\tk\\tv\\n' >&3\n"))
	if strings.Join(interp, " ") != "/bin/bash" {
		t.Fatalf("interpreter = %q, want [/bin/bash]", interp)
	}
	cmd := wrapScript(interp, "/tmp/probe.sh", "/tmp/facts.3", nil)
	if !strings.HasPrefix(cmd, `'/bin/bash' '/tmp/probe.sh'`) {
		t.Errorf("probe not run through its interpreter: %s", cmd)
	}
	if !strings.Contains(cmd, "3>") || !strings.Contains(cmd, "/tmp/facts.3") {
		t.Errorf("fd 3 lost when running through an interpreter: %s", cmd)
	}

	// `#!/usr/bin/env bash` must stay two tokens: quoting the whole line as one
	// would try to execute a file literally named "env bash".
	env := interpreter([]byte("#!/usr/bin/env bash\n"))
	if len(env) != 2 || env[0] != "/usr/bin/env" || env[1] != "bash" {
		t.Errorf("env shebang not split into two fields: %q", env)
	}

	// No shebang means no interpreter, and the wrapper falls back to a direct
	// invocation - the pre-existing behaviour, unchanged where /tmp allows exec.
	if interpreter([]byte("echo no shebang here")) != nil {
		t.Error("a script without a shebang must yield no interpreter")
	}
	if strings.HasPrefix(wrapScript(nil, "/tmp/p", "/tmp/f", nil), "'") == false {
		t.Error("without an interpreter the probe path should lead the command")
	}
}

func TestQuotingSurvivesASingleQuote(t *testing.T) {
	t.Parallel()

	// A path or argument carrying a single quote must not be able to end the
	// quoting and run as a command. This is the shell-injection seam.
	got := shellQuote(`it's`)
	if strings.Contains(got, `'it'`) && !strings.Contains(got, `'\''`) {
		t.Errorf("quote not escaped: %s", got)
	}
	if got == `'it's'` {
		t.Errorf("quoting is broken: %s", got)
	}
}

func TestSudoKeepsThePreparedCommandInsideOnePrivilegeBoundary(t *testing.T) {
	t.Parallel()

	line := "cd '/tmp/a b' && env 'MODE=one two' '/bin/bash' '/tmp/probe' 3>'/tmp/facts'"
	got := elevate(line)
	if !strings.HasPrefix(got, "sudo -n -- sh -c ") {
		t.Fatalf("elevated command = %q, want noninteractive sudo", got)
	}
	if !strings.Contains(got, `cd '\''/tmp/a b'\''`) {
		t.Errorf("working directory escaped the elevated shell: %s", got)
	}
	if !strings.Contains(got, `3>'\''/tmp/facts'\''`) {
		t.Errorf("structured descriptor escaped the elevated shell: %s", got)
	}
}

func TestMediaPutAtomicallyReplacesReadOnlyDestination(t *testing.T) {
	t.Parallel()

	got := mediaPutCommand("/var/tmp/testease-media/input.tar", fs.FileMode(0o444))
	for _, want := range []string{
		"mkdir -p '/var/tmp/testease-media'",
		"mktemp '/var/tmp/testease-media/.testease-put.XXXXXX'",
		"cat > \"$tmp\"",
		"chmod 444 \"$tmp\"",
		"mv -f \"$tmp\" '/var/tmp/testease-media/input.tar'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("media command lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "cat > '/var/tmp/testease-media/input.tar'") {
		t.Errorf("media command overwrites the read-only destination in place: %s", got)
	}
}
