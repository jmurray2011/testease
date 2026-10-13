// Package docker dials a container over the docker CLI.
//
// The CLI rather than the API on purpose: it is the only interface that
// already understands docker contexts, so a target on another daemon is a
// flag rather than a transport, and it keeps the binary free of a client
// library. dockerd is a dependency of dialing docker either way.
package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

// Client runs docker commands, optionally against a named context.
type Client struct {
	// Bin is the docker executable. Empty means "docker" on PATH.
	Bin string

	// Context is a docker context name, as in `docker --context`. Empty uses
	// whatever the environment selects.
	Context string

	// Log records what was invoked, at debug. When a boot fails after three
	// minutes, the commands that were tried are the whole diagnosis; without
	// them there is one line saying it did not work. Nil says nothing.
	Log *slog.Logger
}

// CleanupBudget bounds work that outlives the caller's context. It is longer
// than `docker stop -t 10`'s own grace, which is the shortest thing a bound
// here must not cut off.
const CleanupBudget = 30 * time.Second

// CleanupContext detaches from the caller's cancellation and keeps a deadline.
//
// Detaching is deliberate - a ctrl-C must not stop us removing what we made -
// but context.WithoutCancel strips the DEADLINE along with the cancellation,
// so against a daemon that accepts a connection and then stops answering the
// interrupt path had no bound at all. A removal that expires leaves a target
// standing, which is visible and is what `testease reap` exists for.
func CleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), CleanupBudget)
}

// reapContext is the flag that points reap at this daemon, or nothing when
// this is the context reap already looks in.
func (c Client) reapContext() string {
	if c.Context == "" {
		return ""
	}
	return " --docker-contexts " + c.Context
}

func (c Client) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

