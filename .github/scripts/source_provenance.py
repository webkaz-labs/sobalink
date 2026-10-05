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
    direct = [s for s in sources if s["package"] == "github.com/webkaz-labs/sobalink/internal/directlan"]
    assert len(direct) == 1, "reviewed direct LAN source provenance is required"
    verify_direct_source(share, direct[0])
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


def verify_direct_source(share, source):
    upstream = source["upstream"]
    assert source["source_path"] == "internal/directlan"
    assert upstream["module"] == "github.com/tailscale/wireguard-go"
    assert upstream["version"] == "v0.0.0-20260928213032-417aef361226"
    assert upstream["commit"] == "417aef361226c869ab29e15fe3539b01173c4719"
    assert upstream["license"] == "MIT"
    assert upstream["module_sum"] == "h1:v3Lpj2iHPWQDqeCwemQPz4fWweIEMLqBkwJqCjRyJQc="
    assert upstream["go_mod_sum"] == "h1:rUelGmuK4UnSJYM5gl5Mknp6YbwwcL8+VAPMhNYe+jg="
    assert upstream["files"] == {
        "LICENSE": "91276db973f25602d1aa43491f59cbc84cb88e6f151e1d0cc82a755563ce0195",
        "tun/netstack/tun.go": "dc8bdff07b29630c2e0867c0b2b56d6e4de1035049be89c5979c6cffe4b7623b",
    }
    inputs = {i["path"]: i["sha256"] for i in source["build_inputs"]}
    assert "internal/directlan/stack.go" in inputs
    for name in ("WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md", "stack.go"):
        raw = read_regular(share, "licenses/source/internal/directlan/" + name)
        assert inputs["internal/directlan/" + name] == digest(raw)
        if name == "WIREGUARD_LICENSE":
            assert digest(raw) == upstream["files"]["LICENSE"]
        elif name == "UPSTREAM.json":
            assert json.loads(raw) == upstream
        elif name == "stack.go":
            assert raw.startswith(b"/* SPDX-License-Identifier: MIT\n * Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.\n")
    for name in ("go.mod", "go.sum"):
        assert inputs[name] == digest(read_regular(share, name))
