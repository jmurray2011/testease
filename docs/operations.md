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

## Independent scenario batches

A batch lists tests that can run independently. Its `parallel` value bounds
the number of runs in flight. When omitted, all listed tests may start
together. Each run gets its own record and target lifecycle; a failure in one
does not suppress the others. Give entries distinct `name` values when the
same scenario runs against different parameters or targets; without `name`,
the scenario name is used. A batch-specific suffix keeps their run IDs distinct.

```yaml
schema: 1
name: smoke-set
parallel: 3
tests:
  - name: install-old
    scenario: install-check
    target: vm-lane
    target_catalog: private-targets.yaml
    params: {version: "1.2.3"}
  - name: install-new
    scenario: install-check
    target: second-vm-lane
    target_catalog: private-targets.yaml
    params: {version: "1.2.4"}
  - scenario: api-check
    target: container-lane
    target_catalog: private-targets.yaml
  - scenario: upgrade-check
    target: second-vm-lane
    target_catalog: private-targets.yaml
    input_bundle: private-inputs.yaml
```

```text
testease batch validate smoke-set.yaml
testease batch doctor smoke-set.yaml --strict
testease batch run smoke-set.yaml
testease batch judge <batch-id>
testease batch status <batch-id>
```

Validation checks every scenario and selected catalog target without checking
runtime requirements. `batch doctor` checks every entry's images, media, input
bundle, and provider prerequisites, reporting all entries even when one fails.
`batch run` performs strict preflight before creating a batch record or starting
any child. Catalog and input bundle paths in the file are relative to the current
working directory. Docker media bind sources must be absolute host paths after
scenario parameters resolve. For portable media bindings, use `path_params`
instead of `params` for path valued scenario parameters, or `input_bundle_path`
instead of `input_bundle`. These two fields resolve relative to the batch YAML
file, including `..` segments, and pass absolute paths to doctor and run. For
example, a batch at `batches/install.yaml` can use
`path_params: {store: ../batch-media/package-a}`. The original YAML is snapshotted as
`batch.yaml`, and the absolute values are saved under `path_bindings` in
`batch.json`. A test may also set `aws_profile`, `aws_region`, or
`release_failed_targets`. The batch record and separate child logs live under
`RUNS_DIR/batches/<batch-id>`; child run records stay in `RUNS_DIR`. Live child
progress names its test entry. The batch exits nonzero if any child run fails.
`batch judge` judges every measured child using its saved record's declared judge
when present, or the normal `expect` default. It saves a separate
`judgment.json`, continues after a failed verdict,
and exits nonzero unless every child passes. `batch status` shows measurement
and judgment together; `-o json` includes the saved judgment when present.
Entry names must be distinct, including names inferred from scenarios. For a
single scenario whose values form an axis and need one matrix verdict, use its
matrix and `testease run --par`.

`parallel` counts test entries. A scenario with several hosts can have several
targets live; each child matrix runs one cell at a time. Plan AWS capacity
using the total target count.

## Provisioning EC2 and ECS targets

A scenario selects a provider and image. Keep the scenario independent of the
AWS account by putting launch settings in the selected target catalog's
`provision` map. The `params` map remains for declared scenario parameters.
Catalog entries may contain an AWS profile *name*, region, and resource IDs;
keep credentials outside the catalog.

An EC2 host uses `provider: ec2`, `backend: ssm`, and an AMI ID as its `image`.
The catalog entry for that host needs `ec2.instance_type`, `ec2.subnet_id`, and
`ec2.instance_profile`. `ec2.security_group_id` is optional. The AMI needs an
SSM agent and an instance profile that permits Systems Manager. Testease
records the instance ID and AMI, waits for SSM registration, then opens the
ordinary SSM session for probes and media. `boot_timeout` can raise the
three-minute registration budget. Each matrix cell gets its own instance;
`--par` controls how many can run at once.

An ECS host uses `provider: ecs` and an ECR image reference as its `image`.
Its catalog entry needs `ecs.cluster`, `ecs.task_definition`,
`ecs.subnet_id`, and `ecs.security_group_id`. Set `ecs.assign_public_ip` to `enabled` only when
the subnet needs it. Register the Fargate task definition
before running: it must name exactly one container with the same ECR image,
`awsvpc` networking, a `/bin/sh -c` entry point, and `awslogs` with a log
group and stream prefix. Each probe launches one task; the record captures its
task ARN, exit code, logs, facts, and declared fetched artifacts. This
one-shot target has no persistent session or carry surface, and the task
definition remains operator managed.

For example, a private catalog entry can bind non-secret launch settings:

```yaml
schema: 1
targets:
  - name: aws-ec2-fixture
    tags: [os:linux, lifecycle:ephemeral]
    provision:
      aws.profile: operator-profile
      aws.region: us-east-1
      ec2.instance_type: t3.small
      ec2.subnet_id: subnet-example
      ec2.instance_profile: ssm-instance-profile
```

