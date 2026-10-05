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
        return {"source_components": [copy.deepcopy(source)]}, {"source_components": [source], "modules": []}, {"components": [component]}

    def test_intact_sources(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            verify_sources(root, *self.fixture(root))

    def test_rejects_inconsistent_or_missing_records(self):
        for case in ("metadata", "notice", "missing engine", "upstream runtime", "sbom digest", "input digest", "upstream purl", "notice traversal"):
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
                with self.assertRaises((AssertionError, KeyError)):
                    verify_sources(root, build, notices, bom)


if __name__ == "__main__":
    unittest.main()
