#!/usr/bin/env python3
"""Package existing runner-built binaries without rebuilding or executing them.

Run the *same* binary with --demo --locale ja --render demo-ja.png on its
native hosted runner before packaging. This packager checks archive integrity,
not application behavior, authenticated signing, notarization, or device acceptance.
Run the same daemon binary with --help only before packaging.
Only both binaries, the reviewed guide, and allowlisted build metadata enter the ZIP.
"""

import argparse
import hashlib
import json
import re
import stat
import subprocess
import zipfile
import zlib
from pathlib import Path

TARGETS = {"windows-amd64": "soba-native.exe", "darwin-arm64": "soba-native"}
FIXED_TIME = (1980, 1, 1, 0, 0, 0)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def inspect_build(binary, target, source_commit, go="go", require_mygo=True):
    # Do not include the raw output: its first line contains a local path.
    result = subprocess.run([go, "version", "-m", str(binary)], check=True,
                            capture_output=True, text=True)
    lines = result.stdout.splitlines()
    match = re.search(r":\s+(go\d+\.\d+(?:\.\d+)?(?:[a-z]+\d*)?)$", lines[0] if lines else "")
    if not match:
        raise ValueError("Binary has no supported Go version metadata")
    go_version = match.group(1)
    settings = {}
    mygo_version = None
    for line in lines[1:]:
        fields = line.split()
        if len(fields) >= 3 and fields[0:2] == ["dep", "github.com/egoist/mygo"]:
            mygo_version = fields[2]
        if len(fields) == 2 and fields[0] == "build" and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            settings[key] = value
    goos, goarch = target.split("-")
    if settings.get("GOOS") != goos or settings.get("GOARCH") != goarch:
        raise ValueError("Binary GOOS/GOARCH do not match the requested target")
    if require_mygo and (not mygo_version or not re.fullmatch(r"v[0-9][0-9A-Za-z.+-]*", mygo_version)):
        raise ValueError("Binary does not identify a versioned MyGo dependency")
    revision = settings.get("vcs.revision")
    if revision and revision != source_commit:
        raise ValueError("Binary VCS revision does not match --source-commit")
    if settings.get("vcs.modified") == "true":
        raise ValueError("Binary reports modified source; use a clean source checkout")
    return {
        "go_version": go_version,
        "mygo_version": mygo_version,
        "source_commit": source_commit,
        "source_commit_verification": "matched_binary_vcs_revision" if revision else "caller_declared_no_binary_vcs_revision",
    }


def verify_archive(archive, expected):
    """Read every entry back, checking names, bytes, SHA-256 and executable mode."""
    with zipfile.ZipFile(archive) as bundle:
        if bundle.namelist() != sorted(expected):
            raise ValueError("Archive member list mismatch")
        for name, (data, mode) in expected.items():
            info = bundle.getinfo(name)
            actual = bundle.read(name)
            if actual != data or sha256(actual) != sha256(data):
                raise ValueError("Archive content mismatch: " + name)
            if stat.S_IMODE(info.external_attr >> 16) != mode:
                raise ValueError("Archive permission mismatch: " + name)
        sums = bundle.read("SHA256SUMS").decode("ascii")
        for line in sums.splitlines():
            digest, name = line.split("  ", 1)
            if sha256(bundle.read(name)) != digest:
                raise ValueError("Archive checksum mismatch: " + name)


def package(binary, daemon_binary, guide, target, output_dir, source_commit, go="go"):
    if target not in TARGETS:
        raise ValueError("Unsupported target")
    if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", source_commit):
        raise ValueError("Source commit must be a full lowercase Git object ID")
    binary = Path(binary)
    binary_bytes = binary.read_bytes()
    if not binary_bytes:
        raise ValueError("Binary is empty")
    metadata = inspect_build(binary, target, source_commit, go)
    # Detect changes during metadata inspection, preserving the exact input bits.
    if binary.read_bytes() != binary_bytes:
        raise ValueError("Binary changed during packaging")
    daemon_binary = Path(daemon_binary)
    daemon_bytes = daemon_binary.read_bytes()
    if not daemon_bytes:
        raise ValueError("Daemon binary is empty")
    daemon_metadata = inspect_build(daemon_binary, target, source_commit, go, require_mygo=False)
    if daemon_binary.read_bytes() != daemon_bytes:
        raise ValueError("Daemon binary changed during packaging")
    daemon_name = "soba.exe" if target.startswith("windows-") else "soba"
    daemon_metadata.update({"binary": daemon_name, "binary_sha256": sha256(daemon_bytes)})
    guide_bytes = Path(guide).read_bytes()
    if not guide_bytes.decode("utf-8").strip():
        raise ValueError("Device guide is empty")
    name = TARGETS[target]
    metadata.update({
        "schema_version": 1,
        "archive_compression": {
            "method": "deflate",
            "level": 9,
            "zlib_runtime_version": zlib.ZLIB_RUNTIME_VERSION,
            "reproducibility": "same_inputs_and_same_python_zlib_runtime_only",
        },
        "artifact_kind": "native-ui-device-test-build",
        "target": target,
        "binary": name,
        "binary_sha256": sha256(binary_bytes),
        "signing_status": {
            "authenticated_publisher_signature": "not_provided_by_this_test_build_pipeline",
            "apple_notarization": "not_performed",
            "ad_hoc_signature": "may_be_present_on_macos_not_authenticated_signing",
        },
        "daemon": daemon_metadata,
        "acceptance": "packaging_integrity_only_not_real_device_acceptance",
    })
    files = {
        name: (binary_bytes, 0o755),
        daemon_name: (daemon_bytes, 0o755),
        "DEVICE_TEST.ja.md": (guide_bytes, 0o644),
        "manifest.json": ((json.dumps(metadata, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8"), 0o644),
    }
    # SHA256SUMS covers every payload file, not itself (self-hashing is impossible).
    sums = "".join(f"{sha256(data)}  {filename}\n" for filename, (data, _) in sorted(files.items()))
    files["SHA256SUMS"] = (sums.encode("ascii"), 0o644)
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    archive = output_dir / f"soba-native-{target}-{source_commit[:12]}-test.zip"
    # Repeated builds are deterministic with the same inputs and Python/zlib runtime.
    # Different compressor versions may produce different (equally valid) ZIP bytes.
    with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as bundle:
        for filename, (data, mode) in sorted(files.items()):
            info = zipfile.ZipInfo(filename, FIXED_TIME)
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | mode) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            bundle.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    verify_archive(archive, files)
    archive_sum = archive.with_suffix(archive.suffix + ".sha256")
    archive_sum.write_text(f"{sha256(archive.read_bytes())}  {archive.name}\n", encoding="ascii")
    return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--daemon-binary", required=True, type=Path)
    parser.add_argument("--target", required=True, choices=sorted(TARGETS))
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--guide", type=Path, default=Path(__file__).with_name("DEVICE_TEST.ja.md"))
    parser.add_argument("--go", default="go", help="Go tool used only to inspect binary metadata")
    args = parser.parse_args()
    try:
        archive = package(args.binary, args.daemon_binary, args.guide, args.target, args.output_dir, args.source_commit, args.go)
    except (OSError, ValueError, subprocess.CalledProcessError, zipfile.BadZipFile) as error:
        parser.exit(1, f"Packaging failed: {error}\n")
    print(f"Verified test archive: {archive.name}")


if __name__ == "__main__":
    main()
