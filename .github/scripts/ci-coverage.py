#!/usr/bin/env python3
"""Read-only CI selection/provenance and the non-skippable coverage aggregate.

An optimization failure selects all tests. A validation failure never creates a
successful full-coverage receipt. No prior test executable or result is executed.
"""
import argparse
import datetime
import hashlib
import io
import json
import os
import pathlib
import re
import subprocess
import sys
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
REPOSITORY = "webkaz-labs/sobalink"
WORKFLOW = ".github/workflows/ci.yml"
POLICY = ".github/scripts/ci-impact.py"
TARGETS = {
    "linux-amd64": "native (ubuntu-24.04, linux, amd64)",
    "linux-arm64": "native (ubuntu-24.04-arm, linux, arm64)",
    "darwin-arm64": "native (macos-26, darwin, arm64)",
    "windows-amd64": "native (windows-2025, windows, amd64)",
}
LONG_STEPS = (
    "Verify direct LAN session natural rekey and idle lifecycle",
    "Verify guarded relay real-time lease continuity",
    "Verify relay-only real-time lease and idle continuity",
)
FAST_STEPS = (
    "Test and reproduce locked frontend assets",
    "Check exact toolchain and native target",
    "Local and mock tests, race detector, and vet",
    "Verify adapted engine admission and native underlay denial",
    "Verify direct LAN WireGuard and Core native applications",
    "Verify direct LAN synthetic expiry and rekey",
    "Verify explicit WAN discovery with isolated native STUN",
    "Verify guarded production direct and relay underlays",
    "Verify paired transport and Core applications over an isolated relay",
    "Verify recovery with the ordinary direct-enabled transport",
    "Build native package and smoke archive contents",
)
SHA = re.compile(r"[0-9a-f]{40}\Z")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, stderr=subprocess.DEVNULL)


def head():
    value = git("rev-parse", "HEAD").decode().strip()
    require(bool(SHA.fullmatch(value)), "invalid checkout SHA")
    require(not os.environ.get("GITHUB_SHA") or os.environ["GITHUB_SHA"] == value,
            "checkout differs from the workflow SHA")
    return value


