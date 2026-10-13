// Package ssh runs probes on a Unix machine that stays up between steps.
//
// It is the Linux counterpart to the winrm backend: a machine somebody else
// booted, dialled rather than created. The docker backend covers containers and
// the one-shot backend covers a command per box; neither can measure a VM that
// has to be in a state a container cannot reach - a kernel in FIPS mode is the
// case that forced this, since RHEL gates that on /proc/sys/crypto/fips_enabled
// and a container inherits its host's.
//
// IT LINKS A CLIENT RATHER THAN SHELLING OUT, which is the opposite of what the
// ssm backend does, and for a reason that does not apply there. The aws CLI
// already resolves SSO sessions and assumed roles, so shelling out buys real
// machinery. `ssh` buys none of that here: password authentication needs a tty
// or sshpass, exit codes arrive mixed with the transport's own, and x/crypto/ssh
// is ALREADY in this module's graph as an indirect dependency, so linking it
// adds no supply-chain surface and keeps the binary static.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	cssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/jmurray2011/testease/internal/target"
)

// DefaultPort is the port a machine answers ssh on unless it says otherwise.
const DefaultPort = 22

// DialBudget is how long a connection has to answer before it is called dead.
const DialBudget = 60 * time.Second

// Endpoint is a machine that answers ssh.
type Endpoint struct {
	Host string
	Port int
	User string

	// Password and KeyFile are alternatives. A key is the better answer and a
	// password is what a kickstart-built box actually ships with, so both are
	// supported and neither is ever written to a log or a record.
	Password string
	KeyFile  string

	// KnownHosts is an OpenSSH known_hosts file. Empty means the host key is
	// accepted and RECORDED rather than verified - which is a real reduction
	// and is why the fingerprint is put in the record: a run against a lab VM
	// that was rebuilt yesterday should be able to say the key changed.
	KnownHosts string

	// Sudo runs readiness checks and probes through sudo -n. It is explicit:
	// silently elevating an SSH session would make the recorded account lie
	// about which principal changed the machine.
	Sudo bool

	Log *slog.Logger
}

// Session is a connection that outlives a step.
type Session struct {
	client *cssh.Client
	log    *slog.Logger

	// HostKey is the fingerprint the machine presented, kept so a record can
	// say which machine answered rather than only which address was dialled.
	HostKey string
	sudo    bool
}

var _ target.Session = (*Session)(nil)

// Dial opens a connection.
func Dial(ctx context.Context, e Endpoint) (*Session, error) {
	if e.Host == "" {
		return nil, errors.New("ssh needs a host")
	}
	if e.User == "" {
		return nil, errors.New("ssh needs a user: there is no sensible default, and guessing one dials the wrong account")
	}

	var auth []cssh.AuthMethod
	if e.KeyFile != "" {
		raw, err := os.ReadFile(e.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read ssh key %s: %w", e.KeyFile, err)
		}
		signer, err := cssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("parse ssh key %s: %w", e.KeyFile, err)
		}
		auth = append(auth, cssh.PublicKeys(signer))
	}
	if e.Password != "" {
		// Some hardened images answer keyboard-interactive rather than
		// password; both are offered because a STIG baseline commonly disables
		// one of them and says so nowhere a caller can see.
		auth = append(auth, cssh.Password(e.Password),
			cssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				answers := make([]string, len(qs))
				for i := range qs {
					answers[i] = e.Password
				}
				return answers, nil
			}))
	}
	if len(auth) == 0 {
		return nil, errors.New("ssh needs a password or a key file")
	}

	// The key is recorded either way; a known_hosts file makes it verified as
	// well. Without one the reduction is real and documented on the field.
	verify := func(string, net.Addr, cssh.PublicKey) error { return nil }
	if e.KnownHosts != "" {
		check, err := knownhosts.New(e.KnownHosts)
		if err != nil {
			return nil, fmt.Errorf("read known_hosts %s: %w", e.KnownHosts, err)
		}
		verify = check
	}

	var presented string
	cfg := &cssh.ClientConfig{
		User: e.User,
		Auth: auth,
		HostKeyCallback: func(hostname string, remote net.Addr, key cssh.PublicKey) error {
			presented = cssh.FingerprintSHA256(key)
			return verify(hostname, remote, key)
		},
		Timeout: DialBudget,
	}

	port := e.Port
	if port == 0 {
		port = DefaultPort
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(port))

	dialer := net.Dialer{Timeout: DialBudget}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	c, chans, reqs, err := cssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}

	return &Session{client: cssh.NewClient(c, chans, reqs), log: e.Log, HostKey: presented, sudo: e.Sudo}, nil
}

