// Package doctor checks what a run needs before the run is started, and names
// the command that fixes whatever is missing.
package doctor

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmurray2011/testease/internal/inputbundle"
	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/scenario"
)

// Commands a check can tell a person to run. Named here so a test can check
// them against the real command tree.
const (
	RemedyBackends  = "testease backends"
	RemedyScenarios = "testease scenarios"
	remedyDocker    = "docker context ls   # or start the daemon this context points at"
)

// Remedies is every command-naming remedy this package prints.
func Remedies() []string {
	return []string{RemedyBackends, RemedyScenarios}
}

func scenarioRemedy(root string) string {
	if root == "" {
		return RemedyScenarios
	}
	return RemedyScenarios + " --scenario-root " + root
}

// Status is what a check concluded. Unchecked is separate from OK because
// something nobody looked at has not passed.
type Status string

const (
	OK        Status = "ok"
	Warn      Status = "warn"
	Fail      Status = "fail"
	Unchecked Status = "unchecked"
)

// Check is one precondition, what was found, and how to fix it.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Found  string `json:"found"`
	Remedy string `json:"remedy,omitempty"`
}

// Report is every check, in the order they were made.
type Report struct {
	Checks []Check `json:"checks"`
}

// Docker is the daemon, as much of it as a check needs. Nil means this run has
// no daemon to ask, and the checks that need one say unchecked.
type Docker interface {
	ServerVersion(ctx context.Context) (string, error)
	HasImage(ctx context.Context, ref string) (bool, error)
	ReadMount(ctx context.Context, source, image string) ([]byte, error)
	MountType(ctx context.Context, source, image string) (string, error)
	CopyRoundTrip(ctx context.Context, image string) error

	// IsLocal answers whether this daemon runs on the machine doctor is
	// running on, and names the machine it found. The name is for the report:
	// "not this one" is not something a person can act on.
	IsLocal(ctx context.Context) (bool, string, error)
}

// Backend is one kind of host and what a scenario may ask of it, as
// `testease backends` states it.
//
// Doctor is handed this rather than holding its own copy. The runner refuses
// from the resolved target's real interface, and a second table here would
// eventually pass a scenario the run then refuses - which is the failure
// doctor exists to prevent, arriving through doctor itself.
type Backend struct {
	Provider string

	// Offers is what this kind can do whatever the invocation.
	Offers []string

	// Conditional is what it can also do, and what each one additionally
	// needs. Kept apart from Offers because "docker can fingerprint" is only
	// true of a run given an instrument, and a check that treated the two
	// alike would pass what the run refuses.
	Conditional map[string]string
}

// Options is what to check and what to check it with.
type Options struct {
	RunsDir      string
	ScenarioRoot string

	// Scenario names one to check. Empty checks only the host.
	Scenario string

	// Params are overrides, as `testease run --param` takes them, because a
	// scenario resolves differently under different values.
	Params map[string]string

	// Cells are the complete parameter bindings run will schedule for a
	// census. Empty means this is a single run. Doctor resolves every cell
	// because its images and media may be derived from the cell values.
	Cells []map[string]string

	// InputBundle binds named inputs exactly as run does.
	InputBundle string

	// SSMTransferStore is the temporary s3:// handoff run will use for media
	// larger than one Systems Manager command document.
	SSMTransferStore      string
	SSMTransferStoreValid bool
	SSMInlineMediaLimit   int64

	Docker Docker
	// DockerContext names the CLI context for actionable image remedies.
	DockerContext string

	// OneShotDocker is the daemon that holds hyperv-provider images. It may be
	// distinct from Docker in a cross-platform run.
	OneShotDocker Docker
	// OneShotDockerContext names that daemon's CLI context.
	OneShotDockerContext string

	// Backends is the capability table. Empty leaves the capability check
	// unmade rather than passed.
	Backends []Backend
}

