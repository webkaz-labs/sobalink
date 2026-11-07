#!/usr/bin/env python3
"""Choose the minimum applicable checks and validate their actual completion.

Scope is proved from this event's complete Git delta, never from a previous
success or a cached test result. Release validation is independent of this file.
"""
import argparse
import datetime
import hashlib
import importlib.util
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
INTEGRATION_TARGETS = {target: name.replace("native (", "native-integration (")
                       for target, name in TARGETS.items()}
NATIVE_SHARED_STEPS = (
    "Reproduce locked frontend assets",
    "Check exact toolchain and native target",
)
NATIVE_UNIT_STEPS = (
    "Local and mock tests, race detector, and vet",
    "Verify adapted engine admission and native underlay denial",
)
# GitHub's wait control cannot have an if. It is a no-op in the unit lane.
NATIVE_SHARED_CONTROL_STEPS = ("Wait for Core context checks",
                               "Wait for direct LAN application checks",
                               "Wait for real-time lifecycle and lease checks")
WINDOWS_BARRIER_STEPS = ("Verify Windows receive-retirement directory barriers",)
# Separate merge-tested UI evidence never substitutes for backend qualification.
NATIVE_UI_TARGETS = {
    "linux-amd64": "native-ui / companion (linux-amd64)",
    "linux-arm64": "native-ui / companion (linux-arm64)",
    "darwin-arm64": "native-ui / companion (darwin-arm64)",
    "windows-amd64": "native-ui / companion (windows-amd64)",
}
NATIVE_UI_SCOPE_STEPS = (
    "Validate native UI source and required scope",
    "Check companion toolchain and prepare verified engine",
    "Run complete companion race suite with protected IPC evidence",
    "Vet MyGo lifetimes and check companion formatting",
    "Compile companion and check deterministic kit packaging",
)
NATIVE_UI_PRODUCT_STEPS = ("Prepare locked frontend package inventory for native UI",
                           "Build product package for native UI", "Smoke product package for native UI")
LONG_STEPS = (
    "Verify direct LAN session natural rekey and idle lifecycle",
    "Verify guarded relay real-time lease continuity",
    "Verify relay-only real-time lease and idle continuity",
)
FULL_ONLY_STEPS = (
    "Verify native remote resource inspection",
    "Verify native remote resource management",
    "Verify native fixed-group resource catalog",
)
FAST_STEPS = (
    "Reproduce locked frontend assets",
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
    'Wait for Core context checks',
    "Verify direct LAN WireGuard and Core native applications",
    "Verify explicit dispatch with three separate Core processes",
    "Verify durable outbox restart across Core processes",
    "Verify durable text restart and lost ACK across Core processes",
    "Wait for direct LAN application checks",
    "Verify manual nearby setup across native Core peers",
    "Verify TOFU request approval across native Core peers",
    "Verify full verification request approval across native Core peers",
    "Verify request rejection and cancellation across native Core peers",
    "Verify direct LAN synthetic expiry and rekey",
    "Verify explicit WAN discovery with isolated native STUN",
    "Verify guarded production direct and relay underlays",
    "Verify paired transport and Core applications over an isolated relay",
    "Verify recovery with the ordinary direct-enabled transport",
    "Wait for real-time lifecycle and lease checks",
    "Build native package and smoke archive contents",
)
LINUX_NEARBY_TARGETS = ('linux-amd64', 'linux-arm64')
LINUX_NEARBY_STEPS = ('Verify Linux nearby native discovery without skips',)
NATIVE_UI_STEPS = ('Test experimental native companion',)
NATIVE_PACKAGE_TARGETS = ('windows-amd64', 'darwin-arm64')
NATIVE_PACKAGE_STEPS = ('Collect required whole-Core race evidence', 'Verify remaining native package race coverage', 'Preserve required native package proof')
MACOS_NEARBY_TARGETS = ('darwin-arm64',)
MACOS_NEARBY_STEPS = ('Verify macOS five-scope discovery without skips', 'Preserve required macOS discovery proof')
SHA = re.compile(r"[0-9a-f]{40}\Z")

