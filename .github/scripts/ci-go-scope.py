#!/usr/bin/env python3
"""Run standard Linux race tests and vet for a verified, narrow Go change.

The workflow prepares the exact toolchain and dependencies first. The plan is
an input to package selection, never test-result reuse. Graph uncertainty runs
all standard packages; an invalid plan or unsuccessful command fails the job.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys


MODULE = "github.com/webkaz-labs/sobalink"
ALLOWED_PACKAGES = frozenset(("./internal/servicepresets", "./internal/boundedlog"))
# (race, extra tags), matching standard vet/tests and native/browser variants.
# These widen dependency discovery only, never enable long tests in this job.
GRAPH_VARIANTS = ((False, ()), (True, ()),
                  (True, ("directlan_integration", "directlan_lifecycle")),
                  (True, ("lanlink_integration",)),
                  (True, ("lanlink_integration", "ts_omit_udptransport")),
                  (True, ("soba_e2e",)))
OID = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
IMPORT = re.compile(r"[A-Za-z0-9_.~+/-]+\Z")


class InvalidInput(ValueError):
    """A fixed explanation, without source contents or environment values."""


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "error: Invalid scoped Go arguments; use --help.\n")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise InvalidInput("Duplicate JSON field.")
        result[key] = value
    return result


def invalid_constant(value):
    raise InvalidInput("Invalid JSON number.")


DECODER = json.JSONDecoder(object_pairs_hook=unique_object,
                           parse_constant=invalid_constant)


def read_plan(path):
    try:
        plan = DECODER.decode(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError, UnicodeError, RecursionError) as exc:
        raise InvalidInput("Cannot read a valid scoped Go plan.") from exc
    if (not isinstance(plan, dict) or type(plan.get("version")) is not int
            or plan["version"] != 2 or plan.get("policy_id") != "minimum-ci-v2"
            or plan.get("scope") != "go"):
        raise InvalidInput("Expected a current Go-scope plan.")
    packages = plan.get("go_packages")
    if (not isinstance(packages, list) or not packages
            or any(not isinstance(p, str) or p not in ALLOWED_PACKAGES for p in packages)
            or len(packages) != len(set(packages))):
        raise InvalidInput("Plan does not name allowed changed packages.")
    if any(not isinstance(plan.get(key), str) or not OID.fullmatch(plan[key])
           for key in ("head_sha", "head_tree")):
        raise InvalidInput("Plan does not identify the tested source tree.")
    return plan


def query(command, env):
    try:
        process = subprocess.run(command, env=env, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL, check=False, shell=False)
    except OSError as exc:
        raise InvalidInput("Source or package query could not run.") from exc
    if process.returncode:
        raise InvalidInput("Source or package query failed.")
    try:
        return process.stdout.decode("utf-8")
    except UnicodeError as exc:
        raise InvalidInput("Source or package query returned invalid text.") from exc


def verify_head(plan, env):
    git_env = dict(env, GIT_NO_REPLACE_OBJECTS="1", GIT_OPTIONAL_LOCKS="0",
                   GIT_TERMINAL_PROMPT="0")
    for key, revision in (("head_sha", "HEAD"), ("head_tree", "HEAD^{tree}")):
        if query(["git", "rev-parse", "--verify", revision], git_env).strip() != plan[key]:
            raise InvalidInput("Plan does not match the current checkout.")
    query(["git", "diff", "--no-ext-diff", "--no-textconv", "--quiet", "HEAD", "--"],
          git_env)


def import_path(value):
    if (not isinstance(value, str) or not IMPORT.fullmatch(value)
            or any(part in ("", ".", "..") for part in value.split("/"))):
        raise InvalidInput("Malformed Go import path.")
    return value


def parse_graph(raw):
    """Read concatenated go-list objects, including internal/external test imports."""
    graph = {}
    offset = 0
    try:
        while offset < len(raw):
            if raw[offset].isspace():
                offset += 1
                continue
            item, offset = DECODER.raw_decode(raw, offset)
            if (not isinstance(item, dict) or item.get("Error") or item.get("DepsErrors")
                    or item.get("Incomplete") or item.get("ForTest")):
                raise InvalidInput("Incomplete Go package graph.")
            module = item.get("Module")
            if (not isinstance(module, dict) or module.get("Path") != MODULE
                    or module.get("Main") is not True):
                raise InvalidInput("Unexpected module in Go package graph.")
            package = import_path(item.get("ImportPath"))
            if not (package == MODULE or package.startswith(MODULE + "/")) or package in graph:
                raise InvalidInput("Unexpected or duplicate Go package.")
            imports = set()
            for field in ("Imports", "TestImports", "XTestImports"):
                values = item.get(field, [])
                if not isinstance(values, list):
                    raise InvalidInput("Malformed Go imports.")
                imports.update(import_path(value) for value in values)
            graph[package] = {value for value in imports
                              if value == MODULE or value.startswith(MODULE + "/")}
    except (ValueError, RecursionError) as exc:
        raise InvalidInput("Malformed Go package graph.") from exc
    if not graph or any(value not in graph for imports in graph.values() for value in imports):
        raise InvalidInput("Empty or incomplete Go package graph.")
    return graph


def discover_graph(env):
    # Keep the workflow's standard tags when requesting each extra variant.
    tags = []
    try:
        for flag in shlex.split(env.get("GOFLAGS", "")):
            if flag.startswith("-tags="):
                tags = flag[len("-tags="):].replace(",", " ").split()
    except ValueError as exc:
        raise InvalidInput("Cannot interpret standard Go flags.") from exc
    graph, standard = {}, set()
    for race, extra in GRAPH_VARIANTS:
        command = ["go", "list", "-mod=readonly", "-json"]
        if race:
            command.append("-race")
        if extra:
            command.append("-tags=" + ",".join(tags + list(extra)))
        current = parse_graph(query(command + ["./..."], env))
        if not extra:
            standard.update(current)
        for package, imports in current.items():
            graph.setdefault(package, set()).update(imports)
    return graph, standard


def affected_packages(graph, standard, changed):
    affected = {MODULE + package[1:] for package in changed}
    if not affected or not affected <= standard or not standard <= graph.keys():
        raise InvalidInput("Changed package is missing from the standard Go graph.")
    reverse = {package: set() for package in graph}
    for package, imports in graph.items():
        for imported in imports:
            if imported not in reverse:
                raise InvalidInput("Incomplete reverse Go dependency graph.")
            reverse[imported].add(package)
    pending = list(affected)
    while pending:
        for importer in reverse[pending.pop()] - affected:
            affected.add(importer)
            pending.append(importer)
    # Tag-only packages contribute edges but cannot run under standard flags.
    selected = sorted(affected & standard)
    if not selected:
        raise InvalidInput("No standard Go packages were selected.")
    return ["." if package == MODULE else "." + package[len(MODULE):]
            for package in selected]


def exit_status(code):
    return code if code >= 0 else 128 - code


def race_tests(packages, changed, env):
    """Stream normal test output and reject a zero-test success for changed packages."""
    expected = {MODULE + package[1:] for package in changed}
    tests_passed, packages_passed = set(), set()
    invalid = False
    process = subprocess.Popen(
        ["go", "test", "-json", "-mod=readonly", "-race", "-count=1", "-timeout=10m", *packages],
        env=env, stdout=subprocess.PIPE, stderr=None, shell=False)
    try:
        for line in process.stdout:
            try:
                event = DECODER.decode(line.decode("utf-8"))
                if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                    raise InvalidInput("Malformed test event.")
                output = event.get("Output", "")
                if not isinstance(output, str):
                    raise InvalidInput("Malformed test output.")
                if output:
                    sys.stdout.write(output)
                    sys.stdout.flush()
                action, package = event["Action"], event.get("Package")
                if action in ("fail", "build-fail"):
                    invalid = True
                if package in expected:
                    if action == "skip":
                        invalid = True
                    elif action == "pass":
                        test = event.get("Test")
                        if isinstance(test, str) and test:
                            tests_passed.add(package)
                        elif test is None:
                            packages_passed.add(package)
            except (ValueError, UnicodeError, RecursionError, TypeError):
                invalid = True
    finally:
        process.stdout.close()
        code = process.wait()
    if code:
        return exit_status(code)
    if invalid or tests_passed != expected or packages_passed != expected:
        print("error: Changed Go packages did not complete real tests successfully.", file=sys.stderr)
        return 1
    return 0


def main(argv=None):
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("--plan", required=True, help="current workflow's verified Go-scope JSON plan")
    args = parser.parse_args(argv)
    env = dict(os.environ, GOWORK="off")
    try:
        plan = read_plan(args.plan)
        verify_head(plan, env)
    except InvalidInput as exc:
        print("error: " + str(exc), file=sys.stderr)
        return 2
    try:
        graph, standard = discover_graph(env)
        packages = affected_packages(graph, standard, plan["go_packages"])
        print(f"Scoped Go coverage: {len(packages)} standard packages, including reverse test imports.",
              flush=True)
    except InvalidInput:
        print("warning: Go graph selection was uncertain; running all standard Go packages.",
              file=sys.stderr, flush=True)
        packages = ["./..."]
    try:
        result = race_tests(packages, plan["go_packages"], env)
        if result:
            return result
        return exit_status(subprocess.run(["go", "vet", "-mod=readonly", *packages],
                                          env=env, check=False, shell=False).returncode)
    except OSError:
        print("error: Go test or vet could not run.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
