## ADDED Requirements
### Requirement: Catalog entries as version-controlled YAML

The gNMI path catalog SHALL be a version-controlled directory tree loaded into memory; there SHALL be
no database table for the catalog. Per vendor it comprises a **vendor definition**
(`<vendor>/vendor.yaml`), one or more **variants** (`<vendor>/<variant>/common.yaml` plus optional
`<vendor>/<variant>/<version>.yaml` layers), and a shared **module registry**
(`catalog/modules.yaml`). Each variant SHALL be identified by a unique `key` and SHALL be resolvable
by key at discovery, test, and render time; vendor definitions SHALL be resolvable by `vendor` name.

#### Scenario: Resolve a variant by key

- **WHEN** the catalog is loaded and a variant declares `key: nokia-srlinux`
- **THEN** the catalog can return that variant's modules by the key `nokia-srlinux`

#### Scenario: Duplicate variant key across the catalog rejected

- **WHEN** two variants declare the same `key`
- **THEN** the loader raises a validation error naming the duplicated key and both files

### Requirement: Fail-fast catalog validation

The loader SHALL validate every entry against the catalog schema and SHALL raise a validation error
naming the offending file and field when an entry is malformed, missing a required field, or uses
an unknown subscription mode. Validation errors SHALL abort loading rather than skip the file.

#### Scenario: Missing required field

- **WHEN** an entry omits a required field such as `key` or a subscription object's `path`
- **THEN** the loader raises a validation error naming the file and the missing field, and no partial catalog is returned

#### Scenario: Malformed YAML

- **WHEN** a catalog file contains syntactically invalid YAML
- **THEN** the loader raises an error identifying the file rather than producing a partial catalog

### Requirement: Vendor definition

