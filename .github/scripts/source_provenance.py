"""Verify retained adapted-source provenance in a package or installation."""
import hashlib
import json
import pathlib
import re


# Reviewed independently of the retained manifests. Updating an adaptation must
# update its Go pin and this distribution verifier in the same reviewed change.
ADAPTED_MODULES = {
    "tailscale.com": {
        "name": "tailscale", "source_path": "internal/engineadaptation",
        "manifest_path": "internal/engineadaptation/manifest.json",
        "manifest_sha256": "5b6f5fd829d169b811f0cc0e9843d58edcb0646500a92ce45167ea6e7b11b35a",
        "version": "v1.104.0", "license": "BSD-3-Clause",
        "module_sum": "h1:7LgglawekeutAexqXUzSxP/Kqo3HHwF/SkcbX5HpiU4=",
        "go_mod_sum": "h1:Cb1XjScRgSOeRD7NW6uCEznme782WiOU9IxqyTWoRf0=",
    },
    "github.com/tailscale/wireguard-go": {
        "name": "wireguard", "source_path": "internal/lifecycleadaptation/wireguard",
        "manifest_path": "internal/lifecycleadaptation/wireguard.json",
        "manifest_sha256": "c6af29048317e24df306f7d423d77b4db27f3d188d86c8598e0a12927c3029ef",
        "version": "v0.0.0-20260928213032-417aef361226", "license": "MIT",
        "module_sum": "h1:v3Lpj2iHPWQDqeCwemQPz4fWweIEMLqBkwJqCjRyJQc=",
        "go_mod_sum": "h1:rUelGmuK4UnSJYM5gl5Mknp6YbwwcL8+VAPMhNYe+jg=",
    },
    "gvisor.dev/gvisor": {
        "name": "gvisor", "source_path": "internal/lifecycleadaptation/gvisor",
        "manifest_path": "internal/lifecycleadaptation/gvisor.json",
        "manifest_sha256": "8462cc271c23a11c445c8b2997bf03fd5c822c81a5c59be7197f670b96f18f31",
        "version": "v0.0.0-20260915211658-a6f909f08a72", "license": "Apache-2.0",
        "module_sum": "h1:EytKYr5WrVs+Aah/0xDO8ZAZZzS/iTEVC8ln4ipDbL0=",
        "go_mod_sum": "h1:8aLQqUBHDH8fY5y60lzmwDpMMbQCcT3EBfoSwhfaGCY=",
    },
}

