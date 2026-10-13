package docker

import (
	"fmt"
	"strings"
)

// A Windows container has neither bash nor a third descriptor, so a probe
// reaches it differently: the script arrives on stdin and is written to a file,
// and the structured channel is a path in the environment rather than fd 3.
//
// That is the same bargain the one-shot backend already strikes, and probes are
// written against it either way: a probe reads TESTEASE_FACTS if it is set and
// writes fd 3 if it is not, so the same probe runs on both.
func windowsWrapper(channel string, args []string) string {
	var b strings.Builder
	b.WriteString(`$ErrorActionPreference = 'Stop'; `)
	b.WriteString(`$src = [Console]::In.ReadToEnd(); `)
	b.WriteString(`$probe = Join-Path $env:TEMP 'testease-probe.ps1'; `)
	b.WriteString(`Set-Content -LiteralPath $probe -Value $src -Encoding UTF8; `)

	// The arguments go in as a literal array rather than through PowerShell's
	// own positional parsing, which does not populate $args from a -Command
	// line reliably. Single quotes are doubled, which is the whole escape.
	b.WriteString("$a = @(")
	for i, a := range args {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("'" + strings.ReplaceAll(a, "'", "''") + "'")
	}
	b.WriteString("); ")

	b.WriteString(`$ErrorActionPreference = 'Continue'; `)
	b.WriteString(`& $probe @a; `)

	// A script that never ran an external command leaves this unset, and an
	// unset exit code is not a failure.
	b.WriteString(`if ($null -eq $LASTEXITCODE) { exit 0 } else { exit $LASTEXITCODE }`)
	return b.String()
}

// windowsChannel is where a Windows probe writes its readings. Named in the
// environment because PowerShell cannot be handed a descriptor.
func windowsChannel(nanos int64) string {
	return fmt.Sprintf(`C:\testease-fd3-%d`, nanos)
}

// windowsExistsProbe asks cmd whether a path is there.
//
// THE PATH IS ITS OWN ARGUMENT AND IS NOT QUOTED HERE. Quoting it ourselves is
// the obvious move and it is wrong: an argument's own quotes do not survive the
// round trip into a Windows command line, so cmd receives the escaped quote as
// part of the path and no path ever matches. Measured on nanoserver, against a
// file the daemon had just copied in:
//
//	if exist "C:\License.txt" (exit 0) else (exit 1)   -> 1, and the file is there
//	if exist  C:\License.txt  (exit 0) else (exit 1)   -> 0
//
// It fails closed, which is why it surfaced as staging refusing media that had
// arrived rather than as media that never did - every scenario declaring a mount
// on the Windows daemon, eleven of the twelve in the corpus.
//
// A space in the path is what the quoting was for, and passing the path as a
// single argument answers it properly: the joiner quotes that argument, so cmd
// sees one token. Unquoted-by-us is not the same as unquoted - `if exist
// C:\Program` becoming the condition and the rest the command is what happens
// when the path is pasted into a command string, and it cannot happen here.
// Measured both ways on `C:\staged file.txt`: 0 present, 1 absent.
func windowsExistsProbe(path string) []string {
	return []string{"cmd", "/c", "if", "exist", path, "(exit", "0)", "else", "(exit", "1)"}
}
