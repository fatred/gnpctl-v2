## ADDED Requirements
### Requirement: Render computes a desired config independent of emission

`gnpctl render` SHALL compute a desired data-plane configuration as a pure function
of the state database, the catalog, and the render configuration, with no
dependence on how the result is delivered. A separate emitter SHALL serialize the
result; the initial emitter SHALL write files. The per-target targets artifact
SHALL be emitted separately from the static (ingress/egress) artifacts so the
targets channel can later be delivered live without changing the compute.

#### Scenario: Compute is delivery-agnostic

- **WHEN** `render` computes the desired config from state + catalog + render config
- **THEN** the computed result is produced without invoking any file/network delivery, and an emitter serializes it separately

#### Scenario: Targets emitted as a distinct artifact

- **WHEN** the file emitter runs
- **THEN** it writes the per-target `targets.yaml` as its own file, separate from the static ingress and egress configs

### Requirement: Naming spine `<catalog_key>-<module>`

Each enabled `(variant, module)` pair SHALL be identified by the key
`<catalog_key>-<module>`. The system SHALL use that exact string as the gnmic
subscription name, the NATS subject prefix (via `subject-format: subscription.target`),
the JetStream stream name, and the egress durable-consumer name.

#### Scenario: One key across all four artifacts

- **WHEN** a `nokia-srlinux` target has the `ports` module enabled
- **THEN** the rendered subscription, stream, subject prefix, and egress consumer are all named `nokia-srlinux-ports`

### Requirement: Ingress rendering

