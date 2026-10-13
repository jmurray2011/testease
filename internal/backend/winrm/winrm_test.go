package winrm

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/jmurray2011/testease/internal/target"
)

func TestEncodedCommandsAreUTF16LE(t *testing.T) {
	t.Parallel()

	// PowerShell reads -EncodedCommand as UTF-16LE. That is its rule, not a
	// choice, and getting it wrong produces a script that runs as line noise.
	got := utf16le("hi")
	want := []byte{'h', 0, 'i', 0}
	if !bytes.Equal(got, want) {
		t.Errorf("utf16le = %v, want %v", got, want)
	}

	// And a rune outside the basic plane still round-trips.
	decoded := string(utf16.Decode(asUint16(utf16le("a\U0001F600b"))))
	if decoded != "a\U0001F600b" {
		t.Errorf("round trip = %q", decoded)
	}
}

func asUint16(b []byte) []uint16 {
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return out
}

func TestQuotingSurvivesAValueThatFightsBack(t *testing.T) {
	t.Parallel()

	if got := quote("it's"); got != "'it''s'" {
		t.Errorf("quote = %q", got)
	}
}

func TestDialRefusesAnEndpointItCannotUse(t *testing.T) {
	t.Parallel()

	if _, err := Dial(Endpoint{}); err == nil {
		t.Fatal("dialled nothing at all")
	}
}

// The live check. It needs a Windows machine that answers WinRM and is skipped
// without one, because a test that reaches the network to decide whether it
// passes is not a test of this code.
func liveEndpoint(t *testing.T) Endpoint {
	t.Helper()

	host := os.Getenv("TESTEASE_WINRM_HOST")
	user := os.Getenv("TESTEASE_WINRM_USER")
	passFile := os.Getenv("TESTEASE_WINRM_PASSWORD_FILE")
	if host == "" || user == "" || passFile == "" {
		t.Skip("no WinRM endpoint configured: set TESTEASE_WINRM_HOST, _USER and _PASSWORD_FILE")
	}
	// Plain HTTP by default, against a machine keeping the default
	// AllowUnencrypted = false: the messages are encrypted on this side, so
	// nothing on the server has to be relaxed to make this pass.
	https := os.Getenv("TESTEASE_WINRM_HTTPS") != ""
	body, err := os.ReadFile(passFile)
	if err != nil {
		t.Skipf("cannot read the password file: %v", err)
	}
	return Endpoint{Host: host, User: user, Password: strings.TrimSpace(string(body)), HTTPS: https, Insecure: true}
}

func TestALiveMachineRunsAScriptAndReportsItsCode(t *testing.T) {
	session, err := Dial(liveEndpoint(t))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var out, errBuf bytes.Buffer
	result, err := session.RunScript(context.Background(),
		[]byte("Write-Output \"hello $($args[0])\"\nexit 3\n"), []string{"there"},
		target.RunOpts{Stdout: &out, Stderr: &errBuf})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if result.RC != 3 {
		t.Errorf("rc = %d, want 3 verbatim", result.RC)
	}
	if strings.TrimSpace(out.String()) != "hello there" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestALiveMachineTakesAFileAndGivesItBack(t *testing.T) {
	session, err := Dial(liveEndpoint(t))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	const body = "written by testease\r\n"
	remote := `C:\Windows\Temp\testease-roundtrip.txt`
	if err := session.Put(ctx, strings.NewReader(body), remote, 0o644); err != nil {
		t.Fatalf("Put: %v", err)
	}

	back, err := session.Fetch(ctx, remote)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer func() { _ = back.Close() }()

	var got bytes.Buffer
	if _, err := got.ReadFrom(back); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Byte for byte, carriage returns included: what arrives is what was sent.
	if got.String() != body {
		t.Errorf("round trip = %q, want %q", got.String(), body)
	}
}
