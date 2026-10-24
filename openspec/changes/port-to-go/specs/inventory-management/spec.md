## ADDED Requirements
### Requirement: Inventory source registry

The system SHALL let an operator register, remove, and list inventory sources via
`gnpctl inventory add|remove|list`, managed like git remotes. Each source SHALL have a unique
name, a type, and type-specific connection parameters, persisted across runs. Each source plugin
SHALL declare the parameters it accepts (e.g. NetBox: base URL, token env-var name, tag, gNMI service name), and
`gnpctl inventory add` SHALL validate the supplied parameters against the selected plugin. Source
credentials SHALL be referenced indirectly (by environment-variable name) rather than stored in
plaintext.

#### Scenario: Add and list a source

- **WHEN** an operator runs `gnpctl inventory add <name> --type <type>` with valid parameters
- **THEN** the source is persisted and appears in `gnpctl inventory list`

#### Scenario: Type-specific parameters validated on add

- **WHEN** an operator adds a source of a given type but omits a parameter that type's plugin requires
- **THEN** the command fails naming the missing parameter and persists no source

#### Scenario: Remove a source leaves targets intact

- **WHEN** an operator runs `gnpctl inventory remove <name>` for an existing source
- **THEN** the source no longer appears in `gnpctl inventory list`, and existing target rows are not deleted

#### Scenario: Duplicate source name rejected

- **WHEN** an operator adds a source whose name already exists
- **THEN** the command fails with an error and the existing source is unchanged

### Requirement: Pluggable source interface

The system SHALL define a single inventory-source interface that yields a normalized list of
targets (name, address, port, connection parameters, platform hint). The discover and test
pipelines SHALL depend only on this interface, never on a concrete source type.

#### Scenario: Sources are interchangeable

- **WHEN** a pull runs against a nornir source and, separately, against a NetBox source
- **THEN** both yield targets through the same interface and populate the targets table identically in shape

### Requirement: Nornir/containerlab source

The system SHALL parse a containerlab nornir inventory file into targets, extracting host name,
address, platform hint, and the connection parameters needed for gNMI.

#### Scenario: Parse a clab nornir inventory

- **WHEN** a nornir source points at a valid containerlab nornir inventory file
- **THEN** each host becomes a target carrying its name, address, and platform hint

### Requirement: NetBox source

