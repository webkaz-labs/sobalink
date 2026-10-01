#!/usr/bin/env python3
"""Verify the actual mise installation, including retained license notices."""
import hashlib
import json
import pathlib
import platform
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
version, commit, target = sys.argv[2:]
share = root / "share" / "tsnet-bridge"
meta = json.loads((share / "build.json").read_text(encoding="utf-8"))
assert meta["version"] == version and meta["source_commit"] == commit and meta["target"] == target
actual_os = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}[platform.system()]
actual_arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine()]
assert target == actual_os + "-" + actual_arch, "installation must run on the native target"
for name in ("LICENSE", "README.md", "README.en.md", "SECURITY.md", "go.mod", "go.sum"):
    assert (share / name).is_file(), "missing installed document: " + name
expected_binary = "bin/tsnet-bridge" + (".exe" if actual_os == "windows" else "")
assert meta["binary"]["path"] == expected_binary, "unexpected installed executable path"
binary = root / expected_binary
assert binary.is_file()
assert hashlib.sha256(binary.read_bytes()).hexdigest() == meta["binary"]["sha256"]
assert hashlib.sha256((share / "go.sum").read_bytes()).hexdigest() == meta["go_sum_sha256"]
bom = json.loads((share / "bom.cdx.json").read_text(encoding="utf-8"))
assert bom["bomFormat"] == "CycloneDX" and bom["specVersion"] == "1.5"
notices = json.loads((share / "third-party-notices.json").read_text(encoding="utf-8"))
assert notices["modules"]
for module in notices["modules"] + [notices["go_standard_library"]]:
    assert module["notices"], "missing installed module notices"
    for entry in module["notices"]:
        relative = pathlib.PurePosixPath(entry["path"])
        assert not relative.is_absolute() and ".." not in relative.parts and "\\" not in entry["path"]
        notice = share.joinpath(*relative.parts)
        assert notice.is_file() and not notice.is_symlink()
        assert hashlib.sha256(notice.read_bytes()).hexdigest() == entry["sha256"]
result = subprocess.run([str(binary), "--version"], check=True, capture_output=True, text=True)
assert result.stdout.strip() == "tsnet-bridge " + version
subprocess.run([str(binary), "--help"], check=True)
subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("offline-smoke.py")), str(binary)], check=True)
print("Actual mise installation, native execution, source, SBOM, and all retained notices verified:", target)
