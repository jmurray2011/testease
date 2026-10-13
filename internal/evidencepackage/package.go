// Package evidencepackage builds a portable, hashed archive from a completed
// campaign and the immutable records it cites.
package evidencepackage

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/campaign"
	"github.com/jmurray2011/testease/internal/judge"
)

const (
	Engineering            = "engineering"
	engineeringSanitizerV3 = "engineering@3"
)

type Options struct {
	CampaignDir string
	RunsDir     string
	Output      string
	Preset      string
	Now         func() time.Time
}

type Result struct {
	CampaignID string `json:"campaign_id"`
	Preset     string `json:"preset"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Files      int    `json:"files"`
}

type Manifest struct {
	Schema    int            `json:"schema"`
	Campaign  string         `json:"campaign"`
	Preset    string         `json:"preset"`
	Sanitizer string         `json:"sanitizer"`
	CreatedAt time.Time      `json:"created_at"`
	Files     []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

type item struct {
	name string
	body []byte
}

var (
	userPath         = regexp.MustCompile(`(?i)(/mnt/[a-z]/Users/[^/\s]+|/home/[^/\s]+|[a-z]:\\Users\\[^\\\s]+)`)
	mountedDrive     = regexp.MustCompile(`(?i)/mnt/[a-z]`)
	ipv4Address      = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	macAssignment    = regexp.MustCompile(`(?im)^([ \t]*(?:mac|macaddress[0-9]*)[ \t]*[:=][ \t]*)(?:\$\{[^}\r\n]*:-)?["']?[0-9a-f:-]{12,17}["']?\}?`)
	assignmentSecret = regexp.MustCompile(`(?i)\b(password|passwd|api[_-]?key|access[_-]?token|client[_-]?secret|private[_-]?key)\s*[:=]\s*[^\s,;]+`)
)

func Build(options Options) (Result, error) {
	status, err := campaign.ReadStatus(options.CampaignDir)
	if err != nil {
		return Result{}, err
	}
	if status.Status == campaign.StatusRunning {
		return Result{}, fmt.Errorf("campaign %s is still running", status.ID)
	}
	preset := options.Preset
	if preset == "" {
		preset = Engineering
	}
	if preset != Engineering {
		return Result{}, fmt.Errorf("unknown evidence preset %q; this testease has %s", preset, Engineering)
	}
	output := options.Output
	if output == "" {
		output = filepath.Join(options.RunsDir, "evidence", status.ID+"-"+preset+".zip")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return Result{}, fmt.Errorf("make room for evidence package: %w", err)
	}

	var items []item
	if err := appendTree(&items, options.CampaignDir, "campaign"); err != nil {
		return Result{}, err
	}
	for _, runID := range status.Runs {
		if filepath.Base(runID) != runID {
			return Result{}, fmt.Errorf("campaign record contains invalid run id %q", runID)
		}
		if err := appendTree(&items, filepath.Join(options.RunsDir, runID), filepath.ToSlash(filepath.Join("runs", runID))); err != nil {
			return Result{}, err
		}
	}
	comparisonIDs, err := comparisonIDs(options.CampaignDir)
	if err != nil {
		return Result{}, err
	}
	for _, id := range comparisonIDs {
		if err := appendTree(&items, filepath.Join(options.RunsDir, judge.ComparisonsDir, id), filepath.ToSlash(filepath.Join("comparisons", id))); err != nil {
			return Result{}, err
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	identities := runnerIdentities(items)
	for i := range items {
		items[i].body = sanitize(items[i].name, items[i].body, identities)
		if assignmentSecret.Match(items[i].body) {
			return Result{}, fmt.Errorf("refusing to package %s: it contains a secret-shaped assignment", items[i].name)
		}
	}

	var createdAt time.Time
	if options.Now != nil {
		createdAt = options.Now()
	} else {
		finished, err := campaignFinishedAt(options.CampaignDir)
		if err != nil {
			return Result{}, err
		}
		createdAt = finished
	}
	manifest := Manifest{Schema: 1, Campaign: status.ID, Preset: preset, Sanitizer: engineeringSanitizerV3, CreatedAt: createdAt.UTC().Truncate(time.Second)}
	for _, item := range items {
		sum := sha256.Sum256(item.body)
		manifest.Files = append(manifest.Files, ManifestFile{Path: item.name, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Size: len(item.body)})
	}
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, err
	}
	manifestBody = append(manifestBody, '\n')
	items = append(items, item{name: "manifest.json", body: manifestBody})

	if err := writeZip(output, items); err != nil {
		return Result{}, err
	}
	body, err := os.ReadFile(output)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(body)
	return Result{CampaignID: status.ID, Preset: preset, Path: output, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Files: len(items)}, nil
}

func appendTree(items *[]item, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read evidence input: %w", err)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular evidence input %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "record.lock" || strings.HasSuffix(rel, ".zip") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		*items = append(*items, item{name: filepath.ToSlash(filepath.Join(prefix, rel)), body: body})
		return nil
	})
}

