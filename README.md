# testease

Run scenarios against machines and save what happened.

Test results and the conclusion drawn from them are separate. You can inspect
or rejudge a saved run without running it again.

## Two minutes, from a clone

You need a Docker daemon and Go. Nothing else.

From a checkout, `make build` writes `./testease`. To put it on `PATH`, use
`make install` with `GOBIN` set to a directory already on `PATH`.

    make build
    ./testease doctor hello --scenario-root examples --strict

`doctor` checks what a run needs before the run. On a new machine it will
tell you the image is missing and print the command that fixes it:

`--strict` also makes warnings fail the check, which is useful in CI. Without it,
`doctor` reports warnings but allows a run to proceed.

    fail  images  not on the configured daemon(s) - debian:12-slim
                  try: docker pull debian:12-slim

Do that, then measure something:

    ./testease run hello --scenario-root examples

    hello-20260822T013007Z
    1 of 1 steps measured; record at scenario-runs/hello-20260822T013007Z
    this record declares expect@3 and running does not judge: testease judge scenario-runs/hello-20260822T013007Z

**Running is not judging.** The run measured; it did not produce a verdict. Run:

    ./testease judge scenario-runs/hello-20260822T013007Z

    expect@3   pass
      met      ask the box what it is

And read what was actually recorded:

    testease render --view tsv   scenario-runs/hello-...    # a row per step
    testease render --view facts scenario-runs/hello-...    # what the probes read

    fact        hello-20260822T013007Z
    os.arch     x86_64
    os.id       debian
    os.kernel   linux
    os.version  12

Now break it. Edit `examples/hello/probes/whoami.sh` to exit 1, run it again,
and judge it: the verdict becomes `regressed` and names the step and the
reading.

## Where the real scenarios are

`examples/hello` is only an example. It shows the scenario file format and the
run/judge/render commands. Put product-specific scenarios in a separate
repository:

    testease scenarios --scenario-root /path/to/scenarios

Set `TESTEASE_SCENARIO_ROOT` once instead of passing it every time. See
**Configuration** below.

For the authoring loop, see [Writing scenarios](docs/authoring.md). The short
version is:

    testease validate my-scenario --scenario-root /path/to/scenarios
    testease plan my-scenario --scenario-root /path/to/scenarios
    testease doctor my-scenario --scenario-root /path/to/scenarios --strict

For the command list and external judge contract, see
[Commands](docs/commands.md).

Testease runs scenarios and writes records. Your CI system can handle machine
allocation, scheduling, and publishing. The same commands work from a terminal
or from a job.

## Targets and campaigns

A target catalog gives each machine a name and capability tags. Credentials stay
out of the catalog:

```yaml
schema: 1
targets:
  - name: staging-rhel9-fips
    tags: [os:rhel9, posture:fips-stig, lifecycle:snapshot, capacity:exclusive]
    params: {backend: ssh, address: 192.0.2.10}
```

Select a target by name or require matching tags:

```text
testease targets catalog --target-catalog targets.yaml
testease run upgrade --target staging-rhel9-fips --target-catalog targets.yaml
testease run upgrade --target-tag os:rhel9 --target-tag posture:fips-stig \
  --target-catalog targets.yaml
```

Zero matches are refused. More than one match is also refused and every match
is named; Testease never silently chooses the first machine. The selected name,
full tag set, selector, and catalog SHA-256 are recorded. Tags describe intended
capability, while probes still measure what the target actually is.

A campaign groups related runs and records their setup, parameters, cleanup, and
results:

```yaml
schema: 1
name: upgrade-campaign
scenario: upgrade-scenario
params:
  to: "7.2.3"
matrix:
  from: ["6.2.4", "7.2.1"]
parallel: 1
release_failed_targets: true
target:
  name: staging-rhel9-fips
  tags: [posture:fips-stig, capacity:exclusive]
repeat: 2
judge: expect@3
render: team
reproduce: true
preflight: {strict: true}
lifecycle:
  prepare: prepare.sh
  cleanup: cleanup.sh
  inputs: [common.sh]
```

