## ADDED Requirements
### Requirement: gNMI transport abstraction

The system SHALL access devices through a transport interface exposing a `capabilities()` operation
and a `subscribe_once(paths)` operation — a gNMI `Subscribe` in ONCE mode that returns the current
values of the requested paths and closes. `subscribe_once` is the single state-read primitive: it
works across vendors (validated on Nokia, Arista, and Juniper) and is required where `Get` does not
serve operational state (Junos serves `Get` for configuration only). The system SHALL NOT depend on a
gNMI `Get` for operational reads. The system SHALL provide a live gRPC gNMI-backed implementation and
a fixture implementation that replays captured responses for both operations. Fingerprinting,
platform-probe, and test logic SHALL depend only on the interface, not on a concrete transport.

#### Scenario: Fixture transport replays captured responses

- **WHEN** the fixture transport is configured with a captured `Capabilities` response and Subscribe-ONCE responses for a device
- **THEN** calling `capabilities()` and `subscribe_once(paths)` returns the captured data without any network access

#### Scenario: Live transport implements the same interface

- **WHEN** discovery runs with the gRPC gNMI-backed transport instead of the fixture transport
- **THEN** the logic operates unchanged, calling only `capabilities()` and `subscribe_once(paths)`

#### Scenario: Subscribe-ONCE reads state where Get is unsupported

- **WHEN** a platform probe or `gnpctl test` reads operational state on a device whose `Get` returns `Unimplemented: Supported only for configuration`
- **THEN** the read is performed via `subscribe_once(paths)` and succeeds

### Requirement: Capabilities normalization

The system SHALL parse a raw gNMI `Capabilities` response into a normalized profile containing the
set of supported model names, the set of model organization strings, the set of supported
encodings, and the gNMI version, and SHALL compute a stable hash of the profile for change
detection.

#### Scenario: Capabilities parsed into a profile

- **WHEN** a raw `Capabilities` response listing supported models, encodings, and a gNMI version is parsed
- **THEN** the resulting profile exposes the model-name set, the organization set, the encoding set, and the gNMI version

#### Scenario: Stable capabilities hash

- **WHEN** the same `Capabilities` response is parsed twice
- **THEN** the computed capabilities hash is identical across both parses

### Requirement: Stage-1 fingerprint match to a vendor

The system SHALL evaluate the normalized capabilities profile against the catalog's **vendor
definitions** and SHALL select the highest-priority vendor whose `match` conditions are all
satisfied. Stage-1 yields the matched vendor (and therefore which platform probe to run), not the
catalog key. When two vendors match at equal priority, the system SHALL select deterministically and
record the ambiguity. When no vendor matches, the system SHALL report the device as `unknown` and
perform no further resolution.

#### Scenario: Highest-priority vendor wins

- **WHEN** the profile satisfies the `match` of two vendor definitions with priorities 100 and 50
- **THEN** the system selects the priority-100 vendor

#### Scenario: No vendor matches

- **WHEN** no vendor definition's `match` conditions are satisfied by the profile
- **THEN** the system reports the device as `unknown` and runs no platform probe

### Requirement: Stage-2 platform probe, variant selection, and version layering

When a vendor matches, the system SHALL run that vendor's `platform_probe` — issuing a gNMI
`Subscribe` in ONCE mode (`subscribe_once`) for the probe path(s) and extracting an identity
(`model`, `os`, `os_version`, and any additional fields). Leaf locators SHALL match by trailing leaf
name (tolerant of YANG module prefixes returned by Subscribe), and an extractor MAY select a list
element by a leaf value (the `/components/component` entry whose `type` is `CONTROLLER_CARD` or
`CHASSIS`, since that list is keyed by `name`). It SHALL then select the **variant** whose
`common.platform_match` conditions hold — matching **capability signals** (`models_any`, from the
Stage-1 capabilities profile) and/or **probe identity** (`model`, `os`); the selected variant's `key`
becomes the device's catalog key.
The system SHALL build the **effective profile** by layering, over the variant's `common`, every
version file whose `version_match` matches the running `os_version`, applied in ascending `order`
(additive merge; paths overridden by `key`). The probe SHALL be tolerant of missing fields. When the
vendor matches but no variant's `platform_match` holds, the system SHALL record the vendor and
identity but assign no catalog key (a "vendor known, platform unknown" outcome).

#### Scenario: Variant selected by capability models

- **WHEN** the device advertises `srl_nokia-*` models, variant `nokia-srlinux` declares `platform_match.models_any: ["srl_nokia"]`, and `nokia-sros` declares `platform_match.models_any: ["nokia-state"]`
- **THEN** the selected catalog key is `nokia-srlinux` (the OS-level split resolves from capabilities, not the probe)

#### Scenario: Variant selected by probe model

- **WHEN** the Juniper probe returns model `QFX10002-72Q` (parsed from the CONTROLLER_CARD `description` `"RE-QFX10002-72Q"`) and variant `juniper-mx` declares `platform_match.model: ["MX*", "QFX10*"]`
- **THEN** the selected catalog key is `juniper-mx` (the hardware-level split resolves from the probe), while a `QFX5120-48Y-8C` probe selects `juniper-qfx` (`model: ["QFX5*"]`)

#### Scenario: Probe reads operational state via Subscribe ONCE

- **WHEN** the matched vendor's probe runs against a device whose `Get` serves configuration only
- **THEN** the Subscribe-ONCE probe still returns the operational state and identity is extracted from it

#### Scenario: Version layer applied to the effective profile

- **WHEN** the selected variant has a version file whose `version_match` matches the running `os_version`
- **THEN** the effective profile is the variant's `common` with that version file's additive deltas layered on top

#### Scenario: Vendor known, platform unknown

- **WHEN** a vendor matches but the probed identity matches no variant's `platform_match`
- **THEN** the system records the vendor and identity, assigns no catalog key, and collects no modules

#### Scenario: Partial probe tolerated

- **WHEN** the platform probe is missing a field named by an extractor
- **THEN** the system records the fields it could extract and continues, rather than failing the device

