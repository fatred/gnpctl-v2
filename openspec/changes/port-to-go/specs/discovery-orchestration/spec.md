## ADDED Requirements
### Requirement: discover reads targets from the database

`gnpctl discover` SHALL select targets from the targets table according to its mode and run the
discovery pipeline for each selected target: connect, fetch `Capabilities`, fingerprint to a
catalog key, resolve make/model/version via the entry's detail probes, and enrich the target row.

#### Scenario: Selected target is fingerprinted and enriched

- **WHEN** discover selects a reachable target that matches a catalog entry
- **THEN** the target row is updated with `discovered = true`, a stamped `last_discovery_run`, the matched catalog key, and the resolved fingerprint fields

### Requirement: Effective gNMI port resolution

When connecting to a target, discover SHALL determine the effective port with the precedence:
(1) the target's port if set; (2) the `default_port` of the catalog entry whose `platform_hints`
include the target's platform hint; (3) the global default gNMI port `57400` (the gNMI convention,
matching gnmic). Because the transport (the gNMI client library) applies no default of its own, discover SHALL always
pass an explicit port and SHALL persist the resolved port on the target row.

#### Scenario: Explicit target port is used

- **WHEN** a selected target already has a port set (e.g. from a NetBox application Service)
- **THEN** discover connects on that port and does not override it

#### Scenario: Vendor default applied by platform hint

- **WHEN** a selected target has no port but its platform hint maps to a catalog entry with `default_port: 6030`
- **THEN** discover connects on 6030 and persists 6030 as the target's port

#### Scenario: Global default when nothing else resolves

- **WHEN** a selected target has no port and no catalog entry's `platform_hints` match its platform hint
- **THEN** discover uses the global default port `57400`, connects on it, and persists it

### Requirement: Target-selection modes

With no flags, discover SHALL select targets where `discovered = false`. With `--periodic`, it
SHALL select targets where `discovered = true` AND `last_discovery_run < now - 1h` (the staleness
window defaulting to 1 hour). With `--force`, it SHALL ignore both fields and select all targets,
optionally narrowed by `--target <name>`.

#### Scenario: Default mode selects undiscovered targets

- **WHEN** `gnpctl discover` runs with no flags
- **THEN** only targets with `discovered = false` are selected

#### Scenario: Periodic mode selects stale discovered targets

- **WHEN** `gnpctl discover --periodic` runs
- **THEN** only targets with `discovered = true` and `last_discovery_run` older than one hour are selected

#### Scenario: Force mode ignores both fields

- **WHEN** `gnpctl discover --force` runs
- **THEN** targets are selected regardless of their `discovered` flag or `last_discovery_run` value

### Requirement: Per-target error isolation

A failure on one target SHALL NOT abort the run. A transport or connection error SHALL be recorded
against that target (`last_error`) without advancing `last_discovery_run` or setting `discovered`,
so the target is retried by the default mode, and the loop SHALL continue with the remaining
targets.

#### Scenario: One unreachable target does not stop the run

- **WHEN** three targets are selected and the second is unreachable
- **THEN** the first and third are discovered and enriched, and the second records a `last_error` with `discovered` left false

### Requirement: Unknown fingerprint completes discovery

When a reachable target's capabilities match no catalog entry, discover SHALL stamp
`last_discovery_run`, set `discovered = true`, and leave the catalog key null, so the target is not
re-selected by the default mode but is re-evaluated by `--periodic`.

#### Scenario: Unknown device recorded without a catalog key

- **WHEN** a reachable target matches no catalog entry
- **THEN** its row has `discovered = true`, a stamped `last_discovery_run`, and a null catalog key

### Requirement: Run summary and exit status

At the end of a run the system SHALL emit a summary reporting the count of targets that were
`matched`, `unknown`, and `error`, and SHALL exit non-zero when any target errored and zero
otherwise.

#### Scenario: Summary counts reported

- **WHEN** a run processes two matched, one unknown, and one errored target
- **THEN** the summary reports matched=2, unknown=1, error=1 and the process exits non-zero

### Requirement: discover is a gnpctl subcommand

The CLI SHALL expose `discover` as a subcommand of the top-level `gnpctl` command group (alongside
`inventory` and `test`), accepting `--periodic`, `--force`, and `--target`. The state database and
catalog directory SHALL come from the gnpctl configuration, with optional `--state`/`--catalog`
overrides.

#### Scenario: discover invoked through the command group

- **WHEN** `gnpctl discover` is invoked and the gnpctl config provides the catalog directory and state database
- **THEN** the command loads the catalog, selects targets per its mode, runs discovery, and writes results to the state database

### Requirement: Module enablement resolution

On each successful discovery of a target that resolves to a profile, the system SHALL reconcile
every registry module's enablement column. For each module: if the current value is a force value
(`-1` or `2`), it SHALL be left unchanged; otherwise it SHALL be set to the resolved default — `1`
when the selected profile defines paths for that module and the effective default (the profile's
per-module `default` if present, else the registry global `default`) is on, and `0` otherwise.
Resolution SHALL run within the same atomic enrichment as the rest of discovery.

#### Scenario: Profile default applied to a non-forced module

- **WHEN** a target resolves to the `juniper-mx` profile and that profile enables `ports`
- **THEN** the target's `ports` module column is set to `1`

#### Scenario: Operator override preserved across discovery

- **WHEN** a target's `isis` column is `-1` (force-off) and the target is re-discovered
- **THEN** the `isis` column stays `-1` rather than being reset to the profile default

#### Scenario: Newly added module reconciles for matching profiles

- **WHEN** a new module (default on) is added and a previously-discovered target whose profile defines it is re-discovered
- **THEN** its column flips from the migrated `0` to `1`

#### Scenario: Module without profile paths resolves off

- **WHEN** the selected profile defines no paths for a module
- **THEN** that module's column resolves to `0` regardless of the global default