A NetBox source SHALL be configured with a reference name, a base URL, the name of an environment
variable holding a read-only API token, a unique device tag, and the name of the NetBox application
service that exposes gNMI (default `gnmi`). On pull, the source SHALL read the token from the named
environment variable and construct a GraphQL query that selects only devices carrying the configured
tag, returning each device's name, management address, platform, and its application Services. It
SHALL map each device into a target, taking the gNMI **port** from the device's NetBox application
Service whose name matches the configured service name when present (using that Service's bound IP as
the address when present, otherwise the device's primary IP). When no matching Service exists, the
port SHALL be left unset so a default port is applied at discovery time. The token value SHALL be read only from the
environment (populated from an env file for now) and SHALL NOT be persisted in the database or any
artifact.

#### Scenario: Tag-scoped GraphQL pull

- **WHEN** a NetBox source configured with a base URL, a token env-var name, and a tag is pulled while the env var holds a valid read-only token
- **THEN** the source issues a GraphQL query filtered to devices carrying that tag and creates one target per returned device with its name, management address, and platform hint

#### Scenario: gNMI port from a NetBox application service

- **WHEN** a tagged device has a NetBox application Service whose name matches the configured service name with port 57400
- **THEN** the imported target's port is 57400

#### Scenario: Tagged device missing the gNMI service

- **WHEN** a tagged device has no NetBox application Service matching the configured service name
- **THEN** the device is imported with its port left unset, to be filled by a default port at discovery time

#### Scenario: Token read from the environment, never stored

- **WHEN** a NetBox source is added and later pulled
- **THEN** only the token env-var name is persisted (never the token value), and the token is read from the environment at pull time

#### Scenario: Missing token environment variable

- **WHEN** a pull runs but the configured token env var is unset
- **THEN** the pull fails for that source with a clear error and records the source's `last_error` without writing targets

### Requirement: Inventory pull synchronizes targets

`gnpctl inventory pull` SHALL fetch targets from the configured source(s) and upsert them into the
targets table: new targets are inserted, existing targets (matched by name) have their
inventory-sourced fields refreshed, and discovery-owned fields (`discovered`,
`last_discovery_run`, catalog key, fingerprint) are preserved. Pull SHALL NOT delete targets
absent from the source unless explicitly requested. On completion, pull SHALL stamp the source's
`last_poll` and clear its `last_error` on success, or record `last_error` on failure (leaving
`last_poll` unchanged).

#### Scenario: New target inserted with defaults

- **WHEN** pull runs and a source yields a target not present in the table
- **THEN** a new target row is created with `discovered = false` and `last_discovery_run` at epoch 0

#### Scenario: Existing target preserves discovery state

- **WHEN** pull runs for a target already marked `discovered = true`
- **THEN** its inventory-sourced fields are refreshed while `discovered`, `last_discovery_run`, the matched catalog key, and fingerprint fields are left intact

#### Scenario: Pull records the source poll outcome

- **WHEN** `gnpctl inventory pull` against a source succeeds, and separately when a later pull against it fails
- **THEN** the successful pull stamps the source's `last_poll` and clears `last_error`, and the failed pull records `last_error` while leaving `last_poll` unchanged

### Requirement: Inventory diff reports variance without writing

`gnpctl inventory diff` SHALL compare the configured source(s) against the targets table and report
targets that would be added, changed, or removed, WITHOUT modifying the database.

#### Scenario: Diff is read-only

- **WHEN** diff runs and the source has one new host and one removed host relative to the table
- **THEN** the output lists the addition and the removal and the database is left unchanged

### Requirement: Inventory show

`gnpctl inventory show` SHALL read the `targets` table and render a human-readable table to the
terminal. By default it SHALL display all targets. Filter flags SHALL narrow the output:
`--target-errors` to targets with a recorded `last_error`; `--undiscovered` to targets with
`discovered = false`; and `--stale` to discovered targets whose `last_discovery_run` is older than
the staleness window (default 1h, matching `--periodic`). When multiple filters are supplied, the
output SHALL be the union of the matching targets. `inventory show` SHALL NOT modify the database.

#### Scenario: Show all targets by default

- **WHEN** `gnpctl inventory show` is run with no filters
- **THEN** it prints a table of every target in the `targets` table and writes nothing

#### Scenario: Filter to errored targets

- **WHEN** `gnpctl inventory show --target-errors` is run
- **THEN** only targets with a non-empty `last_error` are listed

#### Scenario: Filter to undiscovered targets

- **WHEN** `gnpctl inventory show --undiscovered` is run
- **THEN** only targets with `discovered = false` are listed

#### Scenario: Filter to stale targets

- **WHEN** `gnpctl inventory show --stale` is run
- **THEN** only targets with `discovered = true` and a `last_discovery_run` older than the staleness window are listed

#### Scenario: Combined filters are a union

- **WHEN** `gnpctl inventory show --undiscovered --target-errors` is run
- **THEN** targets matching either filter are listed

### Requirement: Built-in source type registry

The system SHALL resolve an inventory source-type name to its source class via a built-in,
in-tree registry (a name → class mapping). `gnpctl inventory add --type <type>` SHALL reject a
type absent from the registry and list the available types. Adding a built-in source type SHALL
be a single registry entry; gnpctl does not discover source types from separately installed
packages at runtime. The source interface and the discover/test/pull pipelines SHALL continue
to depend only on the source interface, never on a concrete source type.

#### Scenario: Built-in source resolves from the registry

- **WHEN** the source registry is asked for the type `nornir` or `netbox`
- **THEN** it resolves to the built-in source class from the in-tree registry

#### Scenario: Unknown source type rejected

- **WHEN** `gnpctl inventory add --type <type>` names a type absent from the registry
- **THEN** the command fails and lists the available source types

### Requirement: NetBox source requires HTTPS

A NetBox source URL SHALL be rejected at `inventory add` time (in the source's
`validate_params`) unless it uses the `https://` scheme, so the long-lived NetBox API token —
sent as an `Authorization: Token …` header on every pull — is never transmitted in cleartext.
An `http://` or schemeless URL SHALL fail with a clear `SourceError` and persist no source.

#### Scenario: http NetBox URL rejected

- **WHEN** `gnpctl inventory add <name> --type netbox -p url=http://netbox.lan -p tag=t --token-env NB` is run
- **THEN** the command fails with a `SourceError` requiring an `https://` URL, and no source is persisted

#### Scenario: https NetBox URL accepted

- **WHEN** the NetBox source URL uses `https://`
- **THEN** parameter validation passes and the token rides only over TLS

