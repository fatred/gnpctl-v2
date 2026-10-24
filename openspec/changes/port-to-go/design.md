## Context

Source of truth: `../gnpctl` (3.3k lines Python, 24 test files, 11 specs). Target: Go, module `github.com/fatred/gnpctl-v2`, binary `gnpctl`.

## Decisions

| Python | Go | Why |
| --- | --- | --- |
| click | `spf13/cobra` | Nested groups, per-command flags, repeatable `-p`; de-facto standard |
| pydantic | plain structs + `Validate()` methods returning errors with the same messages | No reflection framework; messages are spec-visible |
| PyYAML | `gopkg.in/yaml.v3` (`yaml.Node` where key order matters) | Render output uses `sort_keys=False`; ordered emission needs `yaml.Node` or ordered structs |
| sqlite3 | `modernc.org/sqlite` | Pure Go, keeps a static binary (no cgo) |
| pygnmi | `github.com/openconfig/gnmi` protos + `google.golang.org/grpc` | Capabilities + Subscribe ONCE only |
| httpx | `net/http` | NetBox REST only |
| logging | `log/slog` | levels `debug/info/warning/error` map to slog levels |
| dotenv | `joho/godotenv` only if the Python code actually loads it (it does not: no import found) -> dropped | |

Layout (one package per Python module group, nothing else):

```
cmd/gnpctl/main.go            cobra wiring only
internal/config  internal/state  internal/catalog  internal/transport
internal/capabilities  internal/fingerprint  internal/detail
internal/modules  internal/pipeline  internal/render  internal/emit  internal/testcmd
internal/inventory (+ sources/{nornir,netbox})
testdata/golden/…             output of the Python version
catalog/  tests fixtures      copied unchanged from ../gnpctl
```

## Parity strategy

Golden files: run the Python CLI against `tests/fixtures` once (task 1) and commit `render` output, `inventory show` output, `modules show` output and capabilities hashes. Go tests assert byte equality. `capabilities_hash` must match Python's (JSON with `sort_keys=True`, same separators) or existing DBs would re-discover every target.

## Risks

- YAML emission order/quoting differs from PyYAML -> golden diff. Mitigation: golden tests from task 1 gate task 11.
- `modernc.org/sqlite` WAL + `ALTER TABLE ADD COLUMN mod_*` self-heal must match Python semantics.
- gNMI encoding defaults: Python's transport applies none of its own, discover always passes one; keep that.

## Model routing (user instruction)

Coding: `deepseek/deepseek-flash`. Reviews: `anthropic/claude-opus-5-5:high`. Everything else (planning, scaffolding, orchestration, merges): `anthropic/claude-sonnet-5-5:high`. Commits use `~/.ssh/id_jhow_github` via repo-local `core.sshCommand`.
