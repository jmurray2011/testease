package ssm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

// Systems Manager is the transport that can lose evidence, and these are the
// places that matters.

func TestTheDocumentFollowsThePlatform(t *testing.T) {
	t.Parallel()

	// The transport question the design left open answers itself: Systems
	// Manager ships one document per shell, and it already knows the platform.
	if got := Document("Windows"); got != "AWS-RunPowerShellScript" {
		t.Errorf("Windows uses %q", got)
	}
	if got := Document("Linux"); got != "AWS-RunShellScript" {
		t.Errorf("Linux uses %q", got)
	}
	// Anything unfamiliar gets the shell document rather than a guess at
	// PowerShell: being wrong towards a shell fails loudly on the instance.
	if got := Document(""); got != "AWS-RunShellScript" {
		t.Errorf("an unknown platform uses %q", got)
	}
}

func TestEveryCallNamesItsAccountAndRegion(t *testing.T) {
	t.Parallel()

	// Ten profiles on one machine is normal. A backend that inherited whichever
	// one was in the environment is one variable away from measuring another
	// account, and a record saying "SSM connected to something" is evidence
	// about nothing.
	c := Client{Profile: "lab", Region: "us-east-2"}
	got := strings.Join(c.Args("ssm", "send-command"), " ")

	if !strings.Contains(got, "--profile lab") || !strings.Contains(got, "--region us-east-2") {
		t.Errorf("args = %q, want the account and region spelled out", got)
	}
}

