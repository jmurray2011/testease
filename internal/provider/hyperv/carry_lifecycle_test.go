package hyperv

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/target"
)

func TestCarriedMediaIsConsumedByTheNextOneShotExecution(t *testing.T) {
	t.Parallel()

	p := New(Options{
		Client:      backend.Client{Bin: "/bin/true"},
		ScratchRoot: filepath.Join(t.TempDir(), "runtime", "carry-run"),
		RunID:       "carry-run",
	})
	c := &cell{
		provider: p,
		spec: target.Spec{
			Host:  "destination",
			Image: "windows:test",
			Media: []target.Media{{Source: "declared.ps1", Dest: "/declared.ps1"}},
		},
	}
	if _, err := c.ReceiveCarry(context.Background(), bytes.NewBufferString("one execution only"), "/fixture.dump"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.RunOnce(context.Background(), []byte("true"), nil, target.RunOpts{
		Stdout: io.Discard, Stderr: io.Discard, Structured: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}

	staged := c.stagedMedia()
	if len(staged) != 1 || staged[0].Remote != "/declared.ps1" {
		t.Fatalf("media after the next execution = %+v, want only declared media", staged)
	}
}
