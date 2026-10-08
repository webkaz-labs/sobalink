#!/usr/bin/env python3
"""Choose the minimum applicable checks and validate their actual completion.

Scope is proved from this event's complete Git delta, never from a previous
success or a cached test result. Release validation is independent of this file.
"""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys

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
    "Verify Core context control over pinned TLS",
    "Verify context control over fixed loopback TCP",
    'Verify managed session tls',
    'Verify managed session outbound-tcp',
    'Verify managed session native-caller',
    'Verify managed session native-simultaneous',
    'Verify managed session native-udp-capacity',
    'Verify managed activation acceptance',
    'Verify managed restart acceptance',
    "Verify direct LAN WireGuard and Core native applications",
    "Verify direct LAN synthetic expiry and rekey",
    "Verify explicit WAN discovery with isolated native STUN",
    "Verify guarded production direct and relay underlays",
    "Verify paired transport and Core applications over an isolated relay",
    "Verify recovery with the ordinary direct-enabled transport",
    "Build native package and smoke archive contents",
)
SHA = re.compile(r"[0-9a-f]{40}\Z")

SCOPES = {"docs", "frontend", "go", "native-short", "full"}
BROWSER_STEPS = ("Test and reproduce locked frontend assets",
                 "Verify actual UI workflows with isolated Playwright fixtures")
GO_STEPS = ("Validate scoped Go selection", "Check exact toolchain and module graph",
            "Test affected Go packages and reverse dependencies")
SCOPE_STEP = "Select minimum CI scope"


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


def classify(sha, force=False):
    args = [sys.executable, str(ROOT / POLICY), "--head", sha]
    if force:
        args.append("--force-full")
    result = subprocess.run(args, cwd=ROOT, check=True, capture_output=True, timeout=90)
    plan = json.loads(result.stdout)
    require(plan.get("version") == 3 and plan.get("policy_id") == "minimum-ci-v3",
            "unknown classifier policy")
    require(plan.get("scope") in SCOPES, "invalid classifier decision")
    return plan


def read_plan(path):
    plan = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    require(plan.get("version") == 3 and plan.get("policy_id") == "minimum-ci-v3",
            "unknown plan schema")
    require(plan.get("scope") in SCOPES, "plan lacks explicit scope")
    require(plan.get("head_sha") == head(), "plan belongs to another checkout")
    require(plan.get("head_tree") == git("rev-parse", "HEAD^{tree}").decode().strip(),
            "plan tree differs")
    require(plan.get("policy_sha256") == digest(POLICY), "plan policy differs")
    return plan


def force_full():
    return os.environ.get("FORCE_FULL", "false") != "false"


def verified_plan(path):
    """An artifact is only a hint: repeat the Git proof in each consuming job."""
    try:
        plan = read_plan(path)
        require(plan == classify(head(), force_full()), "scope decision did not reproduce")
        return plan
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        print("::warning::Invalid scope evidence; full coverage is required")
        return classify(head(), force=True)


def write_plan(plan, path):
    pathlib.Path(path).parent.mkdir(parents=True, exist_ok=True)
    pathlib.Path(path).write_text(json.dumps(plan, indent=2) + "\n", encoding="utf-8")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            print("scope=" + plan["scope"], file=stream)
            # Full native jobs keep the established step names and cache policy.
            print("long_required=" + str(plan["scope"] == "full").lower(), file=stream)
    print("CI scope: " + plan["scope"] + " (" + plan["reason"] + ")")


def successful_step(job, name):
    steps = [s for s in job.get("steps", []) if s.get("name") == name]
    return len(steps) == 1 and steps[0].get("status") == "completed" and steps[0].get("conclusion") == "success"


def evaluate_jobs(jobs, scope):
    require(scope in SCOPES, "unknown required scope")
    index = {}
    for job in jobs:
        name = job.get("name")
        require(name not in index, "duplicate job name")
        index[name] = job

    def passed(name, steps):
        job = index.get(name, {})
        require(job.get("status") == "completed" and job.get("conclusion") == "success",
                "required job did not pass: " + name)
        for step in steps:
            require(successful_step(job, step), "required step did not run: " + name + ": " + step)

    passed("impact", (SCOPE_STEP,))
    required = {"impact"}
    if scope in ("frontend", "native-short", "full"):
        passed("browser", BROWSER_STEPS)
        required.add("browser")
    if scope == "go":
        passed("go-unit", GO_STEPS)
        required.add("go-unit")
    if scope in ("native-short", "full"):
        for target, name in TARGETS.items():
            steps = FAST_STEPS + (LONG_STEPS if scope == "full" else ())
            if target == "windows-amd64":
                steps += ("Verify Windows receive-retirement directory barriers",)
            passed(name, steps)
            if scope == "native-short":
                for long_step in LONG_STEPS:
                    matches = [s for s in index[name].get("steps", []) if s.get("name") == long_step]
                    require(len(matches) == 1 and matches[0].get("status") == "completed"
                            and matches[0].get("conclusion") == "skipped",
                            "native-short must record the unexecuted real-time gate: " + name + ": " + long_step)
            required.add(name)
        passed("manifest-smoke", ("Exercise signing and verification offline with a disposable test key",))
        required.add("manifest-smoke")

    # Job-level skips can appear as one unexpanded matrix job or several jobs.
    # Only explicitly out-of-scope jobs may be skipped; their presence is not
    # required and is never represented as a test pass in the receipt.
    application = {*TARGETS.values(), "native", "browser", "go-unit", "manifest-smoke"}
    for name in application - required:
        if name in index:
            require(index[name].get("status") == "completed"
                    and index[name].get("conclusion") == "skipped",
                    "unexpected out-of-scope job result: " + name)
    return scope == "full"


