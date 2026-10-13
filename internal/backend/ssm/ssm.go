// Package ssm runs probes on an instance through AWS Systems Manager.
//
// It shells out to the aws CLI rather than linking a client library: the CLI
// already knows how to resolve SSO sessions, assumed roles and regions, and
// re-implementing that in order to avoid one subprocess would be trading a
// solved problem for an unsolved one.
//
// The account and region are always explicit. A backend that inherited whichever
// AWS_PROFILE happened to be set would be one environment variable away from
// measuring the wrong account, and a record that says "SSM connected to
// something" is not evidence about anything.
package ssm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

// OutputLimit is how much output an invocation carries back inline.
//
// Systems Manager truncates at 24000 characters. Beyond that the rest exists
// only in S3, if the command was configured to put it there. A step whose
// output was cut short has to say so: a claim selected from a truncated tail is
// testimony about a line the tool never finished printing.
const OutputLimit = 24000

// InlinePutLimit leaves enough room for base64 expansion and the surrounding
// document below Systems Manager's 24,000-character command limit. Larger
// media uses the caller's temporary S3 transfer store instead of pretending a
// command document is a file-transfer protocol.
const InlinePutLimit = 16 * 1024

// FetchChunkSize stays below the same document-output boundary after base64
// expansion. SSM has no native download primitive, so artifacts are read in
// bounded binary chunks and reassembled exactly on the runner.
const FetchChunkSize = 12 * 1024

// CompressFetchAbove avoids turning an ordinary diagnostic log into hundreds
// of three-second SSM polls. Compression happens on the target, uses no AWS
// credentials, and is accepted only when it makes the transport smaller.
const CompressFetchAbove = 128 * 1024

// PollEvery is how often an invocation is asked whether it has finished.
const PollEvery = 3 * time.Second

// WaitBudget bounds an invocation whose step declared no timeout. It is the
// backend's own floor of honesty rather than a tuning knob: without one, a
// command Systems Manager never resolves leaves the runner polling forever.
const WaitBudget = time.Hour

// Client runs aws CLI commands against one account and region.
type Client struct {
	// Profile names the credentials to use. There is no default: ten profiles
	// on one machine is normal, and picking one silently is how a command
	// lands in an account nobody meant.
	Profile string
	Region  string

	// TransferStore is an s3:// bucket or prefix used only while copying large
	// declared media to a managed instance. Each object is removed immediately
	// after the instance finishes downloading it.
	TransferStore string

	// Bin is the aws executable. Empty means "aws" on PATH.
	Bin string

	Log *slog.Logger
}

func (c Client) bin() string {
	if c.Bin == "" {
		return "aws"
	}
	return c.Bin
}