func comparisonIDs(dir string) ([]string, error) {
	events, err := campaign.ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, event := range events {
		if event.Kind != "activity-finished" {
			continue
		}
		var value struct {
			Activity     string `json:"activity"`
			ComparisonID string `json:"comparison_id"`
		}
		if err := json.Unmarshal(event.Data, &value); err != nil {
			return nil, err
		}
		if value.Activity == "reproduce" && value.ComparisonID != "" && !slices.Contains(ids, value.ComparisonID) {
			ids = append(ids, value.ComparisonID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func sanitize(name string, body []byte, identities map[string]string) []byte {
	if !isText(body) {
		return body
	}
	if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl") {
		body = sanitizeJSONLines(body)
	}
	body = userPath.ReplaceAll(body, []byte("<user-path>"))
	body = mountedDrive.ReplaceAll(body, []byte("<host-drive>"))
	body = ipv4Address.ReplaceAllFunc(body, redactPrivateIPv4)
	body = macAssignment.ReplaceAll(body, []byte("${1}<private-mac>"))
	keys := make([]string, 0, len(identities))
	for literal := range identities {
		keys = append(keys, literal)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	for _, literal := range keys {
		body = bytes.ReplaceAll(body, []byte(literal), []byte(identities[literal]))
	}
	return body
}

func redactPrivateIPv4(candidate []byte) []byte {
	ip := net.ParseIP(string(candidate))
	if ip != nil && ip.IsPrivate() {
		return []byte("<private-address>")
	}
	return candidate
}

func runnerIdentities(items []item) map[string]string {
	identities := make(map[string]string)
	for _, item := range items {
		if !isText(item.body) || (!strings.HasSuffix(item.name, ".json") && !strings.HasSuffix(item.name, ".jsonl")) {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(item.body))
		for scanner.Scan() {
			var value any
			if json.Unmarshal(scanner.Bytes(), &value) == nil {
				collectRunnerIdentities(value, identities)
			}
		}
	}
	return identities
}

func collectRunnerIdentities(value any, identities map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, replacement := range map[string]string{
			"daemon":         "<docker-daemon>",
			"docker_context": "<docker-context>",
		} {
			if literal, ok := typed[key].(string); ok && literal != "" {
				identities[literal] = replacement
			}
		}
		if host, ok := typed["host"].(map[string]any); ok {
			for key, replacement := range map[string]string{"hostname": "<runner-host>", "user": "<runner-user>"} {
				if literal, ok := host[key].(string); ok && literal != "" {
					identities[literal] = replacement
				}
			}
		}
		for _, child := range typed {
			collectRunnerIdentities(child, identities)
		}
	case []any:
		for _, child := range typed {
			collectRunnerIdentities(child, identities)
		}
	}
}

func sanitizeJSONLines(body []byte) []byte {
	var output bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var value any
		if json.Unmarshal(scanner.Bytes(), &value) != nil {
			output.Write(scanner.Bytes())
			output.WriteByte('\n')
			continue
		}
		redactJSON(value)
		line, err := marshalJSON(value)
		if err != nil {
			output.Write(scanner.Bytes())
		} else {
			output.Write(line)
		}
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func marshalJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func redactJSON(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			switch lower {
			case "password", "passwd", "api_key", "access_token", "client_secret", "private_key":
				typed[key] = "<redacted>"
			case "requested_path", "input_bundle":
				if text, ok := child.(string); ok {
					typed[key] = filepath.Base(text)
				}
			case "daemon":
				if _, ok := child.(string); ok {
					typed[key] = "<docker-daemon>"
				}
			case "docker_context":
				if _, ok := child.(string); ok {
					typed[key] = "<docker-context>"
				}
			case "host":
				if host, ok := child.(map[string]any); ok {
					if _, exists := host["hostname"]; exists {
						host["hostname"] = "<runner-host>"
					}
					if _, exists := host["user"]; exists {
						host["user"] = "<runner-user>"
					}
					if _, exists := host["pid"]; exists {
						host["pid"] = "<redacted>"
					}
					redactJSON(host)
				}
			default:
				redactJSON(child)
			}
		}
	case []any:
		for _, child := range typed {
			redactJSON(child)
		}
	}
}

func isText(body []byte) bool {
	return !bytes.Contains(body[:min(len(body), 8000)], []byte{0})
}

func writeZip(path string, items []item) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create evidence package: %w", err)
	}
	archive := zip.NewWriter(file)
	for _, item := range items {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		header.SetMode(0o644)
		header.Modified = time.Date(1980, time.January, 1, 12, 0, 0, 0, time.UTC)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			_ = file.Close()
			return err
		}
		if _, err := writer.Write(item.body); err != nil {
			_ = archive.Close()
			_ = file.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func campaignFinishedAt(dir string) (time.Time, error) {
	events, err := campaign.ReadEvents(dir)
	if err != nil {
		return time.Time{}, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "campaign-finished" {
			return events[i].TS, nil
		}
	}
	return time.Time{}, fmt.Errorf("campaign record %s has no finished event", dir)
}
