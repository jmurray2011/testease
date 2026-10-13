package campaign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
)

const (
	StatusRunning     = "running"
	StatusPassed      = "passed"
	StatusVerdictFail = "verdict-fail"
	StatusInfraFail   = "infra-fail"
	StatusInterrupted = "interrupted"
)

type Invoker interface {
	Invoke(context.Context, io.Writer, io.Writer, ...string) int
}

type ExecInvoker struct {
	Executable string
	Env        []string
}

func (e ExecInvoker) Invoke(ctx context.Context, stdout, stderr io.Writer, args ...string) int {
	command := exec.CommandContext(ctx, e.Executable, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = e.Env
	if len(command.Env) == 0 {
		command.Env = os.Environ()
	}
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		_, _ = fmt.Fprintf(stderr, "invoke testease: %v\n", err)
		return 10
	}
	return 0
}

type Options struct {
	Definition    *Definition
	CampaignDir   string
	RunsDir       string
	ScenarioRoot  string
	TargetCatalog string
	CatalogSHA256 string
	TargetName    string
	TargetTags    []string
	InputBundle   string
	Keep          bool
	SelectorName  string
	SelectorTags  []string
	ToolVersion   string
	ToolCommit    string
	Invoker       Invoker
	Now           func() time.Time
	OnStart       func(id, dir string)
}

type Result struct {
	ID            string   `json:"campaign_id"`
	Dir           string   `json:"dir"`
	Status        string   `json:"status"`
	ExitCode      int      `json:"exit_code"`
	Runs          []string `json:"runs"`
	Reproduced    bool     `json:"reproduced"`
	Cleanup       string   `json:"cleanup"`
	DefinitionSHA string   `json:"definition_sha256"`
}

type startedEvent struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	Scenario             string              `json:"scenario"`
	Params               map[string]string   `json:"params,omitempty"`
	Matrix               map[string][]string `json:"matrix,omitempty"`
	Parallel             int                 `json:"parallel"`
	ReleaseFailedTargets bool                `json:"release_failed_targets"`
	DefinitionSHA256     string              `json:"definition_sha256"`
	Repeat               int                 `json:"repeat"`
	Judge                string              `json:"judge,omitempty"`
	Render               string              `json:"render,omitempty"`
	Reproduce            bool                `json:"reproduce"`
	TargetName           string              `json:"target_name,omitempty"`
	TargetTags           []string            `json:"target_tags,omitempty"`
	SelectorName         string              `json:"selector_name,omitempty"`
	SelectorTags         []string            `json:"selector_tags,omitempty"`
	CatalogSHA256        string              `json:"catalog_sha256,omitempty"`
	Keep                 bool                `json:"keep"`
	ToolVersion          string              `json:"tool_version"`
	ToolCommit           string              `json:"tool_commit,omitempty"`
}

type activityEvent struct {
	Activity  string `json:"activity"`
	Iteration int    `json:"iteration,omitempty"`
	Log       string `json:"log"`
}

type commandEvent struct {
	Activity     string `json:"activity"`
	Iteration    int    `json:"iteration,omitempty"`
	ExitCode     int    `json:"exit_code"`
	RunID        string `json:"run_id,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
	ComparisonID string `json:"comparison_id,omitempty"`
	Log          string `json:"log"`
}

type hookEvent struct {
	Hook          string `json:"hook"`
	ExecutableSHA string `json:"executable_sha256"`
	ExitCode      int    `json:"exit_code"`
	DurationMS    int64  `json:"duration_ms"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	Receipt       string `json:"receipt,omitempty"`
	ReceiptSHA    string `json:"receipt_sha256,omitempty"`
}

type hookReceipt struct {
	Schema      int    `json:"schema"`
	InputBundle string `json:"input_bundle,omitempty"`
}