// Run executes a command.
func (s *Session) Run(ctx context.Context, cmd []string, opt target.RunOpts) (target.Result, error) {
	quoted := make([]string, len(cmd))
	for i, a := range cmd {
		quoted[i] = shellQuote(a)
	}
	return s.exec(ctx, strings.Join(quoted, " "), opt)
}

// RunScript ships a script, runs it with args, and returns one ordered log.
//
// The script travels rather than being interpolated into a command line: a
// probe carrying a quote or a newline would otherwise be reassembled wrongly on
// the far side, and the probe is evidence about what ran.
func (s *Session) RunScript(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	stamp := time.Now().UnixNano()
	remote := fmt.Sprintf("/tmp/testease-probe-%d", stamp)
	facts := fmt.Sprintf("/tmp/testease-facts-%d", stamp)

	if err := s.Put(ctx, bytes.NewReader(script), remote, 0o700); err != nil {
		return target.Result{RC: -1}, fmt.Errorf("ship probe: %w", err)
	}
	defer func() { _ = s.remove(ctx, remote, facts) }()

	result, runErr := s.exec(ctx, wrapScript(interpreter(script), remote, facts, args), opt)

	// The structured channel is drained whatever the command did: a probe that
	// failed after writing a fact still wrote it, and dropping it would lose
	// evidence because of an exit code.
	if opt.Structured != nil {
		s.drain(ctx, facts, opt.Structured)
	}
	return result, runErr
}

// exec runs one prepared command line.
func (s *Session) exec(ctx context.Context, line string, opt target.RunOpts) (target.Result, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	// Stdin is deliberately not connected: a step whose command reads stdin
	// would start consuming the runner's own input.
	// The runner owns these and closes them when Run returns; the transport
	// may not have stopped by then.
	outw, errw := detachable(opt.Stdout), detachable(opt.Stderr)
	if opt.Stdout != nil {
		sess.Stdout = outw
	}
	if opt.Stderr != nil {
		sess.Stderr = errw
	}
	for _, kv := range opt.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			// Setenv is refused by most sshd configurations (AcceptEnv is
			// empty by default), so the assignment is also prefixed onto the
			// command line below. Both are attempted; neither is relied on.
			_ = sess.Setenv(k, v)
		}
	}
	if len(opt.Env) > 0 {
		var b strings.Builder
		b.WriteString("env")
		for _, kv := range opt.Env {
			b.WriteString(" ")
			b.WriteString(shellQuote(kv))
		}
		b.WriteString(" ")
		b.WriteString(line)
		line = b.String()
	}
	if opt.Dir != "" {
		line = "cd " + shellQuote(opt.Dir) + " && " + line
	}
	if s.sudo {
		line = elevate(line)
	}

	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- sess.Run(line) }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		// Killing the far side is best-effort; what matters is that the
		// runner learns there is no exit code rather than being handed one.
		_ = sess.Signal(cssh.SIGKILL)

		// Closing the session unblocks Run, and waiting for it keeps late
		// remote output from racing into the caller's writers - the step log
		// may be closed the moment this returns. Bounded, because a wedged
		// transport must not hold the runner forever.
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			// The transport did not unblock inside the bound and this returns
			// anyway. Whatever that goroutine writes from here goes nowhere:
			// the runner is about to read these buffers for the claim and
			// close the log behind them.
			outw.detach()
			errw.detach()
		}

		result := target.Result{RC: -1, Duration: time.Since(started)}
		if opt.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// The probe overran its budget. Every other backend reports that
			// as TimedOut rather than as an error, and the identical overrun
			// must not read infra-fail here and probe-fail there.
			result.TimedOut = true
			return result, nil
		}
		return result, ctx.Err()
	}

	code, known := exitCode(runErr)
	result := target.Result{RC: code, Duration: time.Since(started)}
	if !known {
		// The command may never have run. Saying -1 and returning the error is
		// how the runner tells infra-fail from a probe that exited non-zero.
		return result, runErr
	}
	return result, nil
}

// elevate keeps the entire prepared command together across sudo's privilege
// boundary, including its working directory and explicitly supplied env.
func elevate(line string) string {
	return "sudo -n -- sh -c " + shellQuote(line)
}