For each discovered target, `render` SHALL produce a gnmic ingress configuration
that, for every **enabled** module of the target's effective profile, contains a
subscription (the module's paths grouped by `mode` and `sample_interval`) and a
jetstream output that upserts a **workqueue** stream named `<catalog_key>-<module>`
with `subject-format: subscription.target`, with the subscription routed to that
output. It SHALL also produce a targets entry carrying the address, port, encoding,
TLS and credential references, the enabled subscription names, and device-metadata
`tags` (vendor, model, variant). Disabled modules SHALL NOT be rendered.

#### Scenario: Enabled module renders subscription + jetstream output

- **WHEN** a target has `ports` enabled
- **THEN** the ingress config has a `nokia-srlinux-ports` subscription and a jetstream output that upserts a workqueue stream `nokia-srlinux-ports`

#### Scenario: Disabled module is not rendered

- **WHEN** a target has a module forced-off (effective enum `< 1`)
- **THEN** no subscription, output, or stream is rendered for that module

#### Scenario: Target carries metadata tags and its enabled subscriptions

- **WHEN** a `nokia-srlinux` target with `ports` enabled is rendered
- **THEN** its `targets.yaml` entry lists subscription `nokia-srlinux-ports` and `tags` including its vendor/model/variant

### Requirement: Egress rendering

For each enabled `(variant, module)`, `render` SHALL produce a gnmic egress
configuration with a jetstream input that creates a durable consumer on the
module's stream, feeding the module's resolved processor chain, with a Prometheus
`remote_write` output. NATS streams and consumers SHALL be created by gnmic (ingress
output `create-stream`; egress input), not provisioned externally. A module with no
processor references SHALL render an identity (passthrough) chain.

#### Scenario: Egress consumer to Prometheus

- **WHEN** `ports` is enabled on a `nokia-srlinux` target
- **THEN** the egress config has a jetstream input with a durable consumer on `nokia-srlinux-ports`, the `ports` processor chain, and a Prometheus `remote_write` output

#### Scenario: Module without processors is passthrough

- **WHEN** an enabled module declares no `processors`
- **THEN** its egress renders an identity chain (input straight to the Prometheus output)

### Requirement: render is config-driven and a gnpctl subcommand

The CLI SHALL expose `render` as a subcommand of `gnpctl`. The NATS connection, the
Prometheus `remote_write` endpoint, and the output directory SHALL come from the
gnpctl configuration (with an optional `--output-dir` override). `render` SHALL fail
with a clear error when its required configuration is absent.

#### Scenario: render writes the three artifacts

- **WHEN** `gnpctl render` runs with a valid render configuration
- **THEN** it writes `ingress.yaml`, `egress.yaml`, and `targets.yaml` under the configured output directory

#### Scenario: missing render configuration aborts cleanly

- **WHEN** `gnpctl render` runs and the `render` configuration block is absent
- **THEN** it exits non-zero with a message naming the missing configuration

### Requirement: Deployable ingress config

`gnpctl render` SHALL emit an `ingress.yaml` that is a complete, runnable gnmic ingestor
config: the global TLS setting (`skip-verify` from render config; `tls-ca` when configured),
gNMI credentials as environment references (`username: ${TARGET_USER}` /
`password: ${TARGET_PASS}` — never literal secrets), an `api-server` when configured, a
`loader` of type `file` pointing at the configured targets file, the per-module
subscriptions, and per-module jetstream outputs carrying `format: event` and the configured
output tuning. The stream `create-stream.subjects` SHALL remain empty (paired with
`subject-format: subscription.target`).

#### Scenario: Ingress carries globals, loader, and cred refs

- **WHEN** a discovered target is rendered with `gnmic.tls_ca` and `gnmic.targets_file` set
- **THEN** `ingress.yaml` contains `skip-verify`, `tls-ca`, `username: ${TARGET_USER}`, `password: ${TARGET_PASS}`, and `loader: {type: file, path: <targets_file>}`

#### Scenario: No literal secret is emitted

- **WHEN** any config is rendered
- **THEN** no rendered file contains a literal gNMI username or password (only `${...}` env references)

### Requirement: Deployable egress config

`gnpctl render` SHALL emit an `egress.yaml` whose per-module jetstream inputs carry the
configured input tuning (`num_workers`, `fetch_batch_size`, `max_ack_pending`, `buffer_size`,
`deliver_policy`, `format: event`) and the module's processor chain, and whose
`prometheus_write` output carries the configured Prometheus tuning (`interval`,
`buffer_size`, `max_time_series_per_write`, `timeout`, `num_workers`).

#### Scenario: Egress input + prometheus output carry tuning

- **WHEN** a module is rendered into `egress.yaml`
- **THEN** its jetstream input includes the configured input tuning and processor chain, and the `prometheus_write` output includes the configured Prometheus tuning

### Requirement: Fleet-consistent target identity

`gnpctl render` SHALL name each target by its FQDN (`<device-name>.<target_device_domain>`)
so the emitted `source` tag is the device FQDN without a port, while the connection
`address` retains the discovered IP. It SHALL prepend a shared `add-instance` processor
(value from `gnmic.instance`) to every egress consumer's processor chain.

#### Scenario: Target named by FQDN, address keeps the IP

- **WHEN** a device `CHZUR1-AZ01-LF01` at `172.19.224.34` is rendered with `target_device_domain: net.plabs.ch`
- **THEN** the target is named `CHZUR1-AZ01-LF01.net.plabs.ch` (the `source` value) and its `address` is the IP (`172.19.224.34`)

#### Scenario: instance prepended to every egress chain

- **WHEN** egress is rendered with `gnmic.instance` set
- **THEN** every jetstream input's processor chain begins with an `add-instance` processor carrying that value

### Requirement: Federation tagging for long-term storage

`gnpctl render` SHALL tag for long-term storage the series of every metric whose declaration sets `federate: true` (default `false`). It SHALL derive the set of federated metric names from those flags across the rendered profiles and, when that set is non-empty, define a single shared `add-federate` processor — an `event-add-tag` with `overwrite: false`, `value-names` matching exactly those metric names, adding `federate: "true"` — and append it (after the module's processor chain, where the event value name equals the canonical metric name) to every egress consumer chain that carries at least one federated metric. A chain that carries no federated metric SHALL NOT reference the processor. When no metric is federated, no `add-federate` processor SHALL be emitted. The tag SHALL NOT overwrite a `federate` value already present on an event. This is a render-output behavior only; it does not change the four-corner metric-contract validation.

#### Scenario: Federated metric's chain carries add-federate

- **WHEN** a module with a `federate: true` metric (e.g. `bgp_neighbor_session_state`) is enabled on a target
- **THEN** that module's egress consumer chain is appended an `add-federate` processor whose `value-names` include `^bgp_neighbor_session_state$` and whose `event-add-tag` adds `federate: "true"` with `overwrite: false`

#### Scenario: Non-federated chain has no add-federate

- **WHEN** a module declares no `federate: true` metric (or declares no metrics at all)
- **THEN** its egress consumer chain does not reference the `add-federate` processor

#### Scenario: value-names scope the tag per event

- **WHEN** `add-federate` is appended to a chain that also carries unfederated values
- **THEN** only events whose value name matches a federated name receive `federate=true` (the tag is scoped by `value-names`, not applied to the whole chain)

#### Scenario: No federated metrics yields no processor

- **WHEN** no metric in any rendered profile sets `federate: true`
- **THEN** the egress config contains no `add-federate` processor and no chain references it

#### Scenario: Ordering relative to add-instance

- **WHEN** egress is rendered with both `gnmic.instance` set and a federated metric on a chain
- **THEN** that chain's processor list begins with `add-instance`, then the module's own chain, and ends with `add-federate`

### Requirement: A rendered config never silently disables TLS verification

When `gnpctl render` emits an `ingress.yaml` carrying `skip-verify: true`, it SHALL surface the
disabled gNMI server-certificate check rather than emit it silently: a warning to stderr at
render time naming the output file, and a banner comment at the top of `ingress.yaml` flagging
that device-certificate validation is disabled. The renderer SHALL NOT emit `skip-verify: true`
and `tls-ca` together (the contradictory combination is rejected upstream at config load). A
secure render (`skip-verify: false`, the default) SHALL emit no such warning or banner.

#### Scenario: skip-verify render is warned and bannered

- **WHEN** a render produces `ingress.yaml` with `skip-verify: true`
- **THEN** render prints a stderr warning naming the output file, and the emitted `ingress.yaml` carries a banner comment stating that device-certificate validation is disabled

#### Scenario: Secure render emits no TLS warning

- **WHEN** a render produces `ingress.yaml` with `skip-verify: false` (the default)
- **THEN** no TLS warning is printed and no banner is written