func Run(ctx context.Context, options Options) (Result, error) {
	if options.Definition == nil || options.Invoker == nil {
		return Result{}, fmt.Errorf("campaign needs a definition and invoker")
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	id := ID(options.Definition.Name, now())
	dir := options.CampaignDir
	if dir == "" {
		dir = filepath.Join(options.RunsDir, "campaigns", id)
	}
	options.CampaignDir = dir
	result := Result{ID: id, Dir: dir, Status: StatusRunning, Runs: []string{}, DefinitionSHA: options.Definition.SHA256}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return result, fmt.Errorf("make room for campaign record %s: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o755); err == nil {
		// OpenRecord creates the fixed children inside this atomically claimed
		// directory. Two campaigns starting in the same second cannot share it.
	} else if os.IsExist(err) {
		return result, fmt.Errorf("campaign record %s already exists", dir)
	} else {
		return result, fmt.Errorf("create campaign record %s: %w", dir, err)
	}
	writer, err := OpenRecord(dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = writer.Close() }()
	activity, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return result, fmt.Errorf("open campaign output: %w", err)
	}
	defer func() { _ = activity.Close() }()
	if options.OnStart != nil {
		options.OnStart(id, dir)
	}
	if err := writer.Emit("campaign-started", startedEvent{
		ID: id, Name: options.Definition.Name, Scenario: options.Definition.Scenario,
		Params: options.Definition.Params, Matrix: options.Definition.Matrix, Parallel: options.Definition.Parallel,
		ReleaseFailedTargets: options.Definition.ReleaseFailedTargets,
		DefinitionSHA256:     options.Definition.SHA256, Repeat: options.Definition.Repeat,
		Judge: options.Definition.Judge, Render: options.Definition.Render, Reproduce: options.Definition.Reproduce,
		TargetName: options.TargetName, TargetTags: options.TargetTags,
		SelectorName: options.SelectorName, SelectorTags: options.SelectorTags,
		CatalogSHA256: options.CatalogSHA256, Keep: options.Keep,
		ToolVersion: options.ToolVersion, ToolCommit: options.ToolCommit,
	}); err != nil {
		return result, err
	}
	snapshots, err := snapshotDefinition(options.Definition, dir)
	if err != nil {
		return result, err
	}
	if options.TargetCatalog != "" {
		catalogHash, snapshotErr := snapshotFile(options.TargetCatalog, filepath.Join(dir, "definition", "target-catalog.yaml"), 0o644)
		if snapshotErr != nil {
			return result, fmt.Errorf("snapshot target catalog: %w", snapshotErr)
		}
		if options.CatalogSHA256 != "" && strings.TrimPrefix(catalogHash, "sha256:") != strings.TrimPrefix(options.CatalogSHA256, "sha256:") {
			return result, fmt.Errorf("target catalog changed after selection: selected %s, snapshotted %s", options.CatalogSHA256, catalogHash)
		}
		snapshots["target-catalog.yaml"] = catalogHash
	}
	if err := writer.Emit("definition-snapshotted", map[string]any{"files": snapshots}); err != nil {
		return result, err
	}
	writeActivity(activity, "campaign %s started", id)

	failedCode := 0
	if options.Definition.Lifecycle.Prepare != "" {
		code, hookErr := runHook(ctx, writer, activity, options, "prepare", options.Definition.Lifecycle.Prepare)
		if hookErr != nil {
			failedCode = 10
			writeActivity(activity, "prepare could not run: %v", hookErr)
		} else if code != 0 {
			failedCode = 10
		} else if receipt, receiptErr := readHookReceipt(options.CampaignDir, "prepare"); receiptErr != nil {
			failedCode = 10
			writeActivity(activity, "prepare receipt is invalid: %v", receiptErr)
		} else if receipt.InputBundle != "" {
			options.InputBundle = receipt.InputBundle
		}
	}
	if failedCode == 0 && !options.Definition.Preflight.Skip {
		code, invokeErr := invokeDoctor(ctx, writer, activity, options)
		if invokeErr != nil {
			failedCode = 10
		} else if code != 0 {
			failedCode = code
		}
	}

	for iteration := 1; failedCode == 0 && iteration <= options.Definition.Repeat; iteration++ {
		runID, code, invokeErr := invokeRun(ctx, writer, activity, options, iteration)
		if invokeErr != nil {
			failedCode = 10
			break
		}
		if runID != "" {
			result.Runs = append(result.Runs, runID)
		}
		if code != 0 {
			failedCode = code
			break
		}
		code, invokeErr = invokeJudge(ctx, writer, activity, options, iteration, runID)
		if invokeErr != nil {
			failedCode = 10
			break
		}
		if code == 20 {
			result.Status = StatusVerdictFail
		} else if code != 0 {
			failedCode = code
			break
		}
		if options.Definition.Render != "" {
			code, invokeErr = invokeRender(ctx, writer, activity, options, iteration, runID)
			if invokeErr != nil {
				failedCode = 10
				break
			}
			if code != 0 {
				failedCode = code
				break
			}
		}
	}

	if failedCode == 0 && options.Definition.Reproduce {
		code, invokeErr := invokeReproduce(ctx, writer, activity, options, result.Runs)
		if invokeErr != nil {
			failedCode = 10
		} else if code == 20 {
			result.Status = StatusVerdictFail
		} else if code != 0 {
			failedCode = code
		} else {
			result.Reproduced = true
		}
	}

	if options.Keep {
		result.Cleanup = "kept"
		_ = writer.Emit("cleanup-skipped", map[string]string{"reason": "--keep"})
		writeActivity(activity, "cleanup skipped: --keep")
	} else if options.Definition.Lifecycle.Cleanup != "" {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		code, hookErr := runHook(cleanupCtx, writer, activity, options, "cleanup", options.Definition.Lifecycle.Cleanup)
		cancel()
		if hookErr != nil || code != 0 {
			result.Cleanup = "failed"
			failedCode = 10
		} else {
			result.Cleanup = "released"
		}
	} else {
		result.Cleanup = "not-configured"
	}

	if failedCode != 0 {
		result.ExitCode = failedCode
		result.Status = statusForCode(failedCode, ctx.Err() != nil)
	} else if result.Status == StatusRunning {
		result.Status = StatusPassed
	}
	if result.Status == StatusVerdictFail {
		result.ExitCode = 20
	}
	if err := writer.Emit("campaign-finished", result); err != nil {
		return result, err
	}
	writeActivity(activity, "campaign %s finished %s", id, result.Status)
	return result, nil
}