// Worst is the status a caller acts on. Unchecked counts as a warning: a report
// that answered nothing must not read the same as one that answered yes.
func (r Report) Worst() Status {
	worst := OK
	for _, c := range r.Checks {
		switch c.Status {
		case Fail:
			return Fail
		case Warn, Unchecked:
			worst = Warn
		}
	}
	return worst
}

// Failed is whether this report should stop a caller. A warning is not a
// failure by default - a host that can only run some of the corpus is a
// legitimate host - and strict makes it one, because a script cannot read prose.
func (r Report) Failed(strict bool) bool {
	switch r.Worst() {
	case Fail:
		return true
	case Warn:
		return strict
	default:
		return false
	}
}

// Run makes every check it can and reports what it could not.
func Run(ctx context.Context, opt Options) Report {
	var r Report

	r.add(runsDir(opt.RunsDir))

	if opt.Scenario == "" {
		r.add(daemon(ctx, opt.Docker))
		// Two checks is not a clean bill of health for a run, and a report
		// that stopped without saying so reads like one.
		r.add(Check{
			Name: "scenario", Status: Unchecked,
			Found:  "no scenario named, so its media, images, judge, drive steps and capabilities went unchecked",
			Remedy: "testease doctor <scenario>",
		})
		return r
	}

	plan, c := loadScenario(opt)
	r.add(c)
	if plan == nil {
		r.add(Check{Name: "inputs", Status: Unchecked, Found: "the scenario did not load"})
		r.add(Check{Name: "media", Status: Unchecked, Found: "the scenario did not load"})
		r.add(Check{Name: "images", Status: Unchecked, Found: "the scenario did not load"})
		r.add(Check{Name: "judge", Status: Unchecked, Found: "the scenario did not load"})
		r.add(Check{Name: "drive", Status: Unchecked, Found: "the scenario did not load"})
		r.add(Check{Name: "capabilities", Status: Unchecked, Found: "the scenario did not load"})
	} else {
		if needsDocker(plan) {
			r.add(daemon(ctx, opt.Docker))
		}
		if needsOneShotDocker(plan) {
			r.add(daemonNamed(ctx, "one-shot-docker", opt.OneShotDocker))
		}
		resolved, inputCheck := inputs(plan, opt)
		r.add(inputCheck)
		if inputCheck.Status == Fail {
			r.add(Check{Name: "media", Status: Unchecked, Found: "the scenario's inputs did not resolve"})
		} else if err := inputbundle.Bind(plan, resolved); err != nil {
			r.add(Check{Name: "media", Status: Fail, Found: err.Error(), Remedy: "check the scenario's @input mounts"})
		} else {
			r.add(media(ctx, plan, opt.Docker, opt.SSMTransferStore, opt.SSMTransferStoreValid, opt.SSMInlineMediaLimit))
		}
		r.add(images(ctx, plan, opt.Docker, opt.DockerContext, opt.OneShotDocker, opt.OneShotDockerContext))
		r.add(declaredJudge(plan, opt.ScenarioRoot))
		r.add(drive(ctx, plan, opt.RunsDir, opt.Docker))
		r.add(capabilities(plan, opt.Backends))
	}

	return r
}

// needsDocker is resolved scope, not a global workstation requirement. A
// static SSH-only run must not fail strict preflight because an unrelated
// local daemon is stopped; a drive step still needs Docker even when its
// target is static.
func needsDocker(plan *scenario.Plan) bool {
	for _, host := range plan.Hosts {
		if host.Provider == "docker" {
			return true
		}
	}
	for _, step := range plan.Steps {
		if scenario.IsDrive(step.Probe) {
			return true
		}
	}
	return false
}

func needsOneShotDocker(plan *scenario.Plan) bool {
	for _, host := range plan.Hosts {
		if host.Provider == "hyperv" {
			return true
		}
	}
	return false
}

func (r *Report) add(c Check) { r.Checks = append(r.Checks, c) }