def digest(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


def api(path, binary=False):
    # gh owns authenticated redirects; credentials never enter arguments or logs.
    result = subprocess.run(["gh", "api", "--method", "GET", "repos/" + REPOSITORY + "/" + path],
                            check=True, capture_output=True, timeout=15)
    return result.stdout if binary else json.loads(result.stdout)


def classify(sha, baseline=None, force=False):
    args = [sys.executable, str(ROOT / POLICY), "--head", sha]
    if baseline:
        args += ["--verified-baseline-commit", baseline["head_sha"],
                 "--verified-baseline-run-id", str(baseline["id"])]
    if force:
        args.append("--force-full")
    result = subprocess.run(args, cwd=ROOT, check=True, capture_output=True, timeout=90)
    plan = json.loads(result.stdout)
    require(type(plan.get("long_required")) is bool, "invalid classifier decision")
    return plan


def trusted_run(run):
    require(type(run.get("id")) is int and run["id"] > 0, "invalid baseline run ID")
    require(type(run.get("run_attempt")) is int and run["run_attempt"] > 0, "invalid baseline attempt")
    require(run.get("status") == "completed" and run.get("conclusion") == "success", "baseline CI failed")
    require(run.get("event") in ("push", "workflow_dispatch") and run.get("head_branch") == "main",
            "baseline must be canonical main CI")
    require(run.get("name") == "Cross-platform CI" and run.get("path") == WORKFLOW, "wrong baseline workflow")
    require(run.get("repository", {}).get("full_name") == REPOSITORY
            and run.get("head_repository", {}).get("full_name") == REPOSITORY, "wrong baseline repository")
    require(bool(SHA.fullmatch(run.get("head_sha", ""))), "invalid baseline SHA")


def read_receipt(run):
    listing = api(f"actions/runs/{run['id']}/artifacts?per_page=100")
    require(type(listing.get("total_count")) is int and isinstance(listing.get("artifacts"), list)
            and 0 <= listing["total_count"] <= 100
            and listing["total_count"] == len(listing["artifacts"]), "incomplete baseline artifact inventory")
    matches = [a for a in listing["artifacts"] if a.get("name") == "ci-coverage"]
    require(len(matches) == 1, "missing or duplicate baseline receipt")
    artifact = matches[0]
    require(artifact.get("expired") is False and 0 < artifact.get("size_in_bytes", 0) < 524288,
            "unavailable baseline receipt")
    require(type(artifact.get("id")) is int and artifact["id"] > 0, "invalid artifact ID")
    archive = api(f"actions/artifacts/{artifact['id']}/zip", binary=True)
    require(len(archive) < 524288, "oversized baseline receipt")
    with zipfile.ZipFile(io.BytesIO(archive)) as z:
        require(z.namelist() == ["ci-coverage.json"], "unexpected receipt archive entries")
        info = z.getinfo("ci-coverage.json")
        require(info.file_size < 524288, "oversized receipt content")
        return json.loads(z.read(info))


def validate_receipt(receipt, run):
    trusted_run(run)
    sha = run["head_sha"]
    require(type(receipt.get("schema_version")) is int and receipt["schema_version"] == 1 and receipt.get("repository") == REPOSITORY,
            "invalid receipt identity")
    require(type(receipt.get("run_id")) is int and type(receipt.get("run_attempt")) is int
            and receipt["run_id"] == run["id"] and receipt["run_attempt"] == run["run_attempt"],
            "receipt attempt differs from successful attempt")
    require(receipt.get("head_sha") == sha and receipt.get("full_native") is True,
            "baseline did not execute full native coverage")
    require(receipt.get("browser") == "success" and receipt.get("manifest") == "success"
            and receipt.get("targets") == {t: "success" for t in TARGETS}, "incomplete baseline coverage")
    require(receipt.get("policy_sha256") == digest(POLICY), "baseline uses a different selection policy")
    require(receipt.get("head_tree") == git("rev-parse", sha + "^{tree}").decode().strip(), "wrong baseline tree")
    for field, path in (("policy_sha256", POLICY), ("workflow_sha256", WORKFLOW)):
        require(receipt.get(field) == hashlib.sha256(git("show", sha + ":" + path)).hexdigest(),
                "receipt source fingerprint differs")
    return run


def baseline_by_id(run_id, sha):
    require(type(run_id) is int and run_id > 0 and bool(SHA.fullmatch(sha)), "invalid nominated baseline")
    run = api(f"actions/runs/{run_id}")
    trusted_run(run)
    require(run["head_sha"] == sha, "nominated baseline SHA differs")
    return validate_receipt(read_receipt(run), run)


def find_baseline():
    # Bounded optimization: an older/expired/incomplete history just runs full.
    listing = api("actions/workflows/ci.yml/runs?branch=main&status=success&per_page=10")
    for run in listing.get("workflow_runs", []):
        try:
            trusted_run(run)
            # Do not download receipts for commits outside this checkout history.
            git("merge-base", "--is-ancestor", run["head_sha"], "HEAD")
            return validate_receipt(read_receipt(run), run)
        except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
            continue
    return None


def read_plan(path):
    plan = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    require(plan.get("version") == 1 and plan.get("policy_id") == "native-long-impact-v1", "unknown plan schema")
    require(type(plan.get("long_required")) is bool, "plan lacks explicit decision")
    require(plan.get("head_sha") == head(), "plan belongs to another checkout")
    require(plan.get("head_tree") == git("rev-parse", "HEAD^{tree}").decode().strip(), "plan tree differs")
    require(plan.get("policy_sha256") == digest(POLICY), "plan policy differs")
    if not plan["long_required"]:
        require(bool(SHA.fullmatch(plan.get("baseline_sha", "")))
                and type(plan.get("baseline_run_id")) is int and plan["baseline_run_id"] > 0,
                "skip lacks baseline provenance")
    return plan


def write_plan(plan, path):
    pathlib.Path(path).parent.mkdir(parents=True, exist_ok=True)
    pathlib.Path(path).write_text(json.dumps(plan, indent=2) + "\n", encoding="utf-8")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            print("long_required=" + str(plan["long_required"]).lower(), file=stream)
    mode = "FULL: all real-time native tests required" if plan["long_required"] else "SELECTED: real-time tests not required by the verified change scope"
    print(mode + " (" + plan["reason"] + ")")


def successful_step(job, name):
    steps = [s for s in job.get("steps", []) if s.get("name") == name]
    return len(steps) == 1 and steps[0].get("status") == "completed" and steps[0].get("conclusion") == "success"


def evaluate_jobs(jobs, long_required):
    index = {}
    for job in jobs:
        name = job.get("name")
        require(name not in index, "duplicate job name")
        index[name] = job
    names = [*TARGETS.values(), "browser", "manifest-smoke"]
    for name in names:
        job = index.get(name, {})
        require(job.get("status") == "completed" and job.get("conclusion") == "success",
                "required job did not pass: " + name)
    full = True
    for target, name in TARGETS.items():
        job = index[name]
        for step in FAST_STEPS:
            require(successful_step(job, step), "required fast step did not run: " + target + ": " + step)
        if target == "windows-amd64":
            require(successful_step(job, "Verify Windows receive-retirement directory barriers"), "Windows native barrier probe missing")
        for step in LONG_STEPS:
            passed = successful_step(job, step)
            if long_required:
                require(passed, "required real-time step did not run: " + target + ": " + step)
            else:
                matches = [s for s in job.get("steps", []) if s.get("name") == step]
                require(len(matches) == 1 and matches[0].get("conclusion") in ("success", "skipped"),
                        "unexpected real-time step state: " + target)
            full = full and passed
    require(successful_step(index["browser"], "Verify actual UI workflows with isolated Playwright fixtures"), "browser tests missing")
    require(successful_step(index["manifest-smoke"], "Exercise signing and verification offline with a disposable test key"), "manifest verification missing")
    return full


def current_jobs():
    run_id, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    require(run_id.isdecimal() and attempt.isdecimal(), "invalid current run identity")
    jobs = []
    for page in range(1, 11):
        data = api(f"actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100&page={page}")
        batch = data["jobs"]
        jobs.extend(batch)
        if len(batch) < 100:
            require(len(jobs) == data["total_count"], "incomplete current job inventory")
            return jobs
    raise ValueError("job inventory exceeds validation limit")


def job_timings(jobs):
    """Only fixed CI names and elapsed seconds; no runner/account metadata."""
    def elapsed(item):
        try:
            start = datetime.datetime.fromisoformat(item["started_at"].replace("Z", "+00:00"))
            end = datetime.datetime.fromisoformat(item["completed_at"].replace("Z", "+00:00"))
            value = (end - start).total_seconds()
            return value if value >= 0 else None
        except (ValueError, KeyError, TypeError, AttributeError):
            return None
    names = {*TARGETS.values(), "browser", "manifest-smoke"}
    steps = {*FAST_STEPS, *LONG_STEPS, "Restore trusted main Go caches",
             "Restore isolated development Go caches", "Save Go caches after all native checks pass on main",
             "Save development Go caches after all native checks pass"}
    return [{"job": j["name"], "elapsed_seconds": elapsed(j),
             "steps": [{"step": s["name"], "elapsed_seconds": elapsed(s), "result": s.get("conclusion")}
                       for s in j.get("steps", []) if s.get("name") in steps]}
            for j in jobs if j.get("name") in names]


def finalize(input_path, output):
    try:
        plan = read_plan(input_path)
        if not plan["long_required"]:
            baseline = baseline_by_id(plan["baseline_run_id"], plan["baseline_sha"])
            rechecked = classify(head(), baseline)
            require(rechecked == plan, "change decision did not reproduce")
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        plan = classify(head(), force=True)
        print("::warning::Selection evidence unavailable; aggregate requires every real-time test")
    jobs = current_jobs()
    full = evaluate_jobs(jobs, plan["long_required"])
    receipt = {"schema_version": 1, "repository": REPOSITORY,
               "run_id": int(os.environ["GITHUB_RUN_ID"]), "run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
               "head_sha": head(), "head_tree": git("rev-parse", "HEAD^{tree}").decode().strip(),
               "policy_sha256": digest(POLICY), "workflow_sha256": digest(WORKFLOW),
               "full_native": full, "targets": {t: "success" for t in TARGETS},
               "browser": "success", "manifest": "success", "selection": plan,
               "timings": job_timings(jobs)}
    pathlib.Path(output).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    summary = "Full native coverage passed" if full else "Selected coverage passed; real-time tests NOT RUN for this change scope"
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as stream:
            print("## " + summary + "\n\nDecision: " + plan["reason"] + "\n", file=stream)
            print("All four native targets, fast security/logic checks, browser acceptance and package/manifest checks passed.\n", file=stream)
            print("| Job | Elapsed seconds |\n| --- | ---: |", file=stream)
            for row in receipt["timings"]:
                print(f"| {row['job']} | {row['elapsed_seconds'] if row['elapsed_seconds'] is not None else 'unavailable'} |", file=stream)
            print("\nThese are per-job elapsed times, not summed critical-path time. Cache restore/save step timings are included in the receipt.\n", file=stream)
            if not full:
                print(f"Last full baseline: {plan['baseline_sha']} ([run {plan['baseline_run_id']}](https://github.com/{REPOSITORY}/actions/runs/{plan['baseline_run_id']})). This run does not advance that baseline.\n", file=stream)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "resolve", "finalize"))
    parser.add_argument("--input")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_REPOSITORY", REPOSITORY) == REPOSITORY, "unexpected repository")
    if args.command == "finalize":
        finalize(args.input, args.output)
        return
    if args.command == "resolve":
        try:
            plan = read_plan(args.input)
        except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
            plan = classify(head(), force=True)
            print("::warning::Missing/invalid impact plan; running full real-time coverage")
    else:
        force = os.environ.get("FORCE_FULL", "false") != "false"
        baseline = None
        if not force:
            try:
                baseline = find_baseline()
            except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
                print("::warning::Baseline lookup unavailable; running full real-time coverage")
        plan = classify(head(), baseline, force)
    write_plan(plan, args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        print("CI coverage validation failed; no success receipt was issued", file=sys.stderr)
        sys.exit(1)
