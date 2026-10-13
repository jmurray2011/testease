// Package static hands back machines that already exist.
//
// It creates nothing and destroys nothing: the machine was there before the run
// and will be there after it, which is the whole point of the hardware and
// Windows arms. Resolving one means dialling it and asking what it is.
//
// Credentials never come from a scenario. A manifest is content that lives in a
// repository and is read by people; a password in one is a password in a
// repository. So a scenario names the machine and the caller supplies the way
// in, and the two meet here.
package static

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sshbackend "github.com/jmurray2011/testease/internal/backend/ssh"
	ssmbackend "github.com/jmurray2011/testease/internal/backend/ssm"
	winrmbackend "github.com/jmurray2011/testease/internal/backend/winrm"
	"github.com/jmurray2011/testease/internal/provider/fingerprint"
	"github.com/jmurray2011/testease/internal/target"
)

// SSM is how to reach machines through Systems Manager.
type SSM struct {
	// Profile and Region are always explicit. A backend that inherited
	// whichever profile happened to be in the environment is one variable away
	// from measuring another account.
	Profile string
	Region  string
	// TransferStore is a temporary s3:// handoff for declared media too large
	// to fit in one Systems Manager command document.
	TransferStore string
}

// WinRM is how to reach Windows machines that stay up between steps.
type WinRM struct {
	User     string
	Password string
	Port     int
	HTTPS    bool
	Insecure bool
}

// SSH is how to reach a Unix machine that stays up between steps.
//
// Credentials come from flags and never from a scenario: a manifest is content
// in a repository, and a password in one is a password in everyone's clone.
type SSH struct {
	User     string
	Password string
	KeyFile  string
	Port     int
	Sudo     bool

	// KnownHosts verifies the host key against an OpenSSH known_hosts file.
	// Empty accepts and records the key rather than verifying it.
	KnownHosts string
}

// Options is everything the provider needs that a scenario must not carry.
type Options struct {
	SSM   SSM
	WinRM WinRM
	SSH   SSH

	// Instrument makes a dialled machine fingerprintable with the same
	// collect/diff contract used by disposable containers.
	Instrument *fingerprint.Instrument

	RunID  string
	Runner string
	Log    *slog.Logger
}

// Provider locates machines that already exist.
type Provider struct{ opt Options }

var _ target.Provider = (*Provider)(nil)

// New builds a provider.
func New(opt Options) *Provider {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	return &Provider{opt: opt}
}

// Resolve dials the machine a host names and reads what it is.
func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	if spec.Address == "" {
		return nil, fmt.Errorf("host %q is static and names no address", spec.Host)
	}

	m := &machine{
		spec:   spec,
		labels: p.labels(spec),
		log:    p.opt.Log,
		identity: target.Identity{
			Requested: map[string]any{
				"provider": "static",
				"backend":  spec.Backend,
				"address":  spec.Address,
			},
			Resolved: map[string]any{},
			Facts:    map[string]string{},
		},
	}
	if spec.Origin != "" {
		u, err := url.Parse(spec.Origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("host %q has invalid network origin %q", spec.Host, spec.Origin)
		}
		m.endpoint = u
		m.identity.Requested["origin"] = spec.Origin
		m.identity.Resolved["endpoint"] = u.String()
	}
	if spec.Image != "" {
		// For a pre-existing machine this is the provisioner's immutable image
		// claim, not something Testease resolves. Keeping it in Requested makes
		// that distinction visible beside the identity the backend reads.
		m.identity.Requested["image"] = spec.Image
	}

	switch spec.Backend {
	case "ssm":
		client := ssmbackend.Client{
			Profile: p.opt.SSM.Profile, Region: p.opt.SSM.Region,
			TransferStore: p.opt.SSM.TransferStore, Log: p.opt.Log,
		}
		instance, err := client.Describe(ctx, spec.Address)
		if err != nil {
			m.identity.Resolved["error"] = err.Error()
			m.identity.Facts["error"] = "the instance was never dialled"
			return nil, fmt.Errorf("host %q: %w", spec.Host, err)
		}
		// The artifact that answered, not just the address it was asked at.
		m.identity.Resolved["backend"] = "ssm"
		m.identity.Resolved["ref"] = "instance:" + instance.ID
		m.identity.Resolved["account_profile"] = p.opt.SSM.Profile
		m.identity.Resolved["region"] = p.opt.SSM.Region
		m.identity.Facts["os"] = instance.Platform
		m.identity.Facts["platform"] = instance.Name
		m.identity.Facts["ping"] = instance.Ping
		m.session = &ssmbackend.Session{Client: client, Instance: instance}

	case "winrm":
		session, err := winrmbackend.Dial(winrmbackend.Endpoint{
			Host:     spec.Address,
			Port:     p.opt.WinRM.Port,
			User:     p.opt.WinRM.User,
			Password: p.opt.WinRM.Password,
			HTTPS:    p.opt.WinRM.HTTPS,
			Insecure: p.opt.WinRM.Insecure,
			Log:      p.opt.Log,
		})
		if err != nil {
			m.identity.Resolved["error"] = err.Error()
			m.identity.Facts["error"] = "the machine was never dialled"
			return nil, fmt.Errorf("host %q: %w", spec.Host, err)
		}
		m.identity.Resolved["backend"] = "winrm"
		m.identity.Resolved["ref"] = "winrm:" + spec.Address
		m.session = session
		p.readWindowsFacts(ctx, m)

	case "ssh":
		session, err := sshbackend.Dial(ctx, sshbackend.Endpoint{
			Host:       spec.Address,
			Port:       p.opt.SSH.Port,
			User:       p.opt.SSH.User,
			Password:   p.opt.SSH.Password,
			KeyFile:    p.opt.SSH.KeyFile,
			KnownHosts: p.opt.SSH.KnownHosts,
			Sudo:       p.opt.SSH.Sudo,
			Log:        p.opt.Log,
		})
		if err != nil {
			m.identity.Resolved["error"] = err.Error()
			m.identity.Facts["error"] = "the machine was never dialled"
			return nil, fmt.Errorf("host %q: %w", spec.Host, err)
		}
		m.identity.Resolved["backend"] = "ssh"
		m.identity.Resolved["ref"] = "ssh:" + spec.Address
		if p.opt.SSH.Sudo {
			m.identity.Resolved["elevation"] = "sudo-noninteractive"
		}
		// WHICH MACHINE ANSWERED, not merely which address was dialled. A lab
		// VM rebuilt yesterday presents a different key, and a record that
		// could not say so would make two different boxes look like one.
		m.identity.Resolved["host_key"] = session.HostKey
		m.session = session

	default:
		return nil, fmt.Errorf(
			"host %q is static and asks for backend %q; testease dials ssh, ssm and winrm",
			spec.Host, spec.Backend,
		)
	}

	if p.opt.Instrument != nil {
		m.printer = fingerprint.New(m.session, *p.opt.Instrument, spec.Host)
		return &fingerprinted{machine: m, Printer: m.printer}, nil
	}
	return m, nil
}