DOCUMENT_STEPS = ("Verify document selection and static content", "Require document checks and selected rendering")
SCOPES = {"docs", "frontend", "go", "native-short", "native-ui", "full"}
BROWSER_STEPS = (
                 "Prepare verified browser engine",
                 "Test and reproduce locked frontend assets",
                 "Wait for verified browser engine",
                 "Prepare verified Japanese fonts",
                 "Install official Chromium and fonts",
                 "Build isolated browser fixture",
                 "Wait for browser runtime preparation",
                 "Verify actual UI workflows with isolated Playwright fixtures",
                 "Verify synthetic request pairing UI without skips",
)
NATIVE_BROWSER_STEPS = (
                 "Prepare verified native-browser engine",
                 "Install locked native-browser dependencies",
                 "Wait for verified native-browser engine",
                 "Prepare verified Japanese fonts",
                 "Install official native-browser Chromium and fonts",
                 "Verify privacy-safe fixture diagnostics",
                 "Wait for native-browser runtime preparation",
                 "Verify real browser UI launch without skips",
                 "Verify catalog view with separate agents and actual browser",
                 "Verify selected transfer retry with separate agents and actual browser",
                 "Verify multi-recipient send recovery with separate agents and actual browser",
                 "Verify durable outbox recovery with separate agents and actual browser",
                 "Verify nearby setup with separate agents and actual browser",
                 "Verify durable text recovery with separate agents and actual browser",
)
GO_STEPS = ("Validate scoped Go selection", "Check exact toolchain and module graph",
            "Test affected Go packages and reverse dependencies")
SCHEDULE_ACCEPTANCE = {
    "web-activation": ("Verify fixed Web acceptance policy",
                       "Compile and run seven private synthetic-owner cases"),
    "product-activation": ("Verify fixed product acceptance policy",
                           "Build normal assets and run two private production-entry cases"),
}
REGRESSION_STEPS = ("Run shared CI policy and source regressions",)
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


def native_step_expectations(target, suite, scope):
    """Exact outcomes for both lanes, including gates owned by the other lane."""
    require(target in TARGETS and suite in ("unit", "integration"), "unknown native target or suite")
    require(scope in ("native-short", "full"), "unknown native scope")
    inventory = (FAST_STEPS + LONG_STEPS + FULL_ONLY_STEPS + LINUX_NEARBY_STEPS
                 + MACOS_NEARBY_STEPS + WINDOWS_BARRIER_STEPS + NATIVE_PACKAGE_STEPS + NATIVE_UI_STEPS)
    required = set(NATIVE_SHARED_STEPS + NATIVE_SHARED_CONTROL_STEPS)
    if suite == "unit":
        required.update(NATIVE_UNIT_STEPS)
        if target in LINUX_NEARBY_TARGETS:
            required.update(LINUX_NEARBY_STEPS)
        if target in MACOS_NEARBY_TARGETS:
            required.update(MACOS_NEARBY_STEPS)
        if target == "windows-amd64":
            required.update(WINDOWS_BARRIER_STEPS)
        if target in NATIVE_PACKAGE_TARGETS:
            required.update(NATIVE_PACKAGE_STEPS)
    else:
        required.update(set(FAST_STEPS) - set(NATIVE_UNIT_STEPS))
        if scope == "full":
            required.update(LONG_STEPS + FULL_ONLY_STEPS)
        if target in NATIVE_PACKAGE_TARGETS:
            required.update(NATIVE_UI_STEPS)
    return {name: "success" if name in required else "skipped" for name in inventory}