// Fetch retrieves a file.
func (s *Session) Fetch(ctx context.Context, remote string) (io.ReadCloser, error) {
	var out bytes.Buffer
	result, err := s.exec(ctx, "cat "+shellQuote(remote), target.RunOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", remote, err)
	}
	if result.RC != 0 {
		return nil, fmt.Errorf("fetch %s: cat exited %d", remote, result.RC)
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
}

// Put places a file on the target.
//
// The bytes travel over an exec rather than sftp, because a hardened box may
// have the sftp subsystem disabled while still allowing an exec - and a backend
// that only worked on unhardened machines would be useless for the boxes this
// exists to reach.
func (s *Session) Put(ctx context.Context, local io.Reader, remote string, mode fs.FileMode) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	sess.Stdin = local
	line := mediaPutCommand(remote, mode)

	done := make(chan error, 1)
	go func() { done <- sess.Run(line) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = sess.Signal(cssh.SIGKILL)
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", remote, err)
	}
	return nil
}

// mediaPutCommand writes beside the destination and renames the completed file
// into place. In particular, this lets a repeated run replace media previously
// staged read-only without opening a partial destination if the transport dies.
func mediaPutCommand(remote string, mode fs.FileMode) string {
	dir := path.Dir(remote)
	template := path.Join(dir, ".testease-put.XXXXXX")
	return fmt.Sprintf(
		"mkdir -p %s && tmp=$(mktemp %s) && trap 'rm -f \"$tmp\"' EXIT HUP INT TERM && cat > \"$tmp\" && chmod %o \"$tmp\" && mv -f \"$tmp\" %s && trap - EXIT HUP INT TERM",
		shellQuote(dir), shellQuote(template), mode.Perm(), shellQuote(remote),
	)
}

// Close ends the connection.
func (s *Session) Close() error {
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *Session) remove(ctx context.Context, paths ...string) error {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shellQuote(p)
	}
	_, err := s.exec(ctx, "rm -f "+strings.Join(quoted, " "), target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	return err
}

// drain copies the far side's fd 3 file into the structured writer.
func (s *Session) drain(ctx context.Context, remote string, into io.Writer) {
	rc, err := s.Fetch(ctx, remote)
	if err != nil {
		// A probe that wrote no facts leaves no file, which is ordinary. The
		// warning is left to the runner, which knows whether facts were due.
		if s.log != nil {
			s.log.Debug("no structured output", "path", remote, "error", err)
		}
		return
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(into, rc); err != nil && s.log != nil {
		s.log.Warn("structured output was cut short", "path", remote, "error", err)
	}
}

// wrapScript builds the command line that runs a shipped probe with fd 3 open.
//
// SSH offers a session stdout and stderr and nothing else, so the third
// descriptor is opened on the far side and left in a file the caller fetches.
// What must never happen is fd 3 being folded into stdout: facts are the
// structured channel precisely so that a judge cannot recover one by parsing
// testimony.
// interpreter returns the probe's shebang command, split into fields, or nil
// when the script has no shebang.
//
// A hardened target mounts /tmp noexec - every DISA STIG box does - so the far
// side refuses to execve the shipped probe even with the exec bit set, while it
// will happily let an interpreter open and read the same file. Running the probe
// as `<interp> <path>` sidesteps the mount without relaxing it. Each field is
// quoted separately downstream, so `#!/usr/bin/env bash` stays two tokens and
// cannot be turned into one command by a crafted shebang.
func interpreter(script []byte) []string {
	if !bytes.HasPrefix(script, []byte("#!")) {
		return nil
	}
	line := script[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.Fields(strings.TrimSpace(string(line)))
}

func wrapScript(interp []string, remote, facts string, args []string) string {
	var b strings.Builder
	for _, tok := range interp {
		b.WriteString(shellQuote(tok))
		b.WriteString(" ")
	}
	b.WriteString(shellQuote(remote))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	b.WriteString(" 3>")
	b.WriteString(shellQuote(facts))
	return b.String()
}

// exitCode separates what the command reported from what the transport did.
//
// Invariant 2 lives here: a connection that failed has NO exit code, and
// handing back 0 would read as success while -1 alone would read as a probe
// that failed. The boolean is what lets the runner tell infra-fail from a probe
// exiting non-zero.
func exitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exit *cssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), true
	}
	return -1, false
}

// shellQuote makes one argument survive the far side's shell verbatim.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
