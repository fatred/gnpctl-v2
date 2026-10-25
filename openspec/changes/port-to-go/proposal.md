## Why

`gnpctl` (Python: click, pydantic, pygnmi, sqlite3) is the gNMI discovery and path-catalog CLI for gnp-stack. A Go port ships as one static binary (no Python/uv on discovery hosts) and shares a toolchain with the rest of the stack. The Python repo at `../gnpctl` is the behavioral reference.

## What Changes

- New Go implementation of every CLI command: `inventory {add,remove,list,pull,diff,show}`, `discover`, `modules {enable,disable,reset,show}`, `test`, `render`.
- Same SQLite schema, same catalog tree (`catalog/`), same config file, same rendered gnmic files. A DB written by the Python version is readable by the Go version and vice versa.
- Specs below are carried over verbatim from the Python repo; only the pygnmi reference is reworded.

## Non-goals

- No new features, flags or catalog content; no behavior changes.
- `hwtest/` (Python YANG-facts regeneration and hardware capture harness) and the `metric-validation` capability stay in the Python repo. The Go port only *reads* `catalog/yang/*`.
- MkDocs site is not rebuilt; `README.md` documents the Go build. Existing `docs/` pages are copied later only if the user asks.
- No plugin loading for inventory sources: built-in `nornir` and `netbox` only (the Python registry is a static map too).

## Impact

New Go module in this repo. No effect on `../gnpctl`. Parity is proven by golden files generated from the Python version (see design.md).