def evaluate_jobs(jobs, scope, *, scheduled=False):
    require(scope in SCOPES, "unknown required scope")
    require(not scheduled or scope == "full", "scheduled CI must require full coverage")
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
    passed("documents", DOCUMENT_STEPS)
    required = {"impact", "documents"}
    if scope in ("frontend", "native-short", "full"):
        passed("browser", BROWSER_STEPS)
        required.add("browser")
        passed("browser-native", NATIVE_BROWSER_STEPS)
        required.add("browser-native")
    if scope == "go":
        passed("go-unit", GO_STEPS)
        required.add("go-unit")
    if scope in ("native-short", "native-ui", "full"):
        passed("policy-regressions", REGRESSION_STEPS)
        required.add("policy-regressions")
    if scope == "native-ui":
        for target, name in NATIVE_UI_TARGETS.items():
            steps = NATIVE_UI_SCOPE_STEPS
            if target in NATIVE_PACKAGE_TARGETS:
                steps += NATIVE_UI_PRODUCT_STEPS
            passed(name, steps)
            required.add(name)
    if scope in ("native-short", "full"):
        for suite, targets in (("unit", TARGETS), ("integration", INTEGRATION_TARGETS)):
            for target, name in targets.items():
                passed(name, ())
                for gate, result in native_step_expectations(target, suite, scope).items():
                    matches = [step for step in index[name].get("steps", []) if step.get("name") == gate]
                    require(len(matches) == 1 and matches[0].get("status") == "completed"
                            and matches[0].get("conclusion") == result,
                            "required native gate outcome differs: " + name + ": " + gate + ": " + result)
                required.add(name)
        passed("manifest-smoke", ("Exercise signing and verification offline with a disposable test key",))
        required.add("manifest-smoke")

    # Job-level skips can appear as one unexpanded matrix job or several jobs.
    # Only explicitly out-of-scope jobs may be skipped; their presence is not
    # required and is never represented as a test pass in the receipt.
    application = {*TARGETS.values(), *INTEGRATION_TARGETS.values(), *NATIVE_UI_TARGETS.values(), "native", "native-ui", "browser", "browser-native", "go-unit", "manifest-smoke", "policy-regressions"}
    known = application | {"impact", "documents", "ci-required", "nightly-full-check", *SCHEDULE_ACCEPTANCE}
    require(set(index) <= known, "unknown job name or native suite")
    for name in application - required:
        if name in index:
            require(index[name].get("status") == "completed"
                    and index[name].get("conclusion") == "skipped",
                    "unexpected out-of-scope job result: " + name)
    if scheduled:
        passed("ci-required", ("Require every check selected for this change",))
        for name, steps in SCHEDULE_ACCEPTANCE.items():
            passed(name, steps)
    return scope == "full"


def api(path):
    # gh owns authentication; no credentials enter arguments or saved reports.
    result = subprocess.run(["gh", "api", "--method", "GET", "repos/" + REPOSITORY + "/" + path],
                            check=True, capture_output=True, timeout=15)
    return json.loads(result.stdout)


def event_job_head():
    """Bind REST job identity to the immutable event, not the PR merge SHA.

    A full/fallback plan can bypass impact's event-base proof, so repeat the
    exact PR parent check here rather than trusting the chosen scope.
    """
    commit = head()
    require(os.environ.get("GITHUB_SHA") == commit
            and os.environ.get("GITHUB_REPOSITORY") == REPOSITORY, "invalid job event environment")
    kind, ref = os.environ.get("GITHUB_EVENT_NAME"), os.environ.get("GITHUB_REF")
    require(kind in ("pull_request", "push", "workflow_dispatch", "schedule"), "unknown job event")
    try:
        raw = pathlib.Path(os.environ["GITHUB_EVENT_PATH"]).read_bytes()
        require(len(raw) <= 32 * 1024 * 1024, "job event exceeds validation limit")
        event = json.loads(raw)
        require(event["repository"]["full_name"] == REPOSITORY, "job event repository differs")
        if kind == "pull_request":
            pr, number = event["pull_request"], event["number"]
            require(type(number) is int and number > 0 and ref == f"refs/pull/{number}/merge",
                    "invalid PR job event")
            base, source = pr["base"], pr["head"]
            require(base["ref"] == "main" and base["repo"]["full_name"] == REPOSITORY,
                    "invalid PR job base")
            base_sha, source_sha = base["sha"], source["sha"]
            require(all(type(value) is str and SHA.fullmatch(value) and set(value) != {"0"}
                        for value in (base_sha, source_sha)) and base_sha != source_sha,
                    "invalid PR job source")
            head_repo = source.get("repo")
            require(head_repo is None or type(head_repo) is dict
                    and type(head_repo.get("full_name")) is str
                    and re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", head_repo["full_name"]),
                    "invalid PR job source repository")
            parents = git("rev-list", "--parents", "-n", "1", commit).decode("ascii").split()
            require(parents == [commit, base_sha, source_sha], "unverified PR job merge parents")
            hint = pr.get("merge_commit_sha")
            require(hint is None or type(hint) is str and SHA.fullmatch(hint) and set(hint) != {"0"},
                    "invalid PR merge hint")
            # A stale, well-formed mergeability hint supplies no identity.
            return source_sha
        # GITHUB_REF is supplied by Actions. Dispatch supports branches and
        # tags, including Unicode and long names; do not narrow that surface.
        dispatch_ref = type(ref) is str and any(ref.startswith(prefix) and len(ref) > len(prefix)
                                              for prefix in ("refs/heads/", "refs/tags/"))
        require(type(ref) is str and (ref == "refs/heads/main" or kind == "workflow_dispatch" and dispatch_ref),
                "invalid job event ref")
        if kind == "push":
            require(event["after"] == commit, "push job event head differs")
        return commit
    except (KeyError, TypeError, OSError, UnicodeError) as exc:
        raise ValueError("missing or invalid job event") from exc


