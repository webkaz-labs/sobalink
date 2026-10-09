#!/usr/bin/env python3
"""Run Go tests and require exact package:test pass events.

Usage: ci-go-test.py --expect example.org/project/pkg:TestRequired -- go test ...
Repeat --expect for additional tests, including exact TestName/subtest names.
Each required test and its package must report a pass; a skip never counts.
Optional --exact requires only the expected tests, each run/passed once, and
only their packages, each started/passed once, with no skips anywhere.
Only Go's Output fields are forwarded to stdout; stderr is inherited. No
command arguments, environment values, paths, or raw JSON are recorded.
"""

import argparse
import json
import math
from pathlib import Path
import subprocess
import sys


TEST_ACTIONS = {"start", "run", "pause", "cont", "pass", "bench", "fail",
                "output", "skip", "attr", "artifacts"}
BUILD_ACTIONS = {"build-output", "build-fail"}


class EventError(ValueError):
    """Invalid event errors deliberately omit source contents."""


class SafeArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        # argparse's normal error can disclose arbitrary argv values.
        self.exit(2, "error: Invalid Go test wrapper arguments; use --help.\n")


def expectation(value):
    package, separator, test = value.partition(":")
    if not separator or not package or not test or any(c.isspace() or ord(c) < 32
                                                     or ord(c) == 127 for c in value):
        raise ValueError("Expected an exact package:test identifier.")
    return package, test


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise EventError("Duplicate event field.")
        result[key] = value
    return result


def invalid_constant(value):
    raise EventError("Invalid JSON number.")


def parse_event(line):
    event = json.loads(line.decode("utf-8"), object_pairs_hook=unique_object,
                       parse_constant=invalid_constant)
    if not isinstance(event, dict):
        raise EventError("Invalid event object.")
    action = event.get("Action")
    if not isinstance(action, str) or action not in TEST_ACTIONS | BUILD_ACTIONS:
        raise EventError("Invalid event action.")
    package_key = "ImportPath" if action in BUILD_ACTIONS else "Package"
    if not isinstance(event.get(package_key), str) or not event[package_key]:
        raise EventError("Invalid event package.")
    if "Test" in event and (not isinstance(event["Test"], str) or not event["Test"]):
        raise EventError("Invalid event test.")
    if "Output" in event and not isinstance(event["Output"], str):
        raise EventError("Invalid event output.")
    if action in {"output", "build-output"} and "Output" not in event:
        raise EventError("Missing event output.")
    if "Elapsed" in event:
        elapsed = event["Elapsed"]
        try:
            valid = type(elapsed) in (int, float) and elapsed >= 0 and math.isfinite(elapsed)
        except OverflowError:
            valid = False
        if not valid:
            raise EventError("Invalid event elapsed time.")
    return event


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def run_tests(expected, command, *, exact=False):
    if not expected or len(command) < 2 or Path(command[0]).name not in {"go", "go.exe"} \
            or command[1] != "test":
        print("error: Expected a go test command and at least one required test.", file=sys.stderr)
        return 2
    # A later -json=false can disable this flag, but cannot produce a green
    # result: every output line and every required pass is still checked.
    argv = command[:2] + ["-json"] + command[2:]
    expectations = list(expected)
    expected = set(expectations)
    if exact and len(expected) != len(expectations):
        print("error: Exact Go test expectations must be unique.", file=sys.stderr)
        return 2
    expected_packages = {package for package, _ in expected}
    passed = set()
    rejected = set()
    passed_packages = set()
    rejected_packages = set()
    malformed = False
    failed = False
    exact_invalid = False
    ran, started_packages = set(), set()
    process = None
    try:
        process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=None, shell=False)
        try:
            for line in process.stdout:
                try:
                    event = parse_event(line)
                    if "Output" in event:
                        sys.stdout.write(event["Output"])
                        sys.stdout.flush()
                except (ValueError, UnicodeError, RecursionError):
                    # Keep draining the pipe so invalid output cannot deadlock
                    # the child or hide a later nonzero process result.
                    malformed = True
                    continue
                key = event.get("Package"), event.get("Test")
                action = event["Action"]
                if exact:
                    package = event.get("Package", event.get("ImportPath"))
                    # Build dependencies may emit build metadata, but must not
                    # introduce another executed test package or test/subtest.
                    if action not in BUILD_ACTIONS and (package not in expected_packages
                                                        or (key[1] is not None and key not in expected)):
                        exact_invalid = True
                    if action == "skip":
                        exact_invalid = True
                    if key in expected:
                        if action == "run":
                            exact_invalid |= (key in ran or key in passed or key[0] not in started_packages
                                              or key[0] in passed_packages)
                            ran.add(key)
                        elif action == "pass":
                            exact_invalid |= key not in ran or key in passed
                        elif action == "start":
                            exact_invalid = True
                    elif key[0] in expected_packages and key[1] is None:
                        if action == "start":
                            exact_invalid |= key[0] in started_packages or key[0] in passed_packages
                            started_packages.add(key[0])
                        elif action == "pass":
                            exact_invalid |= key[0] not in started_packages or key[0] in passed_packages
                            exact_invalid |= any(test not in passed for test in expected if test[0] == key[0])
                if key in expected:
                    if action == "pass":
                        passed.add(key)
                    elif action in {"skip", "fail"}:
                        rejected.add(key)
                if key[0] in expected_packages and key[1] is None:
                    if action == "pass":
                        passed_packages.add(key[0])
                    elif action in {"skip", "fail"}:
                        rejected_packages.add(key[0])
                if action in {"fail", "build-fail"}:
                    failed = True
        finally:
            process.stdout.close()
        returncode = process.wait()
    except FileNotFoundError:
        print("error: Go test executable was not found.", file=sys.stderr)
        return 127
    except OSError:
        if process is not None:
            stop_process(process)
        print("error: Go test process could not be run.", file=sys.stderr)
        return 126
    except KeyboardInterrupt:
        if process is not None:
            stop_process(process)
        return 130
    if malformed:
        print("error: Go test JSON output was malformed.", file=sys.stderr)
    incomplete = passed != expected or passed_packages != expected_packages
    if exact and (exact_invalid or ran != expected or started_packages != expected_packages):
        incomplete = True
    if failed or rejected or rejected_packages or incomplete:
        print("error: Required Go tests did not all pass without skips or failures.", file=sys.stderr)
    if returncode != 0:
        return returncode if returncode > 0 else 128 - returncode
    return 1 if malformed or failed or rejected or rejected_packages or incomplete else 0


def main(argv=None):
    parser = SafeArgumentParser(description=__doc__)
    parser.add_argument("--expect", action="append", type=expectation, required=True,
                        help="exact PACKAGE:TEST identifier; repeat for multiple tests")
    parser.add_argument("--exact", action="store_true",
                        help="require one run/pass per expected test, no extras or skips")
    parser.add_argument("command", nargs=argparse.REMAINDER,
                        help="-- go test [arguments]")
    args = parser.parse_args(argv)
    if not args.command or args.command[0] != "--":
        parser.error("Missing command separator.")
    if args.exact:
        return run_tests(args.expect, args.command[1:], exact=True)
    return run_tests(args.expect, args.command[1:])


if __name__ == "__main__":
    sys.exit(main())