func inputs(plan *scenario.Plan, opt Options) (map[string]inputbundle.Resolved, Check) {
	const name = "inputs"
	resolved, err := inputbundle.LoadAndStage(opt.InputBundle, opt.RunsDir, plan.Inputs)
	if err != nil {
		return nil, Check{
			Name: name, Status: Fail, Found: err.Error(),
			Remedy: "testease doctor " + opt.Scenario + " --input-bundle <descriptor>",
		}
	}
	if len(resolved) == 0 {
		return nil, Check{Name: name, Status: OK, Found: "the scenario declares no external inputs"}
	}
	return resolved, Check{Name: name, Status: OK, Found: fmt.Sprintf("%d content-addressed input(s) verified", len(resolved))}
}

// runsDir takes a real lock in the directory records will be written to. The
// record refuses to write without one, and which filesystem this is does not
// answer whether one can be taken.
func runsDir(dir string) Check {
	const name = "runs-dir"
	if dir == "" {
		return Check{Name: name, Status: Fail, Found: "no runs directory", Remedy: "testease run --runs-dir <dir>"}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Check{
			Name: name, Status: Fail,
			Found:  err.Error(),
			Remedy: "testease run --runs-dir <a writable directory>",
		}
	}

	probe := filepath.Join(dir, ".testease-doctor")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Check{
			Name: name, Status: Fail,
			Found:  err.Error(),
			Remedy: "testease run --runs-dir <a writable directory>",
		}
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(probe)
	}()

	if err := lockExclusive(f); err != nil {
		return Check{
			Name: name, Status: Fail,
			Found:  fmt.Sprintf("%s holds records but will not take a lock: %v", dir, err),
			Remedy: "testease run --runs-dir <a directory on a filesystem with flock>",
		}
	}
	return Check{Name: name, Status: OK, Found: dir + " is writable and locks"}
}

func daemon(ctx context.Context, d Docker) Check {
	return daemonNamed(ctx, "docker", d)
}

func daemonNamed(ctx context.Context, name string, d Docker) Check {
	if d == nil {
		return Check{
			Name: name, Status: Unchecked,
			Found:  "no daemon was configured for this check",
			Remedy: RemedyBackends,
		}
	}
	version, err := d.ServerVersion(ctx)
	if err != nil {
		return Check{
			Name: name, Status: Warn,
			Found:  err.Error(),
			Remedy: remedyDocker,
		}
	}
	return Check{Name: name, Status: OK, Found: "server " + version}
}

func loadScenario(opt Options) (*scenario.Plan, Check) {
	const name = "scenario"
	m, err := scenario.Load(filepath.Join(opt.ScenarioRoot, opt.Scenario))
	if err != nil {
		return nil, Check{Name: name, Status: Fail, Found: err.Error(), Remedy: scenarioRemedy(opt.ScenarioRoot)}
	}
	bindings := opt.Cells
	if len(bindings) == 0 {
		bindings = []map[string]string{opt.Params}
	}
	plans := make([]*scenario.Plan, 0, len(bindings))
	for i, params := range bindings {
		plan, resolveErr := m.Resolve(params)
		if resolveErr != nil {
			where := ""
			if len(bindings) > 1 {
				where = fmt.Sprintf("matrix cell %d: ", i+1)
			}
			return nil, Check{Name: name, Status: Fail, Found: where + resolveErr.Error(), Remedy: scenarioRemedy(opt.ScenarioRoot)}
		}
		plans = append(plans, plan)
	}
	plan := combine(plans)
	if len(plans) > 1 {
		return plan, Check{
			Name: name, Status: OK,
			Found: fmt.Sprintf("%s: %d matrix cells, %d step and %d target instances",
				plan.Name, len(plans), len(plan.Steps), len(plan.Hosts)),
		}
	}
	return plan, Check{
		Name: name, Status: OK,
		Found: fmt.Sprintf("%s: %d steps over %d host(s)", plan.Name, len(plan.Steps), len(plan.Hosts)),
	}
}

