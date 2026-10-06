"""Fail-closed checks for independently retained engine source records."""
import copy
import json
import pathlib
import tempfile
import unittest
from source_provenance import digest, verify_sources


class SourceProvenanceTests(unittest.TestCase):
    def fixture(self, root):
        manifest = {"schema": 1, "module": "tailscale.com", "version": "v1.104.0",
                    "module_sum": "h1:7LgglawekeutAexqXUzSxP/Kqo3HHwF/SkcbX5HpiU4=",
                    "go_mod_sum": "h1:Cb1XjScRgSOeRD7NW6uCEznme782WiOU9IxqyTWoRf0=",
                    "upstream_tree_sha256": "a" * 64, "adapted_tree_sha256": "b" * 64,
                    "changes": [{"path": "source.go", "original_sha256": "c" * 64, "adapted_sha256": "d" * 64}]}
        raw = (json.dumps(manifest) + "\n").encode()
        path = "licenses/source/internal/engineadaptation/manifest.json"
        files = {"go.mod": b"module fixture\n", "go.sum": b"fixture checksums\n", path: raw}
        for name, data in files.items():
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        inputs = [{"path": name, "sha256": digest(files[name])} for name in ("go.mod", "go.sum")]
        inputs.append({"path": "internal/engineadaptation/manifest.json", "sha256": digest(raw)})
        source = {"package": "tailscale.com", "source_path": "internal/engineadaptation", "build_inputs": inputs,
                  "build_inputs_sha256": digest(json.dumps(inputs, separators=(",", ":")).encode()),
                  "notices": [{"path": path, "sha256": digest(raw)}], "manifest_sha256": digest(raw),
                  "adapted_tree_sha256": manifest["adapted_tree_sha256"],
                  "upstream": {"module": "tailscale.com", "version": "v1.104.0", "module_sum": manifest["module_sum"],
                               "go_mod_sum": manifest["go_mod_sum"], "tree_sha256": manifest["upstream_tree_sha256"], "files": {"source.go": "c" * 64}}}
        props = {"source:modified": "true", "source:build-inputs:sha256": source["build_inputs_sha256"], "source:build-inputs": json.dumps(inputs),
                 "source:manifest:sha256": digest(raw), "source:adapted-tree:sha256": "b" * 64, "source:upstream-tree:sha256": "a" * 64,
                 "source:upstream-module:sum": manifest["module_sum"], "source:upstream-go-mod:sum": manifest["go_mod_sum"]}
        component = {"bom-ref": "source:internal/engineadaptation", "properties": [{"name": k, "value": v} for k, v in props.items()],
                     "pedigree": {"ancestors": [{"name": "tailscale.com", "version": "v1.104.0"}]}}
        build, notices, bom = {"source_components": [copy.deepcopy(source)]}, {"source_components": [source], "modules": []}, {"components": [component]}
        self.add_direct_source(root, build, notices, bom)
        return build, notices, bom

    def add_direct_source(self, root, build, notices, bom):
        originals = pathlib.Path(__file__).resolve().parents[2] / "internal/directlan"
        inputs = {"go.mod": digest((root / "go.mod").read_bytes()), "go.sum": digest((root / "go.sum").read_bytes()),
                  "internal/directlan/stack.go": "e" * 64}
        retained = []
        for name in ("WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md", "stack.go"):
            raw = (originals / name).read_bytes()
            path = "licenses/source/internal/directlan/" + name
            destination = root / path
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(raw)
            inputs["internal/directlan/" + name] = digest(raw)
            retained.append({"path": path, "sha256": digest(raw)})
        upstream = json.loads((originals / "UPSTREAM.json").read_text())
        inputs = [{"path": p, "sha256": h} for p, h in sorted(inputs.items())]
        source = {"package": "github.com/webkaz-labs/sobalink/internal/directlan", "source_path": "internal/directlan",
                  "build_inputs": inputs, "build_inputs_sha256": digest(json.dumps(inputs, separators=(",", ":")).encode()),
                  "notices": retained, "upstream": upstream}
        build["source_components"].append(copy.deepcopy(source))
        notices["source_components"].append(source)
        props = {"source:modified": "true", "source:build-inputs:sha256": source["build_inputs_sha256"], "source:build-inputs": json.dumps(inputs)}
        bom["components"].append({"bom-ref": "source:internal/directlan", "properties": [{"name": k, "value": v} for k, v in props.items()],
                                  "pedigree": {"ancestors": [{"name": upstream["module"], "version": upstream["version"]}]}})

    def test_intact_sources(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            verify_sources(root, *self.fixture(root))

    def test_rejects_inconsistent_or_missing_records(self):
        for case in ("metadata", "notice", "missing engine", "upstream runtime", "sbom digest", "input digest", "upstream purl", "notice traversal", "missing direct", "direct license", "direct original hash"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                build, notices, bom = self.fixture(root)
                if case == "metadata":
                    build["source_components"][0]["manifest_sha256"] = "0" * 64
                elif case == "notice":
                    (root / notices["source_components"][0]["notices"][0]["path"]).write_text("changed")
                elif case == "missing engine":
                    notices["source_components"] = build["source_components"] = []
                elif case == "upstream runtime":
                    notices["modules"].append({"module": "tailscale.com"})
                elif case == "sbom digest":
                    bom["components"][0]["properties"][1]["value"] = "0" * 64
                elif case == "input digest":
                    (root / "go.mod").write_text("changed")
                elif case == "upstream purl":
                    bom["components"][0]["purl"] = "pkg:golang/tailscale.com@v1.104.0"
                elif case == "notice traversal":
                    for target in (build, notices):
                        target["source_components"][0]["notices"][0]["path"] = "../outside"
                elif case == "missing direct":
                    for target in (build, notices):
                        target["source_components"].pop()
                elif case == "direct license":
                    (root / "licenses/source/internal/directlan/WIREGUARD_LICENSE").write_text("changed")
                elif case == "direct original hash":
                    for target in (build, notices):
                        target["source_components"][1]["upstream"]["files"]["tun/netstack/tun.go"] = "0" * 64
                with self.assertRaises((AssertionError, KeyError)):
                    verify_sources(root, build, notices, bom)


if __name__ == "__main__":
    unittest.main()
