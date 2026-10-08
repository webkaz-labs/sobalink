#!/usr/bin/env python3
"""Fail-closed minimum CI plan for the exact GitHub event and checked-out tree.

Pull requests compare the tested merge commit with its verified base parent;
main pushes compare the event's complete before/after range. No prior run or
coverage receipt is required. Unknown inputs or paths always request full CI.
"""

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys

VERSION = 3
POLICY_ID = "minimum-ci-v3"
REPOSITORY = "webkaz-labs/sobalink"
GO_IMPORTS = {
    "internal/servicepresets": frozenset(("testing",)),
    "internal/boundedlog": frozenset(("bytes", "errors", "fmt", "os", "path/filepath", "sync", "testing")),
}
GO_PACKAGES = frozenset(GO_IMPORTS)
# The presentation boundary is intentionally file-level. These paths do
# not implement transport/lease/rekey policy. New paths and imports stay full;
# native-short still runs all four native short/safety and package gates.
NATIVE_SHORT_IMPORTS = {
    "cmd/soba/help.go": frozenset(("encoding/json",)),
    "cmd/soba/errors.go": frozenset(("context", "encoding/json", "errors", "fmt", "io")),
    "cmd/soba/errors_test.go": frozenset(("bytes", "context", "encoding/json", "errors", "strings", "testing",
                                         "github.com/webkaz-labs/sobalink/internal/control")),
    "cmd/soba/human_output.go": frozenset(("context", "encoding/json", "fmt", "io", "net", "strconv", "strings",
                                          "github.com/webkaz-labs/sobalink/internal/control",
                                          "github.com/webkaz-labs/sobalink/internal/core")),
    "cmd/soba/human_output_test.go": frozenset(("bytes", "context", "encoding/json", "errors", "strings", "testing",
                                               "github.com/webkaz-labs/sobalink/internal/core")),
}

GO_PLATFORMS = frozenset("""
    aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd
    openbsd plan9 solaris wasip1 windows zos 386 amd64 amd64p32 arm arm64 arm64be
    armbe loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64
    ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm
""".split())
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
    if path in POLICY_DOCS or parts[-1] in ("AGENTS.md", "SKILL.md"):
        return "full"
    if path in ROOT_DOCS or (len(parts) == 2 and parts[0] == "docs"
                            and parts[1].endswith(".md")):
        return "docs"
    if path.startswith("web/src/") and path.endswith((".ts", ".tsx", ".css")):
        return "frontend"
    if len(parts) == 3 and parts[:2] == ["web", "browser"] and path.endswith(".mjs"):
        return "browser"
    # Match the reviewed Vite output layout, not arbitrary embedded Go/assets.
    if path == "web/dist/index.html" or (len(parts) == 4
            and parts[:3] == ["web", "dist", "assets"]
            and path.endswith((".js", ".css"))):
        return "dist"
    if path in NATIVE_SHORT_IMPORTS:
        return "native-short"
    package, _, filename = path.rpartition("/")
    if package in GO_PACKAGES and filename.endswith(".go"):
        stem = filename[:-3].removesuffix("_test")
        if any(part in GO_PLATFORMS for part in stem.split("_")[1:]):
            return "full"
        return "go"
    return "full"


def classify_delta(changes, before, after):
    validate_delta(changes, before, after)
    if any(after.get(path, ())[:2] != ("100644", "blob") for path in ROOT_DOCS):
        raise FailClosed("required_document_missing_or_non_regular")
    paths = {path for change in changes for path in change["paths"]}
    kinds = {path_kind(path) for path in paths}
    if "full" in kinds:
        return "full", "unproven_or_shared_runtime_path", []
    if "dist" in kinds and "frontend" not in kinds:
        return "full", "generated_assets_without_source_change", []
    # Join only already-reviewed domains. Keep changed Go packages even when
    # the joined scope runs native jobs: validate_contents must still inspect
    # both complete package inventories/import sets, not only the changed file.
    packages = sorted({"./" + path.rpartition("/")[0]
                       for path in paths if path_kind(path) == "go"})
    frontend = bool(kinds.intersection(("frontend", "browser")))
    if "native-short" in kinds or ("go" in kinds and frontend):
        reason = "reviewed_safe_scope_union" if frontend or packages else "reviewed_presentation"
        return "native-short", reason, packages
    if "go" in kinds:
        return "go", "scoped_go_packages", packages
    if kinds.intersection(("frontend", "browser")):
        return "frontend", "frontend_only", []
    return "docs", "documentation_only" if changes else "unchanged_tree", []


