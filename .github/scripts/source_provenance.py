"""Verify retained adapted-source provenance in a package or installation."""
import hashlib
import json
import pathlib


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_regular(share, relative):
    rel = pathlib.PurePosixPath(relative)
    assert relative and not rel.is_absolute() and ".." not in rel.parts
    assert "\\" not in relative and ":" not in relative
    path = share
    for part in rel.parts:
        path = path / part
        assert not path.is_symlink(), "linked source notice"
    assert path.is_file(), "missing source notice"
    return path.read_bytes()


def verify_sources(share, build, notices, bom):
    sources = notices.get("source_components", [])
    assert sources == build.get("source_components", []), "source metadata differs"
    components = {c["bom-ref"]: c for c in bom["components"]}
    engine = [s for s in sources if s["package"] == "tailscale.com"]
    assert len(engine) == 1, "reviewed adapted engine provenance is required"
    assert not any(m["module"] == "tailscale.com" for m in notices["modules"]), "adapted engine mislabeled as original module"
    for source in sources:
        inputs = source["build_inputs"]
        assert inputs and inputs == sorted(inputs, key=lambda x: x["path"])
        assert len({i["path"] for i in inputs}) == len(inputs)
        canonical = json.dumps(inputs, ensure_ascii=False, separators=(",", ":")).encode()
        assert digest(canonical) == source["build_inputs_sha256"], "source build input digest differs"
        assert source["notices"], "missing source notices"
        for entry in source["notices"]:
            assert digest(read_regular(share, entry["path"])) == entry["sha256"], "source notice digest differs"
        component = components["source:" + source["source_path"]]
        properties = {p["name"]: p["value"] for p in component["properties"]}
        assert properties["source:modified"] == "true"
        assert properties["source:build-inputs:sha256"] == source["build_inputs_sha256"]
        assert json.loads(properties["source:build-inputs"]) == inputs
        assert "purl" not in component, "adapted source must not claim upstream purl"
        ancestor = component["pedigree"]["ancestors"][0]
        upstream = source["upstream"]
        assert ancestor["name"] == upstream["module"] and ancestor["version"] == upstream["version"]
    source = engine[0]
    raw = read_regular(share, "licenses/source/internal/engineadaptation/manifest.json")
    assert digest(raw) == source["manifest_sha256"]
    manifest = json.loads(raw)
    assert manifest["schema"] == 1 and manifest["module"] == "tailscale.com" and manifest["version"] == "v1.104.0"
    assert manifest["module_sum"] == "h1:7LgglawekeutAexqXUzSxP/Kqo3HHwF/SkcbX5HpiU4="
    assert manifest["go_mod_sum"] == "h1:Cb1XjScRgSOeRD7NW6uCEznme782WiOU9IxqyTWoRf0="
    assert manifest["module_sum"] == source["upstream"]["module_sum"]
    assert manifest["go_mod_sum"] == source["upstream"]["go_mod_sum"]
    assert manifest["upstream_tree_sha256"] == source["upstream"]["tree_sha256"]
    assert manifest["adapted_tree_sha256"] == source["adapted_tree_sha256"]
    inputs = {i["path"]: i["sha256"] for i in source["build_inputs"]}
    assert inputs["internal/engineadaptation/manifest.json"] == digest(raw)
    assert inputs["go.mod"] == digest(read_regular(share, "go.mod"))
    assert inputs["go.sum"] == digest(read_regular(share, "go.sum"))
    for change in manifest["changes"]:
        selected = ".sobalink-deps/tailscale/" + change["path"]
        if selected in inputs:
            assert inputs[selected] == change["adapted_sha256"], "adapted runtime source hash differs"
    original = {c["path"]: c["original_sha256"] for c in manifest["changes"] if c["original_sha256"]}
    assert original == source["upstream"]["files"]
    properties = {p["name"]: p["value"] for p in components["source:internal/engineadaptation"]["properties"]}
    for prop, value in (("source:manifest:sha256", digest(raw)),
                        ("source:adapted-tree:sha256", source["adapted_tree_sha256"]),
                        ("source:upstream-tree:sha256", source["upstream"]["tree_sha256"]),
                        ("source:upstream-module:sum", source["upstream"]["module_sum"]),
                        ("source:upstream-go-mod:sum", source["upstream"]["go_mod_sum"])):
        assert properties[prop] == value
