"""Generate Go-parity golden files from the Python gnpctl at ../gnpctl.

Run: uv run --project ../gnpctl python scripts/gen_golden.py
Mirrors ../gnpctl/tests/test_e2e.py (fixture transport, nornir inventory).
"""
import json, pathlib, shutil, sys, tempfile, textwrap

ME = pathlib.Path(__file__).resolve().parents[1]
SRC = ME.parent / "gnpctl"
sys.path[:0] = [str(SRC)]

from click.testing import CliRunner
from gnpctl import capabilities, catalog as catmod, pipeline
from gnpctl.cli import main
from gnpctl.transport import FixtureTransport

FIX = ME / "testdata" / "fixtures"
OUT = ME / "testdata" / "golden"
CAT = catmod.load(ME / "catalog")


def load(rel):
    return json.loads((FIX / rel).read_text())


def transport_for(target, port):
    p = target.get("platform_hint") or ""
    if "juniper" in p:
        caps, subs, vendor = load("juniper/capabilities.json"), dict(load("juniper/qfx10k_components.json")), "juniper"
    elif "nokia" in p or "srl" in p:
        caps, subs, vendor = load("nokia_srl/capabilities.json"), dict(load("nokia_srl/subscribe.json")), "nokia"
    else:
        caps, subs, vendor = load("arista_eos/capabilities.json"), dict(load("arista_eos/subscribe.json")), "arista"
    for v in CAT.variants_for(vendor):
        for m in CAT.effective_profile(v.key).modules.values():
            for path in m.paths:
                subs.setdefault(path.path, {path.path: 1})
    return FixtureTransport(caps, subs)


shutil.rmtree(OUT, ignore_errors=True)
OUT.mkdir(parents=True)
tmp = pathlib.Path(tempfile.mkdtemp())
cfg = tmp / "gnpctl.yaml"
cfg.write_text(textwrap.dedent(f"""
    database: {tmp / 'state.db'}
    catalog: {ME / 'catalog'}
    log:
      level: warning
    render:
      output_dir: {tmp / 'render'}
      nats:
        address: nats:4222
      prometheus:
        remote_write_url: http://prometheus:9090/api/v1/write
"""))
pipeline.default_transport_factory = transport_for
r = CliRunner()


def run(name, *args, check=True):
    res = r.invoke(main, ["--config", str(cfg), *args])
    (OUT / f"{name}.txt").write_text(f"exit={res.exit_code}\n{res.output}")
    if check and res.exit_code != 0:
        raise SystemExit(f"{args}: {res.output}")


run("inv_add", "inventory", "add", "clab", "--type", "nornir", "-p", f"path={FIX / 'clab_nornir.yml'}")
run("inv_diff_before", "inventory", "diff")
run("inv_pull", "inventory", "pull")
run("inv_list", "inventory", "list")
run("inv_show_undiscovered", "inventory", "show", "--undiscovered")
run("discover", "discover")
run("inv_show", "inventory", "show")
run("modules_show", "modules", "show")
run("modules_disable", "modules", "disable", "bgp-peers", "--target", "leaf1", check=False)
run("modules_show_after", "modules", "show", "--target", "leaf1")
run("test", "test", check=False)
run("render", "render")
shutil.copytree(tmp / "render", OUT / "render")
shutil.copy(tmp / "state.db", OUT / "python_state.db")
hashes = {}
for f in sorted(FIX.glob("*/capabilities.json")):
    hashes[f.parent.name] = capabilities.parse(json.loads(f.read_text())).hash
(OUT / "capabilities_hashes.json").write_text(json.dumps(hashes, indent=2) + "\n")
print("ok", sorted(p.name for p in OUT.iterdir()))