def validate_contents(root, changes, before, after, packages):
    # Inspect both old and new blobs, including deleted/renamed prose. Inspect
    # whole eligible Go packages so an unchanged platform file cannot hide.
    paths = {path for change in changes for path in change["paths"]
             if path_kind(path) in ("docs", "frontend", "browser", "dist", "native-short")} | ROOT_DOCS
    for tree in (before, after):
        for package in packages:
            prefix = package.removeprefix("./") + "/"
            for path in tree:
                if path.startswith(prefix):
                    if path_kind(path) != "go":
                        raise FailClosed("unsupported_go_package_file")
                    paths.add(path)
    seen = set()
    for path in sorted(paths):
        for tree in (before, after):
            entry = tree.get(path)
            if entry is None or (path, entry) in seen:
                continue
            seen.add((path, entry))
            if entry[:2] != ("100644", "blob"):
                raise FailClosed("non_regular_or_changed_file_mode")
            raw = git(root, "cat-file", "blob", entry[2])
            try:
                content = raw.decode("utf-8", errors="strict")
            except UnicodeError as exc:
                raise FailClosed("non_text_content") from exc
            if any(ord(char) < 32 and char not in "\t\r\n" or ord(char) == 127
                   for char in content):
                raise FailClosed("non_text_content")
            if path_kind(path) in ("go", "native-short"):
                # Directives can introduce platform, embed or link dependencies.
                # Reject C/unsafe imports conservatively, even inside comments.
                if re.search(r"//\s*(?:go:|\+build\b)|[\"`]C[\"`]|[\"`]unsafe[\"`]",
                             content):
                    raise FailClosed("platform_or_special_go_source")
                validate_go_imports(path, content)


def validate_go_imports(path, content):
    # Tokenize just enough Go to recognize import declarations without mistaking
    # text in comments or strings for imports. Any new dependency expands scope.
    tokens = re.findall(r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\])*"|`[^`]*`|[A-Za-z_][A-Za-z_0-9]*|[^\s]',
                        content, flags=re.DOTALL)
    tokens = [token for token in tokens if not token.startswith(("//", "/*"))]
    allowed = NATIVE_SHORT_IMPORTS.get(path)
    if allowed is None:
        allowed = GO_IMPORTS[path.rpartition("/")[0]]
    for index, token in enumerate(tokens):
        if token != "import":
            continue
        index += 1
        grouped = index < len(tokens) and tokens[index] == "("
        if grouped:
            index += 1
        while index < len(tokens):
            if grouped and tokens[index] == ")":
                break
            if tokens[index] == ";":
                index += 1
                continue
            if tokens[index][0] not in ('"', '`'):
                index += 1  # An optional package name, dot, or blank alias.
            if index >= len(tokens) or tokens[index][0] not in ('"', '`'):
                raise FailClosed("unrecognized_go_import")
            imported = tokens[index][1:-1]
            if imported not in allowed:
                raise FailClosed("unreviewed_go_dependency")
            index += 1
            if not grouped:
                break
        else:
            raise FailClosed("unrecognized_go_import")



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
        "scope": "full",
        "reason": "unverified_inputs",
        "head_sha": None,
        "head_tree": None,
        "base_sha": None,
        "changed_paths": [],
        "go_packages": [],
    }