func invokeRun(ctx context.Context, writer *Writer, activity io.Writer, options Options, iteration int) (string, int, error) {
	logPath := filepath.ToSlash(filepath.Join("runs", fmt.Sprintf("%02d-run.log", iteration)))
	if err := writer.Emit("activity-started", activityEvent{Activity: "run", Iteration: iteration, Log: logPath}); err != nil {
		return "", 10, err
	}
	writeActivity(activity, "run %d of %d started", iteration, options.Definition.Repeat)
	args := []string{"--runs-dir", options.RunsDir, "--scenario-root", options.ScenarioRoot, "run", options.Definition.Scenario, "--output", "json", "--progress", "plain"}
	args = appendCampaignScope(args, options.Definition, true)
	if options.TargetName != "" {
		args = append(args, "--target", options.TargetName)
	}
	for _, tag := range options.TargetTags {
		args = append(args, "--target-tag", tag)
	}
	if options.TargetCatalog != "" {
		args = append(args, "--target-catalog", options.TargetCatalog)
	}
	if options.InputBundle != "" {
		args = append(args, "--input-bundle", options.InputBundle)
	}
	stdout, code, err := invokeLogged(ctx, options.Invoker, filepath.Join(options.CampaignDir, logPath), activity, args...)
	var response struct {
		RunID string `json:"run_id"`
	}
	if len(bytes.TrimSpace(stdout)) > 0 {
		if decodeErr := json.Unmarshal(stdout, &response); decodeErr != nil && code == 0 {
			return "", 10, fmt.Errorf("decode run %d result: %w", iteration, decodeErr)
		}
	}
	if err := writer.Emit("activity-finished", commandEvent{Activity: "run", Iteration: iteration, ExitCode: code, RunID: response.RunID, Log: logPath}); err != nil {
		return response.RunID, code, err
	}
	writeActivity(activity, "run %d finished exit=%d id=%s", iteration, code, response.RunID)
	return response.RunID, code, err
}

