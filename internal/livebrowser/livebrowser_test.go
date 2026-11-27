package livebrowser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workspace(t *testing.T, runs string, owner Owner) string {
	t.Helper()
	ws, err := os.MkdirTemp(runs, ".testease-drive-")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFindIsTheNewestFrameOfThatStep(t *testing.T) {
	runs := t.TempDir()
	rec := filepath.Join(runs, "hello-20261127T000000Z")
	other := workspace(t, runs, Owner{Record: rec, Step: 1})
	dir := workspace(t, runs, Owner{Record: rec, Step: 2})
	for _, d := range []string{other, dir} {
		if err := os.WriteFile(filepath.Join(d, "frame-1.jpg"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newer := filepath.Join(dir, "frame-2.jpg")
	if err := os.WriteFile(newer, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(newer, later, later)
	if err := os.WriteFile(filepath.Join(dir, "frame-2.json"), []byte(`{"url":"https://app/login"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	f, ok := Find(runs, rec, 2)
	if !ok || f.Path != newer || f.URL != "https://app/login" {
		t.Errorf("Find = %+v, %v; want %s at https://app/login", f, ok, newer)
	}
}

func TestFindIsNothingForAnotherRecordOrNoFrames(t *testing.T) {
	runs := t.TempDir()
	rec := filepath.Join(runs, "a")
	dir := workspace(t, runs, Owner{Record: filepath.Join(runs, "b"), Step: 1})
	if err := os.WriteFile(filepath.Join(dir, "frame-1.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, ok := Find(runs, rec, 1); ok {
		t.Errorf("Find = %+v for another record's frames", f)
	}
	workspace(t, runs, Owner{Record: rec, Step: 1})
	if f, ok := Find(runs, rec, 1); ok {
		t.Errorf("Find = %+v before the suite saved a frame", f)
	}
}

func TestMirrorBringsTheNewestFrameHereForTheViewerToFind(t *testing.T) {
	runs := t.TempDir()
	rec := filepath.Join(runs, "saml-20261127T000000Z")
	root, frames, err := Prepare(runs, Owner{Record: rec, Step: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fetch := func(_ context.Context, name string) ([]byte, error) {
		switch name {
		case Latest:
			return []byte("jpeg"), nil
		case LatestAbout:
			return []byte(`{"url":"https://idp/login"}`), nil
		}
		return nil, os.ErrNotExist
	}
	done := make(chan struct{})
	go func() { defer close(done); Mirror(ctx, fetch, frames) }()

	deadline := time.Now().Add(5 * time.Second)
	var f Frame
	var ok bool
	for time.Now().Before(deadline) && !ok {
		time.Sleep(50 * time.Millisecond)
		f, ok = Find(runs, rec, 4)
	}
	cancel()
	<-done
	if !ok || f.URL != "https://idp/login" || filepath.Dir(f.Path) != frames {
		t.Errorf("Find = %+v, %v; want the mirrored frame in %s", f, ok, frames)
	}
	if !strings.HasPrefix(filepath.Base(root), ".testease-live-") {
		t.Errorf("mirror root = %s", root)
	}
}