// combine makes the resource surface of a census look like one plan to the
// existing checks. It does not make an executable plan: host names are scoped
// by cell only so images, media, drives and capabilities from every resolved
// cell are checked without one cell replacing another in a map.
func combine(plans []*scenario.Plan) *scenario.Plan {
	if len(plans) == 1 {
		return plans[0]
	}
	combined := *plans[0]
	combined.Hosts = make(map[string]scenario.Host)
	combined.Steps = nil
	for i, plan := range plans {
		scope := func(name string) string {
			return fmt.Sprintf("cell-%d/%s", i+1, name)
		}
		for name, host := range plan.Hosts {
			combined.Hosts[scope(name)] = host
		}
		for _, step := range plan.Steps {
			step.Host = scope(step.Host)
			if step.Carry != nil {
				carry := *step.Carry
				carry.ToHost = scope(carry.ToHost)
				step.Carry = &carry
			}
			combined.Steps = append(combined.Steps, step)
		}
	}
	return &combined
}

// media checks the source side of every mount. A scenario pins absolute paths
// for its artifact store, so on another machine the run boots, mounts nothing,
// and a probe fails somewhere unrelated.
func media(ctx context.Context, plan *scenario.Plan, docker Docker, ssmTransferStore string, ssmTransferStoreValid bool, ssmInlineMediaLimit int64) Check {
	const name = "media"

	var missing, found []string
	seen := map[string]bool{}
	for _, host := range slices.Sorted(maps.Keys(plan.Hosts)) {
		for _, item := range plan.Hosts[host].Media {
			// The manifest's own parser, not a split on the first colon: a
			// drive letter is not a separator, and doctor checking a different
			// path than run mounts would pass a check about nothing.
			src, _, _ := scenario.ParseMount(item)
			key := plan.Hosts[host].Provider + "\x00" + plan.Hosts[host].Image + "\x00" + src
			if seen[key] {
				continue
			}
			seen[key] = true
			if strings.HasPrefix(src, "%") {
				found = append(found, src+" (run-scoped secret file)")
				continue
			}
			if plan.Hosts[host].Provider == "docker" && docker != nil {
				kind, err := docker.MountType(ctx, src, plan.Hosts[host].Image)
				if err != nil {
					missing = append(missing, fmt.Sprintf("host %s on its Docker daemon: %s (%v)", host, src, err))
					continue
				}
				if local, statErr := os.Stat(src); statErr == nil {
					localKind := "file"
					if local.IsDir() {
						localKind = "directory"
					}
					if kind != localKind {
						missing = append(missing, fmt.Sprintf("host %s on its Docker daemon: %s is a %s, but it is a %s on this machine", host, src, kind, localKind))
						continue
					}
				}
				found = append(found, src+" (Docker daemon can mount it)")
				continue
			}
			info, err := os.Stat(src)
			if err != nil {
				missing = append(missing, fmt.Sprintf("host %s: %s", host, src))
				continue
			}
			if plan.Hosts[host].Provider == "static" && plan.Hosts[host].Backend == "ssm" && ssmInlineMediaLimit > 0 && info.Size() > ssmInlineMediaLimit {
				if !ssmTransferStoreValid {
					missing = append(missing, fmt.Sprintf(
						"host %s: %s is %d bytes and SSM needs --ssm-transfer-store s3://bucket/prefix",
						host, src, info.Size(),
					))
					continue
				}
				found = append(found, src+" (through temporary SSM transfer store)")
				continue
			}
			found = append(found, src)
		}
	}
	for _, step := range plan.Steps {
		for _, item := range step.Media {
			src, _, _ := scenario.ParseMount(item)
			key := "drive\x00" + src
			if seen[key] {
				continue
			}
			seen[key] = true
			if strings.HasPrefix(src, "%") {
				found = append(found, src+" (run-scoped secret file)")
				continue
			}
			if _, err := os.Stat(src); err != nil {
				missing = append(missing, fmt.Sprintf("step %s: %s", step.Label, src))
				continue
			}
			found = append(found, src)
		}
	}

	switch {
	case len(missing) > 0:
		return Check{
			Name: name, Status: Fail,
			Found:  "not available where it will be staged - " + strings.Join(missing, "; "),
			Remedy: "testease run --param <name>=<path>   # or put the media on the machine that stages it",
		}
	case len(found) == 0:
		return Check{Name: name, Status: OK, Found: "the scenario mounts nothing"}
	default:
		return Check{Name: name, Status: OK, Found: fmt.Sprintf("%d mount(s) present", len(found))}
	}
}