func invokeDoctor(ctx context.Context, writer *Writer, activity io.Writer, options Options) (int, error) {
	logPath := filepath.ToSlash(filepath.Join("runs", "doctor.log"))
	if err := writer.Emit("activity-started", activityEvent{Activity: "doctor", Log: logPath}); err != nil {
		return 10, err
	}
	writeActivity(activity, "doctor started")
	args := []string{"--runs-dir", options.RunsDir, "--scenario-root", options.ScenarioRoot, "doctor", options.Definition.Scenario, "--output", "json"}
	args = appendCampaignScope(args, options.Definition, false)
	if options.Definition.Preflight.Strict {
		args = append(args, "--strict")
	}
	if options.TargetName != "" {
		args = append(args, "--target", options.TargetName)
	}
	if options.TargetCatalog != "" {
		args = append(args, "--target-catalog", options.TargetCatalog)
	}
	if options.InputBundle != "" {
		args = append(args, "--input-bundle", options.InputBundle)
	}
	_, code, err := invokeLogged(ctx, options.Invoker, filepath.Join(options.CampaignDir, logPath), activity, args...)
	if emitErr := writer.Emit("activity-finished", commandEvent{Activity: "doctor", ExitCode: code, Log: logPath}); emitErr != nil {
		return code, emitErr
	}
	writeActivity(activity, "doctor finished exit=%d", code)
	return code, err
}

func appendCampaignScope(args []string, definition *Definition, includeParallel bool) []string {
	paramNames := make([]string, 0, len(definition.Params))
	for name := range definition.Params {
		paramNames = append(paramNames, name)
	}
	sort.Strings(paramNames)
	for _, name := range paramNames {
		args = append(args, "--param", name+"="+definition.Params[name])
	}

	axisNames := make([]string, 0, len(definition.Matrix))
	for name := range definition.Matrix {
		axisNames = append(axisNames, name)
	}
	sort.Strings(axisNames)
	for _, name := range axisNames {
		args = append(args, "--matrix", name+"="+strings.Join(definition.Matrix[name], ","))
	}
	if includeParallel {
		args = append(args, "--par", strconv.Itoa(definition.Parallel))
		if definition.ReleaseFailedTargets {
			args = append(args, "--release-failed-targets")
		}
	}
	return args
}

func invokeJudge(ctx context.Context, writer *Writer, activity io.Writer, options Options, iteration int, runID string) (int, error) {
	logPath := filepath.ToSlash(filepath.Join("runs", fmt.Sprintf("%02d-judge.log", iteration)))
	if err := writer.Emit("activity-started", activityEvent{Activity: "judge", Iteration: iteration, Log: logPath}); err != nil {
		return 10, err
	}
	writeActivity(activity, "judge %d started", iteration)
	args := []string{"--runs-dir", options.RunsDir, "judge", runID, "--output", "json"}
	if options.Definition.Judge != "" {
		args = append(args, "--judge", options.Definition.Judge)
	}
	stdout, code, err := invokeLogged(ctx, options.Invoker, filepath.Join(options.CampaignDir, logPath), activity, args...)
	verdict := verdictOf(stdout)
	if err := writer.Emit("activity-finished", commandEvent{Activity: "judge", Iteration: iteration, ExitCode: code, RunID: runID, Verdict: verdict, Log: logPath}); err != nil {
		return code, err
	}
	writeActivity(activity, "judge %d finished exit=%d verdict=%s", iteration, code, verdict)
	return code, err
}

