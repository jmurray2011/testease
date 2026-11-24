package hyperv

import (
	"testing"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

// The docker CLI is faked by the test binary itself, through fakeexec, so the
// same fakes run on every platform.
func TestMain(m *testing.M) {
	fakeexec.Main(m, map[string]fakeexec.Behaviour{
		// true answers every docker command with success and nothing else.
		"true": func(fakeexec.Call) int { return 0 },
	})
}
