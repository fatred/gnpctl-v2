## ADDED Requirements
### Requirement: SQLite targets table

The system SHALL persist state to a SQLite database in WAL mode whose primary table is `targets`.
Each target row SHALL carry identity (`name` unique, `address`, `port`, connection parameters,
platform hint, originating `source`), discovery state (`discovered` boolean default false,
`last_discovery_run` timestamp default epoch 0), the matched gNMI path catalog `key` (nullable),
fingerprint fields (`vendor`, `os`, `model`, `os_version`, `capabilities_hash`), and `last_error`.
This table is the read contract for the downstream renderer.

#### Scenario: Schema created on first use

- **WHEN** a command runs against a state database path that does not yet exist
- **THEN** the system creates the database with the `targets` table in WAL mode

#### Scenario: Defaults applied on insert

- **WHEN** a new target row is inserted by an inventory pull
- **THEN** its `discovered` is false and its `last_discovery_run` is epoch 0

#### Scenario: Renderer-facing fields present

- **WHEN** a target has been discovered and matched
- **THEN** its row exposes the catalog key, vendor, os, model, and os_version for the renderer to resolve the subscription

### Requirement: Source registry storage

The system SHALL persist registered inventory sources in an `inventory_sources` table with a unique
`name`, a `type`, type-specific `parameters`, a credential reference, a `last_poll` timestamp
(default epoch 0), and a `last_error` (nullable). Credentials SHALL NOT be stored in plaintext.

#### Scenario: Source persisted with poll-state defaults

- **WHEN** an inventory source is added
- **THEN** a row exists in `inventory_sources`, the source is returned by `gnpctl inventory list`, and its `last_poll` is epoch 0 with a null `last_error`

### Requirement: Atomic discovery enrichment

discover SHALL update a target's discovery-owned fields within a single transaction, so a reader
never observes a half-updated row. Re-discovering a target SHALL overwrite its prior fingerprint,
catalog key, `discovered`, and `last_discovery_run` values rather than create a second row.

#### Scenario: Enrichment is atomic and idempotent

- **WHEN** a target that was previously discovered is discovered again and resolves to a different catalog key
- **THEN** after the run the single target row carries the new catalog key and fingerprint, with no duplicate row

### Requirement: Connection errors preserve retry eligibility

When discover hits a transport or connection error for a target, the system SHALL record
`last_error` and SHALL NOT set `discovered` or advance `last_discovery_run`, leaving the target
eligible for the default selection mode.

#### Scenario: Errored target remains in the default selection set

- **WHEN** a target's discovery fails with a connection error
- **THEN** its `discovered` stays false and a later default-mode `gnpctl discover` re-selects it

### Requirement: Per-module enablement state

The `targets` table SHALL carry one enablement column per registry module, holding an integer enum:
`-1` (force-off), `0` (off), `1` (on), `2` (force-on). Schema initialization SHALL ensure a column
exists for every module in the registry, adding any missing column with default `0` so the schema
change is backwards-compatible. A module is effectively enabled (collected) when its value is
`>= 1`. The values `0` and `1` are discovery-owned; `-1` and `2` are operator overrides. The
target's `catalog_key` references the selected profile, whose `modules` map expands the enabled
module names to paths.

#### Scenario: New module column migrates at default 0

- **WHEN** a new module is added to the registry and the state database already exists
- **THEN** initialization adds that module's column with default `0`, leaving existing rows and other columns unchanged

#### Scenario: Effective enablement derives from the enum

- **WHEN** a target's module value is `1` (on) or `2` (force-on)
- **THEN** the module is reported enabled; when the value is `0` (off) or `-1` (force-off), it is reported disabled

#### Scenario: Renderer reads enabled modules

- **WHEN** the renderer reads a discovered target
- **THEN** it can determine the enabled modules (value `>= 1`) and expand each to its paths via the target's `catalog_key` profile

### Requirement: Override values survive reconciliation

A force value (`-1` or `2`) recorded on a module column SHALL be preserved across discovery runs;
only non-force values (`0`/`1`) are owned and overwritten by discovery. The column is therefore a
single source of both the effective value and whether it is operator-pinned.

#### Scenario: Force-off persists across discovery

- **WHEN** a target's `isis` column is `-1` and the target is re-discovered
- **THEN** the `isis` column remains `-1`

