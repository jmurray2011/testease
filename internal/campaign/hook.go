package campaign

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// hookCommand is how the hook at path is run on this machine.
func hookCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	argv, err := hookArgv(runtime.GOOS, path, exec.LookPath)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...), nil //nolint:gosec // the hook is the campaign's own, snapshotted and hashed
}

// hookArgv is the command line that runs the hook at path on goos.
//
// A unix hook runs as itself: its executable bit and shebang say how. Windows
// has neither and runs a file by its extension, so a hook says which
// interpreter it needs that way: PowerShell for .ps1, cmd for .cmd and .bat,
// itself for .exe and .com, and sh for .sh - but only an sh on PATH, because
// a shell found anywhere else is an interpreter the campaign did not name.
func hookArgv(goos, path string, lookPath func(string) (string, error)) ([]string, error) {
	if goos != "windows" {
		return []string{path}, nil
	}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".ps1":
		return []string{"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}, nil
	case ".cmd", ".bat":
		return []string{"cmd", "/c", path}, nil
	case ".exe", ".com":
		return []string{path}, nil
	case ".sh":
		sh, err := lookPath("sh")
		if err != nil {
			return nil, fmt.Errorf("hook %s is a shell script and there is no sh on PATH to run it; put one there (Git for Windows ships sh.exe) or write the hook as .ps1 or .cmd", filepath.Base(path))
		}
		return []string{sh, path}, nil
	default:
		return nil, fmt.Errorf("hook %s has no extension Windows can run (.ps1, .cmd, .bat, .exe, .com, or .sh with sh on PATH)", filepath.Base(path))
	}
}