A vendor definition (`catalog/<vendor>/vendor.yaml`) SHALL declare a `vendor` name, a `fingerprint`
block with a `match` (Stage-1 capabilities conditions: `org_contains`, `models_any`,
`encodings_any`, `gnmi_version_min`) and an optional `priority`, and a `platform_probe`. The probe
reads operational state via a gNMI `Subscribe` in ONCE mode (`subscribe_once`) — the single
state-read primitive, since it works across Nokia, Arista, and Juniper (Junos serves `Get` for
configuration only). It SHALL declare a `path` that MAY carry a gNMI origin (e.g.
`eos_native:/Eos/image`), an `extract` map (identity field ← leaf locator), and an optional `parse`
list (each a regex with named capture groups applied to a named leaf's string value, contributing
those groups as identity fields — for vendors that embed model/os in free text). A leaf locator (an
`extract` value or a `parse` rule's `from`) SHALL be matched by its **trailing leaf name, tolerant of
YANG module prefixes** that Subscribe returns (e.g. the locator `version` matches both
`srl_nokia-system-info:information/version` and `eos_native:Eos/image/version`). A locator MAY be
prefixed by a `where` selector that picks the single list element whose named leaf equals a given
value — required for name-keyed OpenConfig lists such as `/components/component` (keyed by `name`),
where the chassis or routing-engine element is identified by its `type` leaf (`CHASSIS` /
`CONTROLLER_CARD`) rather than a path key. The probe yields identity fields (`model`, `os`,
`os_version`, …) used to select a variant and to enrich the target.

#### Scenario: Vendor definition parsed

- **WHEN** `catalog/juniper/vendor.yaml` declares `vendor`, a `fingerprint.match`, and a `platform_probe` with `path` + `extract`
- **THEN** the loaded catalog exposes the vendor's match conditions and platform probe

#### Scenario: Probe extracts identity by leaf and by regex parse

- **WHEN** the Nokia probe `path: /system/information` declares `extract: {os_version: version}` and a `parse` rule over `description` (`"^(?P<os>\S+?)-\S+\s+(?P<model>.+?)\s+Copyright"`) against the Subscribe-ONCE leaf `srl_nokia-system-info:information/description` = `"SRLinux-v24.10.4-244-g5c0afd1a61 7220 IXR-D2L Copyright ..."`
- **THEN** the locator `version` matches `srl_nokia-system-info:information/version` (module prefix tolerated), giving `os_version=v24.10.4-244-g5c0afd1a61`, and the parse yields `os=SRLinux`, `model=7220 IXR-D2L`

#### Scenario: Probe path carries a gNMI origin

- **WHEN** the Arista probe declares `path: eos_native:/Eos/image` and `extract: {os_version: version, os: flavor}`
- **THEN** the probe Subscribe-ONCEs the `eos_native` origin path and extracts `os_version=4.33.4M`, `os=cEOS`

#### Scenario: Probe reads operational state where Get is config-only

- **WHEN** the Juniper probe `path: /components/component[name=*]/state` runs against a Junos device whose `Get` serves configuration only (`Unimplemented: Supported only for configuration`)
- **THEN** the Subscribe-ONCE probe still returns the operational state and identity is extracted from it

#### Scenario: Extractor selects a list element by a leaf value

- **WHEN** an extractor declares `where: {type: CONTROLLER_CARD}` over the `name`-keyed `/components/component[name=*]/state` list, `extract: {os_version: software-version}`, and a `parse` of `description` `"^(?:RE-)?(?P<model>.+)$"`
- **THEN** it selects the routing-engine component and yields `os_version=23.4R2-S4.11` and `model=QFX5120-48Y-8C` (from `"RE-QFX5120-48Y-8C"`); likewise `model=QFX10002-72Q` from `"RE-QFX10002-72Q"`

### Requirement: Variant common profile

A variant's base (`catalog/<vendor>/<variant>/common.yaml`) SHALL declare a unique `key`, the
`vendor` it belongs to, a `platform_match`, and a `modules` map. `platform_match` conditions MAY
combine **capability signals** (`models_any`, evaluated against the device's advertised models — for
OS-level variants such as SR Linux vs SR OS) with **probe-identity** globs (`model`, `os` — for
hardware-level variants such as Juniper MX vs QFX); an omitted condition, or `["*"]`, matches any. It
MAY declare `default_port` — when present it MUST be a positive port number; a non-positive value is
rejected at load — `platform_hints`, and a preferred `encoding` (the renderer's default for
this platform, since not every platform supports every encoding — e.g. Arista EOS advertises no
PROTO). The `modules` map SHALL key each
canonical module name (from the registry) the variant supports to a block with a `paths` list (each
path a subscription object: a stable `key`, a gNMI `path`, a `mode` of `sample`/`on_change`/
`target_defined`, and optional `sample_interval`/`encoding`/`description`) and an optional `default`
(`on`/`off`) overriding the registry default for this variant. `common.yaml` is the base shared by
all of the variant's supported versions. Variants SHALL be resolvable by `key`.

#### Scenario: Variant common parsed with platform_match and modules

- **WHEN** `catalog/juniper/mx/common.yaml` declares `key: juniper-mx`, `platform_match.model: ["MX*", "QFX10*"]`, `default_port: 57400`, and a `modules` map
- **THEN** the loaded variant exposes its platform match, default port, and per-module paths + defaults

#### Scenario: Non-positive default_port rejected at load

- **WHEN** a variant's `common.yaml` declares `default_port: 0` (or a negative value)
- **THEN** catalog loading fails with a validation error naming the file, `default_port`, and "must be positive"

#### Scenario: Single-platform variant matches any model

- **WHEN** a variant's `common.yaml` declares `platform_match.model: ["*"]`
- **THEN** it is selected for any model of its vendor

#### Scenario: Variant declares a capability match and an encoding

- **WHEN** `catalog/nokia/srlinux/common.yaml` declares `platform_match.models_any: ["srl_nokia"]` and `encoding: proto`
- **THEN** the loaded variant exposes the capability-model condition and its preferred encoding

#### Scenario: Invalid subscription mode rejected

- **WHEN** a module path declares a `mode` not in `sample`, `on_change`, `target_defined`
- **THEN** the loader raises a validation error naming the file and the invalid mode

### Requirement: Version layer files

A version layer (`catalog/<vendor>/<variant>/<version>.yaml`) SHALL declare a `version_match` (glob
patterns matched against the running `os_version`) and an optional integer `order`, plus module/path
deltas. The effective profile for a device SHALL be built by layering, over the variant's `common`,
every version file whose `version_match` matches the device's `os_version`, applied in ascending
`order` (default: filename sort). Layering SHALL be **additive**: a layer adds new modules/paths and
MAY override an existing path (matched by its `key`) or a module's `default`; there is no removal
operation. Two matching layers with equal `order` SHALL be recorded as an ambiguity.

#### Scenario: Version layer adds a metric

- **WHEN** `catalog/juniper/mx/23.4.yaml` (`version_match: ["23.4*"]`) declares a new path under `processors` and a device reports `os_version` `23.4R3`
- **THEN** the effective profile is `common` plus that new path

#### Scenario: Version layer overrides a path by key

- **WHEN** a version layer declares a path with the same `key` as one in `common`
- **THEN** the layer's path replaces `common`'s for matching versions, while non-matching versions keep `common`'s

#### Scenario: Metric removed in later releases stays out of common

- **WHEN** a metric exists only up to a release
- **THEN** it is declared in the version files for those releases and omitted from `common`, so newer versions simply never receive it (no removal directive needed)

### Requirement: Module registry

The catalog SHALL include a module registry file (`catalog/modules.yaml`) defining the canonical
module names and each module's global `default` enablement (`on`/`off`). Module names referenced by
variants or version layers SHALL be validated against the registry; an unknown module name SHALL be
a validation error.

#### Scenario: Registry defines names and global defaults

- **WHEN** `modules.yaml` declares modules (e.g. `ports` default on, `bgp-peers` default off)
- **THEN** the loaded catalog exposes the canonical names and each module's global default

#### Scenario: Unknown module referenced

- **WHEN** a variant or version layer declares a module name not present in the registry
- **THEN** the loader raises a validation error naming the file and the unknown module

### Requirement: Processor library

The catalog SHALL include a processor library directory (`catalog/processors/`) of
named, gnmic-native event-processor definitions, each loadable and resolvable by
name. A processor reference that names no library entry SHALL be a validation error
identifying the referrer and the unknown name.

#### Scenario: Library processor resolved by name

- **WHEN** `catalog/processors/add-device-labels.yaml` defines a named gnmic event-processor
- **THEN** the loaded catalog exposes it by the name `add-device-labels`

#### Scenario: Unknown processor reference rejected

- **WHEN** a module references a processor name not present in the library
- **THEN** the loader raises a validation error naming the module and the unknown processor

### Requirement: Module processor references

The catalog SHALL allow a variant's module to declare a `processors` list of names
drawn from the processor library (the egress-side processors for that module), and
the effective profile SHALL expose the resolved processor list per module. A module
that omits `processors` SHALL resolve to an empty (identity) chain.

#### Scenario: Module exposes its resolved processors

- **WHEN** a variant's `ports` module declares `processors: [add-device-labels]`
- **THEN** the effective profile for that variant exposes `[add-device-labels]` for `ports`

#### Scenario: Module without processors resolves to identity

- **WHEN** a module omits `processors`
- **THEN** the effective profile exposes an empty processor list for that module

### Requirement: YANG-facts registry

The catalog SHALL load a YANG-facts registry from per-release files
`catalog/yang/<vendor>/<release>.yaml`, each holding YANG objects resolvable by a stable
id and exposing the source `model`, schema `path`, `type`, `units`, and complete `enum`
value-set for that release. For a given device the catalog SHALL select the registry
release nearest below the device's `os_version` (the newest available, with a warning,
when the device is newer than every pinned release). A reference naming no registry entry
SHALL be a validation error identifying the referrer and the unknown id.

