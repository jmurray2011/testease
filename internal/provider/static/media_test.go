package static

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/target"
)

type mediaSession struct {
	body         []byte
	mode         fs.FileMode
	dest         string
	reportHash   string
	reportHashes []string
	reportRC     int
	reportError  string
	script       []byte
}

func (s *mediaSession) Put(_ context.Context, local io.Reader, remote string, mode fs.FileMode) error {
	var err error
	s.body, err = io.ReadAll(local)
	s.mode = mode
	s.dest = remote
	return err
}

func (s *mediaSession) Run(context.Context, []string, target.RunOpts) (target.Result, error) {
	return target.Result{}, nil
}

func (s *mediaSession) RunScript(_ context.Context, script []byte, _ []string, opt target.RunOpts) (target.Result, error) {
	s.script = append([]byte(nil), script...)
	if s.reportError != "" {
		_, _ = fmt.Fprintln(opt.Stderr, s.reportError)
	}
	if s.reportRC != 0 {
		return target.Result{RC: s.reportRC}, nil
	}
	hash := s.reportHash
	if len(s.reportHashes) > 0 {
		hash = s.reportHashes[0]
		s.reportHashes = s.reportHashes[1:]
	}
	if hash == "" {
		sum := sha256.Sum256(s.body)
		hash = fmt.Sprintf("%x", sum)
	}
	_, _ = fmt.Fprintln(opt.Stdout, hash)
	return target.Result{RC: 0}, nil
}

func TestWindowsMediaHashMakesPowerShellErrorsVisible(t *testing.T) {
	t.Parallel()

	session := &mediaSession{reportRC: 1, reportError: `Get-FileHash: Cannot find path C:\sctools\vt-win-lib.ps1`}
	machine := &machine{spec: target.Spec{Backend: "winrm"}, session: session}

	_, err := machine.remoteSHA256(t.Context(), `C:\sctools\vt-win-lib.ps1`)
	if err == nil || !strings.Contains(err.Error(), "Cannot find path") {
		t.Fatalf("remoteSHA256 error = %v, want the PowerShell diagnostic", err)
	}
	for _, want := range []string{"ErrorActionPreference", "-ErrorAction Stop"} {
		if !bytes.Contains(session.script, []byte(want)) {
			t.Errorf("Windows hash script lacks %q:\n%s", want, session.script)
		}
	}
}

func (s *mediaSession) Fetch(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (s *mediaSession) Close() error { return nil }

func TestNamedMediaIsCopiedAndVerifiedOnAStaticTarget(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "candidate.rpm")
	body := []byte("the exact release bytes")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	session := &mediaSession{}
	machine := &machine{spec: target.Spec{Backend: "ssh"}, session: session}

	err := machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: "/var/tmp/candidate.rpm", ReadOnly: true,
		SHA256: fmt.Sprintf("sha256:%x", sum),
	}})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !bytes.Equal(session.body, body) {
		t.Errorf("remote bytes = %q, want %q", session.body, body)
	}
	if session.dest != "/var/tmp/candidate.rpm" {
		t.Errorf("remote destination = %q", session.dest)
	}
	if session.mode.Perm() != 0o444 {
		t.Errorf("remote mode = %o, want 444", session.mode.Perm())
	}
}

func TestNamedMediaRefusesDifferentBytesOnTheTarget(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "candidate.rpm")
	if err := os.WriteFile(source, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &mediaSession{reportHash: strings.Repeat("0", 64)}
	machine := &machine{spec: target.Spec{Backend: "ssh"}, session: session}

	err := machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: "/var/tmp/candidate.rpm", SHA256: "sha256:" + strings.Repeat("1", 64),
	}})
	if err == nil || !strings.Contains(err.Error(), "on the target") {
		t.Fatalf("Stage error = %v, want target digest refusal", err)
	}
}

func TestLargeNamedMediaAlreadyOnTheTargetIsNotCopiedAgain(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "candidate.exe")
	if err := os.WriteFile(source, []byte("input identity is still locally resolved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(source, residentMediaCheckAbove); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", 64)
	session := &mediaSession{reportHash: want}
	machine := &machine{spec: target.Spec{Backend: "winrm"}, session: session}

	err := machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: `C:\upgrade.exe`, SHA256: "sha256:" + want,
	}})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if session.body != nil || session.dest != "" {
		t.Errorf("resident media was copied again: destination=%q bytes=%d", session.dest, len(session.body))
	}
}

func TestLargeNamedMediaReplacesDifferentResidentBytes(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "candidate.exe")
	body := []byte("the candidate that replaces resident bytes")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(source, residentMediaCheckAbove); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	wantText := fmt.Sprintf("%x", digest.Sum(nil))
	session := &mediaSession{reportHashes: []string{strings.Repeat("0", 64), wantText}}
	machine := &machine{spec: target.Spec{Backend: "winrm"}, session: session}

	err = machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: `C:\upgrade.exe`, SHA256: "sha256:" + wantText,
	}})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if session.dest != `C:\upgrade.exe` || int64(len(session.body)) != residentMediaCheckAbove {
		t.Errorf("replacement destination=%q bytes=%d", session.dest, len(session.body))
	}
}

func TestStaticMediaPreservesExecutableIntent(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "collector")
	if err := os.WriteFile(source, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := &mediaSession{}
	machine := &machine{spec: target.Spec{Backend: "ssh"}, session: session}

	if err := machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: "/var/tmp/collector", ReadOnly: true,
	}}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if session.mode.Perm() != 0o555 {
		t.Errorf("remote mode = %o, want 555", session.mode.Perm())
	}
}

func TestStaticMediaDoesNotPretendToCopyADirectory(t *testing.T) {
	t.Parallel()

	session := &mediaSession{}
	machine := &machine{spec: target.Spec{Backend: "ssh"}, session: session}
	err := machine.Stage(context.Background(), []target.Media{{Source: t.TempDir(), Dest: "/var/tmp/media"}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Stage error = %v, want directory refusal", err)
	}
}

func TestWindowsStaticMediaUsesTheSameTargetRootPathAsDocker(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "collector.exe")
	if err := os.WriteFile(source, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := &mediaSession{}
	machine := &machine{spec: target.Spec{Backend: "winrm"}, session: session}

	if err := machine.Stage(context.Background(), []target.Media{{
		Source: source, Dest: "/sctools/collector.exe", ReadOnly: true,
	}}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if session.dest != `C:\sctools\collector.exe` {
		t.Errorf("remote destination = %q", session.dest)
	}
}
