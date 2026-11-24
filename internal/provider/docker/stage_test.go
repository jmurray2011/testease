package docker

import (
	"context"
	"strings"
	"testing"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/target"
	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

func TestStageRefusesDifferentBytesAtAContentAddressedMount(t *testing.T) {
	t.Parallel()

	bin := fakeexec.Program(t, "docker", "stage-mismatch", nil)
	c := &container{
		name: "box",
		session: &backend.Session{
			Client: backend.Client{Bin: bin}, Container: "box",
		},
	}
	err := c.Stage(context.Background(), []target.Media{{
		Source: "/verified/cache", Dest: "/input", ReadOnly: true,
		SHA256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}})
	if err == nil {
		t.Fatal("Stage accepted bytes different from the verified input")
	}
	if !strings.Contains(err.Error(), "expected sha256:bbbb") {
		t.Fatalf("Stage error = %q, want the digest mismatch", err)
	}
}

func TestStageExplainsAnUnreadableContentAddressedMount(t *testing.T) {
	t.Parallel()

	bin := fakeexec.Program(t, "docker", "stage-unreadable", nil)
	c := &container{
		name: "box",
		session: &backend.Session{
			Client: backend.Client{Bin: bin}, Container: "box",
		},
	}
	err := c.Stage(context.Background(), []target.Media{
		{Source: "/local/profile.siglae", Dest: "/input", ReadOnly: true, SHA256: "sha256:" + strings.Repeat("a", 64)},
	})
	if err == nil {
		t.Fatal("Stage accepted an unreadable content-addressed mount")
	}
	for _, want := range []string{"remote Docker daemon", "/local/profile.siglae"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Stage error = %q, want it to mention %q", err, want)
		}
	}
}
