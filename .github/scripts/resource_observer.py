#!/usr/bin/env python3
"""Passive, content-free Linux observer for a fresh GitHub-hosted CI fixture.

Does not launch programs, open sockets, kill processes, or change settings.
Only the main entry point observes a real PID; tests use temporary /proc data.
This records sampled RSS, not Go heap size or an absolute instantaneous peak.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import stat
import sys
import time


class ObservationError(Exception):
    """Messages are fixed codes, never paths or process/file contents."""


def tree_totals(root, entry_limit=100000):
    result = dict(files=0, directories=0, logical_bytes=0, allocated_bytes=0,
                  symlinks=0, special_files=0, scan_errors=0, scan_complete=True)
    try:
        mode = root.lstat().st_mode
    except FileNotFoundError:
        return result
    if not stat.S_ISDIR(mode):
        raise ObservationError("fixture_root_not_directory")
    stack, inspected = [root], 0
    while stack:
        directory = stack.pop()
        try:
            # Fixture storage is private and has no untrusted writers. Do not
            # dereference symlinks or include filenames in results.
            with os.scandir(directory) as entries:
                for entry in entries:
                    inspected += 1
                    if inspected > entry_limit:
                        result["scan_complete"] = False
                        return result
                    try:
                        info = entry.stat(follow_symlinks=False)
                    except OSError:
                        result["scan_errors"] += 1
                        result["scan_complete"] = False
                        continue
                    if stat.S_ISLNK(info.st_mode):
                        result["symlinks"] += 1
                    elif stat.S_ISDIR(info.st_mode):
                        result["directories"] += 1
                        stack.append(Path(entry.path))
                    elif stat.S_ISREG(info.st_mode):
                        result["files"] += 1
                        result["logical_bytes"] += info.st_size
                        result["allocated_bytes"] += info.st_blocks * 512
                    else:
                        result["special_files"] += 1
        except OSError:
            result["scan_errors"] += 1
            result["scan_complete"] = False
    return result


def disk_sample(fixture):
    result = {}
    for label, relative in (("state", "state"), ("temporary", "tmp"),
                            ("outgoing", "state/outgoing")):
        for name, value in tree_totals(fixture / relative).items():
            result[label + "_" + name] = value
    try:
        info = (fixture / "state/startup.log").lstat()
        if not stat.S_ISREG(info.st_mode):
            raise ObservationError("startup_log_not_regular")
        result["startup_log_bytes"] = info.st_size
    except FileNotFoundError:
        result["startup_log_bytes"] = 0
    return result


def proc_stat(root):
    # comm can contain spaces and parentheses. starttime is field 22.
    value = (root / "stat").read_text(encoding="ascii")
    fields = value[value.rfind(")") + 2:].split()
    if len(fields) < 20:
        raise ObservationError("process_stat_invalid")
    return fields[0], int(fields[19])


def process_sample(pid, expected_start=None, proc_root=Path("/proc")):
    root = proc_root / str(pid)
    try:
        state, start = proc_stat(root)
        if expected_start is not None and start != expected_start:
            raise ObservationError("process_identity_changed")
        if state in ("Z", "X"):
            return dict(process_alive=False), start
        values = {}
        for line in (root / "status").read_text(encoding="ascii").splitlines():
            name, _, raw = line.partition(":")
            if name in ("VmRSS", "VmHWM", "Threads"):
                fields = raw.split()
                values[name] = int(fields[0]) * (1024 if name.startswith("Vm") else 1)
        if not all(name in values for name in ("VmRSS", "VmHWM", "Threads")):
            raise ObservationError("process_memory_unavailable")
        with os.scandir(root / "fd") as descriptors:
            fd_count = sum(1 for _ in descriptors)
        # Detect exit/PID reuse spanning the sampling calls.
        state_after, start_after = proc_stat(root)
        if start_after != start:
            raise ObservationError("process_identity_changed")
        return dict(process_alive=state_after not in ("Z", "X"),
                    rss_bytes=values["VmRSS"], os_peak_rss_bytes=values["VmHWM"],
                    threads=values["Threads"], fd_count=fd_count), start
    except (FileNotFoundError, ProcessLookupError):
        return dict(process_alive=False), expected_start
    except PermissionError:
        raise ObservationError("process_read_not_authorized") from None


def summarize(samples):
    metrics = {}
    for name in sorted(set().union(*(sample.keys() for sample in samples))):
        if name == "elapsed_seconds":
            continue
        values = [sample[name] for sample in samples if type(sample.get(name)) in (int, float)]
        if values:
            metrics[name] = dict(baseline=samples[0].get(name), maximum=max(values),
                                 end=samples[-1].get(name), samples=len(values))
    return dict(metrics=metrics,
                process_alive_at_end=samples[-1].get("process_alive", False),
                all_scans_complete=all(value for sample in samples
                                       for key, value in sample.items() if key.endswith("scan_complete")))


def verify_context(fixture, pid, binary_sha256, env=os.environ):
    if platform.system() != "Linux":
        raise ObservationError("linux_only_observer")
    if env.get("GITHUB_ACTIONS") != "true" or env.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ObservationError("github_hosted_fixture_required")
    if not env.get("RUNNER_TEMP"):
        raise ObservationError("runner_temporary_directory_required")
    runner_temp = Path(env["RUNNER_TEMP"]).resolve(strict=True)
    fixture = fixture.resolve(strict=True)
    if fixture == runner_temp or not fixture.is_relative_to(runner_temp):
        raise ObservationError("fixture_outside_runner_temporary_directory")
    if len(binary_sha256) != 64 or any(c not in "0123456789abcdef" for c in binary_sha256):
        raise ObservationError("expected_binary_hash_required")
    root = Path("/proc") / str(pid)
    if root.stat().st_uid != os.getuid():
        raise ObservationError("process_owner_mismatch")
    before = proc_stat(root)[1]
    digest = hashlib.sha256()
    with (root / "exe").open("rb") as executable:
        for block in iter(lambda: executable.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != binary_sha256:
        raise ObservationError("process_binary_hash_mismatch")
    if proc_stat(root)[1] != before:
        raise ObservationError("process_identity_changed")
    return fixture, before


def observe(fixture, pid, start_identity, seconds, interval):
    if not (0 <= seconds <= 900 and 1 <= interval <= 15):
        raise ObservationError("duration_or_interval_out_of_bounds")
    started, samples = time.monotonic(), []
    while True:
        elapsed = time.monotonic() - started
        current, _ = process_sample(pid, start_identity)
        current.update(disk_sample(fixture))
        current["elapsed_seconds"] = round(elapsed, 3)
        samples.append(current)
        if not current["process_alive"] or elapsed >= seconds:
            break
        time.sleep(min(interval, seconds - elapsed))
    return samples


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--binary-sha256", required=True)
    parser.add_argument("--seconds", type=float, default=900)
    parser.add_argument("--interval", type=float, default=1)
    parser.add_argument("--phase", choices=("idle", "clean_restart", "crash_recovery"), required=True)
    args = parser.parse_args()
    try:
        fixture, identity = verify_context(args.fixture, args.pid, args.binary_sha256)
        samples = observe(fixture, args.pid, identity, args.seconds, args.interval)
        report = dict(schema=1, evidence="published_binary_offline_linux",
                      phase=args.phase, binary_sha256=args.binary_sha256,
                      planned_duration_seconds=args.seconds,
                      observed_duration_seconds=samples[-1]["elapsed_seconds"],
                      nominal_interval_seconds=args.interval,
                      memory_scope="single_application_process_not_heap",
                      disk_scope="fixture_only_outgoing_is_subset_of_state",
                      network_bytes=None, network_measurement="not_measured",
                      summary=summarize(samples), samples=samples)
        print(json.dumps(report, sort_keys=True))
        if not report["summary"]["process_alive_at_end"] or not report["summary"]["all_scans_complete"]:
            return 1
        return 0
    except ObservationError as error:
        print(json.dumps({"error": str(error)}))
        return 1
    except (OSError, ValueError):
        print(json.dumps({"error": "observation_unavailable"}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
