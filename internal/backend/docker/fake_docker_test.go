package docker

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The docker CLI is faked by the test binary itself. fakeDocker places a copy
// of it named docker beside a fake.json naming the behaviour to act out, and
// TestMain hands an invocation under that name to the behaviour instead of
// running tests. A shell script would do the same on unix and nothing at all on
// Windows, where an extensionless script cannot be executed.
//
// The fakes live outside t.TempDir: Windows refuses to delete an executable
// while any process runs it, and parallel tests share one copy of the binary,
// so the directory is removed once every test is done.
func TestMain(m *testing.M) {
	if call, ok := fakeInvocation(); ok {
		os.Exit(call.run())
	}
	code := m.Run()
	if fakeRoot != "" {
		removeFakes(fakeRoot)
	}
	os.Exit(code)
}

var (
	fakeRoot   string
	fakeBinary string
	fakeOnce   sync.Once
	fakeErr    error
	fakeSeq    atomic.Int64
)

// sharedFake copies this test binary once per package run. Every fake is a
// hard link to that copy, so a scanner that inspects new executables, as
// Windows Defender does, inspects one file rather than one per test.
func sharedFake() (string, error) {
	fakeOnce.Do(func() {
		self, err := os.Executable()
		if err != nil {
			fakeErr = err
			return
		}
		fakeRoot, err = os.MkdirTemp("", "testease-fake-docker-")
		if err != nil {
			fakeErr = err
			return
		}
		fakeBinary = filepath.Join(fakeRoot, "binary"+exeSuffix())
		fakeErr = copyFile(self, fakeBinary)
	})
	return fakeBinary, fakeErr
}

// removeFakes retries briefly: a fake a test killed may not have released its
// executable the instant the test returned.
func removeFakes(root string) {
	for range 50 {
		if os.RemoveAll(root) == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// fakeCall is one invocation of the fake docker: its arguments as docker would
// receive them, the parameters the test wrote down, and its standard streams.
type fakeCall struct {
	args   []string
	params map[string]string
	dir    string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// arg is the nth argument counting from 1, as the shell's $1 does, or "".
func (c fakeCall) arg(n int) string {
	if n < 1 || n > len(c.args) {
		return ""
	}
	return c.args[n-1]
}

// words is the arguments joined and split again, the way a shell pattern on
// " $* " sees them.
func (c fakeCall) words() []string {
	return strings.Fields(strings.Join(c.args, " "))
}

// joined is " $* ": the arguments joined by spaces, padded so a pattern can
// match a whole word at either end.
func (c fakeCall) joined() string {
	return " " + strings.Join(c.args, " ") + " "
}

// inContainer maps a path inside the faked container onto the fake's own
// directory, so a container path such as /tmp/... never touches the runner's.
func (c fakeCall) inContainer(path string) string {
	return filepath.Join(c.dir, "container", filepath.FromSlash(strings.ReplaceAll(path, ":", "")))
}

func (c fakeCall) run() int {
	if c.params["behaviour"] == "warm" {
		return 0
	}
	behave, ok := fakeBehaviours[c.params["behaviour"]]
	if !ok {
		_, _ = fmt.Fprintf(c.stderr, "fake docker: no behaviour %q\n", c.params["behaviour"])
		return 99
	}
	return behave(c)
}

type fakeBehaviour func(fakeCall) int

const fakeName = "docker"

// fakeWarmEnv makes a fake exit at once without acting out its behaviour. The
// first start of a new executable on Windows waits on a malware scan, measured
// at over 100ms against 2ms after; an installed docker CLI has long been
// scanned, so each fake is started once before its test times anything.
const fakeWarmEnv = "TESTEASE_FAKE_DOCKER_WARM"

func fakeInvocation() (fakeCall, bool) {
	exe, err := os.Executable()
	if err != nil {
		return fakeCall{}, false
	}
	if strings.TrimSuffix(filepath.Base(exe), ".exe") != fakeName {
		return fakeCall{}, false
	}
	if os.Getenv(fakeWarmEnv) != "" {
		return fakeCall{params: map[string]string{"behaviour": "warm"}}, true
	}
	dir := filepath.Dir(exe)
	raw, err := os.ReadFile(filepath.Join(dir, "fake.json"))
	if err != nil {
		return fakeCall{}, false
	}
	var params map[string]string
	if err := json.Unmarshal(raw, &params); err != nil {
		return fakeCall{}, false
	}
	return fakeCall{
		args: os.Args[1:], params: params, dir: dir,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
	}, true
}

// fakeDocker returns a client whose docker binary acts out the named behaviour
// from fakeBehaviours. params reach the behaviour as fakeCall.params.
func fakeDocker(t *testing.T, behaviour string, params map[string]string) Client {
	t.Helper()
	return Client{Bin: fakeExecutable(t, behaviour, params)}
}

// fakeExecutable is the path of a copy of this test binary that acts out the
// named behaviour when executed.
func fakeExecutable(t *testing.T, behaviour string, params map[string]string) string {
	t.Helper()

	if _, ok := fakeBehaviours[behaviour]; !ok {
		t.Fatalf("no fake behaviour %q", behaviour)
	}
	shared, err := sharedFake()
	if err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}
	dir := filepath.Join(fakeRoot, fmt.Sprintf("fake-%d", fakeSeq.Add(1)))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fakeName+exeSuffix())
	if err := os.Link(shared, path); err != nil {
		if err := copyFile(shared, path); err != nil {
			t.Fatalf("place the fake docker: %v", err)
		}
	}

	all := map[string]string{"behaviour": behaviour}
	for k, v := range params {
		all[k] = v
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.json"), raw, 0o600); err != nil {
		t.Fatalf("write fake docker parameters: %v", err)
	}
	warm := exec.Command(path)
	warm.Env = append(os.Environ(), fakeWarmEnv+"=1")
	if out, err := warm.CombinedOutput(); err != nil {
		t.Fatalf("start the fake docker: %v: %s", err, out)
	}
	return path
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