The scenario named by the campaign must declare the `team` view when
`render: team` is used; `testease campaign validate` checks that prerequisite
before any hook or run starts.

The hooks are executable files, not a second scenario language. Testease hashes
and snapshots them, captures their output and optional receipt, runs `doctor`,
then performs each run and judgment sequentially, compares repetitions, and
runs cleanup even after failure or interruption. `--keep` is the explicit
exception and is recorded. A prepare hook may write
`{"schema":1,"input_bundle":"/path/to/inputs.yaml"}` to
`$TESTEASE_HOOK_RECEIPT` when it creates the bundle needed by the runs.
`params`, `matrix`, and `parallel` define the exact child-run scope. The CLI can
replace it with `--param`, `--matrix`, and `--par`; the effective scope is
recorded in `campaign-started` and passed to hooks as
`TESTEASE_CAMPAIGN_PARAMS_JSON`, `TESTEASE_CAMPAIGN_MATRIX_JSON`, and
`TESTEASE_CAMPAIGN_PARALLEL`. This lets a prepare hook stage exactly the media
the recorded campaign will consume.
`release_failed_targets` is an explicit resource-policy exception for
disposable lanes: failed cells are removed before the matrix continues instead
of being retained for inspection. The default keeps failed cells, and
the selected policy is recorded in the campaign and each child run.

```text
testease campaign run upgrade-campaign --campaign-root campaigns \
  --target-catalog targets.yaml
testease campaign status upgrade-campaign-20260904T120000Z
testease follow upgrade-campaign-20260904T120000Z
testease package upgrade-campaign-20260904T120000Z --preset engineering
```

The campaign record lives under `RUNS_DIR/campaigns/<id>`. Its `output.log` is
safe to tail while the work is active; the event stream is the status.
The archive contains the campaign snapshot, child records and cited
comparison, plus a manifest hashing every included file. The engineering
preset removes personal filesystem prefixes, runner identity, private network
locators and external-input source paths, and refuses secret-shaped assignments
instead of quietly shipping them. The manifest names the sanitization rule
version. Packaging the same completed campaign twice produces the same archive
bytes and SHA-256.

You can also use a machine that is already running. Declare its provider and
connection details in the scenario, then supply credentials and optional
elevation at run time:

    hosts:
      box:
        provider: "{{.provider}}"
        backend: ssh
        address: "{{.address}}"

    testease doctor upgrade --strict \
      --param provider=static --param address=192.0.2.10 \
      --instrument-collect '/opt/instrument collect' \
      --instrument-diff '/opt/instrument diff'
    testease run upgrade \
      --param provider=static --param address=192.0.2.10 \
      --ssh-user operator --ssh-key ~/.ssh/id_ed25519 --ssh-sudo \
      --instrument-collect '/opt/instrument collect' \
      --instrument-diff '/opt/instrument diff'

`--ssh-sudo` means noninteractive `sudo`; the resolved target records that
elevation. Static media keeps the source file's executable intent, so pinned
collectors can run after staging. A supplied fingerprint instrument gives a
static host the same fingerprint interface as Docker. Strict doctor
checks only providers in the resolved scenario scope, so a static-only run
does not require an unrelated local Docker daemon.

Systems Manager carries small files inline. For installers and other large
declared inputs, set `--ssm-transfer-store s3://bucket/prefix` (or
`TESTEASE_SSM_TRANSFER_STORE`). Testease uploads the already verified input to
a random temporary object, has the target download it, verifies its SHA-256 on
the target, and removes the temporary object immediately. The run records the
input hash, not the signed URL or local path. Both inline and large-file paths
create a missing destination directory before writing. A Windows target hash
uses terminating PowerShell errors and retains one diagnostic line, so a
missing or unreadable staged path cannot collapse into “zero digest lines.”

