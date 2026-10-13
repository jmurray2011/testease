// Package winrm runs probes on a Windows machine that stays up between steps.
//
// This is the counterpart to the one-shot backend, not a replacement for it. A
// Windows container runs one command and exits; a Windows box does not, and a
// scenario that installs something in one step and inspects it in the next
// needs the second kind. Which one a scenario wants is a statement about the
// machine, so it is a capability rather than a flag.
package winrm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/masterzen/winrm"

	"github.com/jmurray2011/testease/internal/target"
)

// DefaultPort is the WinRM HTTP port; 5986 is the TLS one.
//
// MEASURED, and it decides how a host must be configured to be usable here. A
// Windows machine ships with Service\AllowUnencrypted = false, and it means
// what it says: every message must be encrypted. PowerShell and pywinrm satisfy
// that over plain 5985 by wrapping each message in the SPNEGO session key that
// NTLM authentication established. The Go client this backend uses authenticates
// with NTLM but does not wrap the messages, so a correctly configured server
// answers 401 with a body that is not SOAP - which surfaces as "invalid content
// type" and reads like a bug in the request rather than a policy on the server.
//
// So there are two ways to be reachable, and turning the policy off is only one
// of them:
//
//   - an HTTPS listener on 5986, where TLS is the encryption and the policy is
//     satisfied without weakening anything;
//   - Service\AllowUnencrypted = true, which is a real reduction in the
//     machine's posture and should be a decision somebody made on purpose.
//
// testease will not make that decision for anyone. It says which one is
// missing and stops.
const (
	DefaultPort = 5985
	TLSPort     = 5986
)

// DialBudget is how long a connection has to answer before it is called dead.
const DialBudget = 60 * time.Second

// Endpoint is a Windows machine that answers WinRM.
type Endpoint struct {
	Host     string
	Port     int
	User     string
	Password string

	// HTTPS uses 5986 with TLS. Insecure skips certificate verification, which
	// a lab with a self-signed certificate needs and a production target
	// should not have.
	HTTPS    bool
	Insecure bool

	Log *slog.Logger
}

// Session runs commands on one Windows machine.
type Session struct {
	client *winrm.Client
	log    *slog.Logger
	host   string
	https  bool
}

var _ target.Session = (*Session)(nil)

// Dial opens a session.
//
// NTLM rather than basic auth: a local administrator on a workgroup machine is
// the usual case, and basic auth over HTTP would put the password on the wire
// in a form anything on the segment could read.
func Dial(e Endpoint) (*Session, error) {
	if e.Host == "" || e.User == "" {
		return nil, fmt.Errorf("a WinRM endpoint needs a host and a user")
	}
	port := e.Port
	if port == 0 {
		port = DefaultPort
	}
	log := e.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if e.HTTPS && e.Port == 0 {
		port = TLSPort
	}
	endpoint := winrm.NewEndpoint(e.Host, port, e.HTTPS, e.Insecure, nil, nil, nil, DialBudget)
	params := winrm.NewParameters(winrm.DefaultParameters.Timeout, winrm.DefaultParameters.Locale, winrm.DefaultParameters.EnvelopeSize)

	// Message encryption over HTTP, TLS over HTTPS. Not doing the first is what
	// makes a correctly configured server answer 401 with a body nobody can
	// parse, and reaching for HTTPS to avoid it is fixing the wrong end.
	if e.HTTPS {
		params.TransportDecorator = func() winrm.Transporter { return &winrm.ClientNTLM{} }
	} else {
		params.TransportDecorator = func() winrm.Transporter {
			encrypted, err := winrm.NewEncryption("ntlm")
			if err != nil {
				// Only unsupported protocols fail here, and ntlm is supported.
				return &winrm.ClientNTLM{}
			}
			return encrypted
		}
	}

	client, err := winrm.NewClientWithParameters(endpoint, e.User, e.Password, params)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", e.Host, err)
	}
	log.Debug("winrm dialled", "host", e.Host, "port", port, "user", e.User)
	return &Session{client: client, log: log, host: e.Host, https: e.HTTPS}, nil
}

// Run executes a command. Stdin is not connected, for the same reason no
// backend connects it: a command that read it would consume the runner's own.
func (s *Session) Run(ctx context.Context, cmd []string, opt target.RunOpts) (target.Result, error) {
	return s.run(ctx, strings.Join(cmd, " "), opt)
}

// RunScript ships a PowerShell script and runs it with args, in one round trip.
//
// The script travels base64-encoded in the command itself. That is not
// squeamishness about quoting: a script crossing cmd.exe, WinRM's own
// escaping, and PowerShell's parser has three chances to be reinterpreted on
// the way, and encoding it means the bytes that arrive are the bytes that were
// sent - which is the same promise the record makes about everything else.
func (s *Session) RunScript(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	// PowerShell has no fd 3 to hand a probe, so the probe is handed a path.
	// The file is collected afterwards and written to the structured channel,
	// so a judge cannot tell which transport carried a reading.
	channel := fmt.Sprintf(`C:\Windows\Temp\testease-facts-%d`, time.Now().UnixNano())

	var b strings.Builder
	fmt.Fprintf(&b, "$env:TESTEASE_FACTS = %s\n", quote(channel))
	for _, kv := range opt.Env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "$env:%s = %s\n", k, quote(v))
	}
	b.WriteString("$args = @(")
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quote(a))
	}
	b.WriteString(")\n")
	b.Write(script)

	// PowerShell reads -EncodedCommand as UTF-16LE, which is its own rule and
	// not a choice available here.
	encoded := base64.StdEncoding.EncodeToString(utf16le(b.String()))
	result, err := s.run(ctx, "powershell.exe -NoProfile -NonInteractive -EncodedCommand "+encoded, opt)
	s.drainChannel(ctx, channel, opt.Structured)
	return result, err
}