```text
testease doctor my-scenario --strict --target aws-ec2-fixture \
  --target-catalog targets.yaml
testease run my-scenario --target aws-ec2-fixture \
  --target-catalog targets.yaml
```

The selected AWS profile and region also apply to SSM. When flags and catalog
settings both name them, they must agree. A failed run retains its target for
inspection by default; `--release-failed-targets` opts into removal. `--keep`
retains targets after a clean run. Review the record's target identity and
disposition before manual cleanup.

`testease reap --platform ec2` and `testease reap --platform ecs` list native
resources belonging to the selected runner. Supply `--aws-profile` and
`--aws-region` (or their `TESTEASE_AWS_*` defaults); `--runner` defaults to the
local runner identity, and ECS also needs `--ecs-cluster`. Pass `--runs-dir`
pointing at the original records. The command only offers a remote target for
removal when its native labels match the run record's resource ID, run, host,
cell, and durable release intent, and no writer still holds that record.
`--force` performs those proved removals. A retained target is never aged out
implicitly.

```text
testease reap --platform ec2 --aws-profile operator-profile \
  --aws-region us-east-1 --runner operator@workstation --runs-dir runs
testease reap --platform ec2 --aws-profile operator-profile \
  --aws-region us-east-1 --runner operator@workstation --runs-dir runs --force
```

### Cloud smoke scenarios

The account-neutral scenarios under `testdata/cloud-smoke` exercise both
provisioned surfaces. They require their artifacts, so a missing transfer leaves
the run `infra-fail` even if the probe exits zero. Run them explicitly against
catalog entries in the intended account. First check the profile's account:

```sh
aws sts get-caller-identity --profile "$AWS_PROFILE" --region "$AWS_REGION"
```

For EC2, make a 4 KiB payload and use a catalog entry with the EC2 settings
above. The selected AMI needs `sha256sum`, `wc`, `cut`, and `tr` in addition to
the SSM agent. Use an empty runs directory or a fresh run ID for each trial:

```sh
head -c 4096 /dev/zero > /tmp/testease-smoke-payload.bin
testease doctor ec2-media --strict --scenario-root testdata/cloud-smoke \
  --param image="$EC2_AMI" --param payload=/tmp/testease-smoke-payload.bin \
  --target ec2-smoke --target-catalog "$EC2_CATALOG" \
  --aws-profile "$AWS_PROFILE" --aws-region "$AWS_REGION"
testease run ec2-media --scenario-root testdata/cloud-smoke \
  --param image="$EC2_AMI" --param payload=/tmp/testease-smoke-payload.bin \
  --target ec2-smoke --target-catalog "$EC2_CATALOG" \
  --aws-profile "$AWS_PROFILE" --aws-region "$AWS_REGION" \
  --runs-dir "$RUNS_DIR" --release-failed-targets
```

For ECS, use a catalog entry with the ECS settings above and a matching
single-container Fargate task definition. The image needs `/bin/sh`, `head`,
`sha256sum`, `wc`, `cut`, and `tr`:

```sh
testease doctor ecs-artifact --strict --scenario-root testdata/cloud-smoke \
  --param image="$ECS_IMAGE" --target ecs-smoke \
  --target-catalog "$ECS_CATALOG" \
  --aws-profile "$AWS_PROFILE" --aws-region "$AWS_REGION"
testease run ecs-artifact --scenario-root testdata/cloud-smoke \
  --param image="$ECS_IMAGE" --target ecs-smoke \
  --target-catalog "$ECS_CATALOG" \
  --aws-profile "$AWS_PROFILE" --aws-region "$AWS_REGION" \
  --runs-dir "$RUNS_DIR" --release-failed-targets
```

After each run, pass the printed record directory to `testease judge`. The
judge writes an `expect@3` verdict; the fetched file is under the record's
`artifacts/` directory. The ECS file should contain 70,000 zero bytes with
SHA-256 `f51b279903037b37ea1828a1021499995718d38016cad6c0da30962a41be052f`.
The EC2 artifact contains the staged payload's SHA-256. Keep account IDs,
subnets, role names, and task definition ARNs in the private catalogs.

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
For a local Docker socket, doctor checks that the source exists on this machine
before mounting it, since some socket integrations create missing bind sources
as empty directories even with `--mount`.

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
embedded commit marker. A public object can instead use `url` with an HTTPS
address, including a public S3 object URL. The same SHA-256 check applies,
and a verified cached copy under the runs directory can be reused offline.
`path` and `url` are mutually exclusive; URLs cannot contain credentials or
query strings. HTTP is accepted only for localhost object stores:

    schema: 1
    inputs:
      suite:
        path: browser-suite.tar
        sha256: sha256:...
        git_commit: 0123456789abcdef...
      license:
        path: license.json
        sha256: sha256:...
      public-rpm:
        url: https://example-bucket.s3.amazonaws.com/public/package.rpm
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
