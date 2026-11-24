package docker

import (
	"slices"
	"testing"
)

func TestADriveRunsAsTheUserRunningTestease(t *testing.T) {
	t.Parallel()

	flags, uid, gid := driveOwner(1000, 1001)
	if want := []string{"-u", "1000:1001"}; !slices.Equal(flags, want) {
		t.Errorf("flags = %q, want %q", flags, want)
	}
	if uid != 1000 || gid != 1001 {
		t.Errorf("staged files owned by %d:%d, want 1000:1001", uid, gid)
	}
}

// Windows has no uid: os.Getuid answers -1, and "-u -1:-1" is a container
// docker refuses to create. There is no runner-owned uid to match, so the
// container keeps its image's user and staged files belong to root.
func TestADriveFromAPlatformWithoutUidsKeepsTheImageUser(t *testing.T) {
	t.Parallel()

	flags, uid, gid := driveOwner(-1, -1)
	if len(flags) != 0 {
		t.Errorf("flags = %q, want none", flags)
	}
	if uid != 0 || gid != 0 {
		t.Errorf("staged files owned by %d:%d, want 0:0", uid, gid)
	}
}