// Release lets go of no machine. It does remove this run's local instrument
// readings after their conclusion and diff have entered the record.
func (p *Provider) Release(_ context.Context, t target.Target, _ bool) error {
	switch v := t.(type) {
	case *machine:
		v.printer.Close()
	case *fingerprinted:
		v.printer.Close()
	}
	return nil
}

// readWindowsFacts asks the machine what it is. A failure here is recorded in
// the layer that failed rather than left out.
func (p *Provider) readWindowsFacts(ctx context.Context, m *machine) {
	var out sink
	script := []byte("$os = Get-CimInstance Win32_OperatingSystem\n" +
		"Write-Output $os.Caption\nWrite-Output $env:PROCESSOR_ARCHITECTURE\nWrite-Output $os.Version\n")

	result, err := m.session.RunScript(ctx, script, nil, target.RunOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil || result.RC != 0 {
		m.identity.Facts["error"] = "the machine did not say what it is"
		return
	}
	lines := out.Lines()
	for i, key := range []string{"platform", "arch", "kernel"} {
		if i < len(lines) {
			m.identity.Facts[key] = lines[i]
		}
	}
	m.identity.Facts["os"] = "windows"
}

func (p *Provider) labels(spec target.Spec) map[string]string {
	return map[string]string{
		target.LabelRunner:  p.opt.Runner,
		target.LabelRun:     p.opt.RunID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: spec.Backend,
		target.LabelHost:    spec.Host,
		target.LabelCell:    spec.Cell,
	}
}

// machine is something that was already running.
type machine struct {
	spec     target.Spec
	identity target.Identity
	labels   map[string]string
	log      *slog.Logger
	session  target.Session
	printer  *fingerprint.Printer
	endpoint *url.URL
}

var (
	_ target.Target      = (*machine)(nil)
	_ target.Sessioner   = (*machine)(nil)
	_ target.MediaStager = (*machine)(nil)
)

func (m *machine) Identity() target.Identity { return m.identity }

// Labels are recorded but reap never acts on them: nothing here was created by
// testease, so nothing here is testease's to remove.
func (m *machine) Labels() map[string]string { return m.labels }

func (m *machine) Session(context.Context) (target.Session, error) { return m.session, nil }

// Endpoint is the network surface the caller associated with this machine.
// It is independent of the execution address: an SSM target is dialled by
// instance id while its application is driven through an HTTP origin.
func (m *machine) Endpoint() (url.URL, bool) {
	if m.endpoint == nil {
		return url.URL{}, false
	}
	return *m.endpoint, true
}

// fingerprinted adds a capability only when an instrument was supplied.
// Embedding machine preserves its session and media-staging behavior.
type fingerprinted struct {
	*machine
	*fingerprint.Printer
}

var _ target.Fingerprinter = (*fingerprinted)(nil)

// mediaPutter is implemented by every persistent static transport. Keeping it
// private means Put remains a transport detail rather than becoming another
// way for scenarios to mutate a target outside their probes.
type mediaPutter interface {
	Put(context.Context, io.Reader, string, fs.FileMode) error
}

// residentMediaCheckAbove keeps ordinary probe and configuration staging
// simple while avoiding a redundant network transfer for package-sized inputs
// already baked into an immutable machine image.
const residentMediaCheckAbove int64 = 64 << 20

// Stage copies declared media onto an existing machine and, for a named input,
// hashes the bytes again on that machine. The runner already verified the local
// file; this second check closes the transport boundary so a record can prove
// that the file installed by a probe is the file named by the input bundle.
func (m *machine) Stage(ctx context.Context, items []target.Media) error {
	putter, ok := m.session.(mediaPutter)
	if !ok {
		return fmt.Errorf("backend %q cannot copy media to an existing machine", m.spec.Backend)
	}

	for _, item := range items {
		if item.Source == "" || item.Dest == "" {
			return fmt.Errorf("media needs both a source and destination")
		}
		info, err := os.Stat(item.Source)
		if err != nil {
			return fmt.Errorf("read media %s: %w", item.Source, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("media %s is not a regular file; static targets stage files, not directories", item.Source)
		}
		dest := m.remotePath(item.Dest)
		want := strings.TrimPrefix(strings.ToLower(item.SHA256), "sha256:")
		if want != "" && info.Size() >= residentMediaCheckAbove {
			actual, hashErr := m.remoteSHA256(ctx, dest)
			if hashErr == nil && actual == want {
				if m.log != nil {
					m.log.Info("resident media matched; transfer skipped",
						"host", m.spec.Host, "destination", dest,
						"sha256", want, "bytes", info.Size())
				}
				continue
			}
		}

		file, err := os.Open(item.Source)
		if err != nil {
			return fmt.Errorf("open media %s: %w", item.Source, err)
		}
		mode := fs.FileMode(0o600)
		executable := info.Mode().Perm()&0o111 != 0
		if executable {
			mode = 0o700
		}
		if item.ReadOnly {
			mode = 0o444
			if executable {
				mode = 0o555
			}
		}
		err = putter.Put(ctx, file, dest, mode)
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("stage media %s at %s: %w", item.Source, dest, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close media %s: %w", item.Source, closeErr)
		}

		if item.SHA256 != "" {
			actual, err := m.remoteSHA256(ctx, dest)
			if err != nil {
				return err
			}
			if actual != want {
				return fmt.Errorf("staged media %s hashes to sha256:%s on the target, want sha256:%s", filepath.Base(item.Source), actual, want)
			}
		}
	}
	return nil
}

// remotePath keeps scenario media destinations portable. Docker already maps
// target-root paths such as /sctools/tool.exe to C:\sctools\tool.exe for a
// Windows container; a persistent Windows target must receive the same path.
func (m *machine) remotePath(path string) string {
	if m.windows() && strings.HasPrefix(path, "/") {
		return `C:` + strings.ReplaceAll(path, "/", `\`)
	}
	return path
}

func (m *machine) remoteSHA256(ctx context.Context, remote string) (string, error) {
	var out, errOut sink
	var script []byte
	if m.windows() {
		script = []byte("$ErrorActionPreference = 'Stop'\n$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $args[0] -ErrorAction Stop).Hash.ToLowerInvariant()\nWrite-Output $hash\n")
	} else {
		script = []byte("#!/bin/sh\nsha256sum -- \"$1\" | awk '{print $1}'\n")
	}
	result, err := m.session.RunScript(ctx, script, []string{remote}, target.RunOpts{Stdout: &out, Stderr: &errOut})
	diagnostic := ""
	if lines := errOut.Lines(); len(lines) > 0 {
		diagnostic = ": " + lines[0]
	}
	if err != nil {
		return "", fmt.Errorf("hash staged media %s%s: %w", remote, diagnostic, err)
	}
	if result.RC != 0 {
		return "", fmt.Errorf("hash staged media %s: remote command exited %d%s", remote, result.RC, diagnostic)
	}
	lines := out.Lines()
	if len(lines) != 1 {
		return "", fmt.Errorf("hash staged media %s: expected one digest, got %d lines%s", remote, len(lines), diagnostic)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(lines[0]))
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("hash staged media %s: target returned an invalid SHA-256", remote)
	}
	return strings.ToLower(lines[0]), nil
}

func (m *machine) windows() bool {
	if m.spec.Backend == "winrm" {
		return true
	}
	if session, ok := m.session.(*ssmbackend.Session); ok {
		return strings.EqualFold(session.Instance.Platform, "Windows")
	}
	return false
}

// sink collects a few lines of output without keeping the whole stream. A
// write boundary is not a line boundary - a line split across two writes must
// come out as one line, not two.
type sink struct {
	lines   []string
	partial []byte
}

func (s *sink) Write(p []byte) (int, error) {
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := trimCR(string(s.partial[:i])); line != "" {
			s.lines = append(s.lines, line)
		}
		s.partial = s.partial[i+1:]
	}
}

// Lines is everything written, an unterminated final line included.
func (s *sink) Lines() []string {
	if line := trimCR(string(s.partial)); line != "" {
		return append(append([]string{}, s.lines...), line)
	}
	return s.lines
}

func trimCR(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
