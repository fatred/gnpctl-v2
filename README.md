# gnpctl (Go)

Go port of [`gnpctl`](https://github.com/fatred/gnpctl), the gNMI discovery and path-catalog control CLI for
[gnp-stack](https://github.com/gnp-stack/gnp-stack). Same commands, SQLite schema, catalog tree and config as the
Python version: a state DB written by either implementation is readable by the other.

```
inventory pull ──► targets table ──► discover ──► fingerprint + catalog_key ──► render
                                       └─ test: gNMI subscribe-once the matched paths
```

## Build and run

```bash
make build            # CGO_ENABLED=0 -> bin/gnpctl (static)
make test             # go test -race ./...

cat > gnpctl.yaml <<'YAML'
database: ./state.db
catalog: ./catalog
log:
  level: info
YAML

bin/gnpctl inventory add clab --type nornir -p path=./testdata/fixtures/clab_nornir.yml
bin/gnpctl inventory pull
bin/gnpctl discover
bin/gnpctl test
bin/gnpctl render     # needs a `render:` block, see the Python docs/reference/configuration.md
```

Commands, flags and the config file are unchanged from the Python version; see its
[CLI reference](https://github.com/fatred/gnpctl/blob/main/docs/reference/cli.md).

## Differences from the Python version

- TLS: the device certificate is verified against system roots or `tls_ca` (pygnmi trusted whatever certificate the device presented). Lab devices with self-signed certificates need `tls_ca` or `skip_verify`.
- Rendered YAML is semantically identical but formatted by `yaml.v3` (indented sequences, different quoting); gnmic reads both.
- Config/catalog validation messages keep file, field and message but drop pydantic's `[type=…]` suffix and URL.
- Duplicate YAML keys in config/catalog files are an error.

Not ported: the `hwtest/` YANG-facts and hardware-capture harness (stays in the Python repo; the Go port only reads `catalog/yang`).

## Development

Spec-driven with OpenSpec: `openspec/changes/port-to-go/`. Implementation plan: `docs/plans/2026-10-24-port-to-go.md`.
`scripts/gen-golden.sh` regenerates `testdata/golden` from the Python reference at `../gnpctl`.

> The shipped `catalog/` entries use representative gNMI paths; validate them against real devices before production.