#### Scenario: Registry entry resolved by id

- **WHEN** `catalog/yang/nokia/v24.10.yaml` defines `oc-bgp-neighbor-session-state` with a `type`, `path`, and `enum`
- **THEN** the loaded catalog exposes that entry by the id `oc-bgp-neighbor-session-state` with its `enum` value-set

#### Scenario: Facts selected by device version

- **WHEN** a device reports an `os_version` of `24.10.x` and the registry has a `v24.10` release file
- **THEN** that device's YANG facts resolve from `catalog/yang/nokia/v24.10.yaml`

#### Scenario: Device newer than pinned facts

- **WHEN** a device's `os_version` exceeds every pinned release
- **THEN** facts resolve from the newest release and the catalog flags that the facts are older than the device

#### Scenario: Unknown YANG reference rejected

- **WHEN** a catalog `yang:` or metric reference names an id not present in the registry
- **THEN** the loader raises a validation error naming the referrer and the unknown id

### Requirement: Path YANG provenance

The catalog SHALL allow a path entry to declare a `yang:` reference to the registry
object it subscribes, and the effective profile SHALL expose the resolved YANG facts for
that path. A path entry MAY omit `yang:`.

#### Scenario: Path exposes its YANG provenance

- **WHEN** a path entry declares `yang: oc-bgp-neighbor-session-state`
- **THEN** the effective profile for that path exposes the resolved registry entry (model/release/path/type/enum)

### Requirement: Module metric declarations

The catalog SHALL allow a variant's module to declare a `metrics:` block, mapping each
normalized metric name to its `type` (`numeric` | `state` | `info`), its source `yang`
reference, an optional enum→number `map` (for `state`), optional `required_labels`, and
an optional `optional` flag. The effective profile SHALL expose the resolved metrics with
their YANG facts. A module that omits `metrics:` SHALL resolve to an empty metric set.

#### Scenario: State metric exposes map and YANG facts

- **WHEN** a `bgp-peers` module declares `bgp_neighbor_session_state` with `type: state`, `yang: oc-bgp-neighbor-session-state`, and a `map` of enum name → number
- **THEN** the effective profile exposes that metric with its `map` and the resolved registry `enum` value-set

#### Scenario: Module without metrics resolves to empty

- **WHEN** a module omits `metrics:`
- **THEN** the effective profile exposes an empty metric set for that module