// capabilities checks what the scenario asks of its hosts against what each
// host's kind can be asked for.
//
// This was `unchecked` on the grounds that a capability is a property of a
// dialled target. It is not, for most of the class: which capabilities a kind
// has is a static fact - the same fact `testease backends` prints - and the
// manifest says which kind each host is. A scenario requiring `oneshot` of a
// docker host is decided before anything boots, and the run's refusal comes
// after it has booted a box and run the steps before it.
//
// The two conditional ones stay honest rather than becoming a guess: docker
// fingerprints only when the run was given an instrument, and endpoints depend
// on a declared port or a state file, so those are reported as unchecked with
// the condition named instead of passed.
func capabilities(plan *scenario.Plan, table []Backend) Check {
	const name = "capabilities"

	requirements := plan.HostRequirements()
	if len(requirements) == 0 {
		return Check{Name: name, Status: OK, Found: "the scenario requires nothing of its hosts"}
	}
	if len(table) == 0 {
		return Check{
			Name: name, Status: Unchecked,
			Found:  "no capability table was given to check against",
			Remedy: RemedyBackends,
		}
	}

	kinds := map[string]Backend{}
	for _, b := range table {
		kinds[b.Provider] = b
	}

	var missing, conditional, unknown []string
	for _, requirement := range requirements {
		host := requirement.Host
		provider := plan.Hosts[host].Provider
		kind, known := kinds[provider]
		if !known {
			unknown = append(unknown, fmt.Sprintf("host %s is %q", host, provider))
			continue
		}
		if slices.ContainsFunc(requirement.AnyOf, func(need scenario.Capability) bool {
			return slices.Contains(kind.Offers, string(need)) ||
				(need == scenario.NeedsEndpoint && (plan.Hosts[host].Port != 0 || plan.Hosts[host].Origin != ""))
		}) {
			continue
		}
		var conditions []string
		for _, need := range requirement.AnyOf {
			if condition := kind.Conditional[string(need)]; condition != "" {
				conditions = append(conditions, fmt.Sprintf("%s needs %s", need, condition))
			}
		}
		if len(conditions) > 0 {
			conditional = append(conditional, fmt.Sprintf("host %s: %s", host, strings.Join(conditions, " or ")))
			continue
		}
		missing = append(missing,
			fmt.Sprintf("host %s is %s, which has no %s", host, provider, join(requirement.AnyOf)))
	}

	switch {
	case len(missing) > 0:
		return Check{
			Name: name, Status: Fail,
			Found:  strings.Join(missing, "; "),
			Remedy: RemedyBackends + "   # or drop it from requires:",
		}
	case len(unknown) > 0:
		return Check{
			Name: name, Status: Unchecked,
			Found:  "no table entry for " + strings.Join(unknown, "; "),
			Remedy: RemedyBackends,
		}
	case len(conditional) > 0:
		return Check{
			Name: name, Status: Unchecked,
			Found:  strings.Join(conditional, "; "),
			Remedy: RemedyBackends,
		}
	default:
		return Check{
			Name: name, Status: OK,
			Found: fmt.Sprintf("every host offers the %d capability contract(s) declared or implied by the scenario", len(requirements)),
		}
	}
}

