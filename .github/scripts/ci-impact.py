#!/usr/bin/env python3
"""Fail-closed native long-test impact classification for an immutable Git delta.

Only the trusted workflow resolver may supply --verified-baseline-* after it
has authenticated a successful, complete full-coverage run for that exact SHA
and compatible policy. These arguments are an explicit trust boundary, not
proof by themselves. This program never loads a report as baseline authority.

Every native target still runs ordinary checks and the short lifecycle suite.
CSS/generated-asset eligibility additionally depends on check-frontend.py
reproducing the checked-in assets from this exact head; the workflow must fail
if that check fails. Release and scheduled/full-validation paths force FULL.
"""

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys

VERSION = 1
POLICY_ID = "native-long-impact-v1"
MAX_GIT_OUTPUT = 32 * 1024 * 1024
OID = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
ROOT_DOCS = frozenset(("README.md", "README.en.md", "SECURITY.md"))
# Authoritative development/agent policies do not qualify as ordinary prose.
POLICY_DOCS = frozenset((
    "docs/DEVELOPMENT_PRINCIPLES.en.md",
    "docs/DEVELOPMENT_PRINCIPLES.ja.md",
))
# These are separately ignored by the repository. A local/global ignore rule
# must not be able to conceal extra runtime source files from classification.
IGNORED_BUILD_DIRECTORIES = (
    ".sobalink-deps/", "bin/", "dist/", ".build/", "web/node_modules/",
    "web/.vite/", "web/coverage/", "web/qa-agent-browser/node_modules/",
    "web/qa-agent-browser/artifacts/",
)


class FailClosed(Exception):
    """A stable reason code; never expose subprocess stderr or local paths."""


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        raise FailClosed("invalid_arguments")


def digest(value):
    return hashlib.sha256(value).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=True).encode("ascii")


def decode_path(raw):
    try:
        path = raw.decode("utf-8", errors="strict")
    except UnicodeError as exc:
        raise FailClosed("invalid_path_encoding") from exc
    if (not path or path.startswith("/") or "\\" in path
            or any(ord(char) < 32 or ord(char) == 127 for char in path)
            or any(part in ("", ".", "..") for part in path.split("/"))):
        raise FailClosed("invalid_path")
    return path


def records(data):
    if not isinstance(data, bytes) or (data and not data.endswith(b"\0")):
        raise FailClosed("malformed_git_output")
    fields = data[:-1].split(b"\0") if data else []
    if any(not field for field in fields):
        raise FailClosed("malformed_git_output")
    return fields


def parse_name_status(data):
    """Parse --name-status -z, including both endpoints of every rename."""
    fields = records(data)
    changes = []
    index = 0
    while index < len(fields):
        try:
            status = fields[index].decode("ascii")
        except UnicodeError as exc:
            raise FailClosed("malformed_git_output") from exc
        index += 1
        if status in ("A", "M", "D"):
            count = 1
        elif re.fullmatch(r"R[0-9]{1,3}", status) and int(status[1:]) <= 100:
            count = 2
        else:
            # Copy, unmerged, broken, type-change, and unknown statuses fail shut.
            raise FailClosed("unsupported_change_status")
        if len(fields) - index < count:
            raise FailClosed("malformed_git_output")
        paths = [decode_path(field) for field in fields[index:index + count]]
        if len(paths) != len(set(paths)):
            raise FailClosed("malformed_git_output")
        changes.append({"status": status, "paths": paths})
        index += count
    seen = [path for change in changes for path in change["paths"]]
    if len(seen) != len(set(seen)):
        raise FailClosed("duplicate_changed_path")
    return changes


def parse_tree(data):
    """Parse the complete recursive ls-tree inventory, retaining type and mode."""
    result = {}
    for field in records(data):
        try:
            metadata, raw_path = field.split(b"\t", 1)
            mode, kind, oid = metadata.decode("ascii").split(" ")
        except (ValueError, UnicodeError) as exc:
            raise FailClosed("malformed_tree_output") from exc
        path = decode_path(raw_path)
        if (not re.fullmatch(r"[0-7]{6}", mode) or kind not in ("blob", "commit")
                or not OID.fullmatch(oid) or path in result):
            raise FailClosed("malformed_tree_output")
        result[path] = (mode, kind, oid)
    return result