func (c Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

// Args is the argument list for one aws call, with the account and region
// always spelled out.
func (c Client) Args(args ...string) []string {
	full := make([]string, 0, len(args)+4)
	if c.Profile != "" {
		full = append(full, "--profile", c.Profile)
	}
	if c.Region != "" {
		full = append(full, "--region", c.Region)
	}
	return append(full, args...)
}

// Output runs an aws command and returns its stdout.
func (c Client) Output(ctx context.Context, args ...string) ([]byte, error) {
	return c.output(ctx, nil, args...)
}

func (c Client) output(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if c.Profile == "" {
		return nil, errors.New("no aws profile given: testease will not pick one for you")
	}
	full := c.Args(args...)
	c.log().Debug("aws", "args", redacted(full))

	cmd := exec.CommandContext(ctx, c.bin(), full...)
	var out, errBuf bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("aws %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// redacted keeps a command payload out of the log. The payload carries the
// whole wrapped script, environment exports included, and an env value in a
// log line is an env value in CI output.
func redacted(args []string) []string {
	out := append([]string{}, args...)
	for i, a := range out {
		if a == "--cli-input-json" && i+1 < len(out) {
			out[i+1] = "[payload redacted]"
		}
	}
	return out
}

// Instance is what Systems Manager knows about a managed instance.
type Instance struct {
	ID       string
	Platform string
	Name     string
	Ping     string
}

// Describe reads what Systems Manager knows about an instance. This is how the
// backend learns which document to run a script with, rather than being told.
func (c Client) Describe(ctx context.Context, id string) (Instance, error) {
	out, err := c.Output(ctx, "ssm", "describe-instance-information",
		"--filters", "Key=InstanceIds,Values="+id, "--output", "json")
	if err != nil {
		return Instance{}, err
	}

	var payload struct {
		List []struct {
			InstanceID   string `json:"InstanceId"`
			PlatformType string `json:"PlatformType"`
			PlatformName string `json:"PlatformName"`
			PingStatus   string `json:"PingStatus"`
		} `json:"InstanceInformationList"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Instance{}, fmt.Errorf("read instance information: %w", err)
	}
	if len(payload.List) == 0 {
		return Instance{}, fmt.Errorf("instance %s is not managed by Systems Manager in this account", id)
	}
	found := payload.List[0]
	return Instance{
		ID:       found.InstanceID,
		Platform: found.PlatformType,
		Name:     found.PlatformName,
		Ping:     found.PingStatus,
	}, nil
}

// Document is how a script reaches an instance, which depends on what the
// instance runs.
//
// This is the transport question the design left open until now, and the answer
// is that there is nothing to choose: Systems Manager ships one document per
// shell, the platform says which, and the platform is something Systems Manager
// already knows.
func Document(platform string) string {
	if strings.EqualFold(platform, "Windows") {
		return "AWS-RunPowerShellScript"
	}
	return "AWS-RunShellScript"
}

// Session runs probes on one managed instance.
type Session struct {
	Client   Client
	Instance Instance
}

var _ target.Session = (*Session)(nil)

// Run executes a command. Stdin is not connected: Systems Manager has no
// stdin to connect, which is the same answer the other backends give for a
// different reason.
func (s *Session) Run(ctx context.Context, cmd []string, opt target.RunOpts) (target.Result, error) {
	return s.send(ctx, strings.Join(cmd, " "), opt)
}

// RunScript ships a script and runs it with args.
//
// The script becomes the document's commands, with the arguments bound the way
// the shell it is written for binds them. One invocation, one ordered log: the
// same primitive the other backends offer, over a transport that has no
// interactive channel at all.
func (s *Session) RunScript(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	// The structured channel has to be a file here. Systems Manager runs a
	// document and carries back two streams; there is no third descriptor to
	// hand a probe, so the probe is handed a path instead and the file is
	// collected afterwards. A judge cannot tell which transport carried a
	// reading, which is the point.
	channel := fmt.Sprintf("%s/testease-fd3-%d", tempDir(s.Instance.Platform), time.Now().UnixNano())

	result, err := s.send(ctx, wrap(s.Instance.Platform, script, args, opt.Env, channel), opt)
	s.drainChannel(ctx, channel, opt.Structured)
	return result, err
}

// drainChannel collects what the probe wrote to its structured channel and
// removes it. A channel that never appeared is not an error: a probe that
// recorded nothing recorded nothing.
func (s *Session) drainChannel(ctx context.Context, path string, into io.Writer) {
	if into == nil {
		return
	}
	script := fmt.Sprintf("cat %s 2>/dev/null; rm -f %s", shellQuote(path), shellQuote(path))
	if strings.EqualFold(s.Instance.Platform, "Windows") {
		script = fmt.Sprintf(
			"if (Test-Path %s) { Get-Content -Raw %s; Remove-Item -Force %s }",
			powershellQuote(path), powershellQuote(path), powershellQuote(path),
		)
	}
	// Sent as a bare document rather than through RunScript, or collecting the
	// channel would open a channel of its own, forever.
	_, _ = s.send(ctx, script, target.RunOpts{Stdout: into, Stderr: io.Discard})
}

func tempDir(platform string) string {
	if strings.EqualFold(platform, "Windows") {
		return `C:\Windows\Temp`
	}
	return "/tmp"
}

// wrap binds arguments and environment into the script, because a document
// takes commands rather than a command line.
func wrap(platform string, script []byte, args []string, env []string, channel string) string {
	var b strings.Builder
	if strings.EqualFold(platform, "Windows") {
		if channel != "" {
			// PowerShell has no fd 3 either, so a probe writes to the path.
			fmt.Fprintf(&b, "$env:TESTEASE_FACTS = %s\n", powershellQuote(channel))
		}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			fmt.Fprintf(&b, "$env:%s = %s\n", k, powershellQuote(v))
		}
		b.WriteString("$args = @(")
		for i, a := range args {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(powershellQuote(a))
		}
		b.WriteString(")\n")
		b.Write(script)
		return b.String()
	}

	if channel != "" {
		// fd 3 is where a probe writes readings, so fd 3 is what it gets -
		// pointed at a file, because there is no third stream on the way back.
		fmt.Fprintf(&b, "export TESTEASE_FACTS=%s\nexec 3>%s\n", shellQuote(channel), shellQuote(channel))
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(v))
	}
	b.WriteString("set -- ")
	for i, a := range args {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(shellQuote(a))
	}
	b.WriteString("\n")
	b.Write(script)
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func powershellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// invocation is what Systems Manager says about a command that has run.
type invocation struct {
	Status         string `json:"Status"`
	ResponseCode   int    `json:"ResponseCode"`
	StandardOutput string `json:"StandardOutputContent"`
	StandardError  string `json:"StandardErrorContent"`
	StandardOutURL string `json:"StandardOutputUrl"`
	StandardErrURL string `json:"StandardErrorUrl"`
}

// send runs one document and waits for it.
func (s *Session) send(ctx context.Context, commands string, opt target.RunOpts) (target.Result, error) {
	payload, err := json.Marshal(map[string]any{
		"InstanceIds":  []string{s.Instance.ID},
		"DocumentName": Document(s.Instance.Platform),
		"Parameters":   map[string][]string{"commands": {commands}},
	})
	if err != nil {
		return target.Result{RC: -1}, fmt.Errorf("build the command: %w", err)
	}

	started := time.Now()
	out, err := s.Client.Output(ctx, "ssm", "send-command", "--cli-input-json", string(payload), "--output", "json")
	if err != nil {
		return target.Result{RC: -1, Duration: time.Since(started)}, err
	}

	var sent struct {
		Command struct {
			CommandID string `json:"CommandId"`
		} `json:"Command"`
	}
	if err := json.Unmarshal(out, &sent); err != nil {
		return target.Result{RC: -1, Duration: time.Since(started)}, fmt.Errorf("read the command id: %w", err)
	}

	inv, err := s.wait(ctx, sent.Command.CommandID, opt.Timeout)
	result := target.Result{Duration: time.Since(started)}
	if err != nil {
		result.RC = -1
		// ONLY A BOUND THE STEP DECLARED makes an overrun the probe's to
		// answer for. A step that declared none gets WaitBudget, this
		// backend's own floor, and the runner maps TimedOut to probe-fail -
		// so an hour nobody in the scenario chose would become a verdict about
		// the product. docker, ssh and winrm all guard this the same way; the
		// two backends that did not were this one and hyperv's one-shot.
		if opt.Timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
			result.TimedOut = true
			return result, nil
		}
		return result, err
	}

	writeStream(opt.Stdout, inv.StandardOutput, inv.StandardOutURL, "stdout")

	// Systems Manager appends its own sentence to stderr when a command exits
	// non-zero. That sentence is the transport narrating, not the probe, and a
	// claim selected from the last line of stderr would quote Systems Manager
	// about a step it merely carried. It is taken out of the stream and said
	// on the diagnostic channel instead, where the transport's voice belongs.
	stderr, narration := splitNarration(inv.StandardError, inv.ResponseCode)
	if narration != "" {
		s.Client.log().Debug("systems manager reported the invocation",
			"instance", s.Instance.ID, "said", narration)
	}
	writeStream(opt.Stderr, stderr, inv.StandardErrURL, "stderr")

	switch inv.Status {
	case "Success", "Failed":
		result.RC = inv.ResponseCode
	case "TimedOut", "DeliveryTimedOut", "ExecutionTimedOut":
		// The instance did not finish, so its exit code is not a reading.
		result.RC = -1
		result.TimedOut = true
	default:
		// Cancelled, Undeliverable, Terminated: the command did not run to a
		// conclusion, and a response code from one that did not is not one.
		// It is the transport's failure, said as one - a bare -1 with no error
		// would be recorded as a measurement nobody took.
		result.RC = -1
		return result, fmt.Errorf("invocation on %s ended %s: the command did not run to a conclusion", s.Instance.ID, inv.Status)
	}
	return result, nil
}

// splitNarration takes Systems Manager's own trailer off the end of stderr.
//
// The match is anchored and exact - the final line, and only when it is the
// sentence Systems Manager writes for this precise exit code. Anything looser
// would be editing a probe's own output, which is the one thing a record must
// never do to evidence.
func splitNarration(stderr string, code int) (rest, narration string) {
	trailer := fmt.Sprintf("failed to run commands: exit status %d", code)
	trimmed := strings.TrimRight(stderr, "\r\n")
	if !strings.HasSuffix(trimmed, trailer) {
		return stderr, ""
	}
	rest = strings.TrimSuffix(trimmed, trailer)
	if len(rest) > 0 && !strings.HasSuffix(rest, "\n") {
		// The trailer was not on a line of its own, so it is not the trailer.
		if !strings.HasSuffix(strings.TrimRight(rest, " "), "\n") && rest != "" {
			return stderr, ""
		}
	}
	return rest, trailer
}

// writeStream passes an invocation's output on, saying plainly when Systems
// Manager cut it short rather than letting a prefix pass for the whole thing.
func writeStream(w io.Writer, content, url, which string) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, content)
	if len(content) < OutputLimit {
		return
	}
	// Truncation is the transport losing evidence, and a reader has to know:
	// the last line here is not the last line the tool printed.
	note := fmt.Sprintf(
		"\n[testease] Systems Manager truncated this %s at %d characters; the rest was not carried back",
		which, OutputLimit,
	)
	if url != "" {
		note += " (the full output is at " + url + ")"
	}
	_, _ = io.WriteString(w, note+"\n")
}

// wait polls until the invocation has finished, or the budget runs out. A
// caller may raise the backend's budget and never lower it to nothing: zero
// means the backend's own, because a poll with no bound at all never returns
// on a command the service loses.
func (s *Session) wait(ctx context.Context, commandID string, budget time.Duration) (invocation, error) {
	if budget <= 0 {
		budget = WaitBudget
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, budget)
	defer cancel()

	for {
		out, err := s.Client.Output(ctx, "ssm", "get-command-invocation",
			"--command-id", commandID, "--instance-id", s.Instance.ID, "--output", "json")
		if err == nil {
			var inv invocation
			if err := json.Unmarshal(out, &inv); err != nil {
				return invocation{}, fmt.Errorf("read the invocation: %w", err)
			}
			switch inv.Status {
			case "Pending", "InProgress", "Delayed":
			default:
				return inv, nil
			}
		}

		select {
		case <-ctx.Done():
			return invocation{}, ctx.Err()
		case <-time.After(PollEvery):
		}
	}
}

// Fetch reads a file off the instance through bounded base64 chunks. Large
// artifacts are first compressed into a random target-side temporary file;
// this preserves the same byte-for-byte result without granting the instance
// access to the transfer bucket. One SSM invocation truncates output at 24,000
// characters, so the remaining chunks stay below that boundary.
func (s *Session) Fetch(ctx context.Context, remote string) (io.ReadCloser, error) {
	size, err := s.remoteSize(ctx, remote)
	if err != nil {
		return nil, err
	}
	if size >= CompressFetchAbove {
		artifact, used, err := s.fetchCompressed(ctx, remote, size)
		if err != nil {
			return nil, err
		}
		if used {
			return io.NopCloser(bytes.NewReader(artifact)), nil
		}
	}
	artifact, err := s.fetchExact(ctx, remote, size)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(artifact)), nil
}

func (s *Session) fetchExact(ctx context.Context, remote string, size int) ([]byte, error) {
	var artifact bytes.Buffer
	for offset := 0; offset < size; offset += FetchChunkSize {
		count := min(FetchChunkSize, size-offset)
		var encoded bytes.Buffer
		result, runErr := s.send(ctx, fetchChunkCommand(s.Instance.Platform, remote, offset, count),
			target.RunOpts{Stdout: &encoded, Stderr: io.Discard})
		if runErr != nil {
			return nil, fmt.Errorf("fetch %s from %s at byte %d: %w", remote, s.Instance.ID, offset, runErr)
		}
		if result.RC != 0 {
			return nil, fmt.Errorf("fetch %s from %s at byte %d: exit %d", remote, s.Instance.ID, offset, result.RC)
		}
		chunk, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded.String()))
		if decodeErr != nil {
			return nil, fmt.Errorf("fetch %s from %s at byte %d: invalid base64: %w", remote, s.Instance.ID, offset, decodeErr)
		}
		if len(chunk) != count {
			return nil, fmt.Errorf("fetch %s from %s at byte %d: got %d bytes, want %d", remote, s.Instance.ID, offset, len(chunk), count)
		}
		_, _ = artifact.Write(chunk)
	}
	return artifact.Bytes(), nil
}

func (s *Session) fetchCompressed(ctx context.Context, remote string, originalSize int) (
	artifact []byte, used bool, resultErr error,
) {
	token, err := transferToken()
	if err != nil {
		return nil, false, fmt.Errorf("name temporary compressed fetch: %w", err)
	}
	packed := compressedFetchPath(s.Instance.Platform, token)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		resultErr = errors.Join(resultErr, s.removeRemote(cleanupCtx, packed))
	}()

	var out bytes.Buffer
	run, err := s.send(ctx, compressCommand(s.Instance.Platform, remote, packed),
		target.RunOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil || run.RC != 0 {
		s.Client.log().Debug("target-side artifact compression unavailable",
			"instance", s.Instance.ID, "path", remote, "error", err, "rc", run.RC)
		return nil, false, nil
	}
	var packedSize int
	if _, err := fmt.Sscan(strings.TrimSpace(out.String()), &packedSize); err != nil || packedSize < 0 {
		s.Client.log().Debug("target-side artifact compression returned no size",
			"instance", s.Instance.ID, "path", remote, "output", strings.TrimSpace(out.String()))
		return nil, false, nil
	}
	if packedSize >= originalSize {
		return nil, false, nil
	}

	packedBytes, err := s.fetchExact(ctx, packed, packedSize)
	if err != nil {
		return nil, false, fmt.Errorf("fetch compressed %s from %s: %w", remote, s.Instance.ID, err)
	}
	artifact, err = decodeGzip(packedBytes, originalSize)
	if err != nil {
		return nil, false, fmt.Errorf("expand compressed %s from %s: %w", remote, s.Instance.ID, err)
	}
	return artifact, true, nil
}

func compressedFetchPath(platform, token string) string {
	if strings.EqualFold(platform, "Windows") {
		return tempDir(platform) + `\testease-fetch-` + token + ".gz"
	}
	return tempDir(platform) + "/testease-fetch-" + token + ".gz"
}

func compressCommand(platform, remote, packed string) string {
	if strings.EqualFold(platform, "Windows") {
		return fmt.Sprintf(`$source = [IO.File]::OpenRead(%s)
$destination = [IO.File]::Create(%s)
try {
  $gzip = [IO.Compression.GZipStream]::new($destination, [IO.Compression.CompressionMode]::Compress)
  try { $source.CopyTo($gzip) } finally { $gzip.Dispose() }
} finally {
  $source.Dispose()
  $destination.Dispose()
}
Write-Output ([IO.FileInfo]::new(%s).Length)`,
			powershellQuote(remote), powershellQuote(packed), powershellQuote(packed))
	}
	return fmt.Sprintf("gzip -c -- %s > %s && stat -c %%s -- %s",
		shellQuote(remote), shellQuote(packed), shellQuote(packed))
}

func (s *Session) removeRemote(ctx context.Context, path string) error {
	command := fmt.Sprintf("rm -f -- %s", shellQuote(path))
	if strings.EqualFold(s.Instance.Platform, "Windows") {
		command = fmt.Sprintf("Remove-Item -Force -ErrorAction SilentlyContinue %s", powershellQuote(path))
	}
	run, err := s.send(ctx, command, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard, Timeout: 2 * time.Minute})
	if err != nil {
		return fmt.Errorf("remove temporary compressed artifact %s on %s: %w", path, s.Instance.ID, err)
	}
	if run.RC != 0 {
		return fmt.Errorf("remove temporary compressed artifact %s on %s: exit %d", path, s.Instance.ID, run.RC)
	}
	return nil
}

func decodeGzip(packed []byte, want int) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	artifact, readErr := io.ReadAll(io.LimitReader(zr, int64(want)+1))
	closeErr := zr.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(artifact) != want {
		return nil, fmt.Errorf("expanded to %d bytes, want %d", len(artifact), want)
	}
	return artifact, nil
}

func (s *Session) remoteSize(ctx context.Context, remote string) (int, error) {
	command := fmt.Sprintf("stat -c %%s -- %s", shellQuote(remote))
	if strings.EqualFold(s.Instance.Platform, "Windows") {
		command = fmt.Sprintf("Write-Output ([IO.FileInfo]::new(%s).Length)", powershellQuote(remote))
	}
	var out bytes.Buffer
	result, err := s.send(ctx, command, target.RunOpts{Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return 0, fmt.Errorf("read size of %s on %s: %w", remote, s.Instance.ID, err)
	}
	if result.RC != 0 {
		return 0, fmt.Errorf("read size of %s on %s: exit %d", remote, s.Instance.ID, result.RC)
	}
	var size int
	if _, err := fmt.Sscan(strings.TrimSpace(out.String()), &size); err != nil || size < 0 {
		return 0, fmt.Errorf("read size of %s on %s: invalid size %q", remote, s.Instance.ID, strings.TrimSpace(out.String()))
	}
	return size, nil
}

func fetchChunkCommand(platform, remote string, offset, count int) string {
	if strings.EqualFold(platform, "Windows") {
		return fmt.Sprintf(`$f = [IO.File]::OpenRead(%s)
try {
  [void]$f.Seek(%d, [IO.SeekOrigin]::Begin)
  $b = New-Object byte[] %d
  $n = $f.Read($b, 0, $b.Length)
  if ($n -eq 0) { Write-Output '' }
  elseif ($n -eq $b.Length) { Write-Output ([Convert]::ToBase64String($b)) }
  else { Write-Output ([Convert]::ToBase64String($b[0..($n-1)])) }
} finally { $f.Dispose() }`, powershellQuote(remote), offset, count)
	}
	return fmt.Sprintf("dd if=%s bs=1 skip=%d count=%d 2>/dev/null | base64 | tr -d '\\n'",
		shellQuote(remote), offset, count)
}

// Put places a file on the instance. Small files fit in one command document;
// large media crosses a temporary S3 handoff when the caller configured one.
// The latter keeps product-specific package acquisition out of this backend:
// it moves the exact verified bytes the runner handed it and nothing else.
func (s *Session) Put(ctx context.Context, local io.Reader, remote string, mode fs.FileMode) error {
	body, err := io.ReadAll(io.LimitReader(local, InlinePutLimit+1))
	if err != nil {
		return fmt.Errorf("read what to put on %s: %w", s.Instance.ID, err)
	}
	if len(body) > InlinePutLimit {
		if s.Client.TransferStore != "" {
			return s.putViaS3(ctx, io.MultiReader(bytes.NewReader(body), local), remote, mode)
		}
		return fmt.Errorf(
			"put %s on %s: media exceeds the %d-byte inline SSM limit; configure --ssm-transfer-store s3://bucket/prefix",
			remote, s.Instance.ID, InlinePutLimit,
		)
	}
	encoded := base64.StdEncoding.EncodeToString(body)

	script := inlinePutCommand(s.Instance.Platform, remote, mode, encoded)

	result, err := s.send(ctx, script, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return err
	}
	if result.RC != 0 {
		return fmt.Errorf("put %s on %s: exit %d", remote, s.Instance.ID, result.RC)
	}
	return nil
}

func inlinePutCommand(platform, remote string, mode fs.FileMode, encoded string) string {
	if strings.EqualFold(platform, "Windows") {
		return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$dest = %s
$parent = Split-Path -Parent $dest
if ($parent -and -not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
[IO.File]::WriteAllBytes($dest, [Convert]::FromBase64String(%s))`,
			powershellQuote(remote), powershellQuote(encoded))
	}
	return fmt.Sprintf("set -e\nmkdir -p -- %s\nprintf %%s %s | base64 -d > %s\nchmod %o %s",
		shellQuote(filepathDir(remote)), shellQuote(encoded), shellQuote(remote), mode.Perm(), shellQuote(remote))
}

func (s *Session) putViaS3(ctx context.Context, local io.Reader, remote string, mode fs.FileMode) (resultErr error) {
	token, err := transferToken()
	if err != nil {
		return fmt.Errorf("name temporary SSM transfer: %w", err)
	}
	uri, err := transferURI(s.Client.TransferStore, token)
	if err != nil {
		return err
	}

	if _, err := s.Client.output(ctx, local, "s3", "cp", "-", uri, "--only-show-errors"); err != nil {
		return fmt.Errorf("stage temporary SSM transfer: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		_, cleanupErr := s.Client.Output(cleanupCtx, "s3", "rm", uri, "--only-show-errors")
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("remove temporary SSM transfer %s: %w", uri, cleanupErr)
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	out, err := s.Client.Output(ctx, "s3", "presign", uri, "--expires-in", "3600")
	if err != nil {
		return fmt.Errorf("sign temporary SSM transfer: %w", err)
	}
	url := strings.TrimSpace(string(out))
	if !strings.HasPrefix(url, "https://") {
		return errors.New("sign temporary SSM transfer: aws did not return an https URL")
	}

	command := downloadCommand(s.Instance.Platform, url, remote, mode, token)
	run, err := s.send(ctx, command, target.RunOpts{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return fmt.Errorf("download temporary media on %s: %w", s.Instance.ID, err)
	}
	if run.RC != 0 {
		return fmt.Errorf("download temporary media on %s: exit %d", s.Instance.ID, run.RC)
	}
	return nil
}

func transferToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func transferURI(store, token string) (string, error) {
	store = strings.TrimRight(strings.TrimSpace(store), "/")
	rest, ok := strings.CutPrefix(store, "s3://")
	bucket, _, _ := strings.Cut(rest, "/")
	if !ok || bucket == "" {
		return "", fmt.Errorf("SSM transfer store %q is not an s3:// bucket or prefix", store)
	}
	return store + "/testease-transfer/" + token, nil
}

// ValidTransferStore is the same validation Put applies, exposed so doctor can
// reject a bad handoff before a target is started.
func ValidTransferStore(store string) bool {
	_, err := transferURI(store, "preflight")
	return err == nil
}

func downloadCommand(platform, url, remote string, mode fs.FileMode, token string) string {
	if strings.EqualFold(platform, "Windows") {
		return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$dest = %s
$tmp = $dest + %s
$parent = Split-Path -Parent $dest
if ($parent -and -not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
(New-Object System.Net.WebClient).DownloadFile(%s, $tmp)
if (Test-Path -LiteralPath $dest) { Remove-Item -LiteralPath $dest -Force }
Move-Item -LiteralPath $tmp -Destination $dest -Force`,
			powershellQuote(remote), powershellQuote(".testease-"+token), powershellQuote(url))
	}
	tmp := remote + ".testease-" + token
	return fmt.Sprintf("set -e\nmkdir -p -- %s\ncurl -fsSL --retry 3 -o %s %s\nchmod %o %s\nmv -f -- %s %s",
		shellQuote(filepathDir(remote)), shellQuote(tmp), shellQuote(url), mode.Perm(), shellQuote(tmp), shellQuote(tmp), shellQuote(remote))
}

func filepathDir(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// Close has nothing to close: Systems Manager holds no connection between
// commands, which is why a step's whole script travels in one invocation.
func (s *Session) Close() error { return nil }
