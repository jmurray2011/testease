package evidencepackage

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/campaign"
)

type invoker struct{}

func (invoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	for _, arg := range args {
		if arg == "run" {
			_, _ = io.WriteString(stdout, `{"run_id":"one"}`)
		}
		if arg == "judge" {
			_, _ = io.WriteString(stdout, `{"verdict":"pass"}`)
		}
	}
	return 0
}

func TestBuildPackagesCampaignAndRunWithHashesAndNoPersonalPath(t *testing.T) {
	t.Parallel()
	definitionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(definitionDir, "campaign.yaml"), []byte("schema: 1\nname: demo\nscenario: example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := campaign.Load(definitionDir)
	if err != nil {
		t.Fatal(err)
	}
	runsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runsDir, "one"), 0o755); err != nil {
		t.Fatal(err)
	}
	events := `{"requested_path":"/mnt/c/Users/alice/private/input.rpm","host":{"hostname":"alice-laptop","pid":41922,"user":"alice"},"params":{"address":"192.168.10.14"},"resolved":{"ref":"ssh:192.168.10.14","daemon":"alice-docker-host","docker_context":"home-lab"}}` + "\n"
	if err := os.WriteFile(filepath.Join(runsDir, "one", "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	campaignDir := filepath.Join(runsDir, "campaigns", "demo-fixed")
	if _, err := campaign.Run(context.Background(), campaign.Options{Definition: definition, CampaignDir: campaignDir, RunsDir: runsDir, ScenarioRoot: t.TempDir(), Invoker: invoker{}}); err != nil {
		t.Fatal(err)
	}
	certificateDigest := "f0:1d:0d:3c:cf:fa:18:aa:48:f0:a7:29:d5:b7:c1:49"
	if err := os.WriteFile(filepath.Join(campaignDir, "host-details.txt"), []byte("VBOX=/mnt/c/Program Files/VirtualBox\nRUNNER=alice\nTARGET=192.168.10.14\nDOCKER=alice-docker-host\nCONTEXT=home-lab\nMAC=08:00:27:b1:7b:fc\nCERT="+certificateDigest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "evidence.zip")
	result, err := Build(Options{CampaignDir: campaignDir, RunsDir: runsDir, Output: output, Preset: Engineering, Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files < 3 || !strings.HasPrefix(result.SHA256, "sha256:") {
		t.Fatalf("result = %#v", result)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	var all strings.Builder
	var hasManifest bool
	for _, file := range archive.File {
		hasManifest = hasManifest || file.Name == "manifest.json"
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(reader)
		_ = reader.Close()
		all.Write(body)
	}
	for _, private := range []string{"/mnt/c/", "/mnt/c/Users/alice", "alice-laptop", "alice", "192.168.10.14", "alice-docker-host", "home-lab", "08:00:27:b1:7b:fc"} {
		if strings.Contains(all.String(), private) {
			t.Fatalf("package retained private runner detail %q", private)
		}
	}
	for _, redaction := range []string{"<host-drive>", "<runner-host>", "<runner-user>", "<docker-daemon>", "<docker-context>", "<private-address>", "<private-mac>"} {
		if !strings.Contains(all.String(), redaction) {
			t.Fatalf("package omitted expected redaction %q", redaction)
		}
	}
	if !strings.Contains(all.String(), certificateDigest) {
		t.Fatal("package mistook a certificate fingerprint for a MAC address")
	}
	if !hasManifest {
		t.Fatal("archive has no manifest.json")
	}
	if !strings.Contains(all.String(), `"sanitizer": "engineering@3"`) {
		t.Fatal("archive does not identify its sanitization policy")
	}
	second, err := Build(Options{CampaignDir: campaignDir, RunsDir: runsDir, Output: filepath.Join(t.TempDir(), "evidence.zip"), Preset: Engineering, Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != result.SHA256 {
		t.Fatalf("same evidence packaged twice as %s and %s", result.SHA256, second.SHA256)
	}
}

func TestBuildRefusesSecretShapedLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writer, err := campaign.OpenRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Emit("campaign-started", map[string]any{"id": "demo"})
	_ = writer.Emit("campaign-finished", campaign.Result{ID: "demo", Status: campaign.StatusPassed})
	_ = writer.Close()
	if err := os.WriteFile(filepath.Join(dir, "bad.log"), []byte("api_key=do-not-package\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Build(Options{CampaignDir: dir, RunsDir: t.TempDir(), Output: filepath.Join(t.TempDir(), "x.zip")})
	if err == nil || !strings.Contains(err.Error(), "secret-shaped") {
		t.Fatalf("error = %v", err)
	}
}