For a static target, a declared input of at least 64 MiB with a SHA-256 may
already be present at its destination (for example, an installer retained in a
short-lived VM image). Testease hashes that resident file first and skips the
network transfer only when the digest matches exactly. The local input remains
resolved in the record, and the matching target hash is logged; missing or
different resident bytes take the ordinary copy-and-verify path.

Artifacts come back through the same output-limited SSM channel. Files at least
128 KiB are first gzip-compressed into a random target-side temporary file; the
compressed bytes are fetched in bounded chunks, expanded locally, and accepted
only when they reproduce the original byte count. The temporary file is removed
on every outcome. Compression uses no bucket access from the target and is
skipped when it would not reduce the transfer.

For a Linux Docker target, declared media is a bind mount and its source path
belongs to the Docker daemon's machine. With `--docker-context` pointing at a
remote daemon, mirror the media there or use a shared filesystem path before
running. Doctor asks that selected daemon to mount every source and compares
its file/directory type with the runner's copy when both exist. It therefore
rejects a path that exists only here and the empty directory Docker's `-v`
shorthand otherwise creates for a missing remote file. Scenario-specific
bootstrap scripts belong beside the scenario so the cache contents and hashes
are reproducible rather than assumed.

A carry step transfers bytes from a persistent source host to another host and
records the byte count and SHA-256 it observed in flight:

    - label: carry the backup
      host: source
      carry: {from: /fixture.dump, to_host: destination, to: /fixture.dump}

The runner and `doctor` derive this operation's requirements per host: the
source must have a session, and the destination must have either a session or
the `carry` capability. They refuse an incompatible topology before the first
step, even when the heterogeneous scenario cannot express those requirements
with the scenario-wide `requires` list. A carry accepts `timeout` and `expect`;
probe-only `args`, `env`, `media`, `fingerprint`, and `control` fields are
rejected rather than ignored.

For a one-shot `hyperv` destination, Testease retains those bytes and stages
them after `docker create` and before `docker start`. Use
`--win-docker-context` (or `TESTEASE_WIN_DOCKER_CONTEXT`) when that destination
lives on a different daemon from ordinary Docker hosts. Doctor checks each
image on the daemon that will actually run it, and the record pins both Docker
contexts. The local retained copy stays mode `0600` in a run-owned `0700`
runtime directory with `owner.json`; normal release removes it, and `testease
reap --force` can identify it after a killed runner. Docker sources stream
directly into the destination with byte counting and SHA-256 computed in
flight. SSH, SSM, and WinRM fetches may still buffer; use a Docker source for
multi-gigabyte carries until those transports gain equivalent streaming
readers.

New records distinguish two scenario identities. `hash_version: runtime-v2`
names the hash over `scenario.yaml` and executable probe inputs, while
`tree_hash` and `files` pin every file in the scenario checkout. Documentation
or a `suite.sh` citation therefore changes complete-tree provenance without
pretending that different measurement code executed. Older records have no
hash-version field and retain the original all-files hash semantics.

Large or separately owned inputs do not belong in the scenario tree. A
scenario names them and mounts them by logical name:

    inputs:
      suite:   {kind: git-archive}
      license: {kind: file}
    steps:
      - label: drive the suite
        host: app
        probe: drive://suite-image:version
        media: ["@suite:/inputs/suite.tar:ro"]

One descriptor binds those names for a run. Paths are relative to the
descriptor; SHA-256 identifies the bytes, and a Git archive also pins its
embedded commit marker:

    schema: 1
    inputs:
      suite:
        path: browser-suite.tar
        sha256: sha256:...
        git_commit: 0123456789abcdef...
      license:
        path: license.json
        sha256: sha256:...

    testease doctor my-scenario --input-bundle /staging/inputs.yaml
    testease run    my-scenario --input-bundle /staging/inputs.yaml

A drive step may use the same Docker context as a target on another machine.
Testease copies the scenario, a fresh workspace, and the drive step's declared
media through that context, runs the drive container on the target's network,
then copies facts, reports and declared artifacts back before deleting the
container. Doctor performs a copy round-trip first. A failed transfer marks the
step unmeasured. It cannot produce a passing result. The remote daemon
therefore needs no shared filesystem and cannot silently substitute an empty
directory for a runner-local suite or licence path.

