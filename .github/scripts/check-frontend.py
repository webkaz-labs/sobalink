#!/usr/bin/env python3
"""Test the frontend and require two byte-identical exact-lock builds.

Browser/release/local runs always test. Native CI may avoid repeating portable
Vitest cases only for a reproduced ordinary-product plan: the same-source
browser job remains required. CI/toolchain/dependency edits, nightly/manual
full runs and unknown input retain native tests as well. Both locked builds
and the checked-in asset comparison run in every mode on every target.
"""
import argparse
import hashlib
import importlib.util
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


def native_tests(plan_path):
    # Share the conservative ordinary-product allowlist, but never the Python
    # suite's Linux host exception. Full intent keeps Vitest on all four targets.
    spec = importlib.util.spec_from_file_location(
        "frontend_regressions", root / ".github/scripts/ci-regressions.py")
    policy = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(policy)
    return policy.full_native_regressions(policy.verified_plan(plan_path))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--native-plan", type=pathlib.Path,
                        help="reproduce native CI's impact plan before selecting repeated tests")
    args = parser.parse_args(argv)
    tests = args.native_plan is None or native_tests(args.native_plan)
    print("Frontend tests: " + ("full suite" if tests else
          "required in browser job; native repetition not selected"), flush=True)
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