def api(path):
    # gh owns authentication; no credentials enter arguments or saved reports.
    result = subprocess.run(["gh", "api", "--method", "GET", "repos/" + REPOSITORY + "/" + path],
                            check=True, capture_output=True, timeout=15)
    return json.loads(result.stdout)


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
    """Fixed CI names and elapsed seconds only; no runner/account metadata."""
    def elapsed(item):
        try:
            start = datetime.datetime.fromisoformat(item["started_at"].replace("Z", "+00:00"))
            end = datetime.datetime.fromisoformat(item["completed_at"].replace("Z", "+00:00"))
            value = (end - start).total_seconds()
            return value if value >= 0 else None
        except (ValueError, KeyError, TypeError, AttributeError):
            return None
    names = {*TARGETS.values(), "impact", "browser", "go-unit", "manifest-smoke"}
    steps = {*FAST_STEPS, *LONG_STEPS, *BROWSER_STEPS, *GO_STEPS,
             "Restore trusted main Go caches", "Restore isolated development Go caches",
             "Save Go caches after all native checks pass on main",
             "Save development Go caches after all native checks pass"}
    return [{"job": j["name"], "elapsed_seconds": elapsed(j),
             "steps": [{"step": s["name"], "elapsed_seconds": elapsed(s), "result": s.get("conclusion")}
                       for s in j.get("steps", []) if s.get("name") in steps]}
            for j in jobs if j.get("name") in names]


def finalize(input_path, output):
    plan = verified_plan(input_path)
    scope = plan["scope"]
    jobs = current_jobs()
    full = evaluate_jobs(jobs, scope)
    receipt = {"schema_version": 3, "repository": REPOSITORY,
               "run_id": int(os.environ["GITHUB_RUN_ID"]), "run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
               "head_sha": head(), "head_tree": git("rev-parse", "HEAD^{tree}").decode().strip(),
               "policy_sha256": digest(POLICY), "workflow_sha256": digest(WORKFLOW),
               "scope": scope, "full_native": full,
               "targets": {t: "success" if full else "short_checks_passed" if scope == "native-short" else "not_run" for t in TARGETS},
               "long_checks": {t: "success" if full else "not_run" for t in TARGETS},
               "browser": "success" if scope in ("frontend", "native-short", "full") else "not_run",
               "manifest": "success" if scope in ("native-short", "full") else "not_run",
               "go_unit": "success" if scope == "go" else "not_run",
               "selection": plan, "timings": job_timings(jobs)}
    pathlib.Path(output).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    descriptions = {
        "docs": "Documentation only; application tests, builds and packages NOT RUN",
        "frontend": "Frontend checks passed; four-target native and package checks NOT RUN",
        "go": "Affected Go packages and reverse dependencies passed on Linux; full native, browser and package checks NOT RUN",
        "native-short": "Four-target short native, browser and package checks passed; real-time lifecycle and lease checks NOT RUN",
        "full": "Full native, browser and package coverage passed",
    }
    summary = descriptions[scope]
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as stream:
            print("## " + summary + "\n\nDecision: " + plan["reason"] + "\n", file=stream)
            print("This result describes this attempt only. No previous test result is reused.\n", file=stream)
            print("| Job | Elapsed seconds |\n| --- | ---: |", file=stream)
            for row in receipt["timings"]:
                print(f"| {row['job']} | {row['elapsed_seconds'] if row['elapsed_seconds'] is not None else 'unavailable'} |", file=stream)
            print("\nPer-job elapsed times are not summed critical-path time. Releases run their independent full validation.\n", file=stream)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "resolve", "finalize"))
    parser.add_argument("--input")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_REPOSITORY", REPOSITORY) == REPOSITORY, "unexpected repository")
    if args.command == "finalize":
        finalize(args.input, args.output)
    else:
        plan = verified_plan(args.input) if args.command == "resolve" else classify(head(), force_full())
        write_plan(plan, args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        print("CI coverage validation failed; no success receipt was issued", file=sys.stderr)
        sys.exit(1)