Verified inputs are copied by content hash beneath the runs directory before
Docker sees them. A session target hashes mounted media again before step 1.
For a local drive, `doctor` also reads a canary through the real bind mount, so
put the runs directory somewhere the daemon can see (a snap-packaged daemon
commonly cannot see the host's `/tmp`). A remote drive uses the copy transport
above instead of relying on the daemon to see that directory.

A forward-only compatibility matrix can encode each supported pair as one
axis. This avoids manufacturing backward and same-version cells merely to
discard them later. The source and target remain available to host and step
templates:

    params:
      crossing: {values: "5.0.2-to-6.0.3,6.0.3-to-6.2.4", axis: true}
    hosts:
      box:
        provider: docker
        image: "app-under-test:{{crossingTarget .crossing}}"
        media: ["@fixture-{{crossingSource .crossing}}:/fixture.dump:ro"]
    steps:
      - label: restore the source fixture
        host: box
        probe: probes/restore.ps1
        args: ["{{crossingSource .crossing}}"]

`crossingSource` and `crossingTarget` accept exactly
`<source>-to-<target>`. A malformed cell is refused before its target is
created, and the resolved image is recorded with the target request. `doctor`
resolves the same declared or `--matrix` scope as `run` and checks every unique
derived image and media source, so it checks the full set of runs rather than
an arbitrary representative cell. A templated named input must resolve to one
of the manifest's declared inputs; the input bundle supplies and hashes that
finite set before any cell starts.

For restore censuses, `restore-matrix@1` turns the low-level readings into the
answer a release team needs. Each cell says `restored`, `hang`,
`source-data-missing`, `target-not-replaced`, or `unverified`; the census says
`restored` only when every measured cell did. The judge first checks the
scenario's declared preconditions, then independently derives the outcome from
`restore.timed_out`, `source.version`, `marker.rung`, and `sentinel.count`. It
does not trust the probe's own `restore.outcome` label. That separation means a
saved record can be re-judged and the team's conclusion does not depend on the
script correctly grading itself.

    testease judge --judge restore-matrix scenario-runs/win-restore-...

    restore-matrix@1  restore-failed
      hang            5.0.2-to-5.0.3
                      the restore exceeded its recorded time limit

For upgrade censuses, `release-readiness@3` keeps the measurement result
separate from the application result. `matrix@3 pass` means every declared
precondition and evidence step measured correctly. The release judge then
requires the requested package, an active service, a usable HTTP response, a
target-compatible database schema, and an installed payload that matches the
RPM with no unowned code. Schema compatibility follows the release line: Flyway
numbers migrations `<major>.<minor>.<patch>.<n>`, so target 7.2.3 is satisfied
by 7.2.3.3 and the four-part hotfix target 7.3.2.1 by 7.3.2.11. When launcher and process-transition readings are
present, it uses them to distinguish a manager handoff failure. Its census verdict is
`release-ready` only when every cell is ready; product failures make it
`release-blocked`, while missing evidence or a failed premise is `unverified`.

    testease judge --judge release-readiness scenario-runs/upgrade-sweep-...

    release-readiness@3  release-blocked
      manager-transition-failed  3.0.5
                      the target systemd launcher is absent
                      no target JVM is running
                      the legacy JVM remains running instead

This second verdict is appended beside the matrix verdict and reads only the
saved record. Re-judging does not rerun or modify the target.

A scenario may also declare which readings should repeat and the projection a
team should see:

    reproducibility:
      stable_facts: [suite.commit, target.version, evidence.sha256]
    views:
      team:
        kind: facts
        facts: [suite.commit, target.version, evidence.sha256]

After two independent runs, Testease can reach and retain the cross-record
conclusion without changing either source record:

    testease reproduce RUN_A RUN_B
    testease render --preset team RUN_A RUN_B -o html > evidence.html

The reproduction result records both `events.jsonl` hashes under
`scenario-runs/comparisons/`. The named view selects recorded values; it does
not add or change results.

For matrix runs, pass the two top-level run IDs the same way. Testease matches
cells by matrix key, compares each cell's declared stable facts only with the
same cell in the other run, and pins both parent and child event streams in the
comparison verdict. A changed matrix scope is therefore unverified rather than
silently comparing unlike rows.

Credentials supplied before a drive still use `--env-file`. When a target must
generate a secret file for a later drive step, the scenario declares a
run-scoped handoff instead of passing a workstation path as a parameter:

    secret_files:
      actors: {}
    hosts:
      app:
        provider: docker
        media: ["%actors:/run/testease/actors.env"]
    steps:
      - label: drive as the generated actors
        probe: drive://suite-image:version
        media: ["%actors:/inputs/actors.env:ro"]

The Docker target gets one read-write mount and drive consumers get read-only
mounts. Testease records the logical name, never the runtime path or contents,
and removes the mode-0600 workspace after releasing the target. A hard-killed
process may leave one behind; `testease reap` identifies it by user, host and
PID and removes it only with `--force` after proving that process is gone.

An application that already exists and should only be driven over HTTP is an
endpoint target. Its origin is explicit and may not contain credentials, a
path, query, or fragment:

    requires: [endpoint]
    hosts:
      app:
        provider: endpoint
        origin: "https://app.example.test"
    steps:
      - label: observe the application
        host: app
        probe: drive://suite-image:version

The record keeps the requested origin, resolved DNS addresses and, for HTTPS,
the leaf-certificate identity and verification result. The driven suite must
record application version/build identity because the transport cannot infer
it honestly. An endpoint target has no execution session and Testease never
creates or removes it.

## What is in a record

A record is a directory. `events.jsonl` is the evidence - append-only, never
rewritten, one JSON object per line. Everything else is derived from it and can
be thrown away:

    events.jsonl     the record. This is the thing.
    run.json         a cache of it, rebuildable with `render --view meta`
    steps/           each step's stdout and stderr
    artifacts/       files a step declared it wanted kept
    verdicts/        what a judge concluded, written once per judge@version
    cells/           for a census, one record per cell, same shape

Repairs are read-time and reported. A damaged line is never fixed on disk; the
reader says what it had to tolerate and every projection repeats it.

## The commands

### Common commands

    validate   check scenario files without running them.
    plan       show resolved steps and matrix cells without running them.
    doctor     check what a run needs, before the run.
    run        measure. Writes a record; reaches no conclusion.
    judge      read a record and reach a verdict, without re-measuring.
    render     project a record: tsv, table, rows for a census, facts across
               several runs, meta to rebuild run.json. -o json or html.
    scenarios  the scenarios under the scenario root.
    runs       the records under the runs directory.
    backends   what a host can be, and what each kind can do.

### Other commands

    campaign validate  check a campaign against its scenario without running hooks.
    reproduce  compare stable facts across runs.
    campaign   run and inspect a group of related runs.
    follow     show campaign progress.
    package    write an archive of campaign records.
    publish    copy cited run records to a shared directory.
    reap       remove targets whose run is provably over.
    import     read a run.tsv written before testease into a record.
    targets    the targets standing right now, and how to get onto them.

Operational commands take `-o json`. The same exit codes are used at a terminal
and in CI:

    0   measured, and any verdict was in its judge's pass set
    2   usage
    3   a precondition the person running it fixes
    4   integrity: a locked record, or a judge disagreeing with itself
    10  the substrate failed
    11  a probe failed
    12  interrupted
    20  a verdict outside its judge's pass set
    21  nothing could be judged

## Configuration

There is no config file. Flags are the interface; environment variables set
operator defaults and connection setup you would otherwise retype. Precedence is
`flag > TESTEASE_* > built-in default`.

Values that change what a run measures belong on the command line so the record
can show them. Use environment variables for paths and other defaults.

Put this in your shell profile, or source it explicitly:

    export TESTEASE_SCENARIO_ROOT=/path/to/scenarios
    export TESTEASE_RUNS_DIR=/path/to/scenario-runs

These environment variables set paths and defaults:

    TESTEASE_SCENARIO_ROOT   scenario tree            TESTEASE_RUNS_DIR      where records go
    TESTEASE_BASELINES       baseline judge's store   TESTEASE_FINDINGS      findings used by publish
    TESTEASE_RUNS_REPO       repository used by publish TESTEASE_LOG_LEVEL   debug/info/warn/error
    TESTEASE_LOG_FORMAT      text or json             TESTEASE_STATEFILE_DIR provisioner state
    TESTEASE_INPUT_BUNDLE    named non-secret input descriptor
    TESTEASE_RUNS_URL        where published records are readable
    TESTEASE_TARGET_CATALOG  target names and tags
    TESTEASE_CAMPAIGN_ROOT   campaign definitions
    TESTEASE_AWS_PROFILE     AWS profile for Systems Manager hosts
    TESTEASE_AWS_REGION      AWS region for Systems Manager hosts
    TESTEASE_SSM_TRANSFER_STORE temporary store for large SSM media
    TESTEASE_WINRM_USER      user for hosts dialled over WinRM
    TESTEASE_WINRM_PASSWORD_FILE password file for WinRM hosts
    TESTEASE_SSH_USER        user for hosts dialled over SSH
    TESTEASE_SSH_PASSWORD_FILE password file for SSH hosts
    TESTEASE_SSH_KEY         private key for SSH hosts
    TESTEASE_SSH_KNOWN_HOSTS known_hosts file for SSH hosts

Pass these as flags because they change what is measured, recorded, or judged:

    TESTEASE_STORE              the artifact store a @store: axis discovers from
    TESTEASE_DOCKER_CONTEXT     which daemon holds the box
    TESTEASE_WIN_DOCKER_CONTEXT which daemon holds one-shot Windows cells
    TESTEASE_INSTRUMENT_NAME    what to record the instrument as
    TESTEASE_INSTRUMENT_COLLECT the fingerprint instrument
    TESTEASE_INSTRUMENT_DIFF    and its comparison
    TESTEASE_WIN_MEMORY         what a one-shot Windows cell gets
    TESTEASE_ENV_FILE            secrets injected into drive steps
    TESTEASE_COMPARE             baseline comparison command

These flags cannot come from environment variables: `--param`, `--matrix`,
`--resume`, `--keep`, `--par`, and `--promote`. They change the run or what a
rerun means. Keep `TESTEASE_ENV_FILE`, `TESTEASE_INSTRUMENT_NAME`, and
`TESTEASE_COMPARE` explicit for the same reason: they change injected inputs,
measurement provenance, or the verdict command.

Long runs print progress on stderr without copying probe output.
Use `--progress=plain|json|off`; `auto` (the default) follows the command's
text or JSON output mode. Target media is named by input name and size
while it is staged, with the same progress output as a long step. The append-only
record separately states when each item began and when its target-side presence
(and declared SHA-256, when applicable) was verified. Progress output is not
stored as run data.

`testease publish` is read-only by default. `--stage` copies cited records and
does not touch Git; `--commit` additionally commits exactly those copied run
directories, and `--push` pushes that commit. Each stronger action must be
named explicitly.

## Building

`make validate` is the gate: format, vet, lint, test under `-race`, and
`govulncheck`. Nothing is done until it is green, and it is never weakened to
get there. The first validation run downloads the pinned formatter, linter, and
vulnerability scanner into the ignored `.tools/` directory; they do not need to
be installed globally. The Go version comes from `go.mod`, so the Go toolchain
may be downloaded automatically by Go. `make build` produces the static trimmed
binary. To install it as `testease`, set `GOBIN` to a directory already on your
PATH before running `make install`.

Some tests need real substrate and skip without it - a docker daemon for the
provider tests, `TESTEASE_TEST_WINDOWS_CONTEXT` for the Windows probe. Skipping
is reported, never silent.