func invokeRender(ctx context.Context, writer *Writer, activity io.Writer, options Options, iteration int, runID string) (int, error) {
	logPath := filepath.ToSlash(filepath.Join("runs", fmt.Sprintf("%02d-render.log", iteration)))
	if err := writer.Emit("activity-started", activityEvent{Activity: "render", Iteration: iteration, Log: logPath}); err != nil {
		return 10, err
	}
	writeActivity(activity, "render %d started", iteration)
	args := []string{"--runs-dir", options.RunsDir, "render", runID, "--preset", options.Definition.Render}
	_, code, err := invokeLogged(ctx, options.Invoker, filepath.Join(options.CampaignDir, logPath), activity, args...)
	if emitErr := writer.Emit("activity-finished", commandEvent{Activity: "render", Iteration: iteration, ExitCode: code, RunID: runID, Log: logPath}); emitErr != nil {
		return code, emitErr
	}
	writeActivity(activity, "render %d finished exit=%d", iteration, code)
	return code, err
}

func invokeReproduce(ctx context.Context, writer *Writer, activity io.Writer, options Options, runs []string) (int, error) {
	logPath := filepath.ToSlash(filepath.Join("runs", "reproduce.log"))
	if err := writer.Emit("activity-started", activityEvent{Activity: "reproduce", Log: logPath}); err != nil {
		return 10, err
	}
	writeActivity(activity, "reproduce started")
	args := []string{"--runs-dir", options.RunsDir, "reproduce"}
	args = append(args, runs...)
	args = append(args, "--output", "json")
	stdout, code, err := invokeLogged(ctx, options.Invoker, filepath.Join(options.CampaignDir, logPath), activity, args...)
	verdict := verdictOf(stdout)
	comparisonID := comparisonIDOf(stdout)
	if err := writer.Emit("activity-finished", commandEvent{Activity: "reproduce", ExitCode: code, Verdict: verdict, ComparisonID: comparisonID, Log: logPath}); err != nil {
		return code, err
	}
	writeActivity(activity, "reproduce finished exit=%d verdict=%s", code, verdict)
	return code, err
}

func invokeLogged(ctx context.Context, invoker Invoker, path string, activity io.Writer, args ...string) ([]byte, int, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, 10, err
	}
	defer func() { _ = file.Close() }()
	var stdout bytes.Buffer
	code := invoker.Invoke(ctx, io.MultiWriter(&stdout, file), io.MultiWriter(file, activity), args...)
	return stdout.Bytes(), code, nil
}

