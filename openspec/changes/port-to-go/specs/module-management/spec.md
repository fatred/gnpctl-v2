## ADDED Requirements
### Requirement: gnpctl modules command tree

The CLI SHALL expose `gnpctl modules` with `enable`, `disable`, `reset`, and `show` subcommands
operating on a target's per-module enablement. `enable <module> --target <name>` SHALL set the
module to force-on (`2`); `disable <module> --target <name>` SHALL set it to force-off (`-1`);
`reset <module> --target <name>` SHALL return the module to discovery-managed by recomputing the
profile default (`0`/`1`); `show [--target <name>]` SHALL display per-module state. Module names
SHALL be validated against the registry, and an unknown module or unknown target SHALL be an error.

#### Scenario: Disable forces a module off and persists

- **WHEN** `gnpctl modules disable isis --target leaf1` runs
- **THEN** leaf1's `isis` column is set to `-1` and remains `-1` after a subsequent discovery

#### Scenario: Enable forces a module on

- **WHEN** `gnpctl modules enable isis --target core1` runs
- **THEN** core1's `isis` column is set to `2`

#### Scenario: Reset returns a module to discovery-managed

- **WHEN** `gnpctl modules reset isis --target core1` runs
- **THEN** the `isis` column is recomputed to the profile default (`0` or `1`)

#### Scenario: Unknown module rejected

- **WHEN** a module not present in the registry is named
- **THEN** the command exits with an error listing the valid module names

#### Scenario: Force-on a module the profile lacks warns

- **WHEN** `enable` targets a module the device's selected profile defines no paths for
- **THEN** the command warns that nothing will be collected for that module

#### Scenario: show renders per-module state

- **WHEN** `gnpctl modules show --target leaf1` runs
- **THEN** each module is printed as `off` / `on` / `forced-off` / `forced-on` per its enum value