def current_jobs():
    run_id, attempt = os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"]
    require(run_id.isdecimal() and attempt.isdecimal(), "invalid current run identity")
    sha = event_job_head()
    jobs, seen, total = [], set(), None
    for page in range(1, 11):
        data = api(f"actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100&page={page}")
        batch, count = data["jobs"], data["total_count"]
        require(type(count) is int and count >= 0 and isinstance(batch, list) and len(batch) <= 100,
                "invalid current job inventory")
        require(total is None or count == total, "current job inventory changed during pagination")
        total = count
        for job in batch:
            require(type(job.get("id")) is int and job["id"] > 0 and job["id"] not in seen,
                    "duplicate or invalid current job ID")
            seen.add(job["id"])
            require(type(job.get("run_id")) is int and job["run_id"] == int(run_id)
                    and type(job.get("run_attempt")) is int and job["run_attempt"] == int(attempt)
                    and job.get("head_sha") == sha, "job belongs to another run, attempt, or head")
        jobs.extend(batch)
        require(len(jobs) <= total, "current job inventory exceeds declared count")
        if len(batch) < 100:
            require(len(jobs) == total, "incomplete current job inventory")
            # The attempt endpoint is authoritative. Successful jobs retained in a
            # partial rerun can have timestamps predating this attempt's start.
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
    names = {*TARGETS.values(), *INTEGRATION_TARGETS.values(), *NATIVE_UI_TARGETS.values(), "impact", "documents", "browser", "browser-native", "go-unit", "manifest-smoke", "policy-regressions"}
    steps = {*NATIVE_UI_SCOPE_STEPS, *NATIVE_UI_PRODUCT_STEPS, *REGRESSION_STEPS, *DOCUMENT_STEPS, *FAST_STEPS, *LONG_STEPS, *FULL_ONLY_STEPS, *BROWSER_STEPS, *NATIVE_BROWSER_STEPS, *GO_STEPS, *LINUX_NEARBY_STEPS, *MACOS_NEARBY_STEPS, *NATIVE_PACKAGE_STEPS, *NATIVE_UI_STEPS, *WINDOWS_BARRIER_STEPS,
             "Restore trusted main Go caches", "Restore isolated development Go caches",
             "Save Go caches after integration checks pass on main",
             "Save development Go caches after integration checks pass"}
    return [{"job": j["name"], "elapsed_seconds": elapsed(j),
             "steps": [{"step": s["name"], "elapsed_seconds": elapsed(s), "result": s.get("conclusion")}
                       for s in j.get("steps", []) if s.get("name") in steps]}
            for j in jobs if j.get("name") in names]


def audit_macos_discovery():
    path = ROOT / '.github/scripts/ci-dnssd-required.py'
    spec = importlib.util.spec_from_file_location('ci_dnssd_required', path)
    required = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), required.__dict__)
    run = api('actions/runs/' + os.environ['GITHUB_RUN_ID'])
    require(type(run.get('id')) is int and run['id'] == int(os.environ['GITHUB_RUN_ID'])
            and type(run.get('run_attempt')) is int and run['run_attempt'] == int(os.environ['GITHUB_RUN_ATTEMPT']),
            'current CI attempt changed')
    return required.audit_artifact(api, run, head(), git('rev-parse', 'HEAD^{tree}').decode().strip())


