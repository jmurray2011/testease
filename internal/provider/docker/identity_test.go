package docker

import (
	"strings"
	"testing"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
)

func TestIdentityProbeUsesTheTargetsOperatingSystem(t *testing.T) {
	t.Parallel()

	linux := identityProbe("box", "linux")
	if got := strings.Join(linux, " "); !strings.Contains(got, "uname") || strings.Contains(got, "powershell") {
		t.Errorf("linux probe = %q, want uname", got)
	}
	windows := identityProbe("box", backend.Windows)
	if got := strings.Join(windows, " "); !strings.Contains(got, "powershell") || strings.Contains(got, "uname") {
		t.Errorf("windows probe = %q, want PowerShell", got)
	}
}
