#!/usr/bin/env python3
"""Install the exact lock twice and require identical frontend build bytes."""
import hashlib
import os
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[2]
web = root / "web"
npm = "npm.cmd" if os.name == "nt" else "npm"

def run(*args):
    subprocess.run([npm, *args], cwd=web, check=True)

def inventory():
    dist = web / "dist"
    files = sorted(dist.rglob("*"))
    assert (dist / "index.html").is_file(), "missing frontend entry point"
    assert all(not p.is_symlink() for p in files), "frontend contains a symlink"
    return {p.relative_to(dist).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in files if p.is_file()}

assert subprocess.check_output(["node", "--version"], text=True).strip() == "v24.19.0"
assert subprocess.check_output([npm, "--version"], text=True).strip() == "11.9.0"
lock = (web / "package-lock.json").read_bytes()
checked_in = inventory()
run("ci", "--no-audit", "--no-fund")
run("run", "test")
run("run", "build")
first = inventory()
assert first == checked_in, "checked-in frontend dist differs from source; rebuild and commit web/dist"
run("ci", "--no-audit", "--no-fund")
run("run", "build")
assert first == inventory(), "locked frontend rebuild is not byte-identical"
assert lock == (web / "package-lock.json").read_bytes(), "build changed dependency lock"
print(f"Frontend tests and two exact-lock builds passed: {len(first)} byte-identical files")
