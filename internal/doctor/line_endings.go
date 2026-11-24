package doctor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmurray2011/testease/internal/scenario"
)

// lineEndings warns about shell probes with Windows line endings. A Windows
// checkout with core.autocrlf writes them, and a Linux target reads each
// carriage return as part of the command: measured, `set -e` fails as
// "set: -" and the probe dies before it takes a reading. It is a warning, not
// a failure, because the file is what the scenario declares; the remedy is in
// how it was checked out.
func lineEndings(plan *scenario.Plan) Check {
	const name = "line-endings"
	seen := map[string]bool{}
	var crlf []string
	shell := 0
	for _, step := range plan.Steps {
		if step.Probe == "" || scenario.IsDrive(step.Probe) || seen[step.Probe] {
			continue
		}
		seen[step.Probe] = true
		body, err := os.ReadFile(filepath.Join(plan.Dir, filepath.FromSlash(step.Probe)))
		if err != nil || !isShellScript(step.Probe, body) {
			continue
		}
		shell++
		if bytes.Contains(body, []byte("\r\n")) {
			crlf = append(crlf, step.Probe)
		}
	}
	if len(crlf) > 0 {
		slices.Sort(crlf)
		return Check{
			Name: name, Status: Warn,
			Found: fmt.Sprintf("%s %s Windows line endings; a Linux target reads each carriage return as part of the command",
				strings.Join(crlf, ", "), plural(len(crlf), "has", "have")),
			Remedy: "add `*.sh text eol=lf` to the scenario repository's .gitattributes, then check the files out again",
		}
	}
	return Check{Name: name, Status: OK, Found: fmt.Sprintf("%d shell probe(s) use unix line endings", shell)}
}

// isShellScript is a probe a POSIX shell will read: one that says so with a
// shebang, or is named .sh. PowerShell reads CRLF as a line ending.
func isShellScript(probe string, body []byte) bool {
	if strings.EqualFold(filepath.Ext(probe), ".ps1") {
		return false
	}
	return bytes.HasPrefix(body, []byte("#!")) || strings.EqualFold(filepath.Ext(probe), ".sh")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
