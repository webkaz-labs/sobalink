"""Packaging unit checks; fixture bytes are not OS-executable acceptance evidence."""
import json
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch
import package_test_build as packaging

COMMIT = "a" * 40


def metadata(target="darwin-arm64", revision=COMMIT, dirty=False):
    goos, goarch = target.split("-")
    return ("fixture: go1.27.3\n"
            "\tdep\tgithub.com/egoist/mygo\tv0.7.4\th1:fixture\n"
            f"\tbuild\tGOOS={goos}\n\tbuild\tGOARCH={goarch}\n"
            f"\tbuild\tvcs.revision={revision}\n"
            f"\tbuild\tvcs.modified={str(dirty).lower()}\n")


class PackagingTests(unittest.TestCase):
    def test_reproducible_roundtrip_both_platforms(self):
        for target in packaging.TARGETS:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as root:
                root = Path(root)
                binary, daemon, guide = root / "native", root / "daemon", root / "guide"
                binary.write_bytes(b"fictional native binary fixture")
                daemon.write_bytes(b"fictional daemon binary fixture")
                guide.write_text("# 架空の試験手順\n", encoding="utf-8")
                with patch.object(packaging.subprocess, "run") as run:
                    run.return_value.stdout = metadata(target)
                    first = packaging.package(binary, daemon, guide, target, root / "one", COMMIT)
                    second = packaging.package(binary, daemon, guide, target, root / "two", COMMIT)
                self.assertEqual(first.read_bytes(), second.read_bytes())
                with zipfile.ZipFile(first) as bundle:
                    manifest = json.loads(bundle.read("manifest.json"))
                    self.assertEqual(manifest["source_commit"], COMMIT)
                    self.assertEqual(manifest["mygo_version"], "v0.7.4")
                    self.assertNotIn(str(root), bundle.read("manifest.json").decode())
                    self.assertEqual(bundle.read(packaging.TARGETS[target]), binary.read_bytes())
                    daemon_name = "soba.exe" if target.startswith("windows") else "soba"
                    self.assertEqual(bundle.read(daemon_name), daemon.read_bytes())
                    self.assertEqual(len(bundle.namelist()), 5)
                    self.assertEqual(manifest["archive_compression"]["method"], "deflate")
                    self.assertEqual(manifest["archive_compression"]["level"], 9)
                    for info in bundle.infolist():
                        self.assertEqual(info.compress_type, zipfile.ZIP_DEFLATED)
                self.assertTrue(first.with_suffix(".zip.sha256").is_file())

    def test_rejects_wrong_platform_source_and_dirty_tree(self):
        cases = [metadata("windows-amd64"), metadata(revision="b" * 40), metadata(dirty=True)]
        for output in cases:
            with self.subTest(output=output), patch.object(packaging.subprocess, "run") as run:
                run.return_value.stdout = output
                with self.assertRaises(ValueError):
                    packaging.inspect_build(Path("fixture"), "darwin-arm64", COMMIT)

    def test_no_binary_vcs_is_honestly_declared(self):
        with patch.object(packaging.subprocess, "run") as run:
            run.return_value.stdout = metadata().replace(f"\tbuild\tvcs.revision={COMMIT}\n", "")
            result = packaging.inspect_build(Path("fixture"), "darwin-arm64", COMMIT)
            self.assertEqual(result["source_commit_verification"], "caller_declared_no_binary_vcs_revision")

    def test_daemon_does_not_require_mygo(self):
        with patch.object(packaging.subprocess, "run") as run:
            run.return_value.stdout = metadata().replace("\tdep\tgithub.com/egoist/mygo\tv0.7.4\th1:fixture\n", "")
            packaging.inspect_build(Path("fixture"), "darwin-arm64", COMMIT, require_mygo=False)
            with self.assertRaises(ValueError):
                packaging.inspect_build(Path("fixture"), "darwin-arm64", COMMIT)


if __name__ == "__main__":
    unittest.main()
