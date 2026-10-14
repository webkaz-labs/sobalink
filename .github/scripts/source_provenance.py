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
        "manifest_sha256": "e9554c46a24480e22e50e55b2c2474736284505b03f60120496ddb9ad63f2aa2",
        "version": "v1.104.0", "license": "BSD-3-Clause",
        "module_sum": "h1:7LgglawekeutAexqXUzSxP/Kqo3HHwF/SkcbX5HpiU4=",
        "go_mod_sum": "h1:Cb1XjScRgSOeRD7NW6uCEznme782WiOU9IxqyTWoRf0=",
    },
    "github.com/tailscale/wireguard-go": {
        "name": "wireguard", "source_path": "internal/lifecycleadaptation/wireguard",
        "manifest_path": "internal/lifecycleadaptation/wireguard.json",
        "manifest_sha256": "8903049b7e08c55c985faea522b1c042bad5937ba14c8d7be901754401819c9a",
        "version": "v0.0.0-20260928213032-417aef361226", "license": "MIT",
        "module_sum": "h1:v3Lpj2iHPWQDqeCwemQPz4fWweIEMLqBkwJqCjRyJQc=",
        "go_mod_sum": "h1:rUelGmuK4UnSJYM5gl5Mknp6YbwwcL8+VAPMhNYe+jg=",
    },
    "gvisor.dev/gvisor": {
        "name": "gvisor", "source_path": "internal/lifecycleadaptation/gvisor",
        "manifest_path": "internal/lifecycleadaptation/gvisor.json",
        "manifest_sha256": "e37db9389f8dd9fd1514fba9fc9b6b0a2e4351d858d7f597c7ff791e1a80af08",
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


# Finite Darwin native boundary. Paths below are public SDK install names or
# repository/module-relative names, never paths on a developer's computer.
DARWIN_LIBRARIES = (
    '/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation',
    '/System/Library/Frameworks/Security.framework/Versions/A/Security',
    '/usr/lib/libSystem.B.dylib',
    '/usr/lib/libresolv.9.dylib',
)
DARWIN_SDK_INPUTS = (
    'SDKSettings.json',
    'System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation.tbd',
    'System/Library/Frameworks/Security.framework/Versions/A/Security.tbd',
    'usr/include/dispatch/dispatch.h', 'usr/include/dns_sd.h',
    'usr/lib/libSystem.tbd', 'usr/lib/libresolv.9.tbd',
)
DARWIN_ROUTECAT_PACKAGE = 'github.com/webkaz-labs/sobalink/internal/routecat'
DARWIN_ROUTECAT_PATH = 'internal/routecat'
# Keep these compiled-source pins identical to native_routecat.go. Archive
# metadata is bound to the same review; this does not claim source redistribution.
DARWIN_ROUTECAT_INPUTS = {
    'disco.go': 'dc8c1e122ab92a046917289ec2dbffb9499cfbccda8bcebe251ff17f2a1d1a93',
    'listen.go': '8de4853cfb91199686d1f3451fa929f67ad7a185440b28a5c941d2c384353ae9',
    'regions.go': '36156c657a106fbe28d974557a6d8859ca779f4d3cc9b717aff7ccd54fab8ef6',
    'relay_failure.go': '832a2b47a70d05aae1fbb72e6a97873c11f6c11784a527616233cba670c4e633',
    'relay_failure_other.go': 'd5c96b35476ec68958671677a2f8bcb76eed14067ab9d00130bce492771b599a',
    'relay_resources.go': 'a5f5b30b450622575c5b31af55fb924762c1d9d26db5a99fa94da3a49b6abc6e',
    'tailcat.go': '2b1220b8833f438cd3d84350dff4f19e0b62c37dabe06c5bf11470b55694b9fb',
    'underlay_policy.go': '88c29c156ff423aa9ebbde6d709e909757a77c0bf576edc53b7bc0a3afc14621',
    'wan_candidates.go': '9f9037784c9d80a9e9b7ec6675f91cd23d4545f02def89490c876676e8eb75f0',
    'wire.go': 'cb7d714cc51c77dd90ea0602109788742d94e9b96be37505a2bcf988aa78e31b',
}
DARWIN_POLICY_INPUTS = {
    'tailscale.com/control/controlclient/sign_supported.go': '2319d740882481d8cf4f1dfd1f1ac853b678971928fd176ecfe5be1ffda9082b',
    'tailscale.com/util/syspolicy/policyclient/policyclient.go': 'e7fa6ea83699de60241f0f96c018c66ec1fb1061860435c7a325097ce5ebbaba',
    'tailscale.com/tsd/tsd.go': '255dee50953d2a19b3dd8e57a21d9bc2c3d10095a4828fb6d1126776a645383d',
    'github.com/tailscale/certstore/certstore.go': '4c63ea6a06aeac9f027de208d1113997867b1ad77bf75d91f12740ba3df5b977',
    'github.com/tailscale/certstore/certstore_darwin.go': '4e2698e090e5cec688c4d7d3d5178d4233b070c47c60fbb9e73cf1430179c117',
    **{DARWIN_ROUTECAT_PACKAGE + '/' + name: sha for name, sha in DARWIN_ROUTECAT_INPUTS.items()},
}
DARWIN_PACKAGES = ('github.com/tailscale/certstore',
                   'github.com/webkaz-labs/sobalink/internal/nearby', 'runtime/cgo')
DARWIN_CFLAGS = ['-O2', '-g0', '-isysroot', '${SDKROOT}', '-mmacosx-version-min=13.0']
DARWIN_LDFLAGS = ['-isysroot', '${SDKROOT}', '-mmacosx-version-min=13.0']
NATIVE_KEYS = {'schema', 'host_os', 'host_arch', 'host_version', 'host_build',
               'compiler_version', 'compiler_sha256', 'linker_sha256',
               'xcode_version', 'xcode_build', 'sdk_version', 'sdk_build',
               'deployment_target', 'sdk_inputs', 'cflags', 'ldflags',
               'packages', 'policy_inputs', 'system_libraries'}


def native_digest(native):
    return digest(json.dumps(native, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode())


DARWIN_GO_VERSION = 'go1.27.1'
DARWIN_BUILD_TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'


def darwin_build_uuid(source_commit, version, native_identity):
    """Public UUIDv8 build identity; full content SHA256 is verified separately."""
    assert isinstance(source_commit, str) and re.fullmatch(r'[0-9a-f]{40}', source_commit), 'invalid UUID source commit'
    assert isinstance(version, str) and re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?', version), 'invalid UUID version'
    if '-' in version:
        assert all(not (len(part) > 1 and part[0] == '0' and part.isdecimal()) for part in version.split('-', 1)[1].split('.')), 'invalid UUID prerelease version'
    assert isinstance(native_identity, str) and re.fullmatch(r'[0-9a-f]{64}', native_identity), 'invalid UUID native identity'
    identity = ('sobalink-darwin-uuid-v1\n' + source_commit + '\n' + version +
                '\ndarwin-arm64\n' + DARWIN_GO_VERSION + '\n' + DARWIN_BUILD_TAGS + '\n' + native_identity + '\n')
    uuid = bytearray(hashlib.sha256(identity.encode('ascii')).digest()[:16])
    uuid[6] = (uuid[6] & 0x0f) | 0x80
    uuid[8] = (uuid[8] & 0x3f) | 0x80
    return uuid.hex()


def verify_native(build, notices, bom, binary=None):
    """The same target-aware admission in assets, archive smoke and installation."""
    target = build.get('target')
    assert target in ('linux-amd64', 'linux-arm64', 'darwin-arm64', 'windows-amd64'), 'invalid build target'
    darwin = target == 'darwin-arm64'
    assert type(build.get('cgo_enabled')) is bool and build['cgo_enabled'] == darwin, 'wrong target cgo policy'
    assert build.get('go_version') == 'go1.27.1' and build.get('trimpath') is True and build.get('buildvcs') is False, 'unexpected native build settings'
    expected_flags = '-s -w -buildid= -X main.version=' + build['version']
    if darwin:
        assert build.get('build_tags') == DARWIN_BUILD_TAGS, 'unexpected Darwin build tags'
    else:
        assert build.get('ldflags') == expected_flags, 'unexpected linker flags'
    props = source_properties(bom['metadata'])
    assert props.get('build:target') == target and props.get('source:commit') == build['source_commit'], 'SBOM build identity differs'
    assert props.get('build:cgo') == ('1' if darwin else '0'), 'SBOM cgo policy differs'
    assert notices.get('scope') == props.get('inventory:scope') and ('CGO_ENABLED=' + ('1' if darwin else '0')) in notices['scope'], 'notice cgo scope differs'
    system = [c for c in bom.get('components', []) if c.get('bom-ref', '').startswith('system:')]
    if not darwin:
        assert 'native_build' not in build and 'native_build' not in notices and not system and 'build:native:sha256' not in props, 'unexpected native provenance for cgo0 target'
        return
    native = build.get('native_build')
    assert type(native) is dict and set(native) == NATIVE_KEYS and native == notices.get('native_build'), 'native provenance missing or inconsistent'
    expected_uuid = darwin_build_uuid(build['source_commit'], build['version'], native_digest(native))
    expected_flags += ' -linkmode=external -B=0x' + expected_uuid
    assert build.get('ldflags') == expected_flags, 'unexpected linker flags'
    assert type(native['schema']) is int and native['schema'] == 1, 'invalid native schema'
    assert native['host_os'] == 'darwin' and native['host_arch'] == 'arm64', 'non-native Darwin compiler'
    assert native['deployment_target'] == '13.0', 'unexpected deployment target'
    for key in ('host_version', 'xcode_version', 'sdk_version'):
        assert isinstance(native[key], str) and re.fullmatch(r'[0-9]{1,3}(\.[0-9]{1,3}){1,2}', native[key]), 'invalid native version'
    for key in ('host_build', 'xcode_build', 'sdk_build'):
        assert isinstance(native[key], str) and re.fullmatch(r'[0-9A-Za-z.]{1,32}', native[key]), 'invalid native build identifier'
    assert isinstance(native['compiler_version'], str) and re.fullmatch(r'Apple clang version [0-9.]{3,24} \(clang-[0-9.]{3,40}\)', native['compiler_version']), 'invalid compiler identity'
    for key in ('compiler_sha256', 'linker_sha256'):
        assert isinstance(native[key], str) and re.fullmatch('[0-9a-f]{64}', native[key]), 'missing native tool digest'
    assert native['cflags'] == DARWIN_CFLAGS and native['ldflags'] == DARWIN_LDFLAGS, 'unexpected C flags'
    assert list(record_hashes(native['sdk_inputs'])) == list(DARWIN_SDK_INPUTS), 'SDK input inventory differs'
    assert record_hashes(native['policy_inputs']) == DARWIN_POLICY_INPUTS, 'credential boundary is not reviewed'
    assert native['policy_inputs'] == sorted(native['policy_inputs'], key=lambda x: x['path']), 'unsorted policy inventory'
    sources = notices.get('source_components', [])
    assert sources == build.get('source_components', []), 'source metadata differs'
    routecat = [s for s in sources if s.get('package') == DARWIN_ROUTECAT_PACKAGE]
    assert len(routecat) == 1 and routecat[0].get('source_path') == DARWIN_ROUTECAT_PATH, 'reviewed policy-container source is required'
    source_inputs = record_hashes(routecat[0]['build_inputs'])
    metadata_inputs = {'go.mod', 'go.sum', *(DARWIN_ROUTECAT_PATH + '/' + name for name in ('LICENSE', 'UPSTREAM.json', 'UPSTREAM.md'))}
    runtime_inputs = {path: sha for path, sha in source_inputs.items() if path not in metadata_inputs}
    assert runtime_inputs == {DARWIN_ROUTECAT_PATH + '/' + name: sha for name, sha in DARWIN_ROUTECAT_INPUTS.items()}, 'policy-container source inventory differs from review'
    assert metadata_inputs <= set(source_inputs), 'policy-container metadata inputs are missing'
    cert_name = DARWIN_PACKAGES[0]
    cert_version = 'v0.1.1-0.20260409135935-3638fb84b77d'
    cert_sum = 'h1:JcGKBZAL7ePLwOhUdN8qGQZlP5GueEiIZwY7R62pejE='
    cert_ref = 'golang:' + digest((cert_name + '@' + cert_version).encode())
    cert_notices = [m for m in notices.get('modules', []) if m.get('module') == cert_name]
    assert len(cert_notices) == 1 and cert_notices[0].get('version') == cert_version and cert_notices[0].get('go_module_sum') == cert_sum, 'certificate-store module inventory differs'
    cert_license = record_hashes(cert_notices[0]['notices'])
    assert cert_license.get('licenses/' + cert_ref.removeprefix('golang:') + '/LICENSE.md') == 'e4a0c3d87230b78d441e4666fba1ab95211fd1e9be76bf445d664685ae0a9e48', 'certificate-store notice missing or changed'
    cert_components = [c for c in bom.get('components', []) if c.get('bom-ref') == cert_ref]
    assert len(cert_components) == 1 and cert_components[0].get('name') == cert_name and cert_components[0].get('version') == cert_version, 'certificate-store SBOM identity differs'
    assert source_properties(cert_components[0]).get('go:module:sum') == cert_sum, 'certificate-store SBOM source differs'
    packages = native['packages']
    assert isinstance(packages, list) and [p.get('package') for p in packages] == list(DARWIN_PACKAGES), 'native package closure differs'
    for package in packages:
        assert set(package) == {'package', 'inputs', 'cflags', 'cppflags', 'cxxflags', 'ldflags'}, 'invalid native package record'
        inputs = record_hashes(package['inputs'])
        assert inputs and package['inputs'] == sorted(package['inputs'], key=lambda x: x['path']), 'missing or unsorted native inputs'
        assert all('/' not in p and not p.endswith('_test.go') for p in inputs), 'invalid native source name'
        assert package['cppflags'] == [] and package['cxxflags'] == [], 'unreviewed preprocessor/C++ flags'
        if package['package'] == DARWIN_PACKAGES[0]:
            assert package['cflags'] == ['-x', 'objective-c'] and package['ldflags'] == ['-framework', 'CoreFoundation', '-framework', 'Security'], 'certificate-store flags differ'
            assert inputs == {p.rsplit('/', 1)[-1]: h for p, h in DARWIN_POLICY_INPUTS.items() if p.startswith(DARWIN_PACKAGES[0] + '/')}, 'certificate-store source differs'
        elif package['package'] == DARWIN_PACKAGES[1]:
            assert package['cflags'] == ['-std=c11'] and package['ldflags'] == [], 'DNS-SD flags differ'
            assert {'provider_darwin.go', 'dnssd_bridge_darwin.c', 'dnssd_bridge_darwin.h'} <= set(inputs), 'DNS-SD shim inputs missing'
        else:
            assert package['cflags'] == ['-Wall', '-Werror', '-fno-stack-protector', '-Wdeclaration-after-statement', '-std=gnu90'] and package['ldflags'] == [], 'Go runtime cgo flags differ'
    libraries = native['system_libraries']
    assert libraries == list(DARWIN_LIBRARIES), 'system library inventory differs'
    assert len(system) == len(libraries) and {c['bom-ref'] for c in system} == {'system:' + p for p in libraries}, 'system SBOM inventory differs'
    for component in system:
        assert component.get('type') == 'library' and component.get('name') in libraries and component['bom-ref'] == 'system:' + component['name'], 'system component identity differs'
        assert source_properties(component) == {'distribution:provided-by': 'operating-system', 'distribution:redistributed': 'false'}, 'system component distribution differs'
    root_edges = [e for e in bom['dependencies'] if e.get('ref') == 'application:sobalink']
    assert len(root_edges) == 1 and {'system:' + p for p in libraries} <= set(root_edges[0]['dependsOn']), 'system dependency edges missing'
    assert props.get('build:native:sha256') == native_digest(native), 'native SBOM digest differs'
    if binary is not None:
        assert read_macho_libraries(binary, expected_uuid) == libraries, 'installed native linkage differs'


def read_macho_libraries(path, expected_uuid):
    """Bounded direct Mach-O inspection; no compiler or credential API invoked."""
    import struct
    assert isinstance(expected_uuid, str) and re.fullmatch(r'[0-9a-f]{12}8[0-9a-f]{3}[89ab][0-9a-f]{15}', expected_uuid), 'invalid expected Darwin UUID'
    uuid = bytes.fromhex(expected_uuid)
    with pathlib.Path(path).open('rb') as stream:
        header = stream.read(32)
        assert len(header) == 32, 'incomplete Mach-O header'
        magic, cpu, subtype, filetype, count, size, flags, reserved = struct.unpack('<8I', header)
        assert magic == 0xfeedfacf and cpu == 0x100000c and filetype == 2, 'wrong installed Mach-O target'
        assert 0 < count <= 1024 and count * 8 <= size <= 1024 * 1024, 'unbounded Mach-O commands'
        raw = stream.read(size)
        assert len(raw) == size, 'incomplete Mach-O commands'
    libraries, offset, deployment, found_uuid = [], 0, False, False
    for _ in range(count):
        assert offset + 8 <= len(raw), 'short Mach-O command'
        command, length = struct.unpack_from('<2I', raw, offset)
        assert length >= 8 and length % 8 == 0 and offset + length <= len(raw), 'invalid Mach-O command length'
        block = raw[offset:offset + length]
        assert command != 0x8000001c, 'unreviewed Mach-O runtime search path'
        if command == 0x1b:
            assert not found_uuid and length == 24 and block[8:] == uuid, 'invalid or unexpected Darwin UUID'
            found_uuid = True
        if command in (0xc, 0x80000018, 0x8000001f, 0x20, 0x80000023):
            assert length >= 24, 'short Mach-O library command'
            start = struct.unpack_from('<I', block, 8)[0]
            assert 24 <= start < length and b'\0' in block[start:], 'invalid Mach-O library name'
            libraries.append(block[start:].split(b'\0', 1)[0].decode('ascii'))
        if command == 0x32:
            assert not deployment and length >= 24, 'invalid Mach-O deployment command'
            assert struct.unpack_from('<I', block, 20)[0] * 8 + 24 == length, 'invalid Mach-O deployment tools'
            assert struct.unpack_from('<2I', block, 8) == (1, 13 << 16), 'wrong installed macOS deployment target'
            deployment = True
        assert command not in (0x24, 0x25, 0x2f, 0x30), 'unreviewed Mach-O deployment command'
        offset += length
    assert offset == size and deployment and found_uuid and sorted(libraries) == list(DARWIN_LIBRARIES), 'invalid Mach-O inventory'
    return sorted(libraries)
