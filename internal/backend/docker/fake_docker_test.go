package docker

import (
	"testing"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

// The docker CLI is faked by the test binary itself, through fakeexec, so the
// same fakes run on every platform.
func TestMain(m *testing.M) {
	fakeexec.Main(m, fakeBehaviours)
}

// fakeDocker returns a client whose docker binary acts out the named behaviour
// from fakeBehaviours. params reach the behaviour as Call.Params.
func fakeDocker(t *testing.T, behaviour string, params map[string]string) Client {
	t.Helper()
	return Client{Bin: fakeExecutable(t, behaviour, params)}
}

// fakeExecutable is the path of a fake docker that acts out the named
// behaviour when executed.
func fakeExecutable(t *testing.T, behaviour string, params map[string]string) string {
	t.Helper()
	return fakeexec.Program(t, "docker", behaviour, params)
}