def validate_delta(changes, before, after):
    """Reject incomplete record-boundary truncation and mode/type anomalies."""
    expected = {path for path in before.keys() | after.keys()
                if before.get(path) != after.get(path)}
    observed = {path for change in changes for path in change["paths"]}
    if expected != observed:
        raise FailClosed("incomplete_tree_delta")
    for change in changes:
        status, paths = change["status"], change["paths"]
        if status == "A":
            valid = paths[0] not in before and paths[0] in after
        elif status == "D":
            valid = paths[0] in before and paths[0] not in after
        elif status == "M":
            valid = paths[0] in before and paths[0] in after
        else:  # The parser only permits a two-path rename here.
            valid = (paths[0] in before and paths[0] not in after
                     and paths[1] not in before and paths[1] in after)
        if not valid:
            raise FailClosed("inconsistent_change_status")
        for path in paths:
            for entry in (before.get(path), after.get(path)):
                if entry is not None and entry[:2] != ("100644", "blob"):
                    raise FailClosed("non_regular_or_changed_file_mode")


def path_kind(path):
    parts = path.split("/")
    # The small positive allowlist is intentional. In particular, no .go, TS,
    # TSX, dependency, lock, helper, fixture, build, or workflow file is safe.
    if (path in POLICY_DOCS or parts[-1] in ("AGENTS.md", "SKILL.md")
            or path.endswith(".go") or path.startswith(("internal/", "cmd/", ".github/"))):
        return "full"
    if path in ROOT_DOCS or (path.startswith("docs/") and path.endswith(".md")):
        return "docs"
    if path.startswith("web/src/") and path.endswith(".css"):
        return "css"
    if path.startswith("web/dist/"):
        return "dist"
    return "full"


def classify_delta(changes, before, after):
    validate_delta(changes, before, after)
    kinds = {path_kind(path) for change in changes for path in change["paths"]}
    if "full" in kinds:
        return True, "unproven_or_runtime_path", False
    if "dist" in kinds and "css" not in kinds:
        return True, "generated_assets_without_safe_source_change", False
    if not changes:
        return False, "unchanged_tree_from_verified_full_baseline", False
    frontend = "css" in kinds
    return False, "presentation_only" if frontend else "documentation_only", frontend


def git(root, *args):
    env = os.environ.copy()
    env.update({"GIT_NO_REPLACE_OBJECTS": "1", "GIT_OPTIONAL_LOCKS": "0",
                "GIT_TERMINAL_PROMPT": "0", "LC_ALL": "C"})
    try:
        process = subprocess.run(["git", "--no-pager", *args], cwd=root,
                                 env=env, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL, check=False,
                                 timeout=60)
    except (OSError, subprocess.SubprocessError) as exc:
        raise FailClosed("git_query_failed") from exc
    if process.returncode != 0:
        raise FailClosed("git_query_failed")
    if len(process.stdout) > MAX_GIT_OUTPUT:
        raise FailClosed("git_output_limit")
    return process.stdout


def known_ignored_output(raw):
    directory = raw.endswith(b"/")
    path = decode_path(raw[:-1] if directory else raw)
    if directory:
        path += "/"
    if any(path.startswith(prefix) for prefix in IGNORED_BUILD_DIRECTORIES):
        return True
    # Python creates these while the policy/tests are imported. Go ignores
    # underscore-prefixed directories; the frontend never consumes them.
    if directory and (path == "__pycache__/" or path.endswith("/__pycache__/")):
        return True
    return False


def validate_checkout(root, head):
    index_paths = set()
    for entry in records(git(root, "ls-files", "--cached", "-v", "-z")):
        # Lowercase tags indicate assume-unchanged; S indicates skip-worktree.
        # Neither may hide bytes that differ from the authenticated source tree.
        if not entry.startswith(b"H "):
            raise FailClosed("unsafe_index_flags")
        path = decode_path(entry[2:])
        if path in index_paths:
            raise FailClosed("inconsistent_index_entries")
        index_paths.add(path)
    if records(git(root, "ls-files", "--others", "--exclude-standard", "-z")):
        raise FailClosed("untracked_checkout_paths")
    # Check ignored entries too: only known build outputs may be disregarded,
    # even if a machine-local Git exclude rule hides some other source file.
    for raw in records(git(root, "ls-files", "--others", "--directory",
                           "--no-empty-directory", "-z")):
        if not known_ignored_output(raw):
            raise FailClosed("unrecognized_ignored_checkout_paths")
    try:
        for index in ((), ("--cached",)):
            git(root, "diff", "--no-ext-diff", "--no-textconv", "--quiet",
                "--ignore-submodules=none", *index, head, "--")
    except FailClosed as exc:
        raise FailClosed("dirty_or_unverifiable_checkout") from exc
    return index_paths


def object_id(root, revision):
    raw = git(root, "rev-parse", "--verify", "--end-of-options", revision)
    try:
        value = raw.decode("ascii").rstrip("\n")
    except UnicodeError as exc:
        raise FailClosed("invalid_git_object") from exc
    if not OID.fullmatch(value):
        raise FailClosed("invalid_git_object")
    return value


