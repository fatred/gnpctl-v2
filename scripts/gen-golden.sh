#!/bin/sh
# Regenerate testdata/golden from the Python reference at ../gnpctl.
cd "$(dirname "$0")/.." && uv run --project ../gnpctl python scripts/gen_golden.py