func (c Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

func (c Client) command(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if c.Context != "" {
		full = append(full, "--context", c.Context)
	}
	full = append(full, args...)

	// Redacted: an env value in a log line is an env value in CI output.
	c.log().Debug("docker", "args", redact(full))
	return exec.CommandContext(ctx, c.bin(), full...)
}

// Output runs a docker command and returns its stdout, with stderr folded into
// the error so a failure says what docker said about it.
func (c Client) Output(ctx context.Context, args ...string) (string, error) {
	cmd := c.command(ctx, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Session is an execution surface on one running container.
type Session struct {
	Client    Client
	Container string

	// OS is what the daemon runs. A Windows container has no bash and no third
	// descriptor, so how a probe reaches it differs - and that is a difference
	// about the shell rather than about the machine.
	OS string
}

func (s *Session) windows() bool { return strings.EqualFold(s.OS, Windows) }

var _ target.Session = (*Session)(nil)

// Run executes a command on the target.
//
// Stdin is deliberately not connected. A step whose command happens to read
// stdin would start consuming the runner's own input, which inside a fan-out
// loop is the loop's input - measured, once, expensively.
func (s *Session) Run(ctx context.Context, cmd []string, opt target.RunOpts) (target.Result, error) {
	args := s.execArgs(false, opt)
	args = append(args, cmd...)
	return s.exec(ctx, args, nil, opt)
}

// RunScript ships a script and runs it with args, in one round trip, so a
// step's log reads in order.
//
// The script arrives on stdin, which is why this is the call that connects it.
// The probe's fd 3 - the structured channel facts travel on - is redirected to
// a file on the target and collected afterwards by exec-cat: docker exec
// carries three streams and no more, and folding facts into stdout would put
// readings in the same channel as testimony.
func (s *Session) RunScript(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	if s.windows() {
		return s.runScriptWindows(ctx, script, args, opt)
	}

	channel := fmt.Sprintf("/tmp/testease-fd3-%d", time.Now().UnixNano())

	// $0 is a placeholder, $1 is the channel path, the rest are the probe's
	// interpreter followed by its arguments. exec replaces the wrapper so the
	// probe is the process whose exit code docker reports.
	wrapper := `fd3="$1"; shift; exec 3>"$fd3"; exec "$@"`
	if opt.Timeout > 0 {
		// The bound is applied inside the container so that it kills the
		// probe rather than the client watching it. A host-side deadline
		// would leave the real process running and report on a corpse.
		wrapper = `fd3="$1"; shift; exec 3>"$fd3"; exec timeout ` +
			strconv.Itoa(int(opt.Timeout.Seconds())) + ` "$@"`
	}

	execArgs := s.execArgs(true, opt)
	execArgs = append(execArgs, "sh", "-c", wrapper, "testease", channel)
	execArgs = append(execArgs, scriptInterpreter(script)...)
	execArgs = append(execArgs, "-s", "--")
	execArgs = append(execArgs, args...)

	result, err := s.exec(ctx, execArgs, bytes.NewReader(script), opt)

	// Collect the structured channel whatever happened to the probe: a probe
	// that died after recording a reading still recorded it.
	s.drainChannel(ctx, channel, opt.Structured)
	return result, err
}

// scriptInterpreter returns the command a Unix probe declares in its shebang.
// Docker streams the probe on stdin, so a script with no declaration keeps the
// historical bash default. Each field becomes its own exec argument, which
// preserves forms such as `#!/usr/bin/env bash` without shell interpolation.
func scriptInterpreter(script []byte) []string {
	if !bytes.HasPrefix(script, []byte("#!")) {
		return []string{"bash"}
	}
	line := script[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	interpreter := strings.Fields(strings.TrimSpace(string(line)))
	if len(interpreter) == 0 {
		return []string{"bash"}
	}
	return interpreter
}

// Exists answers whether a path is on the target. The question is the same on
// both platforms; only how you ask it differs, and that difference lives here.
func (s *Session) Exists(ctx context.Context, path string) (bool, error) {
	probe := []string{"test", "-e", path}
	if s.windows() {
		probe = windowsExistsProbe(s.Client.ContainerPath(ctx, path))
	}
	result, err := s.Run(ctx, probe, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return false, err
	}
	return result.RC == 0, nil
}

// SHA256 reads the identity of the file the target can actually see. This is
// deliberately asked through the running container rather than calculated
// from the host path: a remote daemon or a private mount namespace may resolve
// the same spelling to different bytes (or to an empty directory).
func (s *Session) SHA256(ctx context.Context, path string) (string, error) {
	probe := []string{"sha256sum", "--", path}
	if s.windows() {
		path = s.Client.ContainerPath(ctx, path)
		quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
		probe = []string{
			"powershell", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-FileHash -LiteralPath " + quoted + " -Algorithm SHA256).Hash",
		}
	}

	var out, errBuf bytes.Buffer
	result, err := s.Run(ctx, probe, target.RunOpts{Stdout: &out, Stderr: &errBuf})
	if err != nil {
		return "", err
	}
	if result.RC != 0 {
		return "", fmt.Errorf("hash file: exit %d: %s", result.RC, strings.TrimSpace(errBuf.String()))
	}
	fields := strings.Fields(out.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("hash file: no digest returned")
	}
	raw := strings.ToLower(fields[0])
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("hash file: invalid SHA-256 %q", fields[0])
	}
	return "sha256:" + raw, nil
}

// Fetch retrieves a file by exec-cat rather than docker cp. A tmpfs path is
// unreadable by docker cp - measured - and probes write where they like. A
// Windows container has no cat, so the bytes come back base64-encoded through
// PowerShell instead, which also keeps them binary-safe across the console.
func (s *Session) Fetch(ctx context.Context, remote string) (io.ReadCloser, error) {
	if s.windows() {
		return s.fetchWindows(ctx, remote)
	}
	cmd := s.Client.command(ctx, "exec", s.Container, "cat", remote)
	return commandStream(cmd, func(err error, stderr string) error {
		return fmt.Errorf("fetch %s from %s: %w: %s", remote, s.Container, err, stderr)
	})
}

func (s *Session) fetchWindows(ctx context.Context, remote string) (io.ReadCloser, error) {
	quoted := "'" + strings.ReplaceAll(remote, "'", "''") + "'"
	cmd := s.Client.command(ctx, "exec", s.Container, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		"[Convert]::ToBase64String([IO.File]::ReadAllBytes("+quoted+"))")
	raw, err := commandStream(cmd, func(err error, stderr string) error {
		return fmt.Errorf("fetch %s from %s: %w: %s", remote, s.Container, err, stderr)
	})
	if err != nil {
		return nil, err
	}
	return &readerCloser{Reader: base64.NewDecoder(base64.StdEncoding, raw), Closer: raw}, nil
}

type readerCloser struct {
	io.Reader
	io.Closer
}

// commandReadCloser turns a process stdout pipe into a stream without losing
// its eventual exit status. The first terminal read waits for the command and
// reports a late failure after returning every byte already produced.
type commandReadCloser struct {
	body    io.ReadCloser
	cmd     *exec.Cmd
	stderr  *bytes.Buffer
	explain func(error, string) error
	once    sync.Once
	waitErr error
}

func commandStream(cmd *exec.Cmd, explain func(error, string) error) (io.ReadCloser, error) {
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	body, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = body.Close()
		return nil, explain(err, strings.TrimSpace(stderr.String()))
	}
	return &commandReadCloser{body: body, cmd: cmd, stderr: stderr, explain: explain}, nil
}

func (r *commandReadCloser) Read(p []byte) (int, error) {
	n, readErr := r.body.Read(p)
	if readErr == nil {
		return n, nil
	}
	if waitErr := r.wait(); waitErr != nil {
		return n, r.explain(waitErr, strings.TrimSpace(r.stderr.String()))
	}
	return n, readErr
}

func (r *commandReadCloser) Close() error {
	closeErr := r.body.Close()
	return errors.Join(closeErr, r.wait())
}

func (r *commandReadCloser) wait() error {
	r.once.Do(func() { r.waitErr = r.cmd.Wait() })
	return r.waitErr
}

// alive is whether the container still runs, asked when an exec fails: docker
// reports a dead target with an ordinary exit code, and only the daemon can
// say which kind of answer that was.
func (s *Session) alive(ctx context.Context) bool {
	out, err := s.Client.Output(ctx, "inspect", "-f", "{{.State.Running}}", s.Container)
	return err == nil && strings.TrimSpace(out) == "true"
}

// Put places a file on the target, again without docker cp, and for the same
// reason. A Windows container has no sh and no cat, so the bytes travel
// base64-encoded through PowerShell instead - the same bargain Fetch strikes
// in the other direction.
func (s *Session) Put(ctx context.Context, local io.Reader, remote string, mode os.FileMode) error {
	if s.windows() {
		return s.putWindows(ctx, local, remote)
	}
	script := `dest="$1"; cat > "$dest" && chmod "$2" "$dest"`
	cmd := s.Client.command(ctx, "exec", "-i", s.Container,
		"sh", "-c", script, "testease", remote, fmt.Sprintf("%o", mode.Perm()))
	cmd.Stdin = local

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("put %s on %s: %w: %s", remote, s.Container, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// putWindows carries the file inside the command, which bounds it to what a
// command line holds - the same bound the winrm backend accepts, and Put's
// callers ship small files. The mode is dropped: NTFS has no bits to chmod.
func (s *Session) putWindows(ctx context.Context, local io.Reader, remote string) error {
	body, err := io.ReadAll(local)
	if err != nil {
		return fmt.Errorf("read what to put on %s: %w", s.Container, err)
	}

	quoted := "'" + strings.ReplaceAll(remote, "'", "''") + "'"
	script := "$dir = Split-Path -Parent " + quoted + "; " +
		"if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir | Out-Null }; " +
		"[IO.File]::WriteAllBytes(" + quoted + ", [Convert]::FromBase64String('" +
		base64.StdEncoding.EncodeToString(body) + "'))"

	cmd := s.Client.command(ctx, "exec", s.Container,
		"powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("put %s on %s: %w: %s", remote, s.Container, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// Close does nothing: the session is the container, and whether the container
// outlives the run is the provider's decision, not the session's.
func (s *Session) Close() error { return nil }

func (s *Session) execArgs(stdin bool, opt target.RunOpts) []string {
	args := []string{"exec"}
	if stdin {
		args = append(args, "-i")
	}
	for _, kv := range opt.Env {
		args = append(args, "-e", kv)
	}
	if opt.Dir != "" {
		args = append(args, "-w", opt.Dir)
	}
	return append(args, s.Container)
}

func (s *Session) exec(ctx context.Context, args []string, stdin io.Reader, opt target.RunOpts) (target.Result, error) {
	// A backstop well outside the in-container bound: if the probe's own
	// timeout works, this never fires; if the daemon stops answering, the
	// runner is not left waiting forever.
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout+30*time.Second)
		defer cancel()
	}

	cmd := s.Client.command(ctx, args...)
	cmd.Stdin = stdin
	cmd.Stdout = opt.Stdout
	cmd.Stderr = opt.Stderr

	started := time.Now()
	err := cmd.Run()
	result := target.Result{Duration: time.Since(started)}

	if err != nil {
		rc, reported := codeOf(err)
		if !reported {
			// Either docker itself did not run, or the process was killed
			// rather than exiting. Neither is an exit code, and neither may be
			// recorded as one.
			result.RC = -1

			// A process the backstop killed is a probe that overran, which is
			// the probe's failure and not the substrate's - so it is reported
			// the same way the in-container bound reports one, rather than as
			// an error that would read as infrastructure. Only a deadline
			// counts: a cancelled context is the person at the keyboard, and
			// calling that a probe timeout blames the probe for them.
			if opt.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.TimedOut = true
				return result, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return result, err
		}
		if rc == 125 {
			// 125 is the docker CLI's own failure code - the daemon refused or
			// never answered. The command inside never ran, so the code is
			// docker's and not the probe's.
			result.RC = -1
			return result, fmt.Errorf("docker could not run the command on %s: %w", s.Container, err)
		}
		if !s.alive(ctx) {
			// docker exec against a stopped or missing container exits 1 with
			// its own message - measured - and that 1 would pass a negative
			// control for a probe that never executed.
			result.RC = -1
			return result, fmt.Errorf("container %s is not running, so exit %d is docker's answer and not the probe's: %w",
				s.Container, rc, err)
		}
		result.RC = rc
	}

	// 124 is what timeout(1) exits with when it killed what it was watching.
	// The process was cut short, so its code is not a reading.
	if opt.Timeout > 0 && result.RC == 124 {
		result.TimedOut = true
		result.RC = -1
	}
	return result, nil
}

// drainChannel reads the probe's fd 3 file and removes it. A channel that
// never appeared is not an error: a probe that recorded nothing recorded
// nothing.
// runScriptWindows is the same round trip for a container that has neither bash
// nor fd 3: the script goes in on stdin and is written to a file, and the
// readings come back from a path named in the environment.
func (s *Session) runScriptWindows(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	channel := windowsChannel(time.Now().UnixNano())

	// The bound is the caller's here rather than the container's: Windows has
	// no `timeout` that kills a process, so a probe that overruns is stopped
	// from outside. That is weaker than the linux path and is said out loud
	// rather than left to be discovered.
	opt.Env = append(append([]string{}, opt.Env...), "TESTEASE_FACTS="+channel)

	execArgs := s.execArgs(true, opt)
	execArgs = append(execArgs,
		"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", windowsWrapper(channel, args))

	result, err := s.exec(ctx, execArgs, bytes.NewReader(script), opt)
	s.drainChannel(ctx, channel, opt.Structured)
	return result, err
}

func (s *Session) drainChannel(ctx context.Context, path string, into io.Writer) {
	if into == nil {
		return
	}

	var cmd *exec.Cmd
	if s.windows() {
		// The path is written into the command rather than passed after it:
		// PowerShell does not populate $args from a -Command line, which is
		// the same quirk the wrapper works around.
		quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
		cmd = s.Client.command(ctx, "exec", s.Container, "powershell", "-NoProfile", "-Command",
			"if (Test-Path -LiteralPath "+quoted+") { Get-Content -Raw -LiteralPath "+quoted+
				"; Remove-Item -Force -LiteralPath "+quoted+" }")
	} else {
		cmd = s.Client.command(ctx, "exec", s.Container, "sh", "-c", `cat "$1" 2>/dev/null; rm -f "$1"`, "testease", path)
	}
	cmd.Stdout = into
	_ = cmd.Run()
}

// Stream runs a docker command with its output going straight to the caller's
// writers, for the commands whose output is the point rather than a value to
// parse.
func (c Client) Stream(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := c.command(ctx, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// CodeOf reads a process's exit status, and says whether there was one.
func CodeOf(err error) (rc int, reported bool) { return codeOf(err) }

// LabelledNetworks lists every network this daemon holds that carries a label,
// with the labels it carries. A network outlives the containers on it, so it is
// the leftover that survives a hand-removed target.
func (c Client) LabelledNetworks(ctx context.Context, label string) (map[string]map[string]string, error) {
	out, err := c.Output(ctx, "network", "ls", "--filter", "label="+label, "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}

	names := nonEmptyLines(out)
	if len(names) == 0 {
		return map[string]map[string]string{}, nil
	}
	// One inspect for the lot rather than one per network. The error is
	// tolerated on purpose: a network removed between the listing and the
	// inspect makes the whole call non-zero while the survivors still print.
	raw, err := c.Output(ctx, append([]string{"network", "inspect", "--format", "{{.Name}}\t{{json .Labels}}"}, names...)...)
	return describedOrNone("network", names, raw, err)
}

// describedOrNone is where the tolerated inspect error stops being tolerable.
//
// Some described and some not is the case the tolerance exists for: one target
// removed between the listing and the inspect makes the whole call non-zero,
// and dropping the survivors with it would hide the leftovers reap came for.
// NONE described is a different event - the daemon stopped answering, or the
// argv went past the kernel's limit - and returning an empty map and no error
// there makes reap say "nothing to reap" over targets that are still standing,
// which is the quietest way a leak survives.
func describedOrNone(kind string, listed []string, raw string, err error) (map[string]map[string]string, error) {
	found := parseLabelled(raw)
	if len(found) == 0 && len(listed) > 0 {
		if err == nil {
			err = errors.New("it described none of them")
		}
		return nil, fmt.Errorf("this daemon lists %d labelled %s(s) and none could be described, so it cannot be reported as empty: %w",
			len(listed), kind, err)
	}
	return found, nil
}

// parseLabelled reads `name<TAB>labels-json` lines back into a map.
func parseLabelled(out string) map[string]map[string]string {
	found := map[string]map[string]string{}
	for _, line := range nonEmptyLines(out) {
		name, labels, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var parsed map[string]string
		if err := json.Unmarshal([]byte(strings.TrimSpace(labels)), &parsed); err != nil {
			continue
		}
		found[strings.TrimPrefix(name, "/")] = parsed
	}
	return found
}

func nonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// RemoveNetwork removes one. Docker refuses while containers are attached,
// which is the check: a network with a kept target on it is not abandoned.
func (c Client) RemoveNetwork(ctx context.Context, name string) error {
	_, err := c.Output(ctx, "network", "rm", name)
	return err
}

// Contexts is every docker context configured here. A target left standing on
// another daemon is invisible until somebody names that daemon, so the callers
// that hunt for one enumerate rather than ask people to remember.
func (c Client) Contexts(ctx context.Context) ([]string, error) {
	out, err := c.Output(ctx, "context", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}

// Labelled lists every container this daemon holds that carries a label,
// running or not, with the labels it carries.
//
// Exited containers count. A run that died left its targets standing on
// purpose, and those are exactly the ones nobody came back for.
func (c Client) Labelled(ctx context.Context, label string) (map[string]map[string]string, error) {
	out, err := c.Output(ctx, "ps", "-a", "--filter", "label="+label, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}

	names := nonEmptyLines(out)
	if len(names) == 0 {
		return map[string]map[string]string{}, nil
	}
	// One inspect for the lot; see LabelledNetworks for why its error is
	// tolerated and describedOrNone for where the tolerance stops.
	raw, err := c.Output(ctx, append([]string{"inspect", "-f", "{{.Name}}\t{{json .Config.Labels}}"}, names...)...)
	return describedOrNone("container", names, raw, err)
}

// Remove takes a container away.
func (c Client) Remove(ctx context.Context, name string) error {
	_, err := c.Output(ctx, "rm", "-f", name)
	return err
}

// codeOf reads a process's exit status, and says whether there was one.
//
// exec.ExitError.ExitCode() returns -1 for a process that was KILLED rather
// than exited: there is no code, and -1 is how the standard library says so.
// Passing that on as a status would put "no reading" and "a reading of -1" in
// the same field, which is the confusion invariant 3 exists to prevent.
func codeOf(err error) (rc int, reported bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1, false
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code, true
	}
	return -1, false
}