// join names the capabilities a scenario declared, in the order it declared
// them.
func join(needs []scenario.Capability) string {
	out := make([]string, 0, len(needs))
	for _, n := range needs {
		out = append(out, string(n))
	}
	return strings.Join(out, ", ")
}

// drive checks how a drive step will exchange its scenario and evidence with
// the selected daemon. A local daemon gets a real bind-mount canary. A remote
// daemon gets a Docker API copy round-trip, which is the transport the runner
// uses there instead of pretending both machines share paths.
func drive(ctx context.Context, plan *scenario.Plan, runsDir string, d Docker) Check {
	const name = "drive"

	var steps []string
	var image string
	for _, s := range plan.Steps {
		if scenario.IsDrive(s.Probe) {
			steps = append(steps, s.Label)
			if image == "" {
				image = strings.TrimPrefix(s.Probe, scenario.DrivePrefix)
			}
		}
	}
	if len(steps) == 0 {
		return Check{Name: name, Status: OK, Found: "the scenario drives nothing beside its target"}
	}
	if d == nil {
		return Check{
			Name: name, Status: Unchecked,
			Found:  fmt.Sprintf("%d drive step(s) mount this machine, and no daemon was configured to ask where it is", len(steps)),
			Remedy: RemedyBackends,
		}
	}

	local, where, err := d.IsLocal(ctx)
	if err != nil {
		return Check{
			Name: name, Status: Warn,
			Found:  "could not ask the daemon where it is: " + err.Error(),
			Remedy: "docker context ls   # or start the daemon this context points at",
		}
	}
	if !local {
		if err := d.CopyRoundTrip(ctx, image); err != nil {
			return Check{
				Name: name, Status: Fail,
				Found:  fmt.Sprintf("the daemon on %s could not return a drive-transfer canary: %v", where, err),
				Remedy: "check Docker context access and free space on the selected daemon",
			}
		}
		return Check{
			Name: name, Status: OK,
			Found: fmt.Sprintf("%d drive step(s), and daemon %s returned the transferred canary",
				len(steps), where),
		}
	}

	// "The daemon is on this host" is not enough for snap-packaged Docker,
	// which may have a private /tmp mount namespace. Read random bytes through
	// an actual bind mount rooted where drive workspaces and verified inputs
	// will live; host-side stat cannot answer this question.
	canary, err := os.CreateTemp(runsDir, ".testease-doctor-mount-*")
	if err != nil {
		return Check{Name: name, Status: Fail, Found: "could not make a mount canary: " + err.Error(), Remedy: "testease run --runs-dir <a daemon-visible directory>"}
	}
	canaryPath := canary.Name()
	defer func() { _ = os.Remove(canaryPath) }()
	token := []byte(fmt.Sprintf("testease-mount-%d", os.Getpid()))
	if _, err := canary.Write(token); err != nil {
		_ = canary.Close()
		return Check{Name: name, Status: Fail, Found: "could not write a mount canary: " + err.Error()}
	}
	if err := canary.Close(); err != nil {
		return Check{Name: name, Status: Fail, Found: "could not close a mount canary: " + err.Error()}
	}
	seen, err := d.ReadMount(ctx, canaryPath, image)
	if err != nil {
		return Check{
			Name: name, Status: Fail,
			Found:  fmt.Sprintf("the local daemon could not read a bind mount under %s: %v", runsDir, err),
			Remedy: "testease run --runs-dir <a directory visible to the Docker daemon>",
		}
	}
	if !slices.Equal(seen, token) {
		return Check{
			Name: name, Status: Fail,
			Found:  fmt.Sprintf("the local daemon read different bytes from a bind mount under %s", runsDir),
			Remedy: "testease run --runs-dir <a directory visible to the Docker daemon>",
		}
	}
	return Check{
		Name: name, Status: OK,
		Found: fmt.Sprintf("%d drive step(s), and daemon %s read the runs-directory mount canary", len(steps), where),
	}
}

