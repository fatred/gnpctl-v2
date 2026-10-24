## ADDED Requirements
### Requirement: Configuration located via GNPCONFIG

The system SHALL load a single YAML configuration file whose path is given by the `GNPCONFIG`
environment variable. When `GNPCONFIG` is unset, the system SHALL fall back to a default path
(`./gnpctl.yaml`). When the resolved file is missing or fails to parse, the command SHALL fail fast
with a clear error naming the path, before performing any work.

#### Scenario: Config path from the environment

- **WHEN** `GNPCONFIG` is set to a readable config file
- **THEN** gnpctl loads its configuration from that file

#### Scenario: Default path when unset

- **WHEN** `GNPCONFIG` is unset and `./gnpctl.yaml` exists
- **THEN** gnpctl loads its configuration from `./gnpctl.yaml`

#### Scenario: Missing or invalid config fails fast

- **WHEN** the resolved config path does not exist or contains invalid YAML
- **THEN** the command exits with a clear error naming the path and performs no work

### Requirement: Configuration contents

The configuration file SHALL provide the path to the SQLite state database, the path to the gNMI
path catalog directory (the YAML tree), the log destination, and the log level. Every command SHALL
resolve the state database and the catalog from this configuration.

#### Scenario: Core settings parsed

- **WHEN** a valid config declares the database path, the catalog directory, a log destination, and a log level
- **THEN** the loaded configuration exposes all four, and commands use the database and catalog paths from it

### Requirement: Command-line overrides

Command-line flags SHALL override the corresponding configuration values; the configuration provides
the defaults. At minimum `--state` SHALL override the database path and `--catalog` SHALL override
the catalog directory.

#### Scenario: Flag overrides config

- **WHEN** the config sets a catalog directory and a command is run with `--catalog <other>`
- **THEN** the command uses `<other>` and ignores the config's catalog value for that run

### Requirement: Logging from configuration

The system SHALL configure logging to the configured destination at the configured level. When the
log destination is omitted it SHALL log to standard error, and when the level is omitted it SHALL
default to `info`.

#### Scenario: Logs honor configured destination and level

- **WHEN** the config sets a log destination and a level of `debug`
- **THEN** gnpctl writes log records to that destination at `debug` level

### Requirement: Data-plane render configuration

The gnpctl configuration SHALL provide an optional `render` block carrying the
output directory (`output_dir`), the NATS connection (`nats.address`), and the
Prometheus `remote_write` URL (`prometheus.remote_write_url`). These are consumed by
`gnpctl render`. The block is optional at config load (other commands do not require
it); `gnpctl render` SHALL require it and fail with a clear error when it is absent.

#### Scenario: Render settings exposed from config

- **WHEN** the gnpctl config declares a `render` block with `output_dir`, `nats.address`, and `prometheus.remote_write_url`
- **THEN** the loaded config exposes those render settings

#### Scenario: Render config optional for non-render commands

- **WHEN** the config omits `render` and a non-render command (e.g. `discover`) runs
- **THEN** the config loads and the command proceeds without error

### Requirement: Render deployment settings

The `render` config block SHALL accept deployment settings, all optional with
production-reference defaults so a minimal `render` (output dir + NATS address + Prometheus
URL) still renders a working pipeline: `target_device_domain` (used to build the FQDN
`source`); a `gnmic` sub-block with `skip_verify` (default `false`), `tls_ca`,
`targets_file`, `instance`, and `api_server`; `nats.output` and `nats.input` jetstream
tuning; and `prometheus` output tuning. Credentials SHALL NOT appear in config — they are
environment references resolved at gnmic runtime.

#### Scenario: Minimal render block still loads

- **WHEN** a `render` block sets only `output_dir`, `nats.address`, and `prometheus.remote_write_url`
- **THEN** the config loads and the omitted deployment/tuning fields take their defaults

#### Scenario: skip_verify is an exposed option defaulting to false

- **WHEN** `render.gnmic.skip_verify` is omitted
- **THEN** it resolves to `false`; setting it `true` is accepted (for environments without a trusted CA)

#### Scenario: Tuning fields are overridable

- **WHEN** `render.nats.input.fetch_batch_size` (or any tuning field) is set
- **THEN** the rendered config uses the override; unset tuning fields use their defaults

### Requirement: skip_verify must not contradict tls_ca

Config loading SHALL reject a `render.gnmic` block that sets `skip_verify: true` together with a
`tls_ca` path, raising a clear `ConfigError` that points at `tls_ca` as the secure escape hatch.
The two are contradictory — a CA bundle is the escape hatch used *instead of* disabling
verification — and silently honoring both ships a config that disables the device-certificate
check while appearing to pin a CA. An effective `skip_verify: true` with `tls_ca` unset SHALL
remain accepted (the escape hatch for an untrusted-CA environment), but SHALL be surfaced rather
than applied silently.

#### Scenario: Contradictory skip_verify + tls_ca rejected

- **WHEN** a config sets both `render.gnmic.skip_verify: true` and `render.gnmic.tls_ca`
- **THEN** config loading fails with a `ConfigError` naming the contradiction and pointing at `tls_ca` as the secure escape hatch, and no pipeline is rendered

#### Scenario: skip_verify without tls_ca still accepted

- **WHEN** `render.gnmic.skip_verify: true` is set and `tls_ca` is unset
- **THEN** the config loads (the escape hatch for an environment without a trusted CA) and the disabled-verification choice is surfaced rather than silent

