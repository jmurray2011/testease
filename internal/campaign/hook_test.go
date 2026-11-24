package campaign

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func noShell(string) (string, error) { return "", exec.ErrNotFound }

func shellAt(path string) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == "sh" {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

// A unix hook is run as itself: its executable bit and shebang say how.
func TestAUnixHookRunsAsItself(t *testing.T) {
	t.Parallel()

	got, err := hookArgv("linux", "/campaigns/demo/prepare.sh", noShell)
	if err != nil || !slices.Equal(got, []string{"/campaigns/demo/prepare.sh"}) {
		t.Errorf("argv = %q, %v", got, err)
	}
}

// Windows runs a file by what its extension says it is, so a hook names its
// interpreter that way.
func TestAWindowsHookRunsByItsExtension(t *testing.T) {
	t.Parallel()

	dir := `C:\campaigns\demo`
	for _, tc := range []struct {
		hook string
		want []string
	}{
		{"prepare.ps1", []string{"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", dir + `\prepare.ps1`}},
		{"prepare.PS1", []string{"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", dir + `\prepare.PS1`}},
		{"prepare.cmd", []string{"cmd", "/c", dir + `\prepare.cmd`}},
		{"prepare.bat", []string{"cmd", "/c", dir + `\prepare.bat`}},
		{"prepare.exe", []string{dir + `\prepare.exe`}},
		{"prepare.sh", []string{`C:\tools\sh.exe`, dir + `\prepare.sh`}},
	} {
		got, err := hookArgv("windows", dir+`\`+tc.hook, shellAt(`C:\tools\sh.exe`))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: argv = %q, %v; want %q", tc.hook, got, err, tc.want)
		}
	}
}

// A shell hook on Windows needs a shell. Without one on PATH it is refused
// before anything runs, saying what is missing, rather than failing as an
// unexplained hook exit code once the campaign has started.
func TestAWindowsShellHookWithoutAShellIsRefused(t *testing.T) {
	t.Parallel()

	_, err := hookArgv("windows", `C:\campaigns\demo\prepare.sh`, noShell)
	if err == nil {
		t.Fatal("a shell hook was accepted with no shell to run it")
	}
	for _, want := range []string{"prepare.sh", "sh", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestAWindowsHookWithNoKnownExtensionIsRefused(t *testing.T) {
	t.Parallel()

	for _, hook := range []string{`C:\campaigns\demo\prepare`, `C:\campaigns\demo\prepare.py`} {
		if _, err := hookArgv("windows", hook, shellAt(`C:\tools\sh.exe`)); err == nil {
			t.Errorf("%s was accepted, but Windows cannot tell how to run it", hook)
		}
	}
}

// The real thing: a batch hook runs here on Windows and its exit code is the
// hook's, and a non-batch file here on unix runs as itself.
func TestHookCommandRunsAHookOnThisPlatform(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	hook := filepath.Join(dir, hookName("prepare"))
	writeHook(t, hook, hookExitsSeven)
	cmd, err := hookCommand(t.Context(), hook)
	if err != nil {
		t.Fatal(err)
	}
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Errorf("hook ran with %v, want its own exit code 7", err)
	}
}

// hookName is the hook file a test writes: a shell script on unix, a batch
// file on Windows, where a hook runs by its extension and sh is rarely on PATH.
func hookName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".cmd"
	}
	return base + ".sh"
}

// hookBody is one hook in both of the forms hookName chooses between.
type hookBody struct{ sh, cmd string }

var (
	hookExitsZero  = hookBody{"#!/bin/sh\nexit 0\n", "@exit /b 0\r\n"}
	hookExitsSeven = hookBody{"#!/bin/sh\nexit 7\n", "@exit /b 7\r\n"}
	// hookWritesReceipt names a generated input bundle in its receipt.
	hookWritesReceipt = hookBody{
		"#!/bin/sh\nprintf '{\"schema\":1,\"input_bundle\":\"/staged/inputs.yaml\"}\\n' >\"$TESTEASE_HOOK_RECEIPT\"\n",
		"@echo {\"schema\":1,\"input_bundle\":\"/staged/inputs.yaml\"}> \"%TESTEASE_HOOK_RECEIPT%\"\r\n",
	}
	// hookWritesReceiptFromRoot does the same after leaving the directory it
	// was started in, so only an absolute receipt path reaches it.
	hookWritesReceiptFromRoot = hookBody{
		"#!/bin/sh\ncd /\nprintf '{\"schema\":1,\"input_bundle\":\"/staged/inputs.yaml\"}\\n' >\"$TESTEASE_HOOK_RECEIPT\"\n",
		"@cd /d \\\r\n@echo {\"schema\":1,\"input_bundle\":\"/staged/inputs.yaml\"}> \"%TESTEASE_HOOK_RECEIPT%\"\r\n",
	}
)

func writeHook(t *testing.T, path string, body hookBody) {
	t.Helper()
	text := body.sh
	if runtime.GOOS == "windows" {
		text = body.cmd
	}
	if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
}
