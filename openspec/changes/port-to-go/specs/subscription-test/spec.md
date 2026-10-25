## ADDED Requirements
### Requirement: test validates matched catalog paths

`gnpctl test` SHALL, for each selected target, resolve its catalog key to the **effective**
(version-layered) profile, collect the subscription paths of the modules that are **enabled** for
that target (effective module enum `>= 1`), and validate each path with a gNMI `Subscribe` in ONCE
mode (`subscribe_once`), reporting per-path success or failure (with a sample value on success or the
error on failure). Subscribe-ONCE SHALL be used rather than `Get` because it exercises the same
subscription mechanism the renderer targets and because some platforms (e.g. Junos) serve `Get` for
configuration only. By default it SHALL select targets where `discovered = true`; a `--target <name>`
flag SHALL scope it to one target.

#### Scenario: Per-path results reported for a discovered target

- **WHEN** `gnpctl test` runs against a discovered target whose effective profile has three enabled paths
- **THEN** it issues a Subscribe-ONCE for each path and reports ok or fail per path

#### Scenario: Disabled module paths are not tested

- **WHEN** a target's `bgp-peers` module is disabled (effective enum `< 1`)
- **THEN** `gnpctl test` does not subscribe to that module's paths

#### Scenario: Scope to a single target

- **WHEN** `gnpctl test --target leaf1` runs
- **THEN** only `leaf1` is tested

### Requirement: test skips targets with no catalog key

When a selected target has no matched catalog key (undiscovered or unknown), `gnpctl test` SHALL
report it as not-ready and SHALL NOT attempt any gNMI read for it.

#### Scenario: Not-ready target skipped

- **WHEN** test selects a target whose catalog key is null
- **THEN** it reports that target as not-ready and performs no Subscribe-ONCE calls for it

### Requirement: test exit status

`gnpctl test` SHALL exit non-zero when any attempted path check failed, and zero when every attempted
path check succeeded.

#### Scenario: Non-zero exit on a failed path

- **WHEN** at least one path's Subscribe-ONCE returns an error during a test run
- **THEN** the process exits non-zero

#### Scenario: Zero exit when all paths succeed

- **WHEN** every attempted path's Subscribe-ONCE succeeds
- **THEN** the process exits zero

### Requirement: test is a gnpctl subcommand

The CLI SHALL expose `test` as a subcommand of the top-level `gnpctl` command group, accepting an
optional `--target`. The state database and catalog directory SHALL come from the gnpctl
configuration, with optional `--state`/`--catalog` overrides.

#### Scenario: test invoked through the command group

- **WHEN** `gnpctl test` is invoked and the gnpctl config provides the catalog directory and state database
- **THEN** the command resolves the effective profile's enabled paths for the selected targets and runs the per-path Subscribe-ONCE checks

