"""Fail-closed checks for independently retained adapted-source records."""
import copy
import json
import pathlib
import re
import tempfile
import unittest
from unittest import mock

from source_provenance import ADAPTED_MODULES, ENGINE_NOTICES, digest, read_regular, verify_sources


REPOSITORY = pathlib.Path(__file__).resolve().parents[2]


class SourceProvenanceTests(unittest.TestCase):
    def write(self, root, path, raw):
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(raw)

    def fixture(self, root):
        self.write(root, "go.mod", b"module fixture\n")
        self.write(root, "go.sum", b"fixture checksums\n")
        build = {"version": "1.2.3", "source_components": []}
        notices, bom = {"source_components": [], "modules": []}, {"components": [], "dependencies": []}
        pins = copy.deepcopy(ADAPTED_MODULES)
        engine_notices = {name: digest(("fixture notice: " + name).encode()) for name in ENGINE_NOTICES}
        for module, pin in pins.items():
            name, base = pin["name"], str(pathlib.PurePosixPath(pin["manifest_path"]).parent)
            destination = "licenses/source/" + pin["source_path"] + "/"
            files = {}
            notice_names = list(engine_notices) if name == "tailscale" else (["LICENSE"] if name == "wireguard" else ["AUTHORS", "LICENSE"])
            original_notices = []
            for notice in notice_names:
                files[notice] = ("fixture notice: " + notice).encode()
                original_notices.append({"path": notice, "sha256": digest(files[notice])})
            paths = ("source.go", "added.go") if name == "tailscale" else (
                ("device/device.go", "device/owned.go") if name == "wireguard" else
                ("pkg/tcpip/transport/tcp/forwarder.go", "pkg/tcpip/transport/tcp/forwarder_owned.go"))
            changes = []
            for index, path in enumerate(paths):
                raw = ("// fixture adapted " + name + "/" + path + "\n").encode()
                change = {"path": path, "original_sha256": "c" * 64 if index == 0 else "", "adapted_sha256": digest(raw)}
                if name != "tailscale":
                    change["source_path"] = "sources/" + name + "/" + path + ".txt"
                    files[change["source_path"]] = raw
                changes.append(change)
            if name == "tailscale":
                files["net/underlayguard/LICENSE"] = b"fixture added license\n"
                changes.append({"path": "net/underlayguard/LICENSE", "original_sha256": "",
                                "adapted_sha256": digest(files["net/underlayguard/LICENSE"])})
            manifest = {"schema": 1, "module": module, "version": pin["version"],
                        "module_sum": pin["module_sum"], "go_mod_sum": pin["go_mod_sum"],
                        "source_url": "https://example.invalid/" + name,
                        "upstream_tree_sha256": "a" * 64, "adapted_tree_sha256": "b" * 64, "changes": changes}
            if name != "tailscale":
                manifest.update(revision="e" * 40, license=pin["license"], notices=original_notices)
            files["manifest.json"] = (json.dumps(manifest) + "\n").encode()
            pin["manifest_sha256"] = digest(files["manifest.json"])
            retained = {pin["manifest_path"]: "manifest.json", "cmd/prepare-engine/main.go": "prepare-engine/main.go"}
            for filename in ("UPSTREAM.md", "pin.go", "adaptation.go"):
                retained[base + "/" + filename] = filename
            if name == "tailscale":
                retained[base + "/archive.go"] = "archive.go"
            else:
                retained[base + "/materialize.go"] = "materialize.go"
                for filename in ("adaptation.go", "archive.go"):
                    retained["internal/engineadaptation/" + filename] = "engineadaptation/" + filename
                if name == "gvisor":
                    retained[base + "/licenses/gvisor/LICENSE"] = "licenses/gvisor/LICENSE"
                    files["licenses/gvisor/LICENSE"] = files["LICENSE"]
                for change in changes:
                    retained["internal/lifecycleadaptation/" + change["source_path"]] = change["source_path"]
            for original, target in retained.items():
                if target not in files:
                    files[target] = ("fixture retained input: " + original + "\n").encode()
            inputs = {original: digest(files[target]) for original, target in retained.items()}
            inputs.update({path: digest((root / path).read_bytes()) for path in ("go.mod", "go.sum")})
            for change in changes:
                if change["path"].endswith(".go"):
                    inputs[".sobalink-deps/" + name + "/" + change["path"]] = change["adapted_sha256"]
            for path, raw in files.items():
                self.write(root, destination + path, raw)
            original_files = {c["path"]: c["original_sha256"] for c in changes if c["original_sha256"]}
            if name != "tailscale":
                original_files.update({n["path"]: n["sha256"] for n in original_notices})
            source = {"package": module, "source_path": pin["source_path"],
                      "manifest_sha256": pin["manifest_sha256"], "adapted_tree_sha256": manifest["adapted_tree_sha256"],
                      "notices": [{"path": destination + path, "sha256": digest(raw)} for path, raw in sorted(files.items())],
                      "build_inputs": [{"path": path, "sha256": sha} for path, sha in sorted(inputs.items())],
                      "upstream": {"module": module, "version": pin["version"], "commit": manifest.get("revision", ""),
                                   "source_url": manifest["source_url"], "license": pin["license"], "files": original_files,
                                   "module_sum": pin["module_sum"], "go_mod_sum": pin["go_mod_sum"], "tree_sha256": manifest["upstream_tree_sha256"]}}
            notices["source_components"].append(source)
        self.add_direct_source(root, notices)
        for source in notices["source_components"]:
            self.sync_source(build, notices, bom, source)
        patcher = mock.patch.dict(ADAPTED_MODULES, pins, clear=True)
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = mock.patch.dict(ENGINE_NOTICES, engine_notices, clear=True)
        patcher.start()
        self.addCleanup(patcher.stop)
        return build, notices, bom

    def add_direct_source(self, root, notices):
        originals = REPOSITORY / "internal/directlan"
        inputs = {path: digest((root / path).read_bytes()) for path in ("go.mod", "go.sum")}
        retained = []
        for name in ("WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md", "stack.go"):
            raw = (originals / name).read_bytes()
            path = "licenses/source/internal/directlan/" + name
            self.write(root, path, raw)
            inputs["internal/directlan/" + name] = digest(raw)
            retained.append({"path": path, "sha256": digest(raw)})
        notices["source_components"].append({
            "package": "github.com/webkaz-labs/sobalink/internal/directlan", "source_path": "internal/directlan",
            "build_inputs": [{"path": path, "sha256": sha} for path, sha in sorted(inputs.items())],
            "notices": retained, "upstream": json.loads((originals / "UPSTREAM.json").read_text())})

    def sync_source(self, build, notices, bom, source):
        """Keep redundant metadata consistent so mutations reach deeper checks."""
        source["build_inputs_sha256"] = digest(json.dumps(source["build_inputs"], separators=(",", ":")).encode())
        build["source_components"] = copy.deepcopy(notices["source_components"])
        upstream = source["upstream"]
        props = {"source:path": source["source_path"], "source:modified": "true",
                 "source:build-inputs:sha256": source["build_inputs_sha256"], "source:build-inputs": json.dumps(source["build_inputs"])}
        if "manifest_sha256" in source:
            props.update({"source:manifest:sha256": source["manifest_sha256"], "source:adapted-tree:sha256": source["adapted_tree_sha256"],
                          "source:upstream-tree:sha256": upstream["tree_sha256"], "source:upstream-module:sum": upstream["module_sum"],
                          "source:upstream-go-mod:sum": upstream["go_mod_sum"]})
        ancestor = {"name": upstream["module"], "version": upstream["version"],
                    "properties": [{"name": "source:commit", "value": upstream["commit"]},
                                   {"name": "source:original-files:sha256", "value": json.dumps(upstream["files"])}],
                    "licenses": [{"license": {"id": upstream["license"]}}],
                    "externalReferences": [{"type": "vcs", "url": upstream["source_url"]}]}
        component = {"type": "library", "name": source["package"], "version": build["version"],
                     "bom-ref": "source:" + source["source_path"], "properties": [{"name": key, "value": value} for key, value in props.items()],
                     "pedigree": {"ancestors": [ancestor]}}
        bom["components"] = [c for c in bom["components"] if c["bom-ref"] != component["bom-ref"]]
        bom["components"].append(component)

    def source(self, notices, module):
        return next(s for s in notices["source_components"] if s["package"] == module)

    def replace_input(self, source, path, value):
        source["build_inputs"] = [i for i in source["build_inputs"] if i["path"] != path]
        if value is not None:
            source["build_inputs"].append({"path": path, "sha256": value})
            source["build_inputs"].sort(key=lambda i: i["path"])

    def test_reviewed_pins_match_repository(self):
        for module, pin in ADAPTED_MODULES.items():
            with self.subTest(module=module):
                raw = (REPOSITORY / pin["manifest_path"]).read_bytes()
                self.assertEqual(digest(raw), pin["manifest_sha256"])
                pin_file = (REPOSITORY / pin["manifest_path"]).parent / "pin.go"
                self.assertIn('"' + pin["manifest_sha256"] + '"', pin_file.read_text())
                manifest = json.loads(raw)
                self.assertEqual(manifest["module"], module)
                for key in ("version", "module_sum", "go_mod_sum"):
                    self.assertEqual(manifest[key], pin[key])

    def test_intact_sources(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            verify_sources(root, *self.fixture(root))

    def test_requires_all_three_adapted_modules(self):
        for module in tuple(ADAPTED_MODULES):
            with self.subTest(module=module), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                build, notices, bom = self.fixture(root)
                notices["source_components"] = [s for s in notices["source_components"] if s["package"] != module]
                build["source_components"] = copy.deepcopy(notices["source_components"])
                with self.assertRaisesRegex(AssertionError, "adapted module provenance is required"):
                    verify_sources(root, build, notices, bom)
                self.doCleanups()

    def test_rejects_adapted_source_drift(self):
        cases = ("notice bytes", "missing notice record", "upstream runtime", "SBOM upstream runtime", "SBOM upstream edge", "SBOM upstream purl",
                 "source identity", "upstream module", "upstream version", "module checksum", "go.mod checksum", "tree digest",
                 "adapted tree digest", "original file", "manifest bytes", "manifest repinned", "missing runtime inputs",
                 "runtime input hash", "input outside module", "retained helper", "missing helper input", "input digest",
                 "missing SBOM", "SBOM name", "SBOM version", "SBOM purl", "SBOM property", "duplicate SBOM",
                 "duplicate source", "duplicate input", "duplicate notice", "unsorted inputs")
        for module in tuple(ADAPTED_MODULES):
            for case in cases:
                with self.subTest(module=module, case=case), tempfile.TemporaryDirectory() as temp:
                    root = pathlib.Path(temp)
                    build, notices, bom = self.fixture(root)
                    source = self.source(notices, module)
                    pin = ADAPTED_MODULES[module]
                    destination = "licenses/source/" + source["source_path"] + "/"
                    runtime_inputs = [i for i in source["build_inputs"] if i["path"].startswith(".sobalink-deps/")]
                    if case == "notice bytes":
                        self.write(root, destination + "LICENSE", b"changed")
                    elif case == "missing notice record":
                        source["notices"] = [n for n in source["notices"] if n["path"] != destination + "LICENSE"]
                    elif case == "upstream runtime":
                        notices["modules"].append({"module": module})
                    elif case == "source identity":
                        source["source_path"] += "/other"
                    elif case.startswith("upstream "):
                        source["upstream"][case.split()[1]] = "other"
                    elif case in ("module checksum", "go.mod checksum", "tree digest"):
                        key = {"module checksum": "module_sum", "go.mod checksum": "go_mod_sum", "tree digest": "tree_sha256"}[case]
                        source["upstream"][key] = "0" * 64
                    elif case == "adapted tree digest":
                        source["adapted_tree_sha256"] = "0" * 64
                    elif case == "original file":
                        source["upstream"]["files"]["unreviewed.go"] = "0" * 64
                    elif case in ("manifest bytes", "manifest repinned"):
                        raw = (root / (destination + "manifest.json")).read_bytes() + b" "
                        self.write(root, destination + "manifest.json", raw)
                        if case == "manifest repinned":
                            source["manifest_sha256"] = digest(raw)
                            self.replace_input(source, pin["manifest_path"], digest(raw))
                            for entry in source["notices"]:
                                if entry["path"] == destination + "manifest.json":
                                    entry["sha256"] = digest(raw)
                    elif case == "missing runtime inputs":
                        source["build_inputs"] = [i for i in source["build_inputs"] if i not in runtime_inputs]
                    elif case == "runtime input hash":
                        runtime_inputs[0]["sha256"] = "0" * 64
                    elif case == "input outside module":
                        self.replace_input(source, ".sobalink-deps/other/source.go", "0" * 64)
                    elif case == "retained helper":
                        self.write(root, destination + "pin.go", b"changed")
                    elif case == "missing helper input":
                        self.replace_input(source, "cmd/prepare-engine/main.go", None)
                    elif case == "input digest":
                        self.write(root, "go.mod", b"changed")
                    elif case == "duplicate source":
                        notices["source_components"].append(copy.deepcopy(source))
                    elif case == "duplicate input":
                        source["build_inputs"].insert(0, copy.deepcopy(source["build_inputs"][0]))
                    elif case == "duplicate notice":
                        source["notices"].append(copy.deepcopy(source["notices"][0]))
                    elif case == "unsorted inputs":
                        source["build_inputs"].reverse()
                    self.sync_source(build, notices, bom, source)
                    component = next(c for c in bom["components"] if c["bom-ref"] == "source:" + source["source_path"])
                    if case == "missing SBOM":
                        bom["components"].remove(component)
                    elif case == "SBOM upstream runtime":
                        bom["components"].append({"bom-ref": "golang:" + digest((module + "@" + pin["version"]).encode()), "name": module})
                    elif case == "SBOM upstream edge":
                        bom["dependencies"].append({"ref": "application:sobalink", "dependsOn": ["golang:" + digest((module + "@" + pin["version"]).encode())]})
                    elif case == "SBOM upstream purl":
                        bom["components"].append({"bom-ref": "unreviewed:module", "purl": "pkg:golang/" + module + "@" + pin["version"]})
                    elif case == "SBOM name":
                        component["name"] = "other"
                    elif case == "SBOM version":
                        component["version"] = pin["version"]
                    elif case == "SBOM purl":
                        component["purl"] = "pkg:golang/" + module + "@" + pin["version"]
                    elif case == "SBOM property":
                        next(p for p in component["properties"] if p["name"] == "source:manifest:sha256")["value"] = "0" * 64
                    elif case == "duplicate SBOM":
                        bom["components"].append(copy.deepcopy(component))
                    with self.assertRaises((AssertionError, KeyError)):
                        verify_sources(root, build, notices, bom)
                    self.doCleanups()

    def test_rejects_missing_or_changed_readable_overlays(self):
        for module in ("github.com/tailscale/wireguard-go", "gvisor.dev/gvisor"):
            for case in ("missing", "changed", "changed with metadata", "missing input", "missing notice", "symbolic link"):
                with self.subTest(module=module, case=case), tempfile.TemporaryDirectory() as temp:
                    root = pathlib.Path(temp)
                    build, notices, bom = self.fixture(root)
                    source = self.source(notices, module)
                    entry = next(n for n in source["notices"] if "/sources/" in n["path"])
                    overlay_path = entry["path"].split("/sources/", 1)[1]
                    original = "internal/lifecycleadaptation/sources/" + overlay_path
                    target = root / entry["path"]
                    if case in ("missing", "symbolic link"):
                        raw = target.read_bytes()
                        target.unlink()
                        if case == "symbolic link":
                            self.write(root, "outside.txt", raw)
                            try:
                                target.symlink_to(root / "outside.txt")
                            except (OSError, NotImplementedError):
                                self.skipTest("symbolic links unavailable")
                    elif case.startswith("changed"):
                        target.write_bytes(b"changed overlay\n")
                        if case == "changed with metadata":
                            entry["sha256"] = digest(target.read_bytes())
                            self.replace_input(source, original, entry["sha256"])
                    elif case == "missing input":
                        self.replace_input(source, original, None)
                    elif case == "missing notice":
                        source["notices"].remove(entry)
                    self.sync_source(build, notices, bom, source)
                    with self.assertRaises((AssertionError, KeyError)):
                        verify_sources(root, build, notices, bom)
                    self.doCleanups()

    def test_rejects_noncanonical_source_paths(self):
        for path in ("../outside", "/absolute", "C:/absolute", "nested\\file", "./file", "nested//file", "nested/../file", ".", "", "file\n"):
            with self.subTest(path=path), tempfile.TemporaryDirectory() as temp:
                with self.assertRaises(AssertionError):
                    read_regular(pathlib.Path(temp), path)

    def test_rejects_manifest_overlay_path_substitution(self):
        for path in ("../outside", "sources/gvisor/device/device.go.txt", "sources//wireguard/device/device.go.txt"):
            with self.subTest(path=path), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                build, notices, bom = self.fixture(root)
                module = "github.com/tailscale/wireguard-go"
                source, pin = self.source(notices, module), ADAPTED_MODULES[module]
                manifest_path = "licenses/source/" + source["source_path"] + "/manifest.json"
                manifest = json.loads((root / manifest_path).read_bytes())
                manifest["changes"][0]["source_path"] = path
                raw = json.dumps(manifest).encode()
                self.write(root, manifest_path, raw)
                # Authenticate the deliberately invalid synthetic manifest so
                # this test reaches path checks after the independent pin gate.
                source["manifest_sha256"] = pin["manifest_sha256"] = digest(raw)
                self.replace_input(source, pin["manifest_path"], digest(raw))
                next(n for n in source["notices"] if n["path"] == manifest_path)["sha256"] = digest(raw)
                self.sync_source(build, notices, bom, source)
                with self.assertRaises(AssertionError):
                    verify_sources(root, build, notices, bom)
                self.doCleanups()

    def test_rejects_redundant_metadata_drift(self):
        for case in ("build metadata", "input digest", "SBOM input digest", "ancestor identity", "duplicate property", "notice traversal"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                build, notices, bom = self.fixture(root)
                source = self.source(notices, "tailscale.com")
                component = next(c for c in bom["components"] if c["name"] == "tailscale.com")
                if case == "build metadata":
                    build["source_components"][0]["manifest_sha256"] = "0" * 64
                elif case == "input digest":
                    source["build_inputs_sha256"] = "0" * 64
                    build["source_components"] = copy.deepcopy(notices["source_components"])
                elif case == "SBOM input digest":
                    next(p for p in component["properties"] if p["name"] == "source:build-inputs:sha256")["value"] = "0" * 64
                elif case == "ancestor identity":
                    component["pedigree"]["ancestors"][0]["name"] = "other"
                elif case == "duplicate property":
                    component["properties"].append(copy.deepcopy(component["properties"][0]))
                elif case == "notice traversal":
                    source["notices"][0]["path"] = "../outside"
                    build["source_components"] = copy.deepcopy(notices["source_components"])
                with self.assertRaises((AssertionError, KeyError)):
                    verify_sources(root, build, notices, bom)
                self.doCleanups()

    def test_rejects_changed_gvisor_license_mirror(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            build, notices, bom = self.fixture(root)
            source = self.source(notices, "gvisor.dev/gvisor")
            path = "licenses/source/" + source["source_path"] + "/licenses/gvisor/LICENSE"
            raw = b"changed mirror\n"
            self.write(root, path, raw)
            next(n for n in source["notices"] if n["path"] == path)["sha256"] = digest(raw)
            self.replace_input(source, "internal/lifecycleadaptation/licenses/gvisor/LICENSE", digest(raw))
            self.sync_source(build, notices, bom, source)
            with self.assertRaisesRegex(AssertionError, "gVisor license mirror differs"):
                verify_sources(root, build, notices, bom)

    def test_rejects_linked_notice_parent(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            self.write(root, "actual/notice", b"notice")
            try:
                (root / "linked").symlink_to(root / "actual", target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest("symbolic links unavailable")
            with self.assertRaisesRegex(AssertionError, "linked source notice"):
                read_regular(root, "linked/notice")

    def test_preserves_direct_lan_checks(self):
        for case in ("missing", "license", "original hash", "stack source"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                build, notices, bom = self.fixture(root)
                source = self.source(notices, "github.com/webkaz-labs/sobalink/internal/directlan")
                if case == "missing":
                    notices["source_components"].remove(source)
                    build["source_components"] = copy.deepcopy(notices["source_components"])
                else:
                    if case == "license":
                        self.write(root, "licenses/source/internal/directlan/WIREGUARD_LICENSE", b"changed")
                    elif case == "original hash":
                        source["upstream"]["files"]["tun/netstack/tun.go"] = "0" * 64
                    elif case == "stack source":
                        self.write(root, "licenses/source/internal/directlan/stack.go", b"changed")
                    self.sync_source(build, notices, bom, source)
                with self.assertRaises((AssertionError, KeyError)):
                    verify_sources(root, build, notices, bom)
                self.doCleanups()


if __name__ == "__main__":
    unittest.main()


def add_native_fixture(build, notices, bom, target='darwin-arm64'):
    """Coherent synthetic metadata only; never invokes native credential APIs."""
    from source_provenance import (DARWIN_CFLAGS, DARWIN_LDFLAGS, DARWIN_LIBRARIES,
        DARWIN_PACKAGES, DARWIN_POLICY_INPUTS, DARWIN_SDK_INPUTS, native_digest,
        DARWIN_ROUTECAT_PACKAGE, DARWIN_ROUTECAT_PATH, DARWIN_ROUTECAT_INPUTS)
    darwin = target == 'darwin-arm64'
    build.update(target=target, cgo_enabled=darwin, go_version='go1.27.1', trimpath=True,
                 buildvcs=False, source_commit='a' * 40)
    build['ldflags'] = '-s -w -buildid= -X main.version=' + build['version'] + (' -linkmode=external -B=none' if darwin else '')
    notices['scope'] = 'Target-filtered inventory (CGO_ENABLED=' + ('1' if darwin else '0') + ').'
    props = {'build:target': target, 'source:commit': build['source_commit'],
             'build:cgo': '1' if darwin else '0', 'inventory:scope': notices['scope']}
    bom.setdefault('components', [])
    bom.setdefault('dependencies', [])
    if darwin:
        source_inputs = {DARWIN_ROUTECAT_PATH + '/' + name: sha for name, sha in DARWIN_ROUTECAT_INPUTS.items()}
        source_inputs.update({path: 'd' * 64 for path in ('go.mod', 'go.sum', *(DARWIN_ROUTECAT_PATH + '/' + name for name in ('LICENSE', 'UPSTREAM.json', 'UPSTREAM.md')))})
        notices.setdefault('source_components', []).append({'package': DARWIN_ROUTECAT_PACKAGE,
            'source_path': DARWIN_ROUTECAT_PATH, 'build_inputs': [{'path': path, 'sha256': sha} for path, sha in sorted(source_inputs.items())]})
        build['source_components'] = copy.deepcopy(notices['source_components'])
        packages = []
        for name in DARWIN_PACKAGES:
            files, cflags, ldflags = {'cgo.go': '1' * 64}, ['-Wall', '-Werror', '-fno-stack-protector', '-Wdeclaration-after-statement', '-std=gnu90'], []
            if name == DARWIN_PACKAGES[0]:
                files = {p.rsplit('/', 1)[1]: h for p, h in DARWIN_POLICY_INPUTS.items() if p.startswith(name + '/')}
                cflags, ldflags = ['-x', 'objective-c'], ['-framework', 'CoreFoundation', '-framework', 'Security']
            elif name == DARWIN_PACKAGES[1]:
                files = {p: '2' * 64 for p in ('provider_darwin.go', 'dnssd_bridge_darwin.c', 'dnssd_bridge_darwin.h')}
                cflags, ldflags = ['-std=c11'], []
            packages.append({'package': name, 'inputs': [{'path': p, 'sha256': h} for p, h in sorted(files.items())],
                'cflags': cflags, 'cppflags': [], 'cxxflags': [], 'ldflags': ldflags})
        cert_name, cert_version = DARWIN_PACKAGES[0], 'v0.1.1-0.20260409135935-3638fb84b77d'
        cert_sum = 'h1:JcGKBZAL7ePLwOhUdN8qGQZlP5GueEiIZwY7R62pejE='
        cert_key = digest((cert_name + '@' + cert_version).encode())
        notices.setdefault('modules', []).append({'module': cert_name, 'version': cert_version, 'go_module_sum': cert_sum,
            'notices': [{'path': 'licenses/' + cert_key + '/LICENSE.md', 'sha256': 'e4a0c3d87230b78d441e4666fba1ab95211fd1e9be76bf445d664685ae0a9e48'}]})
        bom['components'].append({'bom-ref': 'golang:' + cert_key, 'name': cert_name, 'version': cert_version,
            'properties': [{'name': 'go:module:sum', 'value': cert_sum}]})
        native = {'schema': 1, 'host_os': 'darwin', 'host_arch': 'arm64', 'host_version': '26.0', 'host_build': '25A1',
            'compiler_version': 'Apple clang version 17.0.0 (clang-1700.1.2)', 'compiler_sha256': 'a' * 64,
            'linker_sha256': 'b' * 64, 'xcode_version': '26.0', 'xcode_build': '17A1', 'sdk_version': '26.0',
            'sdk_build': '25A1', 'deployment_target': '13.0', 'sdk_inputs': [{'path': p, 'sha256': 'c' * 64} for p in DARWIN_SDK_INPUTS],
            'cflags': DARWIN_CFLAGS.copy(), 'ldflags': DARWIN_LDFLAGS.copy(), 'packages': packages,
            'policy_inputs': [{'path': p, 'sha256': h} for p, h in sorted(DARWIN_POLICY_INPUTS.items())],
            'system_libraries': list(DARWIN_LIBRARIES)}
        build['native_build'] = copy.deepcopy(native)
        notices['native_build'] = copy.deepcopy(native)
        props['build:native:sha256'] = native_digest(native)
        refs = []
        for library in DARWIN_LIBRARIES:
            ref = 'system:' + library
            refs.append(ref)
            bom['components'].append({'type': 'library', 'name': library, 'bom-ref': ref,
                'properties': [{'name': 'distribution:provided-by', 'value': 'operating-system'},
                               {'name': 'distribution:redistributed', 'value': 'false'}]})
            bom['dependencies'].append({'ref': ref, 'dependsOn': []})
        bom['dependencies'].append({'ref': 'application:sobalink', 'dependsOn': refs})
    bom['metadata'] = {'properties': [{'name': k, 'value': v} for k, v in props.items()]}


class NativeProvenanceTests(unittest.TestCase):
    def fixture(self, target='darwin-arm64'):
        build, notices, bom = {'version': '1.2.3'}, {}, {}
        add_native_fixture(build, notices, bom, target)
        return build, notices, bom

    def test_exact_target_policies(self):
        from source_provenance import verify_native
        for target in ('linux-amd64', 'linux-arm64', 'darwin-arm64', 'windows-amd64'):
            with self.subTest(target=target):
                values = self.fixture(target)
                verify_native(*values)
                values[0]['cgo_enabled'] = not values[0]['cgo_enabled']
                with self.assertRaises(AssertionError):
                    verify_native(*values)

    def test_routecat_policy_pins_match_go_and_actual_sources(self):
        from source_provenance import DARWIN_ROUTECAT_INPUTS, DARWIN_ROUTECAT_PATH
        source = (REPOSITORY / 'internal/distribution/native_routecat.go').read_text()
        literal = source.split('var darwinRoutecatPolicyInputs = map[string]string{', 1)[1].split('\n}', 1)[0]
        go_pins = dict(re.findall(r'"([a-z_]+\.go)":\s*"([0-9a-f]{64})"', literal))
        self.assertEqual(go_pins, DARWIN_ROUTECAT_INPUTS)
        self.assertEqual(len(go_pins), 10)
        for name, sha in go_pins.items():
            with self.subTest(source=name):
                self.assertEqual(digest((REPOSITORY / DARWIN_ROUTECAT_PATH / name).read_bytes()), sha)

    def test_routecat_native_policy_inventory_fails_closed(self):
        from source_provenance import DARWIN_ROUTECAT_PACKAGE, native_digest, verify_native
        for kind in ('old-five-only', 'missing', 'extra', 'duplicate', 'changed'):
            with self.subTest(kind=kind):
                build, notices, bom = self.fixture()
                native = build['native_build']
                inputs = native['policy_inputs']
                selected = next(i for i in inputs if i['path'].startswith(DARWIN_ROUTECAT_PACKAGE + '/'))
                if kind == 'old-five-only':
                    native['policy_inputs'] = [i for i in inputs if not i['path'].startswith(DARWIN_ROUTECAT_PACKAGE + '/')]
                elif kind == 'missing': inputs.remove(selected)
                elif kind == 'extra': inputs.append({'path': DARWIN_ROUTECAT_PACKAGE + '/extra.go', 'sha256': 'f' * 64})
                elif kind == 'duplicate': inputs.append(copy.deepcopy(selected))
                elif kind == 'changed': selected['sha256'] = 'f' * 64
                native['policy_inputs'].sort(key=lambda i: i['path'])
                notices['native_build'] = copy.deepcopy(native)
                for prop in bom['metadata']['properties']:
                    if prop['name'] == 'build:native:sha256': prop['value'] = native_digest(native)
                with self.assertRaises(AssertionError):
                    verify_native(build, notices, bom)

    def test_routecat_archived_source_inventory_binding_fails_closed(self):
        from source_provenance import DARWIN_ROUTECAT_PATH, verify_native
        for kind in ('missing-component', 'duplicate-component', 'wrong-source-path', 'missing', 'extra', 'duplicate', 'changed', 'missing-metadata'):
            with self.subTest(kind=kind):
                build, notices, bom = self.fixture()
                sources = notices['source_components']
                source = sources[0]
                inputs = source['build_inputs']
                selected = next(i for i in inputs if i['path'] == DARWIN_ROUTECAT_PATH + '/tailcat.go')
                if kind == 'missing-component': sources.clear()
                elif kind == 'duplicate-component': sources.append(copy.deepcopy(source))
                elif kind == 'wrong-source-path': source['source_path'] = 'internal/other'
                elif kind == 'missing': inputs.remove(selected)
                elif kind == 'extra': inputs.append({'path': DARWIN_ROUTECAT_PATH + '/extra.go', 'sha256': 'f' * 64})
                elif kind == 'duplicate': inputs.append(copy.deepcopy(selected))
                elif kind == 'changed': selected['sha256'] = 'f' * 64
                elif kind == 'missing-metadata': inputs[:] = [i for i in inputs if i['path'] != 'go.mod']
                inputs.sort(key=lambda i: i['path'])
                build['source_components'] = copy.deepcopy(sources)
                with self.assertRaises(AssertionError):
                    verify_native(build, notices, bom)

    def test_darwin_native_mutations_fail_closed(self):
        from source_provenance import native_digest, verify_native
        mutations = [
            lambda n: n.update(schema=True), lambda n: n.update(host_os='linux'),
            lambda n: n.update(host_arch='amd64'), lambda n: n.update(deployment_target='26.0'),
            lambda n: n.update(compiler_version='Apple clang version private-user'),
            lambda n: n.update(compiler_sha256=''), lambda n: n.update(host_build='private/path'),
            lambda n: n.update(xcode_version='26.0\nextra'), lambda n: n.update(sdk_build=''),
            lambda n: n['sdk_inputs'].pop(), lambda n: n['sdk_inputs'].append(n['sdk_inputs'][0]),
            lambda n: n['policy_inputs'][0].update(sha256='f' * 64),
            lambda n: n['cflags'].append('-I/unreviewed'), lambda n: n['packages'].pop(),
            lambda n: n['packages'][0]['ldflags'].append('-levil'),
            lambda n: n['packages'][0]['inputs'][0].update(sha256='f' * 64),
            lambda n: n['packages'][1]['inputs'].pop(),
            lambda n: n['packages'][1]['ldflags'].append('-ldns_sd'),
            lambda n: n['sdk_inputs'].append({'path': 'usr/lib/libdns_sd.tbd', 'sha256': 'c' * 64}),
            lambda n: n['system_libraries'].append('/usr/lib/libdns_sd.dylib'),
            lambda n: n['system_libraries'].append('/tmp/unreviewed.dylib'),
        ]
        for index, mutation in enumerate(mutations):
            with self.subTest(case=index):
                build, notices, bom = self.fixture()
                mutation(build['native_build'])
                notices['native_build'] = copy.deepcopy(build['native_build'])
                for prop in bom['metadata']['properties']:
                    if prop['name'] == 'build:native:sha256':
                        prop['value'] = native_digest(build['native_build'])
                with self.assertRaises((AssertionError, KeyError, ValueError)):
                    verify_native(build, notices, bom)

    def test_missing_inconsistent_and_duplicate_provenance(self):
        from source_provenance import verify_native
        for kind in ('missing', 'notice', 'digest', 'edge', 'duplicate-property', 'component', 'cgo', 'flags', 'cert-notices', 'cert-component'):
            with self.subTest(kind=kind):
                build, notices, bom = self.fixture()
                if kind == 'missing': del build['native_build']
                elif kind == 'notice': notices['native_build']['sdk_version'] = '27.0'
                elif kind == 'digest': bom['metadata']['properties'][-1]['value'] = 'f' * 64
                elif kind == 'edge': bom['dependencies'][-1]['dependsOn'].pop()
                elif kind == 'duplicate-property': bom['metadata']['properties'].append(bom['metadata']['properties'][0])
                elif kind == 'component': bom['components'][1]['properties'][1]['value'] = 'true'
                elif kind == 'cgo': build['cgo_enabled'] = 1
                elif kind == 'flags': build['ldflags'] += ' -buildid=unexpected'
                elif kind == 'cert-notices': notices['modules'] = []
                elif kind == 'cert-component': bom['components'].pop(0)
                with self.assertRaises((AssertionError, KeyError)):
                    verify_native(build, notices, bom)

    def test_cgo0_cannot_carry_native_metadata(self):
        from source_provenance import verify_native
        build, notices, bom = self.fixture('linux-amd64')
        build['native_build'] = self.fixture()[0]['native_build']
        with self.assertRaises(AssertionError): verify_native(build, notices, bom)

    def test_macho_actual_linkage_and_deployment(self):
        import struct
        from source_provenance import DARWIN_LIBRARIES, read_macho_libraries, verify_native
        def write(path, libraries, minimum=13, rpath=False):
            commands = [struct.pack('<6I', 0x32, 24, 1, minimum << 16, 26 << 16, 0)]
            for name in libraries:
                string = name.encode() + b'\0'
                size = (24 + len(string) + 7) // 8 * 8
                commands.append(struct.pack('<6I', 0xc, size, 24, 0, 0, 0) + string + b'\0' * (size - 24 - len(string)))
            if rpath: commands.append(struct.pack('<2I', 0x8000001c, 8))
            body = b''.join(commands)
            path.write_bytes(struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, len(commands), len(body), 0, 0) + body)
        with tempfile.TemporaryDirectory() as temp:
            path = pathlib.Path(temp) / 'synthetic-macho'
            write(path, DARWIN_LIBRARIES)
            self.assertEqual(read_macho_libraries(path), list(DARWIN_LIBRARIES))
            verify_native(*self.fixture(), path)
            for libraries, minimum, rpath in ((DARWIN_LIBRARIES, 26, False), (DARWIN_LIBRARIES, 13, True), (DARWIN_LIBRARIES + ('/tmp/evil.dylib',), 13, False), (DARWIN_LIBRARIES + ('/usr/lib/libdns_sd.dylib',), 13, False)):
                write(path, libraries, minimum, rpath)
                with self.assertRaises(AssertionError): verify_native(*self.fixture(), path)