# The engine's older edit manifest has no notice inventory. These are all
# original notice files in its pinned module, including nested notices.
ENGINE_NOTICES = {
    ".github/licenses.tmpl": "d449925239921c1f51ad832efc4a43d4fe85c4b9df1b1702b5e6f509b004249d",
    "LICENSE": "a7ca6186a7963a0a60740f6047760eecd7a0234e8c38bd7e1e0bbcb324bda45b",
    "PATENTS": "debc5ca73d082a6c7de743fecdba6f75deafdf9200acfbddeb8ca32d66e6e128",
    "cmd/tailscale/cli/licenses.go": "273ce9ce95cbb24fc410b8da8717ede9e1e834f31f2cdbe6cf8f0dd2b49931f0",
    "derp/xdp/headers/LICENSE.BSD-2-Clause": "e1638b9a0c68ca90fad3df1d6b4e430804d2fbdc15e58d02cffddfae38953bbf",
    "license_test.go": "6c10979d9859262305f3a9971502aca4a20d9531f71ad2fc5cf66c258d21fd1e",
    "licenses/licenses.go": "7c3b3ec700df78e56eb955df2c5c9140ed79e2b736e03d272d27d585f3d40339",
    "tempfork/spf13/cobra/LICENSE.txt": "5e3400b93bbb099e83e52bab885e7441750673c21f97988ca3f1240639b63283",
    "tstest/tailmac/LICENSE/LICENSE.txt": "39f3ea9e9fc438419ed8c132f2a6a5f45f6f2d6ba47df100513f2b65d751aecd",
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def source_path(relative):
    assert isinstance(relative, str), "invalid source path"
    rel = pathlib.PurePosixPath(relative)
    assert relative and relative != "." and not rel.is_absolute() and ".." not in rel.parts
    assert rel.as_posix() == relative, "noncanonical source path"
    assert not any(c in relative for c in "\\:\r\n\0"), "invalid source path"
    return rel


def read_regular(share, relative):
    rel = source_path(relative)
    path = share
    for part in rel.parts:
        path = path / part
        assert not path.is_symlink(), "linked source notice"
    assert path.is_file(), "missing source notice"
    return path.read_bytes()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        assert key not in result, "duplicate source JSON key"
        result[key] = value
    return result


def read_json(raw):
    return json.loads(raw, object_pairs_hook=unique_object)


def record_hashes(records):
    result = {}
    for record in records:
        assert set(record) == {"path", "sha256"}, "invalid source record"
        path, sha256 = record["path"], record["sha256"]
        source_path(path)
        assert path not in result, "duplicate source record"
        assert isinstance(sha256, str) and re.fullmatch(r"[0-9a-f]{64}", sha256), "invalid source digest"
        result[path] = sha256
    return result


def source_properties(component):
    return unique_object((p["name"], p["value"]) for p in component["properties"])


def verify_sources(share, build, notices, bom):
    sources = notices.get("source_components", [])
    assert sources == build.get("source_components", []), "source metadata differs"
    components = {c["bom-ref"]: c for c in bom["components"]}
    assert len(components) == len(bom["components"]), "duplicate SBOM component"
    assert len({s["source_path"] for s in sources}) == len(sources), "duplicate source component"
    assert len({s["package"] for s in sources}) == len(sources), "duplicate source package"
    for module, pin in ADAPTED_MODULES.items():
        selected = [s for s in sources if s["package"] == module]
        assert len(selected) == 1, "reviewed adapted module provenance is required: " + module
        assert selected[0]["source_path"] == pin["source_path"], "adapted source identity differs"
        assert not any(m["module"] == module for m in notices["modules"]), "adapted source mislabeled as original module"
        ref = "source:" + pin["source_path"]
        original_ref = "golang:" + digest((module + "@" + pin["version"]).encode())
        for component in bom["components"]:
            assert component.get("name") != module or component["bom-ref"] == ref, "upstream runtime SBOM component"
            assert component["bom-ref"] != original_ref, "upstream runtime SBOM identity"
            assert not component.get("purl", "").startswith("pkg:golang/" + module + "@"), "upstream runtime SBOM purl"
        for dependency in bom.get("dependencies", []):
            for edge in [dependency["ref"], *dependency["dependsOn"]]:
                assert edge != original_ref, "upstream runtime SBOM dependency"
    shared_inputs = {}
    for source in sources:
        source_path(source["source_path"])
        inputs = source["build_inputs"]
        assert inputs and inputs == sorted(inputs, key=lambda x: x["path"])
        for path, sha256 in record_hashes(inputs).items():
            assert shared_inputs.get(path, sha256) == sha256, "shared source input digest differs"
            shared_inputs[path] = sha256
        canonical = json.dumps(inputs, ensure_ascii=False, separators=(",", ":")).encode()
        assert digest(canonical) == source["build_inputs_sha256"], "source build input digest differs"
        assert source["notices"], "missing source notices"
        record_hashes(source["notices"])
        for entry in source["notices"]:
            assert entry["path"].startswith("licenses/source/" + source["source_path"] + "/"), "source notice outside component"
            assert digest(read_regular(share, entry["path"])) == entry["sha256"], "source notice digest differs"
        component = components["source:" + source["source_path"]]
        assert component["type"] == "library" and component["name"] == source["package"], "source SBOM identity differs"
        assert component["version"] == build["version"], "source SBOM version differs"
        properties = source_properties(component)
        assert properties["source:path"] == source["source_path"]
        assert properties["source:modified"] == "true"
        assert properties["source:build-inputs:sha256"] == source["build_inputs_sha256"]
        assert read_json(properties["source:build-inputs"]) == inputs
        assert "purl" not in component, "adapted source must not claim upstream purl"
        assert len(component["pedigree"]["ancestors"]) == 1, "ambiguous source ancestor"
        ancestor = component["pedigree"]["ancestors"][0]
        upstream = source["upstream"]
        assert ancestor["name"] == upstream["module"] and ancestor["version"] == upstream["version"]
        ancestor_properties = source_properties(ancestor)
        assert ancestor_properties["source:commit"] == upstream["commit"]
        assert read_json(ancestor_properties["source:original-files:sha256"]) == upstream["files"]
        assert ancestor["licenses"] == [{"license": {"id": upstream["license"]}}]
        assert ancestor["externalReferences"] == [{"type": "vcs", "url": upstream["source_url"]}]
        if source["package"] in ADAPTED_MODULES:
            verify_adapted_source(share, source, component, ADAPTED_MODULES[source["package"]])
    direct = [s for s in sources if s["package"] == "github.com/webkaz-labs/sobalink/internal/directlan"]
    assert len(direct) == 1, "reviewed direct LAN source provenance is required"
    verify_direct_source(share, direct[0])


def retained_inputs(pin):
    """Map original repository inputs to their component-local retained paths."""
    base = str(pathlib.PurePosixPath(pin["manifest_path"]).parent)
    retained = {pin["manifest_path"]: "manifest.json"}
    for name in ("UPSTREAM.md", "pin.go", "adaptation.go"):
        retained[base + "/" + name] = name
    retained["cmd/prepare-engine/main.go"] = "prepare-engine/main.go"
    if pin["name"] == "tailscale":
        retained[base + "/archive.go"] = "archive.go"
    else:
        retained[base + "/materialize.go"] = "materialize.go"
        for name in ("adaptation.go", "archive.go"):
            retained["internal/engineadaptation/" + name] = "engineadaptation/" + name
        if pin["name"] == "gvisor":
            retained[base + "/licenses/gvisor/LICENSE"] = "licenses/gvisor/LICENSE"
    return retained


def notice_name(path):
    return pathlib.PurePosixPath(path).name.upper().startswith(
        ("LICENSE", "LICENCE", "COPYING", "NOTICE", "COPYRIGHT", "UNLICENSE", "PATENTS", "AUTHORS"))


def verify_adapted_source(share, source, component, pin):
    destination = "licenses/source/" + pin["source_path"] + "/"
    raw = read_regular(share, destination + "manifest.json")
    assert digest(raw) == pin["manifest_sha256"] == source["manifest_sha256"], "reviewed source manifest differs"
    manifest = read_json(raw)
    assert manifest["schema"] == 1 and manifest["module"] == source["package"]
    upstream = source["upstream"]
    assert upstream["module"] == manifest["module"]
    for key in ("version", "module_sum", "go_mod_sum"):
        assert manifest[key] == pin[key] == upstream[key], "original module identity differs"
    assert upstream["license"] == pin["license"]
    assert upstream["source_url"] == manifest["source_url"]
    assert upstream["commit"] == manifest.get("revision", "")
    assert manifest["upstream_tree_sha256"] == source["upstream"]["tree_sha256"]
    assert manifest["adapted_tree_sha256"] == source["adapted_tree_sha256"]
    inputs, notices = record_hashes(source["build_inputs"]), record_hashes(source["notices"])
    retained = retained_inputs(pin)
    original = {}
    module_notices = dict(ENGINE_NOTICES) if pin["name"] == "tailscale" else record_hashes(manifest["notices"])
    if pin["name"] != "tailscale":
        assert manifest["license"] == pin["license"]
        assert module_notices, "missing original module notices"
        original.update(module_notices)
    runtime = ".sobalink-deps/" + pin["name"] + "/"
    assert any(path.startswith(runtime) for path in inputs), "missing adapted runtime inputs"
    changed = set()
    for change in manifest["changes"]:
        path = change["path"]
        source_path(path)
        assert path not in changed, "duplicate adapted path"
        changed.add(path)
        if change["original_sha256"]:
            assert path not in original, "original source and notice paths overlap"
            original[path] = change["original_sha256"]
        selected = runtime + path
        if selected in inputs:
            assert inputs[selected] == change["adapted_sha256"], "adapted runtime source hash differs"
        if pin["name"] != "tailscale":
            overlay = change["source_path"]
            source_path(overlay)
            assert overlay == "sources/" + pin["name"] + "/" + path + ".txt", "adapted overlay path differs"
            assert digest(read_regular(share, destination + overlay)) == change["adapted_sha256"], "adapted overlay bytes differ"
            retained["internal/lifecycleadaptation/" + overlay] = overlay
        if notice_name(path):
            module_notices[path] = change["adapted_sha256"]
    assert original == source["upstream"]["files"]
    for path, expected in module_notices.items():
        assert notices[destination + path] == expected, "missing or changed original module notice"
        assert digest(read_regular(share, destination + path)) == expected, "original module notice bytes differ"
    if pin["name"] == "gvisor":
        assert notices[destination + "licenses/gvisor/LICENSE"] == module_notices["LICENSE"], "gVisor license mirror differs"
    for original_path, retained_path in retained.items():
        actual = digest(read_regular(share, destination + retained_path))
        assert inputs[original_path] == actual, "retained source input digest differs"
        assert notices[destination + retained_path] == actual, "retained source input missing from notices"
    for name in ("go.mod", "go.sum"):
        assert inputs[name] == digest(read_regular(share, name)), "retained module input differs"
    for path in inputs:
        assert path in retained or path in ("go.mod", "go.sum") or path.startswith(runtime), "source input outside exact adaptation"
    properties = source_properties(component)
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