// drainChannel collects what the probe recorded and removes it. A channel that
// never appeared is not an error: a probe that recorded nothing recorded
// nothing.
func (s *Session) drainChannel(ctx context.Context, path string, into io.Writer) {
	if into == nil {
		return
	}
	script := fmt.Sprintf(
		"if (Test-Path %s) { Get-Content -Raw %s; Remove-Item -Force %s }",
		quote(path), quote(path), quote(path),
	)

	// Run rather than RunScript, or collecting a channel would open one of its
	// own, forever.
	encoded := base64.StdEncoding.EncodeToString(utf16le(script))
	_, _ = s.run(ctx, "powershell.exe -NoProfile -NonInteractive -EncodedCommand "+encoded,
		target.RunOpts{Stdout: into, Stderr: io.Discard})
}

func (s *Session) run(ctx context.Context, command string, opt target.RunOpts) (target.Result, error) {
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	stdout, stderr := opt.Stdout, opt.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	started := time.Now()
	code, err := s.client.RunWithContext(ctx, command, stdout, stderr)
	result := target.Result{Duration: time.Since(started)}

	if err != nil {
		result.RC = -1
		if opt.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Cut short rather than finished; an exit code we did not receive
			// is not zero and is not the one it would have returned.
			result.TimedOut = true
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Cancelled is the person at the keyboard, not the probe
			// overrunning, and TimedOut here would escalate a ctrl-C into a
			// probe failure.
			return result, ctxErr
		}
		return result, s.explain(err)
	}
	result.RC = code
	return result, nil
}

// explain turns the transport's own wording into what a person can act on.
//
// A 401 whose body is not SOAP is almost never a wrong password here: it is the
// server refusing an unencrypted message, and the wording it produces points at
// the request rather than the policy.
func (s *Session) explain(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "401") && !s.https {
		return fmt.Errorf(
			"run on %s: the server refused the request (%w).\n"+
				"  This is usually Service\\AllowUnencrypted = false, which is the default and is correct: "+
				"the server requires every message to be encrypted, and this client authenticates with NTLM "+
				"but does not encrypt the messages.\n"+
				"  Either give the machine an HTTPS listener on %d and dial it with https, "+
				"or decide on purpose to set AllowUnencrypted = true. testease will not weaken it for you.\n"+
				"  check: winrm get winrm/config/service",
			s.host, err, TLSPort,
		)
	}
	return fmt.Errorf("run on %s: %w", s.host, err)
}

// Fetch reads a file back. It is encoded on the way out for the same reason a
// script is encoded on the way in: what arrives should be what was there.
func (s *Session) Fetch(ctx context.Context, remote string) (io.ReadCloser, error) {
	script := fmt.Sprintf(
		"[Convert]::ToBase64String([IO.File]::ReadAllBytes(%s))", quote(remote),
	)

	var out bytes.Buffer
	result, err := s.RunScript(ctx, []byte(script), nil, target.RunOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return nil, err
	}
	if result.RC != 0 {
		return nil, fmt.Errorf("fetch %s from %s: exit %d", remote, s.host, result.RC)
	}

	body, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(out.String()), ""))
	if err != nil {
		return nil, fmt.Errorf("fetch %s from %s: %w", remote, s.host, err)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// Put places a file, encoded the same way.
func (s *Session) Put(ctx context.Context, local io.Reader, remote string, _ fs.FileMode) error {
	body, err := io.ReadAll(local)
	if err != nil {
		return fmt.Errorf("read what to put on %s: %w", s.host, err)
	}
	script := fmt.Sprintf(
		"$dir = Split-Path -Parent %s\nif ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir | Out-Null }\n"+
			"[IO.File]::WriteAllBytes(%s, [Convert]::FromBase64String(%s))",
		quote(remote), quote(remote), quote(base64.StdEncoding.EncodeToString(body)),
	)

	result, err := s.RunScript(ctx, []byte(script), nil, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return err
	}
	if result.RC != 0 {
		return fmt.Errorf("put %s on %s: exit %d", remote, s.host, result.RC)
	}
	return nil
}

// Close has nothing to close: WinRM holds no connection between commands.
func (s *Session) Close() error { return nil }

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// utf16le is what PowerShell's -EncodedCommand expects.
func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			high := 0xD800 + (r >> 10)
			low := 0xDC00 + (r & 0x3FF)
			out = append(out, byte(high), byte(high>>8), byte(low), byte(low>>8))
			continue
		}
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}
