// Package fakeexec fakes the programs testease runs - the docker CLI, a judge,
// a campaign hook - with the test binary itself, so that the same fake runs on
// every platform. Only tests import it.
//
// A test package hands its TestMain to Main with the behaviours it defines.
// Program places a copy of the test binary beside a fake.json naming the
// behaviour to act out, and Main hands an invocation of that copy to the
// behaviour instead of running tests. A shell script would do the same on unix
// and nothing at all on Windows, where an extensionless script cannot be
// executed.
//
// The fakes live outside t.TempDir: Windows refuses to delete an executable
// while any process runs it, and parallel tests share one copy of the binary,
// so the directory is removed once every test is done.
package fakeexec

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

// Call is one invocation of a fake: its arguments as the real program would
// receive them, the parameters the test wrote down, and its standard streams.
type Call struct {
	Args   []string
	Params map[string]string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Arg is the nth argument counting from 1, as the shell's $1 does, or "".
func (c Call) Arg(n int) string {
	if n < 1 || n > len(c.Args) {
		return ""
	}
	return c.Args[n-1]
}

// Words is the arguments joined and split again, the way a shell pattern on
// " $* " sees them.
func (c Call) Words() []string {
	return strings.Fields(strings.Join(c.Args, " "))
}

// Joined is " $* ": the arguments joined by spaces, padded so a pattern can
// match a whole word at either end.
func (c Call) Joined() string {
	return " " + strings.Join(c.Args, " ") + " "
}

// InContainer maps a path inside a faked container onto the fake's own
// directory, so a container path such as /tmp/... never touches the runner's.
func (c Call) InContainer(path string) string {
	return filepath.Join(c.Dir, "container", filepath.FromSlash(strings.ReplaceAll(path, ":", "")))
}

// Behaviour acts out one fake and returns its exit code.
type Behaviour func(Call) int

const (
	paramsFile   = "fake.json"
	behaviourKey = "behaviour"

	// warmEnv makes a fake exit at once without acting out its behaviour. The
	// first start of a new executable on Windows waits on a malware scan,
	// measured at over 100ms against 2ms after; an installed program has long
	// been scanned, so each fake is started once before its test times
	// anything.
	warmEnv = "TESTEASE_FAKEEXEC_WARM"
)

var (
	behaviours map[string]Behaviour
	root       string
	shared     string
	once       sync.Once
	sharedErr  error
	seq        atomic.Int64
)

// Main runs the package's tests, unless this process is one of its fakes, in
// which case it acts out the fake's behaviour and exits with its code.
func Main(m *testing.M, defined map[string]Behaviour) {
	behaviours = defined
	if call, ok := invocation(); ok {
		os.Exit(call.run())
	}
	code := m.Run()
	if root != "" {
		remove(root)
	}
	os.Exit(code)
}

func (c Call) run() int {
	name := c.Params[behaviourKey]
	if name == "" {
		return 0
	}
	behave, ok := behaviours[name]
	if !ok {
		_, _ = fmt.Fprintf(c.Stderr, "fakeexec: no behaviour %q\n", name)
		return 99
	}
	return behave(c)
}

func invocation() (Call, bool) {
	exe, err := os.Executable()
	if err != nil {
		return Call{}, false
	}
	dir := filepath.Dir(exe)
	raw, err := os.ReadFile(filepath.Join(dir, paramsFile))
	if err != nil {
		return Call{}, false
	}
	if os.Getenv(warmEnv) != "" {
		return Call{Params: map[string]string{}}, true
	}
	var params map[string]string
	if err := json.Unmarshal(raw, &params); err != nil {
		return Call{}, false
	}
	return Call{
		Args: os.Args[1:], Params: params, Dir: dir,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}, true
}

// Program is the path of a fake named name that acts out behaviour when
// executed. On Windows the name is given .exe unless it carries an extension.
// params reach the behaviour as Call.Params.
func Program(t testing.TB, name, behaviour string, params map[string]string) string {
	t.Helper()

	if _, ok := behaviours[behaviour]; !ok {
		t.Fatalf("no fake behaviour %q: is TestMain calling fakeexec.Main?", behaviour)
	}
	binary, err := sharedBinary()
	if err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}
	dir := filepath.Join(root, fmt.Sprintf("fake-%d", seq.Add(1)))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.Link(binary, path); err != nil {
		if err := copyFile(binary, path); err != nil {
			t.Fatalf("place the fake %s: %v", name, err)
		}
	}

	all := map[string]string{behaviourKey: behaviour}
	for k, v := range params {
		all[k] = v
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, paramsFile), raw, 0o600); err != nil {
		t.Fatalf("write fake %s parameters: %v", name, err)
	}
	warm := exec.Command(path)
	warm.Env = append(os.Environ(), warmEnv+"=1")
	if out, err := warm.CombinedOutput(); err != nil {
		t.Fatalf("start the fake %s: %v: %s", name, err, out)
	}
	return path
}

// sharedBinary copies this test binary once per package run. Every fake is a
// hard link to that copy, so a scanner that inspects new executables, as
// Windows Defender does, inspects one file rather than one per test.
func sharedBinary() (string, error) {
	once.Do(func() {
		self, err := os.Executable()
		if err != nil {
			sharedErr = err
			return
		}
		root, err = os.MkdirTemp("", "testease-fakeexec-")
		if err != nil {
			sharedErr = err
			return
		}
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		shared = filepath.Join(root, "binary"+suffix)
		sharedErr = copyFile(self, shared)
	})
	return shared, sharedErr
}

// remove retries briefly: a fake a test killed may not have released its
// executable the instant the test returned.
func remove(dir string) {
	for range 50 {
		if os.RemoveAll(dir) == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
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