def event_base(root, head, environment):
    if environment.get("GITHUB_SHA") != head:
        raise FailClosed("event_head_mismatch")
    if environment.get("GITHUB_REPOSITORY") != REPOSITORY:
        raise FailClosed("event_repository_mismatch")
    event_name = environment.get("GITHUB_EVENT_NAME")
    if event_name not in ("push", "pull_request"):
        raise FailClosed("unsupported_event")
    try:
        raw = pathlib.Path(environment["GITHUB_EVENT_PATH"]).read_bytes()
        if len(raw) > MAX_GIT_OUTPUT:
            raise FailClosed("event_output_limit")
        event = json.loads(raw)
        if event["repository"]["full_name"] != REPOSITORY:
            raise FailClosed("event_repository_mismatch")
        if event_name == "push":
            if (event["ref"] != "refs/heads/main"
                    or environment.get("GITHUB_REF", event["ref"]) != event["ref"]):
                raise FailClosed("event_branch_mismatch")
            if event["after"] != head:
                raise FailClosed("event_head_mismatch")
            if event.get("deleted") or event.get("created") or event.get("forced"):
                raise FailClosed("unsupported_push")
            base = event["before"]
        else:
            pr = event["pull_request"]
            if pr["base"]["ref"] != "main":
                raise FailClosed("event_branch_mismatch")
            if pr["base"]["repo"]["full_name"] != REPOSITORY:
                raise FailClosed("event_repository_mismatch")
            # Fork PRs are valid: their head repository need not be the target.
            # When supplied, require a well-formed head repository identity.
            head_repo = pr["head"].get("repo")
            if head_repo is not None and not re.fullmatch(
                    r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", head_repo["full_name"]):
                raise FailClosed("event_repository_mismatch")
            number = event["number"]
            if type(number) is not int or number <= 0:
                raise FailClosed("invalid_event")
            ref = environment.get("GITHUB_REF")
            if ref != "refs/pull/" + str(number) + "/merge":
                raise FailClosed("event_branch_mismatch")
            base = pr["base"]["sha"]
            source = pr["head"]["sha"]
            if not isinstance(source, str) or not OID.fullmatch(source):
                raise FailClosed("invalid_event")
            parents = git(root, "rev-list", "--parents", "-n", "1", head).decode("ascii").split()
            if parents != [head, base, source] or base == source:
                raise FailClosed("unverified_pr_merge")
            # Actions GITHUB_SHA and the exact two parents identify the tested
            # merge. The PR API's mergeability-derived hint can lag that event.
            # A well-formed stale hint grants nothing; malformed metadata still
            # fails closed. Never replace the tested SHA or its parents with it.
            hint = pr.get("merge_commit_sha")
            if hint is not None and (not isinstance(hint, str) or not OID.fullmatch(hint)
                                     or set(hint) == {"0"}):
                raise FailClosed("invalid_event_merge_hint")
        if not isinstance(base, str) or not OID.fullmatch(base) or set(base) == {"0"}:
            raise FailClosed("missing_or_invalid_event_base")
    except (KeyError, TypeError, ValueError, OSError, UnicodeError) as exc:
        raise FailClosed("invalid_event") from exc
    if object_id(root, base + "^{commit}") != base:
        raise FailClosed("event_base_mismatch")
    try:
        git(root, "merge-base", "--is-ancestor", base, head)
    except FailClosed as exc:
        raise FailClosed("event_base_not_ancestor") from exc
    return base


def classify(root, *, head=None, force_full=False, environment=None):
    report = empty_report()
    try:
        if report["policy_sha256"] is None:
            raise FailClosed("policy_unavailable")
        if not isinstance(head, str) or not OID.fullmatch(head):
            raise FailClosed("missing_or_invalid_head")
        report["head_sha"] = object_id(root, head + "^{commit}")
        if report["head_sha"] != head:
            raise FailClosed("head_commit_mismatch")
        report["head_tree"] = object_id(root, head + "^{tree}")
        event_environment = os.environ if environment is None else environment
        if event_environment.get("GITHUB_EVENT_NAME") == "schedule":
            # Periodic coverage is full even with no changed paths or inputs.
            raise FailClosed("scheduled_full")
        if force_full:
            raise FailClosed("forced_full")
        if object_id(root, "HEAD^{commit}") != head:
            raise FailClosed("head_checkout_mismatch")
        if git(root, "rev-parse", "--is-shallow-repository") != b"false\n":
            raise FailClosed("incomplete_repository_history")
        base = event_base(root, head, os.environ if environment is None else environment)
        report["base_sha"] = base
        index_paths = validate_checkout(root, head)
        before = parse_tree(git(root, "ls-tree", "-r", "-z", "--full-tree", base))
        after = parse_tree(git(root, "ls-tree", "-r", "-z", "--full-tree", head))
        if index_paths != after.keys():
            raise FailClosed("incomplete_checkout_index")
        changes = parse_name_status(git(
            root, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z",
            "--find-renames=50%", "--ignore-submodules=none", base, head, "--"))
        validate_delta(changes, before, after)
        report["changed_paths"] = sorted({path for change in changes for path in change["paths"]})
        scope, reason, packages = classify_delta(changes, before, after)
        # event_base already proves the exact PR merge or complete main-push
        # range. Both may use native-short; dispatch/unknown events remain full.
        if scope != "full":
            validate_contents(root, changes, before, after, packages)
        report.update({"scope": scope, "reason": reason, "go_packages": packages})
    except FailClosed as exc:
        report["reason"] = str(exc)
    except Exception:
        # Unexpected parse/IO/runtime failures must never become a skip decision.
        report["scope"] = "full"
        report["reason"] = "classification_failed"
    return report


def parse_args(argv):
    parser = ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--head", help="immutable tested commit SHA")
    parser.add_argument("--force-full", action="store_true")
    parser.add_argument("--output", type=pathlib.Path, help="also write the JSON plan here")
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    options = [argument.split("=", 1)[0] for argument in argv if argument.startswith("--")]
    if len(options) != len(set(options)):
        raise FailClosed("conflicting_inputs")
    return parser.parse_args(argv)


def main(argv=None):
    args = None
    try:
        args = parse_args(sys.argv[1:] if argv is None else argv)
        report = classify(args.repo, head=args.head, force_full=args.force_full)
    except FailClosed as exc:
        report = empty_report()
        report["reason"] = str(exc)
    output = json.dumps(report, sort_keys=True, indent=2) + "\n"
    if args is not None and args.output is not None:
        try:
            args.output.write_text(output, encoding="utf-8")
        except OSError:
            report["scope"] = "full"
            report["go_packages"] = []
            report["reason"] = "report_write_failed"
            print(json.dumps(report, sort_keys=True, indent=2))
            return 1
    print(output, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
