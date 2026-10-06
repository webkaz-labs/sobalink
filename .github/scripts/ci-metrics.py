#!/usr/bin/env python3
"""Record content-free CI suite timings without changing command results.

Use fixed, non-sensitive suite identifiers. Command arguments, environment
values, paths, and captured output are never included in the metric records.
Choose a fresh SOBALINK_CI_METRICS file for each job; calls within a job append.
"""

import argparse
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys
import time


SUITE_PATTERN = re.compile(r"[A-Za-z][A-Za-z0-9_.-]{0,79}\Z")
RECORD_KEYS = {"suite", "elapsed_seconds", "status", "returncode"}


class MetricsError(Exception):
    """Errors contain fixed messages, never input contents or paths."""


def metrics_path():
    configured = os.environ.get("SOBALINK_CI_METRICS")
    if configured:
        return Path(configured)
    return Path(os.environ.get("RUNNER_TEMP") or ".build") / "sobalink-ci-metrics.jsonl"


def valid_suite(value):
    return isinstance(value, str) and SUITE_PATTERN.fullmatch(value) is not None


def warn(message):
    # Messages are constant so they cannot introduce workflow commands or leak
    # exception details, command arguments, or filesystem paths into CI output.
    prefix = "::warning::" if os.environ.get("GITHUB_ACTIONS") == "true" else "warning: "
    print(prefix + message, file=sys.stderr)


def append_record(record):
    path = metrics_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as output:
        output.write(json.dumps(record, allow_nan=False, separators=(",", ":")) + "\n")


def run_suite(suite, command):
    start = time.monotonic()
    try:
        # Inherit stdin/stdout/stderr. Do not capture logs or reinterpret argv.
        returncode = subprocess.run(command, check=False, shell=False).returncode
    except FileNotFoundError:
        warn("CI suite command was not found.")
        returncode = 127
    except OSError:
        warn("CI suite command could not be started.")
        returncode = 126
    except KeyboardInterrupt:
        returncode = 130
    elapsed = time.monotonic() - start
    record = {"suite": suite, "elapsed_seconds": round(elapsed, 6),
              "status": "passed" if returncode == 0 else "failed", "returncode": returncode}
    try:
        append_record(record)
    except (OSError, ValueError, UnicodeError):
        warn("CI timing record could not be written; the command result is unchanged.")
    # subprocess uses negative signal numbers on POSIX. Expose the conventional
    # shell status while retaining the original subprocess value in the record.
    return returncode if returncode >= 0 else 128 - returncode


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise MetricsError("Duplicate fields in CI timing data.")
        result[key] = value
    return result


def validate_record(record):
    if not isinstance(record, dict) or set(record) != RECORD_KEYS:
        raise MetricsError("Invalid fields in CI timing data.")
    if not valid_suite(record["suite"]):
        raise MetricsError("Invalid suite identifier in CI timing data.")
    elapsed = record["elapsed_seconds"]
    try:
        valid_elapsed = type(elapsed) in (int, float) and elapsed >= 0 and math.isfinite(elapsed)
    except OverflowError:
        valid_elapsed = False
    if not valid_elapsed:
        raise MetricsError("Invalid elapsed time in CI timing data.")
    code = record["returncode"]
    if type(code) is not int or record["status"] != ("passed" if code == 0 else "failed"):
        raise MetricsError("Invalid command result in CI timing data.")
    return record


def read_records():
    try:
        source = metrics_path().open(encoding="utf-8")
    except FileNotFoundError:
        return []
    with source:
        return [validate_record(json.loads(line, object_pairs_hook=unique_object)) for line in source]


def summary(output_path=None):
    # Validate the complete source before writing either output. No raw source
    # content or exception details are included in an error or summary.
    try:
        records = read_records()
    except (OSError, ValueError, UnicodeError, RecursionError, MetricsError):
        print("error: CI timing data could not be read or is malformed.", file=sys.stderr)
        return 1
    lines = ["### CI suite timings", ""]
    if records:
        lines.extend(["| Suite | Elapsed (seconds) | Result | Return code |",
                      "| --- | ---: | --- | ---: |"])
        for record in records:
            lines.append(f"| {record['suite']} | {record['elapsed_seconds']:.3f} | "
                         f"{record['status']} | {record['returncode']} |")
        lines.extend(["", "Elapsed time covers each recorded command, not the whole job."])
    else:
        lines.append("No suite timings were recorded.")
    rendered = "\n".join(lines) + "\n"
    try:
        if output_path:
            destination = Path(output_path)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(json.dumps(records, indent=2, allow_nan=False) + "\n", encoding="utf-8")
        step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if step_summary:
            with Path(step_summary).open("a", encoding="utf-8") as output:
                output.write(rendered + "\n")
        else:
            print(rendered, end="")
    except (OSError, ValueError, UnicodeError):
        print("error: CI timing summary could not be written.", file=sys.stderr)
        return 1
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    run = commands.add_parser("run", help="run one command and append its elapsed time")
    run.add_argument("--suite", required=True, help="fixed non-sensitive suite identifier")
    run.add_argument("command", nargs=argparse.REMAINDER)
    summarize = commands.add_parser("summary", help="render validated timings for this job")
    summarize.add_argument("--output", help="optional JSON summary artifact path")
    args = parser.parse_args(argv)
    if args.action == "summary":
        return summary(args.output)
    if not valid_suite(args.suite):
        parser.error("suite must be a fixed identifier using letters, digits, dots, underscores or hyphens")
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        parser.error("run requires a command after --")
    return run_suite(args.suite, command)


if __name__ == "__main__":
    sys.exit(main())
