"""Socket-free lifecycle, identity, privacy, and cancellation checks."""
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("measure_resources", Path(__file__).with_name("measure-release-resources.py"))
measure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(measure)


def session(root):
    return measure.Session(root / "binary", "a" * 64, root / "sobalink-resource-fixture",
                           root / "control.json", root / "report.json", 100)


def snapshot(pid=123):
    return dict(processId=pid, version=measure.VERSION, settings={"network": "none"},
                self={"status": "idle"}, peers=[], services=[], shares=[], proxies=[])


class ResourceLifecycleTests(unittest.TestCase):
    def test_local_lifecycle_refused(self):
        with patch.object(measure.platform, "system", return_value="Linux"), patch.object(measure.platform, "machine", return_value="x86_64"):
            with self.assertRaisesRegex(measure.MeasurementError, "github_hosted_runner_required"):
                measure.hosted_root({})

    def test_environment_excludes_credentials_and_backend_overrides(self):
        env = dict(PATH="/fixture/bin", RUNNER_TRACKING_ID="fixture-tracking", GH_TOKEN="secret",
                   GITHUB_TOKEN="secret", HTTPS_PROXY="proxy", TS_PROXY="proxy",
                   SOBALINK_BACKGROUND_LOG="private", HOME="private")
        result = measure.child_environment(Path("/fixture"), env)
        self.assertEqual(set(result), {"PATH", "RUNNER_TRACKING_ID", "LANG", "LC_ALL", "TZ", "TMPDIR", "TMP", "TEMP"})
        self.assertEqual(result["TMPDIR"], str(Path("/fixture") / "tmp"))

    def test_missing_or_online_status_is_rejected(self):
        measure.assert_offline(snapshot(), 123)
        for key in ("peers", "services", "shares", "proxies"):
            value = snapshot()
            value.pop(key)
            with self.assertRaisesRegex(measure.MeasurementError, "empty_fixture_required"):
                measure.assert_offline(value, 123)
        value = snapshot()
        value["settings"]["network"] = "lan"
        with self.assertRaisesRegex(measure.MeasurementError, "offline_state_required"):
            measure.assert_offline(value, 123)
        with self.assertRaisesRegex(measure.MeasurementError, "status_identity_mismatch"):
            measure.assert_offline(snapshot(), 124)

    def test_exact_lifecycle_contract_without_starting_processes(self):
        value = session(Path("/not-read"))
        events = []
        value.checkpoint = lambda *args: events.append(("checkpoint", *args))
        value.start = lambda: events.append(("start",))
        value.stop = lambda: events.append(("stop",))
        value.force_stop = lambda: events.append(("force_stop",))
        value.collect = lambda *args: events.append(("collect", *args))
        value.measure()
        self.assertEqual(events.count(("start",)), 13)
        self.assertEqual(events.count(("stop",)), 12)
        self.assertEqual(events.count(("force_stop",)), 1)
        self.assertEqual([item for item in events if item[0] == "collect"],
                         [("collect", "idle", 900)] + [("collect", "clean_restart", 10, n) for n in range(1, 11)]
                         + [("collect", "before_forced_stop", 10), ("collect", "crash_recovery", 60)])
        self.assertEqual(value.report["clean_restarts_completed"], 10)
        self.assertTrue(value.report["crash_restart_completed"])

    def test_force_stop_uses_pidfd_only_after_guard(self):
        value = session(Path("/not-read"))
        value.pid, value.start_identity = 123, 100
        value.guard = Mock(side_effect=measure.MeasurementError("process_identity_changed"))
        with patch.object(measure.os, "pidfd_open", return_value=11, create=True), patch.object(measure.os, "close") as close:
            with patch.object(measure.signal, "pidfd_send_signal", create=True) as send:
                with self.assertRaisesRegex(measure.MeasurementError, "process_identity_changed"):
                    value.force_stop()
                send.assert_not_called()
                close.assert_called_once_with(11)

    def test_force_stop_exact_identity_and_bounded_exit(self):
        value = session(Path("/not-read"))
        value.pid, value.start_identity = 123, 100
        value.guard, value.wait_exit, value.save_control = Mock(), Mock(return_value=True), Mock()
        with patch.object(measure.os, "pidfd_open", return_value=11, create=True), patch.object(measure.os, "close"):
            with patch.object(measure.signal, "pidfd_send_signal", create=True) as send, patch.object(measure.signal, "SIGKILL", 9, create=True):
                value.force_stop()
                value.guard.assert_called_once_with(123, 100)
                send.assert_called_once_with(11, signal.SIGKILL)
        value.wait_exit.assert_called_once_with(3)
        self.assertIsNone(value.pid)

    def test_failed_clean_stop_does_not_become_success(self):
        value = session(Path("/not-read"))
        value.pid, value.start_identity = 123, 100
        value.guard, value.invoke = Mock(), Mock(return_value={"state": "stopping"})
        value.wait_exit, value.save_control = Mock(return_value=False), Mock()
        with self.assertRaisesRegex(measure.MeasurementError, "fixture_clean_stop_timeout"):
            value.stop()
        value.save_control.assert_not_called()
        self.assertEqual(value.pid, 123)

    def test_shutdown_liveness_does_not_require_memory_fields(self):
        value = session(Path("/not-read"))
        value.pid, value.start_identity = 123, 100
        with patch.object(measure.observer, "process_sample", side_effect=AssertionError("memory must not be sampled")):
            with patch.object(measure.observer, "proc_stat", return_value=("S", 100)):
                self.assertTrue(value.live())
            with patch.object(measure.observer, "proc_stat", return_value=("Z", 100)):
                self.assertFalse(value.live())
            with patch.object(measure.observer, "proc_stat", return_value=("S", 101)):
                self.assertFalse(value.live())

    def test_acknowledged_stop_accepts_process_disappearance_during_wait(self):
        for missing in (FileNotFoundError, ProcessLookupError):
            with self.subTest(error=missing.__name__):
                value = session(Path("/not-read"))
                value.pid, value.start_identity = 123, 100
                value.guard = Mock()
                value.invoke = Mock(return_value={"state": "stopping"})
                value.save_control = Mock()
                with patch.object(measure.observer, "proc_stat", side_effect=[("S", 100), missing("private-fixture")]):
                    with patch.object(measure.time, "sleep"):
                        value.stop()
                value.invoke.assert_called_once_with(["stop", "--json"])
                value.save_control.assert_called_once()
                self.assertIsNone(value.pid)
                self.assertIsNone(value.start_identity)

    def test_stop_wait_still_fails_on_unexpected_stat_errors(self):
        for error in (PermissionError("private-fixture"), OSError("private-fixture"),
                      measure.observer.ObservationError("process_stat_invalid")):
            with self.subTest(error=type(error).__name__):
                value = session(Path("/not-read"))
                value.pid, value.start_identity = 123, 100
                value.guard = Mock()
                value.invoke = Mock(return_value={"state": "stopping"})
                value.save_control = Mock()
                with patch.object(measure.observer, "proc_stat", side_effect=error):
                    with self.assertRaises(type(error)):
                        value.stop()
                self.assertEqual(value.operation, "stop_wait")
                self.assertEqual(value.pid, 123)
                value.save_control.assert_not_called()

    def test_unexpected_failure_records_only_fixed_operation_and_category(self):
        class PrivateFixtureException(RuntimeError):
            pass

        with tempfile.TemporaryDirectory() as tmp:
            value = session(Path(tmp))
            value.control.touch()
            value.operation = "stop_wait"
            value.cleanup = Mock(side_effect=PrivateFixtureException("/private/path secret-fixture-token"))
            args = ["measure", "--cleanup-only", "--control", str(value.control), "--report", str(value.report_path)]
            with patch.object(measure.sys, "argv", args), patch.object(measure.sys, "stdout", io.StringIO()):
                with patch.object(measure, "hosted_root", return_value=Path(tmp)), patch.object(measure, "load_cleanup", return_value=value):
                    with patch.object(measure.os, "pidfd_open", create=True), patch.object(measure.signal, "pidfd_send_signal", create=True):
                        self.assertEqual(measure.main(), 1)
            report = json.loads(value.report_path.read_text())
            self.assertEqual(report["outcome"], "failed")
            self.assertEqual(report["failure_code"], "measurement_unavailable")
            self.assertEqual(report["failure_operation"], "stop_wait")
            self.assertEqual(report["failure_category"], "unexpected_error")
            encoded = json.dumps(report)
            for private in ("/private/path", "secret-fixture-token", "PrivateFixtureException", tmp):
                self.assertNotIn(private, encoded)

    def test_start_already_running_is_not_adopted(self):
        value = session(Path("/not-read"))
        value.invoke = Mock(return_value={"state": "already-running", "pid": 123, "startupApplied": False})
        value.guard = Mock()
        with self.assertRaisesRegex(measure.MeasurementError, "fixture_start_not_ready"):
            value.start()
        value.guard.assert_not_called()

    def test_partial_phase_persists_on_cancellation(self):
        value = session(Path("/not-read"))
        value.pid, value.start_identity = 123, 100
        value.persist = Mock()
        with patch.object(measure.observer, "process_sample", return_value=({"process_alive": True, "rss_bytes": 20}, 100)):
            with patch.object(measure.observer, "disk_sample", return_value={"startup_log_bytes": 10, "state_scan_complete": True}):
                with patch.object(measure.time, "monotonic", side_effect=[0, 0]):
                    with patch.object(measure.time, "sleep", side_effect=measure.MeasurementCancelled("measurement_cancelled")):
                        with self.assertRaises(measure.MeasurementCancelled):
                            value.collect("idle", 900)
        phase = value.report["phases"][0]
        self.assertFalse(phase["completed"])
        self.assertEqual(phase["summary"]["metrics"]["rss_bytes"]["baseline"], 20)
        value.persist.assert_called()

    def test_log_limit_fails_observation(self):
        value = session(Path("/not-read"))
        value.persist = Mock()
        with patch.object(measure.observer, "process_sample", return_value=({"process_alive": True}, 100)):
            with patch.object(measure.observer, "disk_sample", return_value={"startup_log_bytes": 1048577}):
                with self.assertRaisesRegex(measure.MeasurementError, "background_log_bound_exceeded"):
                    value.collect("idle", 900)

    def test_report_excludes_fixture_locations_and_private_content(self):
        value = session(Path("/private-location"))
        encoded = json.dumps(value.report)
        for forbidden in ("private-location", "control.json", "binary\"", "pid\"", "startup.log"):
            self.assertNotIn(forbidden, encoded)
        self.assertIsNone(value.report["network_bytes"])
        self.assertEqual(value.report["source_commit"], measure.COMMIT)

    def test_cleanup_uses_bounded_stop_then_pidfd_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            value = session(Path(tmp))
            value.fixture.mkdir()
            value.pid, value.start_identity = 123, 100
            value.live = Mock(return_value=True)
            value.guard, value.invoke, value.checkpoint = Mock(), Mock(), Mock()
            value.wait_exit, value.force_stop = Mock(return_value=False), Mock()
            value.recover_owned = Mock(return_value=False)
            value.cleanup()
            value.invoke.assert_called_once_with(["stop", "--json"], timeout=2)
            value.force_stop.assert_called_once()
            self.assertTrue(value.report["cleanup_complete"])
            self.assertFalse(value.fixture.exists())

    def test_cleanup_never_deletes_with_remaining_owned_process(self):
        with tempfile.TemporaryDirectory() as tmp:
            value = session(Path(tmp))
            value.fixture.mkdir()
            value.live = Mock(return_value=False)
            value.recover_owned = Mock(return_value=True)
            with self.assertRaisesRegex(measure.MeasurementError, "fixture_process_remains"):
                value.cleanup()
            self.assertTrue(value.fixture.exists())

    def test_installed_identity_requires_exact_released_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "share/sobalink").mkdir(parents=True)
            (root / "share/sobalink/build.json").write_text(json.dumps({"product": "sobalink", "version": measure.VERSION,
                "source_commit": "0" * 40, "target": measure.TARGET, "binary": {"path": "bin/soba", "sha256": "a" * 64}}))
            with self.assertRaisesRegex(measure.MeasurementError, "installed_identity_mismatch"):
                measure.installed_identity(root, root / "share/sobalink/build.json")

    def test_installed_metadata_must_match_verified_public_copy(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "share/sobalink").mkdir(parents=True)
            (root / "share/sobalink/build.json").write_text("{}")
            (root / "public-build.json").write_text('{"different":true}')
            with self.assertRaisesRegex(measure.MeasurementError, "installed_public_metadata_mismatch"):
                measure.installed_identity(root, root / "public-build.json")


if __name__ == "__main__":
    unittest.main()
