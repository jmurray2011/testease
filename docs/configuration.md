# Configuration

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

## Progress output

Long runs print progress on stderr without copying probe output.
Use `--progress=plain|json|off`; `auto` (the default) follows the command's
text or JSON output mode. Target media is named by input name and size
while it is staged, with the same progress output as a long step. The append-only
record separately states when each item began and when its target-side presence
(and declared SHA-256, when applicable) was verified. Progress output is not
stored as run data.

## Publishing

`testease publish` is read-only by default. `--stage` copies cited records and
does not touch Git; `--commit` additionally commits exactly those copied run
directories, and `--push` pushes that commit. Each stronger action must be
named explicitly.
