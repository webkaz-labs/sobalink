#!/usr/bin/env python3
"""Inspect and execute a native CI package without connecting to a network."""
import hashlib
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from document_images import verify_document_images
from source_provenance import read_json, verify_native_companion, verify_native, verify_sources

archive = pathlib.Path(sys.argv[1])
version = sys.argv[2]
if sys.argv[3:] not in ([], ["--native-ui-scope"]):
    raise ValueError("unknown smoke scope")
native_ui_scope = sys.argv[3:] == ["--native-ui-scope"]
with tempfile.TemporaryDirectory(prefix="sobalink-smoke-") as tmp:
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
    share = root / "share" / "sobalink"
    verify_document_images(share)
    build = read_json((share / "build.json").read_bytes())
    binary = destination(build["binary"]["path"])
    assert hashlib.sha256(binary.read_bytes()).hexdigest() == build["binary"]["sha256"]
    assert build["product"] == "sobalink"
    assert build["version"] == version
    assert (share / "LICENSE").is_file()
    assert (share / "README.md").is_file()
    assert (share / "SECURITY.md").is_file()
    bom = read_json((share / "bom.cdx.json").read_bytes())
    assert bom["bomFormat"] == "CycloneDX" and bom["specVersion"] == "1.5"
    notices = read_json((share / "third-party-notices.json").read_bytes())
    assert notices["modules"] and notices["frontend_modules"], "no target dependencies recorded"
    for module in notices["modules"] + notices["frontend_modules"] + [notices["go_standard_library"]]:
        assert module["notices"], "missing notices"
        for entry in module["notices"]:
            notice = share / entry["path"]
            assert hashlib.sha256(notice.read_bytes()).hexdigest() == entry["sha256"]
    verify_native(build, notices, bom, binary)
    verify_sources(share, build, notices, bom)
    native_ui = verify_native_companion(build, notices, bom, root)
    if native_ui is not None:
        companion = root / native_ui["binary"]["path"]
        companion.chmod(0o755)
        result = subprocess.run([str(companion), "--version"], check=True, capture_output=True, text=True, timeout=30)
        identity = read_json(result.stdout)
        assert identity == {"product": "soba-native", "version": build["version"], "commit": build["source_commit"], "go_version": "go1.27.3", "goos": build["target"].split("-")[0], "goarch": build["target"].split("-")[1], "experimental": True}, "installed companion identity differs"
        subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("offline-smoke.py")), "--native-companion", str(companion)], check=True)
    binary.chmod(0o755)
    result = subprocess.run([str(binary), "--version"], check=True, capture_output=True, text=True)
    assert version in result.stdout, result.stdout
    subprocess.run([str(binary), "--help"], check=True)
    subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("offline-smoke.py")), str(binary)], check=True)
    subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("pty-smoke.py")), str(binary)], check=True)
    if native_ui_scope:
        if native_ui is None:
            raise ValueError("native UI scope requires a packaged companion")
        from native_ui_smoke import verify_scope
        verify_scope(companion, binary, build["target"])
    print(f"Packaged executable, SBOM and {len(notices['modules'])} module notice sets verified")
