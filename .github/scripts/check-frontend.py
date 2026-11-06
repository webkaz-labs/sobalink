#!/usr/bin/env python3
"""Test the frontend and require two byte-identical exact-lock builds.

Browser/local runs test by default. Native CI and prerelease use --build-only:
the required same-source browser job owns the portable Vitest suite, including
CI/dependency changes and nightly/forced full runs. Prerelease requires that
exact commit's full-CI proof. Both locked builds, the checked-in asset comparison
and the unchanged-lock check run in every mode on every target.
"""
import argparse
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


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--build-only", action="store_true",
                        help="reproduce assets without repeating the required browser job's tests")
    args = parser.parse_args(argv)
    tests = not args.build_only
    print("Frontend tests: " + ("full suite" if tests else
          "required in browser job; build-only mode"), flush=True)
    assert subprocess.check_output(["node", "--version"], text=True).strip() == "v24.19.0"
    assert subprocess.check_output([npm, "--version"], text=True).strip() == "11.9.0"
    lock = (web / "package-lock.json").read_bytes()
    checked_in = inventory()
    run("ci", "--no-audit", "--no-fund")
    if tests:
        run("run", "test")
    run("run", "build")
    first = inventory()
    assert first == checked_in, "checked-in frontend dist differs from source; rebuild and commit web/dist"
    run("ci", "--no-audit", "--no-fund")
    run("run", "build")
    assert first == inventory(), "locked frontend rebuild is not byte-identical"
    assert lock == (web / "package-lock.json").read_bytes(), "build changed dependency lock"
    print(f"Frontend {'tests and ' if tests else ''}two exact-lock builds passed: {len(first)} byte-identical files")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