func verdictOf(body []byte) string {
	var value struct {
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal(body, &value)
	return value.Verdict
}

func comparisonIDOf(body []byte) string {
	var verdict judge.Verdict
	if err := json.Unmarshal(body, &verdict); err != nil {
		return ""
	}
	if verdict.Judge != judge.ReproduceName || len(verdict.Evidence.Records) < 2 {
		return ""
	}
	id, _ := judge.ComparisonID(verdict)
	return id
}

func statusForCode(code int, canceled bool) string {
	if canceled || code == 12 {
		return StatusInterrupted
	}
	switch code {
	case 3:
		return "precondition-fail"
	case 4:
		return "integrity-fail"
	case 11:
		return "probe-fail"
	case 20:
		return StatusVerdictFail
	case 21:
		return "unjudgeable"
	default:
		return StatusInfraFail
	}
}

func runHook(ctx context.Context, writer *Writer, activity io.Writer, options Options, name, configured string) (int, error) {
	path := HookPath(options.Definition, configured)
	body, err := os.ReadFile(path)
	if err != nil {
		return 10, err
	}
	sum := sha256.Sum256(body)
	stdoutRel := filepath.ToSlash(filepath.Join("hooks", name+".out"))
	stderrRel := filepath.ToSlash(filepath.Join("hooks", name+".err"))
	receiptRel := filepath.ToSlash(filepath.Join("hooks", name+".receipt"))
	stdout, err := os.OpenFile(filepath.Join(options.CampaignDir, stdoutRel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 10, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(filepath.Join(options.CampaignDir, stderrRel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 10, err
	}
	defer func() { _ = stderr.Close() }()
	if err := writer.Emit("activity-started", activityEvent{Activity: name, Log: stderrRel}); err != nil {
		return 10, err
	}
	writeActivity(activity, "%s started", name)
	started := time.Now()
	command := exec.CommandContext(ctx, path)
	command.Dir = options.Definition.Dir
	command.Stdout = io.MultiWriter(stdout, activity)
	command.Stderr = io.MultiWriter(stderr, activity)
	command.Env = append(
		os.Environ(),
		"TESTEASE_CAMPAIGN_ID="+filepath.Base(options.CampaignDir),
		"TESTEASE_CAMPAIGN_DIR="+options.CampaignDir,
		"TESTEASE_RUNS_DIR="+options.RunsDir,
		"TESTEASE_TARGET_NAME="+options.TargetName,
		"TESTEASE_TARGET_TAGS="+strings.Join(options.TargetTags, ","),
		"TESTEASE_CAMPAIGN_PARAMS_JSON="+mustJSON(options.Definition.Params),
		"TESTEASE_CAMPAIGN_MATRIX_JSON="+mustJSON(options.Definition.Matrix),
		"TESTEASE_CAMPAIGN_PARALLEL="+strconv.Itoa(options.Definition.Parallel),
		"TESTEASE_CAMPAIGN_RELEASE_FAILED_TARGETS="+strconv.FormatBool(options.Definition.ReleaseFailedTargets),
		"TESTEASE_HOOK_RECEIPT="+filepath.Join(options.CampaignDir, receiptRel),
	)
	runErr := command.Run()
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			code = exit.ExitCode()
		} else {
			code = 10
		}
	}
	event := hookEvent{Hook: name, ExecutableSHA: hex.EncodeToString(sum[:]), ExitCode: code, DurationMS: time.Since(started).Milliseconds(), Stdout: stdoutRel, Stderr: stderrRel}
	if receiptBody, readErr := os.ReadFile(filepath.Join(options.CampaignDir, receiptRel)); readErr == nil {
		receiptSum := sha256.Sum256(receiptBody)
		event.Receipt = receiptRel
		event.ReceiptSHA = hex.EncodeToString(receiptSum[:])
	}
	if err := writer.Emit("hook-finished", event); err != nil {
		return code, err
	}
	writeActivity(activity, "%s finished exit=%d", name, code)
	return code, nil
}

func mustJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func readHookReceipt(campaignDir, name string) (hookReceipt, error) {
	path := filepath.Join(campaignDir, "hooks", name+".receipt")
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return hookReceipt{}, nil
	}
	if err != nil {
		return hookReceipt{}, err
	}
	var receipt hookReceipt
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	if receipt.Schema != 1 {
		return receipt, fmt.Errorf("schema is %d, want 1", receipt.Schema)
	}
	return receipt, nil
}

func writeActivity(writer io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(writer, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func snapshotDefinition(definition *Definition, campaignDir string) (map[string]string, error) {
	destination := filepath.Join(campaignDir, "definition")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, fmt.Errorf("snapshot campaign definition: %w", err)
	}
	files := []string{"campaign.yaml", definition.Lifecycle.Prepare, definition.Lifecycle.Cleanup}
	files = append(files, definition.Lifecycle.Inputs...)
	seen := map[string]bool{}
	hashes := map[string]string{}
	for _, configured := range files {
		if configured == "" {
			continue
		}
		source := HookPath(definition, configured)
		name := filepath.Base(configured)
		if seen[name] {
			return nil, fmt.Errorf("campaign definition inputs share the basename %q", name)
		}
		seen[name] = true
		mode := os.FileMode(0o644)
		if configured == definition.Lifecycle.Prepare || configured == definition.Lifecycle.Cleanup {
			mode = 0o755
		}
		hash, err := snapshotFile(source, filepath.Join(destination, name), mode)
		if err != nil {
			return nil, fmt.Errorf("snapshot campaign input %s: %w", configured, err)
		}
		hashes[name] = hash
	}
	return hashes, nil
}

func snapshotFile(source, destination string, mode os.FileMode) (string, error) {
	body, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(destination, body, mode); err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
