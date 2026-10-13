# Running against real machines

The `hello` example runs on a local Docker daemon. Everything here is about
the rest: named fleets, machines that already exist, large media, bytes that
must cross hosts, secrets, and applications you may only drive over HTTP.

## Target catalogs

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

## Campaigns

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

## A machine that is already running

Declare the provider and connection details in the scenario, then supply
credentials and optional elevation at run time:

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

## Large media over Systems Manager

Systems Manager carries small files inline. For installers and other large
declared inputs, set `--ssm-transfer-store s3://bucket/prefix` (or
`TESTEASE_SSM_TRANSFER_STORE`). Testease uploads the already verified input to
a random temporary object, has the target download it, verifies its SHA-256 on
the target, and removes the temporary object immediately. The run records the
input hash, not the signed URL or local path. Both inline and large-file paths
create a missing destination directory before writing. A Windows target hash
uses terminating PowerShell errors and retains one diagnostic line, so a
missing or unreadable staged path cannot collapse into "zero digest lines."

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

## Media and a remote Docker daemon

For a Linux Docker target, declared media is a bind mount and its source path
belongs to the Docker daemon's machine. With `--docker-context` pointing at a
remote daemon, mirror the media there or use a shared filesystem path before
running. Doctor asks that selected daemon to mount every source and compares
its file/directory type with the runner's copy when both exist. It therefore
rejects a path that exists only here and the empty directory Docker's `-v`
shorthand otherwise creates for a missing remote file. Scenario-specific
bootstrap scripts belong beside the scenario so the cache contents and hashes
are reproducible rather than assumed.

## Carry steps

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

## Named inputs and input bundles

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

## Driving through a remote daemon

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

## Secret files generated during a run

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

## Endpoint targets

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
it. An endpoint target has no execution session and Testease never creates or
removes it.

## Scenario identity in records

New records distinguish two scenario identities. `hash_version: runtime-v2`
names the hash over `scenario.yaml` and executable probe inputs, while
`tree_hash` and `files` pin every file in the scenario checkout. Documentation
or a `suite.sh` citation therefore changes complete-tree provenance without
pretending that different measurement code executed. Older records have no
hash-version field and retain the original all-files hash semantics.