def audit_native_packages():
    path = ROOT / '.github/scripts/ci-native-core-required.py'
    spec = importlib.util.spec_from_file_location('ci_native_core_required', path)
    required = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), required.__dict__)
    run = api('actions/runs/' + os.environ['GITHUB_RUN_ID'])
    require(type(run.get('id')) is int and run['id'] == int(os.environ['GITHUB_RUN_ID'])
            and type(run.get('run_attempt')) is int and run['run_attempt'] == int(os.environ['GITHUB_RUN_ATTEMPT']),
            'current CI attempt changed')
    return {platform: required.audit_artifact(api, run, head(), git('rev-parse', 'HEAD^{tree}').decode().strip(), platform)
            for platform in NATIVE_PACKAGE_TARGETS}


def finalize(input_path, output, *, scheduled=False):
    require(not scheduled or os.environ.get("GITHUB_EVENT_NAME") == "schedule",
            "nightly aggregate requires a schedule event")
    plan = verified_plan(input_path)
    scope = plan["scope"]
    jobs = current_jobs()
    full = evaluate_jobs(jobs, scope, scheduled=scheduled)
    macos_proof, package_proof = None, None
    if scope in ('native-short', 'full'):
        macos_proof = audit_macos_discovery()
        package_proof = audit_native_packages()
    receipt = {"schema_version": 3, "repository": REPOSITORY,
               "run_id": int(os.environ["GITHUB_RUN_ID"]), "run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
               "head_sha": head(), "head_tree": git("rev-parse", "HEAD^{tree}").decode().strip(),
               "policy_sha256": digest(POLICY), "workflow_sha256": digest(WORKFLOW),
               "scope": scope, "full_native": full,
               "documents": "success",
               "documents_policy_sha256": digest(".github/scripts/ci-documents.py"),
               "targets": {t: "success" if full else "short_checks_passed" if scope == "native-short" else "not_run" for t in TARGETS},
               "native_suites": {suite: {t: "success" if full else "short_checks_passed" if scope == "native-short" else "not_run" for t in TARGETS} for suite in ("unit", "integration")},
               "long_checks": {t: "success" if full else "not_run" for t in TARGETS},
               "browser_native": "success" if scope in ("frontend", "native-short", "full") else "not_run",
               "browser": "success" if scope in ("frontend", "native-short", "full") else "not_run",
               "manifest": "success" if scope in ("native-short", "full") else "not_run",
               "go_unit": "success" if scope == "go" else "not_run",
               "selection": plan, "timings": job_timings(jobs), "macos_discovery_proof": macos_proof, "native_package_proof": package_proof}
    if scope == "native-ui":
        receipt["native_ui_workflow_sha256"] = digest(".github/workflows/native-ui-checks.yml")
        receipt["native_ui"] = {t: "success" for t in NATIVE_UI_TARGETS}
        receipt["native_ui_packages"] = {t: "success" for t in NATIVE_PACKAGE_TARGETS}
    if scheduled:
        receipt["scheduled_acceptance"] = {name: "success" for name in SCHEDULE_ACCEPTANCE}
    pathlib.Path(output).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    descriptions = {
        "docs": "Documentation only; application tests, builds and packages NOT RUN",
        "frontend": "Frontend checks passed; four-target native and package checks NOT RUN",
        "go": "Selected Go package race and vet checks passed on Linux; full native, browser and package checks NOT RUN",
        "native-short": "Four-target short native, browser and package checks passed; real-time lifecycle, lease and native remote resource inspection/management and fixed-group catalog checks NOT RUN",
        "native-ui": "Four-platform companion race, protected IPC, MyGo and Windows/macOS product package checks passed; backend qualification, browser and manifest checks NOT RUN",
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
    parser.add_argument("command", choices=("plan", "resolve", "finalize", "nightly-finalize"))
    parser.add_argument("--input")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_REPOSITORY", REPOSITORY) == REPOSITORY, "unexpected repository")
    if args.command in ("finalize", "nightly-finalize"):
        finalize(args.input, args.output, scheduled=args.command == "nightly-finalize")
    else:
        plan = verified_plan(args.input) if args.command == "resolve" else classify(head(), force_full())
        write_plan(plan, args.output)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        print("CI coverage validation failed; no success receipt was issued", file=sys.stderr)
        sys.exit(1)