func images(ctx context.Context, plan *scenario.Plan, docker Docker, dockerContext string, oneShotDocker Docker, oneShotContext string) Check {
	const name = "images"

	type wantedImage struct {
		ref     string
		surface string
		docker  Docker
		context string
	}
	var want []wantedImage
	seen := map[string]bool{}
	for _, host := range slices.Sorted(maps.Keys(plan.Hosts)) {
		provider := plan.Hosts[host].Provider
		ref := plan.Hosts[host].Image
		if ref == "" || provider != "docker" && provider != "hyperv" {
			continue
		}
		surface, d, daemonContext := "docker", docker, dockerContext
		if provider == "hyperv" {
			surface, d, daemonContext = "one-shot-docker", oneShotDocker, oneShotContext
		}
		key := surface + "\x00" + ref
		if !seen[key] {
			want = append(want, wantedImage{ref: ref, surface: surface, docker: d, context: daemonContext})
			seen[key] = true
		}
	}
	for _, step := range plan.Steps {
		if scenario.IsDrive(step.Probe) {
			ref := strings.TrimPrefix(step.Probe, scenario.DrivePrefix)
			key := "docker\x00" + ref
			if !seen[key] {
				want = append(want, wantedImage{ref: ref, surface: "docker", docker: docker, context: dockerContext})
				seen[key] = true
			}
		}
	}
	if len(want) == 0 {
		return Check{Name: name, Status: OK, Found: "the scenario names no image"}
	}
	var missing []wantedImage
	var found []string
	for _, item := range want {
		label := item.ref
		if item.surface != "docker" {
			label = item.surface + ": " + item.ref
		}
		if item.docker == nil {
			return Check{
				Name: name, Status: Unchecked,
				Found:  "no " + item.surface + " daemon to ask about " + item.ref,
				Remedy: RemedyBackends,
			}
		}
		has, err := item.docker.HasImage(ctx, item.ref)
		if err != nil {
			return Check{Name: name, Status: Unchecked, Found: err.Error(), Remedy: remedyDocker}
		}
		if !has {
			missing = append(missing, item)
		} else {
			found = append(found, label)
		}
	}
	if len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for _, item := range missing {
			if item.surface == "docker" {
				labels = append(labels, item.ref)
			} else {
				labels = append(labels, item.surface+": "+item.ref)
			}
		}
		pull := "docker"
		if missing[0].context != "" {
			pull += " --context " + missing[0].context
		}
		return Check{
			Name: name, Status: Fail,
			Found:  "not on the configured daemon(s) - " + strings.Join(labels, ", "),
			Remedy: pull + " pull " + missing[0].ref,
		}
	}
	return Check{Name: name, Status: OK, Found: strings.Join(found, ", ")}
}

// declaredJudge checks the scenario can be judged by this binary. A version it
// no longer ships measures fine and cannot be judged at all.
func declaredJudge(plan *scenario.Plan, scenarioRoot string) Check {
	const name = "judge"
	if plan.Judge == nil {
		return Check{Name: name, Status: OK, Found: "the scenario names no judge"}
	}

	j, err := judge.ByName(plan.Judge.Name)
	if err != nil {
		return Check{Name: name, Status: Fail, Found: err.Error(), Remedy: scenarioRemedy(scenarioRoot)}
	}
	if plan.Judge.Version != "" && plan.Judge.Version != j.Version() {
		return Check{
			Name: name, Status: Fail,
			Found: fmt.Sprintf("the scenario asks for %s@%s and this testease ships %s@%s",
				plan.Judge.Name, plan.Judge.Version, j.Name(), j.Version()),
			Remedy: fmt.Sprintf("judge: {name: %s, version: %q} in the scenario", j.Name(), j.Version()),
		}
	}
	return Check{Name: name, Status: OK, Found: j.Name() + "@" + j.Version()}
}