func TestAClientWithNoProfileRefuses(t *testing.T) {
	t.Parallel()

	_, err := Client{Region: "us-east-2"}.Output(t.Context(), "sts", "get-caller-identity")
	if err == nil {
		t.Fatal("a client with no profile ran a command anyway")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

func TestArgumentsAndEnvironmentAreBoundIntoTheScript(t *testing.T) {
	t.Parallel()

	// A document takes commands, not a command line, so the two have to be
	// written into the script the way the shell it is for binds them.
	shell := wrap("Linux", []byte("echo \"$1\"\n"), []string{"6.2.4"}, []string{"VERSION=3.0"}, "")
	if !strings.Contains(shell, "set -- '6.2.4'") {
		t.Errorf("shell script does not bind its arguments:\n%s", shell)
	}
	if !strings.Contains(shell, "export VERSION='3.0'") {
		t.Errorf("shell script does not bind its environment:\n%s", shell)
	}

	pwsh := wrap("Windows", []byte("Write-Host $args[0]\n"), []string{"6.2.4"}, []string{"VERSION=3.0"}, "")
	if !strings.Contains(pwsh, "$args = @('6.2.4')") {
		t.Errorf("powershell script does not bind its arguments:\n%s", pwsh)
	}
	if !strings.Contains(pwsh, "$env:VERSION = '3.0'") {
		t.Errorf("powershell script does not bind its environment:\n%s", pwsh)
	}
}

func TestQuotingSurvivesAValueThatFightsBack(t *testing.T) {
	t.Parallel()

	shell := wrap("Linux", nil, []string{"it's"}, nil, "")
	if !strings.Contains(shell, `'it'\''s'`) {
		t.Errorf("a quote in an argument was not escaped:\n%s", shell)
	}
	pwsh := wrap("Windows", nil, []string{"it's"}, nil, "")
	if !strings.Contains(pwsh, "'it''s'") {
		t.Errorf("a quote in an argument was not escaped:\n%s", pwsh)
	}
}

func TestTruncatedOutputSaysSoRatherThanPassingForTheWhole(t *testing.T) {
	t.Parallel()

	// Systems Manager stops at 24000 characters. A claim selected from a
	// truncated tail is testimony about a line the tool never finished
	// printing, so the cut has to be visible in the log itself.
	var out strings.Builder
	writeStream(&out, strings.Repeat("x", OutputLimit), "s3://somewhere/stdout", "stdout")

	got := out.String()
	if !strings.Contains(got, "truncated") {
		t.Errorf("a truncated stream did not say so")
	}
	if !strings.Contains(got, "s3://somewhere/stdout") {
		t.Errorf("the note does not say where the rest is")
	}
}

func TestOutputInsideTheLimitIsPassedThroughUntouched(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	writeStream(&out, "all done\n", "", "stdout")
	if out.String() != "all done\n" {
		t.Errorf("output = %q, want it verbatim", out.String())
	}
}

// The live check. It needs credentials and a managed instance, and is skipped
// without them: a test that reaches the network to decide whether it passes is
// not a test of this code.
func liveClient(t *testing.T) (Client, string) {
	t.Helper()

	profile := os.Getenv("TESTEASE_SSM_PROFILE")
	instance := os.Getenv("TESTEASE_SSM_INSTANCE")
	if profile == "" || instance == "" {
		t.Skip("no managed instance configured: set TESTEASE_SSM_PROFILE and TESTEASE_SSM_INSTANCE")
	}
	return Client{Profile: profile, Region: os.Getenv("TESTEASE_SSM_REGION")}, instance
}

func TestALiveInstanceRunsAScriptAndReportsItsCode(t *testing.T) {
	client, id := liveClient(t)
	ctx := t.Context()

	// The backend learns the platform rather than being told, so that the
	// document it runs is the one the instance can actually run.
	instance, err := client.Describe(ctx, id)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if instance.Ping != "Online" {
		t.Skipf("instance %s is %s", id, instance.Ping)
	}
	t.Logf("instance %s is %s (%s), document %s", instance.ID, instance.Platform, instance.Name, Document(instance.Platform))

	session := &Session{Client: client, Instance: instance}
	script := []byte("echo \"hello $1\"\nexit 3\n")
	if strings.EqualFold(instance.Platform, "Windows") {
		script = []byte("Write-Output \"hello $($args[0])\"\nexit 3\n")
	}

	var out, errBuf strings.Builder
	result, err := session.RunScript(ctx, script, []string{"there"},
		target.RunOpts{Stdout: &out, Stderr: &errBuf, Timeout: 3 * time.Minute})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if result.RC != 3 {
		t.Errorf("rc = %d, want 3 verbatim (stderr: %q)", result.RC, errBuf.String())
	}
	if strings.TrimSpace(out.String()) != "hello there" {
		t.Errorf("stdout = %q, want the argument bound and the output carried back", out.String())
	}
	if strings.TrimSpace(errBuf.String()) != "" {
		t.Errorf("stderr = %q, want the two streams apart", errBuf.String())
	}
}

func TestALiveInstanceFetchesALargeArtifactExactlyAndCleansItsTemporaryFile(t *testing.T) {
	client, id := liveClient(t)
	ctx := t.Context()
	instance, err := client.Describe(ctx, id)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if instance.Ping != "Online" {
		t.Skipf("instance %s is %s", id, instance.Ping)
	}

	session := &Session{Client: client, Instance: instance}
	want := []byte(strings.Repeat("installer log line\n", 20_000))
	remote := "/tmp/testease-fetch-live.txt"
	create := `yes 'installer log line' | head -n 20000 > /tmp/testease-fetch-live.txt`
	remove := `rm -f -- /tmp/testease-fetch-live.txt`
	leftovers := `find /tmp -maxdepth 1 -name 'testease-fetch-*.gz' -print | wc -l`
	if strings.EqualFold(instance.Platform, "Windows") {
		remote = `C:\Windows\Temp\testease-fetch-live.txt`
		create = "[IO.File]::WriteAllText('C:\\Windows\\Temp\\testease-fetch-live.txt', ('installer log line' + \"`n\") * 20000, [Text.UTF8Encoding]::new($false))"
		remove = `Remove-Item -Force -ErrorAction SilentlyContinue 'C:\Windows\Temp\testease-fetch-live.txt'`
		leftovers = `(Get-ChildItem 'C:\Windows\Temp\testease-fetch-*.gz' -ErrorAction SilentlyContinue | Measure-Object).Count`
	}
	defer func() {
		_, _ = session.Run(context.WithoutCancel(ctx), []string{remove}, target.RunOpts{Timeout: time.Minute})
	}()

	result, err := session.Run(ctx, []string{create}, target.RunOpts{Timeout: time.Minute})
	if err != nil || result.RC != 0 {
		t.Fatalf("create live artifact: rc=%d error=%v", result.RC, err)
	}
	started := time.Now()
	reader, err := session.Fetch(ctx, remote)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read fetched artifact: read=%v close=%v", err, closeErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("fetched %d bytes, want the exact %d-byte artifact", len(got), len(want))
	}
	t.Logf("fetched %d bytes exactly in %s", len(got), time.Since(started).Round(time.Millisecond))

	var out strings.Builder
	result, err = session.Run(ctx, []string{leftovers}, target.RunOpts{Stdout: &out, Timeout: time.Minute})
	if err != nil || result.RC != 0 {
		t.Fatalf("check compressed-temp cleanup: rc=%d error=%v", result.RC, err)
	}
	if strings.TrimSpace(out.String()) != "0" {
		t.Fatalf("compressed temporary files remain: %q", out.String())
	}
}

func TestTheTransportsOwnSentenceIsNotTheProbes(t *testing.T) {
	t.Parallel()

	// Systems Manager appends this when a command exits non-zero. A claim
	// selected from the last line of stderr would otherwise quote Systems
	// Manager about a step it merely carried.
	rest, narration := splitNarration("something the probe said\nfailed to run commands: exit status 3", 3)
	if rest != "something the probe said\n" {
		t.Errorf("stderr = %q, want the probe's own output kept", rest)
	}
	if narration == "" {
		t.Error("the transport's sentence was left in the probe's stream")
	}
}

func TestOnlyTheExactTrailerIsTaken(t *testing.T) {
	t.Parallel()

	// Anything looser would be editing a probe's own output.
	for _, stderr := range []string{
		"failed to run commands: exit status 9",
		"the probe printed: failed to run commands: exit status 3 and carried on",
		"nothing like it at all",
	} {
		if rest, _ := splitNarration(stderr, 3); rest != stderr {
			t.Errorf("splitNarration(%q) edited the probe's output to %q", stderr, rest)
		}
	}
}

func TestAProbeIsGivenSomewhereToWriteReadings(t *testing.T) {
	t.Parallel()

	// Systems Manager carries back two streams and there is no third
	// descriptor to hand a probe, so the probe is handed a path - and gets
	// fd 3 pointed at it, so that a probe written for any other backend works
	// here unchanged.
	shell := wrap("Linux", nil, nil, nil, "/tmp/channel")
	if !strings.Contains(shell, "exec 3>'/tmp/channel'") {
		t.Errorf("fd 3 was not opened:\n%s", shell)
	}
	if !strings.Contains(shell, "TESTEASE_FACTS='/tmp/channel'") {
		t.Errorf("the path was not named:\n%s", shell)
	}

	pwsh := wrap("Windows", nil, nil, nil, `C:\Temp\channel`)
	if !strings.Contains(pwsh, "TESTEASE_FACTS") {
		t.Errorf("powershell was not given a path:\n%s", pwsh)
	}
}

func TestLargeMediaRefusesBeforeBuildingAnOversizedDocument(t *testing.T) {
	t.Parallel()

	session := &Session{Client: Client{Profile: "lab", Region: "us-east-2"}, Instance: Instance{ID: "i-test", Platform: "Windows"}}
	err := session.Put(t.Context(), strings.NewReader(strings.Repeat("x", InlinePutLimit+1)), `C:\upgrade.exe`, 0o444)
	if err == nil {
		t.Fatal("oversized inline media was sent as a command document")
	}
	if !strings.Contains(err.Error(), "--ssm-transfer-store") {
		t.Errorf("error %q does not give the large-media remedy", err)
	}
}

func TestInlineMediaCreatesItsDestinationDirectory(t *testing.T) {
	t.Parallel()

	windows := inlinePutCommand("Windows", `C:\sctools\vt-win-lib.ps1`, 0o444, "eA==")
	for _, want := range []string{"ErrorActionPreference", "Split-Path -Parent", "New-Item -ItemType Directory", "WriteAllBytes"} {
		if !strings.Contains(windows, want) {
			t.Errorf("Windows inline put lacks %q:\n%s", want, windows)
		}
	}

	linux := inlinePutCommand("Linux", "/opt/testease/probe", 0o555, "eA==")
	for _, want := range []string{"set -e", "mkdir -p -- '/opt/testease'", "chmod 555"} {
		if !strings.Contains(linux, want) {
			t.Errorf("Linux inline put lacks %q:\n%s", want, linux)
		}
	}
}

func TestTemporaryTransferNamesStayInsideTheConfiguredPrefix(t *testing.T) {
	t.Parallel()

	got, err := transferURI("s3://lab-media/scratch/", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3://lab-media/scratch/testease-transfer/abc123" {
		t.Errorf("uri = %q", got)
	}
	if _, err := transferURI("lab-media", "abc123"); err == nil {
		t.Fatal("a non-S3 transfer store was accepted")
	}
	if ValidTransferStore("s3:///missing-bucket") {
		t.Fatal("an S3 URI without a bucket was accepted")
	}
}

func TestLargeMediaDownloadIsAtomicAndPlatformSpecific(t *testing.T) {
	t.Parallel()

	win := downloadCommand("Windows", "https://example.invalid/object?token=secret", `C:\media\upgrade.exe`, 0o444, "abc")
	for _, want := range []string{"WebClient", ".testease-abc", "Move-Item", `C:\media\upgrade.exe`} {
		if !strings.Contains(win, want) {
			t.Errorf("Windows download command lacks %q:\n%s", want, win)
		}
	}

	linux := downloadCommand("Linux", "https://example.invalid/object?token=secret", "/var/tmp/upgrade.rpm", 0o555, "abc")
	for _, want := range []string{"curl -fsSL", ".testease-abc", "chmod 555", "mv -f"} {
		if !strings.Contains(linux, want) {
			t.Errorf("Linux download command lacks %q:\n%s", want, linux)
		}
	}
}

func TestArtifactChunksFitTheSSMOutputLimitAndPreserveBinaryData(t *testing.T) {
	t.Parallel()

	if base64.StdEncoding.EncodedLen(FetchChunkSize) >= OutputLimit {
		t.Fatalf("chunk expands to %d bytes, SSM limit is %d", base64.StdEncoding.EncodedLen(FetchChunkSize), OutputLimit)
	}
	win := fetchChunkCommand("Windows", `C:\i4j.log`, 12288, 4096)
	for _, want := range []string{"OpenRead", "Seek(12288", "byte[] 4096", "ToBase64String"} {
		if !strings.Contains(win, want) {
			t.Errorf("Windows chunk command lacks %q:\n%s", want, win)
		}
	}
	linux := fetchChunkCommand("Linux", "/var/log/install.log", 12288, 4096)
	for _, want := range []string{"skip=12288", "count=4096", "base64"} {
		if !strings.Contains(linux, want) {
			t.Errorf("Linux chunk command lacks %q:\n%s", want, linux)
		}
	}
}

func TestLargeArtifactsAreCompressedWithoutGivingTheTargetAWSCredentials(t *testing.T) {
	t.Parallel()

	win := compressCommand("Windows", `C:\i4j.log`, `C:\Windows\Temp\testease-fetch-abc.gz`)
	for _, want := range []string{"GZipStream", "CopyTo", `C:\i4j.log`, "testease-fetch-abc.gz", "Length"} {
		if !strings.Contains(win, want) {
			t.Errorf("Windows compression command lacks %q:\n%s", want, win)
		}
	}
	linux := compressCommand("Linux", "/var/log/install.log", "/tmp/testease-fetch-abc.gz")
	for _, want := range []string{"gzip -c", "install.log", "testease-fetch-abc.gz", "stat -c %s"} {
		if !strings.Contains(linux, want) {
			t.Errorf("Linux compression command lacks %q:\n%s", want, linux)
		}
	}
	for _, command := range []string{win, linux} {
		if strings.Contains(command, "s3") || strings.Contains(command, "AWS_") {
			t.Errorf("compression unexpectedly grants or uses AWS access:\n%s", command)
		}
	}
}

func TestCompressedArtifactMustExpandToTheSizeTheTargetReported(t *testing.T) {
	t.Parallel()

	want := []byte(strings.Repeat("installer log line\n", 1000))
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	if _, err := zw.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := decodeGzip(packed.Bytes(), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("decompressed artifact differs from its original bytes")
	}
	if _, err := decodeGzip(packed.Bytes(), len(want)+1); err == nil {
		t.Fatal("a decompressed artifact with the wrong byte count was accepted")
	}
	if _, err := decodeGzip([]byte("not gzip"), 8); err == nil {
		t.Fatal("invalid gzip was accepted")
	}
}