def empty_report():
    try:
        policy_sha256 = digest(pathlib.Path(__file__).read_bytes())
    except OSError:
        policy_sha256 = None
    return {
        "version": VERSION,
        "policy_id": POLICY_ID,
        "policy_sha256": policy_sha256,
        "long_required": True,
        "reason": "unverified_inputs",
        "baseline_sha": None,
        "baseline_run_id": None,
        "head_sha": None,
        "baseline_tree": None,
        "head_tree": None,
        "paths_sha256": None,
        "changed_paths": [],
        "changes": [],
        "frontend_revalidation_required": False,
    }


def classify(root, *, head=None, baseline=None, baseline_run_id=None, force_full=False):
    report = empty_report()
    try:
        if report["policy_sha256"] is None:
            raise FailClosed("policy_unavailable")
        if not head or not OID.fullmatch(head):
            raise FailClosed("missing_or_invalid_head")
        report["head_sha"] = object_id(root, head + "^{commit}")
        if report["head_sha"] != head:
            raise FailClosed("head_commit_mismatch")
        report["head_tree"] = object_id(root, head + "^{tree}")
        if force_full:
            raise FailClosed("forced_full")
        if object_id(root, "HEAD^{commit}") != head:
            raise FailClosed("head_checkout_mismatch")
        index_paths = validate_checkout(root, head)
        if (not baseline or not OID.fullmatch(baseline)
                or not isinstance(baseline_run_id, str)
                or not re.fullmatch(r"[1-9][0-9]*", baseline_run_id)):
            raise FailClosed("missing_or_invalid_verified_baseline")
        report["baseline_sha"] = object_id(root, baseline + "^{commit}")
        report["baseline_run_id"] = int(baseline_run_id)
        if report["baseline_sha"] != baseline:
            raise FailClosed("baseline_commit_mismatch")
        report["baseline_tree"] = object_id(root, baseline + "^{tree}")
        if git(root, "rev-parse", "--is-shallow-repository") != b"false\n":
            raise FailClosed("incomplete_repository_history")
        try:
            git(root, "merge-base", "--is-ancestor", baseline, head)
        except FailClosed as exc:
            raise FailClosed("baseline_not_proven_ancestor") from exc
        before = parse_tree(git(root, "ls-tree", "-r", "-z", "--full-tree", baseline))
        after = parse_tree(git(root, "ls-tree", "-r", "-z", "--full-tree", head))
        if index_paths != after.keys():
            raise FailClosed("incomplete_checkout_index")
        changes = parse_name_status(git(
            root, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z",
            "--find-renames=50%", "--ignore-submodules=none", baseline, head, "--"))
        # Preserve a reviewable complete delta only after independent validation.
        validate_delta(changes, before, after)
        paths = sorted({path for change in changes for path in change["paths"]})
        report.update({"changes": changes, "changed_paths": paths,
                       "paths_sha256": digest(canonical(paths))})
        long_required, reason, frontend = classify_delta(changes, before, after)
        report.update({"long_required": long_required, "reason": reason,
                       "frontend_revalidation_required": frontend})
    except FailClosed as exc:
        report["reason"] = str(exc)
    except Exception:
        # Unexpected parse/IO/runtime failures must never become a skip decision.
        report["long_required"] = True
        report["reason"] = "classification_failed"
    return report


def parse_args(argv):
    parser = ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--head", help="immutable head commit SHA")
    parser.add_argument("--verified-baseline-commit", dest="baseline")
    parser.add_argument("--verified-baseline-run-id", dest="baseline_run_id")
    parser.add_argument("--force-full", action="store_true")
    parser.add_argument("--output", type=pathlib.Path, help="also write the JSON report here")
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    options = [argument.split("=", 1)[0] for argument in argv if argument.startswith("--")]
    if len(options) != len(set(options)):
        raise FailClosed("conflicting_inputs")
    return parser.parse_args(argv)


def main(argv=None):
    args = None
    try:
        args = parse_args(sys.argv[1:] if argv is None else argv)
        report = classify(args.repo, head=args.head, baseline=args.baseline,
                          baseline_run_id=args.baseline_run_id, force_full=args.force_full)
    except FailClosed as exc:
        report = empty_report()
        report["reason"] = str(exc)
    output = json.dumps(report, sort_keys=True, indent=2) + "\n"
    if args is not None and args.output is not None:
        try:
            args.output.write_text(output, encoding="utf-8")
        except OSError:
            report["long_required"] = True
            report["reason"] = "report_write_failed"
            print(json.dumps(report, sort_keys=True, indent=2))
            return 1
    print(output, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
