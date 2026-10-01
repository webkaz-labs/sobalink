#!/usr/bin/env python3
"""Inspect and execute a native CI package without connecting to a network."""
import hashlib
import json
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import zipfile

archive = pathlib.Path(sys.argv[1])
version = sys.argv[2]
with tempfile.TemporaryDirectory(prefix="tsnet-bridge-smoke-") as tmp:
    root = pathlib.Path(tmp)

    def destination(name):
        relative = pathlib.PurePosixPath(name)
        if relative.is_absolute() or ".." in relative.parts or "\\" in name or ":" in name:
            raise ValueError("unsafe package path")
        return root.joinpath(*relative.parts)

    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as package:
            for entry in package.infolist():
                target = destination(entry.filename)
                if entry.is_dir():
                    target.mkdir(parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(package.read(entry))
    else:
        with tarfile.open(archive, "r:gz") as package:
            for entry in package:
                target = destination(entry.name)
                if entry.isdir():
                    target.mkdir(parents=True, exist_ok=True)
                elif entry.isfile():
                    target.parent.mkdir(parents=True, exist_ok=True)
                    with package.extractfile(entry) as source:
                        target.write_bytes(source.read())
                else:
                    raise ValueError("package contains a special file")
    share = root / "share" / "tsnet-bridge"
    build = json.loads((share / "build.json").read_text(encoding="utf-8"))
    binary = destination(build["binary"]["path"])
    assert hashlib.sha256(binary.read_bytes()).hexdigest() == build["binary"]["sha256"]
    assert build["version"] == version
    assert (share / "LICENSE").is_file()
    assert (share / "README.md").is_file()
    assert (share / "SECURITY.md").is_file()
    bom = json.loads((share / "bom.cdx.json").read_text(encoding="utf-8"))
    assert bom["bomFormat"] == "CycloneDX" and bom["specVersion"] == "1.5"
    notices = json.loads((share / "third-party-notices.json").read_text(encoding="utf-8"))
    assert notices["modules"], "no target dependencies recorded"
    for module in notices["modules"] + [notices["go_standard_library"]]:
        assert module["notices"], "missing notices"
        for entry in module["notices"]:
            notice = share / entry["path"]
            assert hashlib.sha256(notice.read_bytes()).hexdigest() == entry["sha256"]
    binary.chmod(0o755)
    result = subprocess.run([str(binary), "--version"], check=True, capture_output=True, text=True)
    assert version in result.stdout, result.stdout
    subprocess.run([str(binary), "--help"], check=True)
    subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("offline-smoke.py")), str(binary)], check=True)
    print(f"Packaged executable, SBOM and {len(notices['modules'])} module notice sets verified")
