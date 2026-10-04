#!/usr/bin/env python3
"""Observe one verified published binary in a disposable native CI fixture.

Only aggregate counters leave the fixture. No application output, file paths,
process command lines, credentials, or log contents are written to the report.
The actual lifecycle is restricted to GitHub-hosted Linux runners.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import tempfile
import time

import resource_observer as observer

VERSION = "0.3.0-alpha.1"
COMMIT = "8e6cbb00d60757f701d7d453adb92590cc5d2544"
TARGET = "linux-amd64"
PHASE_SECONDS = (900, 10, 10, 60)


class MeasurementError(Exception):
    """Fixed, content-free failure codes only."""


class MeasurementCancelled(MeasurementError):
    pass


def exception_category(error):
    # Only fixed categories leave the fixture, never exception names or text.
    for kind, code in ((MeasurementCancelled, "cancelled"),
                       (MeasurementError, "measurement_check"),
                       (observer.ObservationError, "observation_check"),
                       (subprocess.TimeoutExpired, "command_timeout"),
                       (ProcessLookupError, "process_missing"),
                       (FileNotFoundError, "file_missing"),
                       (PermissionError, "permission_denied"),
                       (OSError, "os_error"),
                       (ValueError, "invalid_value")):
        if isinstance(error, kind):
            return code
    return "unexpected_error"


def require(condition, code):
    if not condition:
        raise MeasurementError(code)


def hosted_root(env=os.environ):
    require(platform.system() == "Linux" and platform.machine() in ("x86_64", "AMD64"), "native_target_required")
    require(env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted", "github_hosted_runner_required")
    require(bool(env.get("RUNNER_TEMP")), "runner_temporary_directory_required")
    return Path(env["RUNNER_TEMP"]).resolve(strict=True)


def file_hash(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def installed_identity(install, public_build):
    require(file_hash(install / "share/sobalink/build.json") == file_hash(public_build), "installed_public_metadata_mismatch")
    meta = json.loads((install / "share/sobalink/build.json").read_text(encoding="utf-8"))
    require(meta.get("product") == "sobalink" and meta.get("version") == VERSION
            and meta.get("source_commit") == COMMIT and meta.get("target") == TARGET, "installed_identity_mismatch")
    require(meta.get("binary", {}).get("path") == "bin/soba", "installed_binary_path_mismatch")
    binary = (install / "bin/soba").resolve(strict=True)
    require(binary.is_relative_to(install.resolve(strict=True)), "installed_binary_outside_package")
    digest = file_hash(binary)
    require(digest == meta["binary"]["sha256"], "installed_binary_hash_mismatch")
    return binary, digest


def child_environment(fixture, env=os.environ):
    # Keep the runner's orphan-process tracking marker for final runner teardown,
    # but never pass repository tokens, proxy overrides, or private app markers.
    result = {name: env[name] for name in ("PATH", "RUNNER_TRACKING_ID") if name in env}
    result.update(LANG="C.UTF-8", LC_ALL="C.UTF-8", TZ="UTC",
                  TMPDIR=str(fixture / "tmp"), TMP=str(fixture / "tmp"), TEMP=str(fixture / "tmp"))
    return result


def assert_offline(snapshot, pid):
    require(snapshot.get("processId") == pid and snapshot.get("version") == VERSION, "status_identity_mismatch")
    require(snapshot.get("settings", {}).get("network") == "none"
            and snapshot.get("self", {}).get("status") == "idle", "offline_state_required")
    require(all(isinstance(snapshot.get(name), list) and not snapshot[name]
                for name in ("peers", "services", "shares", "proxies")), "empty_fixture_required")


def write_json(path, value, private=False):
    # A sibling replacement preserves the last complete report if interrupted.
    scratch = path.with_name(path.name + ".new")
    with scratch.open("w", encoding="utf-8") as output:
        if private:
            os.chmod(scratch, 0o600)
        json.dump(value, output, sort_keys=True)
        output.write("\n")
    os.replace(scratch, path)


def initial_report(digest):
    return dict(schema=1, evidence="published_binary_offline", version=VERSION,
                source_commit=COMMIT, target=TARGET, binary_sha256=digest,
                outcome="in_progress", failure_code=None, failure_operation=None, failure_category=None,
                cleanup_failure_code=None, cleanup_failure_operation=None, cleanup_failure_category=None,
                memory_scope="one_application_process_rss_not_heap",
                disk_scope="fixture_only_outgoing_is_subset_of_state",
                network_bytes=None, network_measurement="not_measured",
                log_limit_bytes=1048576, phases=[], checkpoints=[], clean_restarts_completed=0,
                crash_restart_completed=False, cleanup_complete=False,
                observer_sampling_seconds=1,
                limitations=["sampled_maximum_can_miss_short_peaks", "offline_mode_is_not_packet_capture",
                             "idle_crash_does_not_test_interrupted_transfers", "linux_amd64_only",
                             "file_receiving_and_log_rollover_are_not_exercised",
                             "disk_blocks_exclude_directory_metadata_and_unlinked_open_files"])


class Session:
    def __init__(self, binary, digest, fixture, control, report_path, minimum_start):
        self.binary, self.digest, self.fixture = binary, digest, fixture
        self.control, self.report_path = control, report_path
        self.minimum_start = minimum_start
        self.pid, self.start_identity = None, None
        self.operation = "initialize_session"
        self.report = initial_report(digest)

    @property
    def arguments(self):
        return ["--state-dir", str(self.fixture / "state"), "--locale", "en"]

    def save_control(self):
        write_json(self.control, dict(binary=str(self.binary), digest=self.digest, fixture=str(self.fixture),
                                    minimum_start=self.minimum_start, pid=self.pid, start_identity=self.start_identity), private=True)

    def persist(self):
        write_json(self.report_path, self.report)

    def invoke(self, args, timeout=5):
        result = subprocess.run([str(self.binary), *self.arguments, *args],
                                env=child_environment(self.fixture), stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout, check=False)
        require(result.returncode == 0, "fixture_command_failed")
        require(len(result.stdout) <= 1024 * 1024, "fixture_response_too_large")
        try:
            return json.loads(result.stdout)
        except (ValueError, UnicodeError):
            raise MeasurementError("fixture_response_invalid") from None

    def guard(self, pid, expected_start=None):
        root = Path("/proc") / str(pid)
        require(root.stat().st_uid == os.getuid(), "process_owner_mismatch")
        state, start = observer.proc_stat(root)
        require(state not in ("Z", "X") and start >= self.minimum_start, "process_not_owned_live_fixture")
        if expected_start is not None:
            require(start == expected_start, "process_identity_changed")
        command = (root / "cmdline").read_bytes().split(b"\0")
        require(command[-1:] == [b""] and [part.decode() for part in command[1:-1]]
                == [*self.arguments, "run", "--offline"], "process_arguments_mismatch")
        require(file_hash(root / "exe") == self.digest, "process_binary_hash_mismatch")
        require(observer.proc_stat(root)[1] == start, "process_identity_changed")
        return start

    def recover_owned(self):
        # Used only if startup was interrupted before it returned its PID. An
        # exact private fixture path, start boundary, UID, argv, and binary hash
        # are all required; never select processes by name alone.
        candidates = []
        expected = [part.encode() for part in [*self.arguments, "run", "--offline"]]
        for root in Path("/proc").iterdir():
            if not root.name.isdigit():
                continue
            try:
                if root.stat().st_uid != os.getuid():
                    continue
                raw = (root / "cmdline").read_bytes().split(b"\0")
                if raw[1:-1] != expected or raw[-1:] != [b""]:
                    continue
                pid = int(root.name)
                candidates.append((pid, self.guard(pid)))
            except (FileNotFoundError, ProcessLookupError):
                continue
        require(len(candidates) <= 1, "multiple_owned_fixture_processes")
        if candidates:
            self.pid, self.start_identity = candidates[0]
            self.save_control()
        return bool(candidates)

    def live(self):
        if self.pid is None:
            return False
        try:
            state, start = observer.proc_stat(Path("/proc") / str(self.pid))
        except (FileNotFoundError, ProcessLookupError):
            return False
        # A reused numeric PID is not the owned application. Never signal it.
        return start == self.start_identity and state not in ("Z", "X")

    def wait_exit(self, seconds):
        deadline = time.monotonic() + seconds
        while self.live():
            if time.monotonic() >= deadline:
                return False
            time.sleep(0.05)
        return True

    def start(self):
        self.operation = "start_command"
        result = self.invoke(["start", "--background", "--offline"], timeout=20)
        require(result.get("state") == "ready" and result.get("startupApplied") is True
                and type(result.get("pid")) is int and result["pid"] > 0, "fixture_start_not_ready")
        self.pid = result["pid"]
        self.operation = "start_identity"
        self.start_identity = self.guard(self.pid)
        self.save_control()
        self.operation = "start_status"
        assert_offline(self.invoke(["status", "--json"]), self.pid)

    def stop(self, seconds=15):
        self.operation = "stop_identity"
        self.guard(self.pid, self.start_identity)
        self.operation = "stop_command"
        result = self.invoke(["stop", "--json"])
        require(result.get("state") == "stopping", "fixture_stop_not_acknowledged")
        self.operation = "stop_wait"
        require(self.wait_exit(seconds), "fixture_clean_stop_timeout")
        self.pid, self.start_identity = None, None
        self.operation = "stop_control"
        self.save_control()

    def force_stop(self):
        self.operation = "force_stop_identity"
        # pidfd addresses this exact process even if the numeric PID is reused.
        descriptor = os.pidfd_open(self.pid)
        try:
            self.guard(self.pid, self.start_identity)
            self.operation = "force_stop_signal"
            signal.pidfd_send_signal(descriptor, signal.SIGKILL)
        finally:
            os.close(descriptor)
        self.operation = "force_stop_wait"
        require(self.wait_exit(3), "fixture_forced_stop_timeout")
        self.pid, self.start_identity = None, None
        self.save_control()

    def checkpoint(self, name, cycle=0):
        self.operation = "disk_checkpoint"
        value = observer.disk_sample(self.fixture)
        require(all(v for k, v in value.items() if k.endswith("scan_complete")), "incomplete_disk_sample")
        self.report["checkpoints"].append(dict(name=name, cycle=cycle, counters=value))
        self.persist()

    def collect(self, name, seconds, cycle=0):
        require(0 < seconds <= 900, "observation_duration_out_of_bounds")
        phase = dict(name=name, cycle=cycle, planned_seconds=seconds, observed_seconds=0,
                     completed=False, samples=[], summary=None)
        self.report["phases"].append(phase)
        started = time.monotonic()
        try:
            while True:
                elapsed = time.monotonic() - started
                self.operation = "observe_process"
                sample, _ = observer.process_sample(self.pid, self.start_identity)
                self.operation = "observe_disk"
                sample.update(observer.disk_sample(self.fixture))
                sample["elapsed_seconds"] = round(elapsed, 3)
                phase["samples"].append(sample)
                phase["observed_seconds"] = sample["elapsed_seconds"]
                require(sample["process_alive"], "fixture_exited_during_observation")
                require(all(v for k, v in sample.items() if k.endswith("scan_complete")), "incomplete_disk_sample")
                require(sample["startup_log_bytes"] <= 1048576, "background_log_bound_exceeded")
                if elapsed >= seconds:
                    self.operation = "observe_status"
                    assert_offline(self.invoke(["status", "--json"]), self.pid)
                    phase["completed"] = True
                    break
                if len(phase["samples"]) % 15 == 0:
                    self.persist()
                time.sleep(min(1, seconds - elapsed))
        finally:
            if phase["samples"]:
                phase["summary"] = observer.summarize(phase["samples"])
            self.persist()

    def measure(self):
        self.checkpoint("before_initial_start")
        self.start()
        self.collect("idle", PHASE_SECONDS[0])
        self.stop()
        self.checkpoint("after_idle_stop")
        for cycle in range(1, 11):
            self.checkpoint("before_clean_restart", cycle)
            self.start()
            self.collect("clean_restart", PHASE_SECONDS[1], cycle)
            self.stop()
            self.checkpoint("after_clean_restart", cycle)
            self.report["clean_restarts_completed"] = cycle
        self.checkpoint("before_crash_start")
        self.start()
        self.collect("before_forced_stop", PHASE_SECONDS[2])
        self.force_stop()
        self.checkpoint("after_forced_stop")
        self.start()
        self.collect("crash_recovery", PHASE_SECONDS[3])
        self.stop()
        self.checkpoint("after_crash_recovery_stop")
        self.report["crash_restart_completed"] = True
        self.report["outcome"] = "passed"

    def cleanup(self):
        self.operation = "cleanup_process"
        # Recover even after a launcher timeout or cancellation before JSON/PID.
        # Normal measurement stops already ran; this is bounded failure cleanup.
        if not self.live():
            self.pid, self.start_identity = None, None
            self.recover_owned()
        if self.live():
            try:
                self.guard(self.pid, self.start_identity)
                self.invoke(["stop", "--json"], timeout=2)
            except (MeasurementError, subprocess.TimeoutExpired, OSError):
                pass
            if not self.wait_exit(1):
                self.force_stop()
        self.pid, self.start_identity = None, None
        require(not self.recover_owned(), "fixture_process_remains")
        self.checkpoint("before_fixture_cleanup")
        self.operation = "cleanup_fixture"
        if self.fixture.exists():
            shutil.rmtree(self.fixture)
        self.report["cleanup_complete"] = not self.fixture.exists()
        self.control.unlink(missing_ok=True)
        self.persist()


def cancelled(signum, frame):
    raise MeasurementCancelled("measurement_cancelled")


def load_cleanup(control, report_path, runner_root):
    saved = json.loads(control.read_text(encoding="utf-8"))
    fixture = Path(saved["fixture"]).resolve()
    require(fixture != runner_root and fixture.is_relative_to(runner_root)
            and fixture.name.startswith("sobalink-resource-"), "cleanup_fixture_outside_scope")
    session = Session(Path(saved["binary"]), saved["digest"], fixture, control, report_path, saved["minimum_start"])
    session.pid, session.start_identity = saved["pid"], saved["start_identity"]
    if report_path.exists():
        session.report = json.loads(report_path.read_text(encoding="utf-8"))
    return session


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--install", type=Path)
    parser.add_argument("--public-build", type=Path)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--control", type=Path, required=True)
    parser.add_argument("--cleanup-only", action="store_true")
    args = parser.parse_args()
    session = None
    operation = "host_context"
    try:
        runner_root = hosted_root()
        require(hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"), "pidfd_support_required")
        args.report.parent.mkdir(parents=True, exist_ok=True)
        if args.cleanup_only:
            if not args.control.exists():
                return 0
            operation = "load_cleanup"
            session = load_cleanup(args.control, args.report, runner_root)
            session.cleanup()
            return 0
        require(args.install is not None and args.public_build is not None, "verified_installed_package_required")
        operation = "installed_identity"
        binary, digest = installed_identity(args.install, args.public_build)
        # The release workflow already ran its full installed offline smoke on
        # this exact artifact. Avoid starting an untracked second fixture here.
        operation = "installed_version"
        result = subprocess.run([str(binary), "--version"], stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=30)
        require(result.returncode == 0 and result.stdout.strip() == ("sobalink " + VERSION + " (soba)").encode(),
                "installed_version_mismatch")
        operation = "create_fixture"
        fixture = Path(tempfile.mkdtemp(prefix="sobalink-resource-", dir=runner_root))
        (fixture / "tmp").mkdir(mode=0o700)
        # The application creates its own state/log files. No fake log growth,
        # transfer data, settings, or private state is planted by this driver.
        minimum_start = int(float(Path("/proc/uptime").read_text().split()[0]) * os.sysconf("SC_CLK_TCK"))
        session = Session(binary, digest, fixture, args.control, args.report, minimum_start)
        session.save_control()
        session.persist()
        signal.signal(signal.SIGINT, cancelled)
        signal.signal(signal.SIGTERM, cancelled)
        session.measure()
    except MeasurementError as error:
        if session is not None:
            session.report.update(outcome="cancelled" if isinstance(error, MeasurementCancelled) else "failed", failure_code=str(error),
                                  failure_operation=session.operation, failure_category=exception_category(error))
        else:
            write_json(args.report, dict(schema=1, outcome="failed", failure_code=str(error),
                                         failure_operation=operation, failure_category=exception_category(error)))
        if session is not None:
            session.persist()
    except Exception as error:
        if session is not None:
            session.report.update(outcome="failed", failure_code="measurement_unavailable",
                                  failure_operation=session.operation, failure_category=exception_category(error))
        else:
            write_json(args.report, dict(schema=1, outcome="failed", failure_code="measurement_unavailable",
                                         failure_operation=operation, failure_category=exception_category(error)))
        if session is not None:
            session.persist()
    finally:
        if session is not None and not args.cleanup_only:
            signal.signal(signal.SIGINT, signal.SIG_IGN)
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            try:
                session.cleanup()
            except Exception as error:
                session.report.update(outcome="failed", cleanup_complete=False, cleanup_failure_code="fixture_cleanup_incomplete",
                                      cleanup_failure_operation=session.operation, cleanup_failure_category=exception_category(error))
                session.persist()
    if session is None or session.report["outcome"] != "passed" or not session.report["cleanup_complete"]:
        print("Published-binary resource observation did not complete; see the aggregate report.")
        return 1
    print("Published-binary offline idle, ten restarts, crash recovery, and cleanup completed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
