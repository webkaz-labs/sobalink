"""Pure schema/arithmetic/privacy tests; no application or socket execution."""
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("relay_measure", Path(__file__).with_name("measure-relay-traffic.py"))
measure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(measure)
FIXTURE_HASH = "a" * 64


def phase(name, sent=0, elapsed=1, payload=0):
    useful, relay = 2 * sent, 4 * sent + 100
    return dict(name=name, observation_seconds=elapsed, payload_seconds=payload,
                useful_sent_bytes=sent, useful_received_bytes=sent, useful_total_bytes=useful,
                relay_to_bytes=relay // 2, relay_from_bytes=relay // 2, relay_total_bytes=relay,
                two_leg_payload_bytes=2 * useful, relay_excess_bytes=100,
                relay_per_useful_byte=relay / useful if useful else None,
                relay_per_two_leg_payload=relay / (2 * useful) if useful else None,
                relay_bytes_per_second=relay / elapsed, useful_bytes_per_second=useful / payload if payload else 0,
                connections_opened=2, active_connections_start=2, active_connections_end=2, dial_failures=0, relay_admissions=2)


def report():
    phases = [phase("startup_pairing"), phase("idle", elapsed=60)]
    phases += [phase("bulk_" + str(size), sent=size, elapsed=2, payload=1) for size in (1 << 20, 16 << 20, 64 << 20)]
    phases += [phase("small_tcp_frames", sent=256000, elapsed=2, payload=1),
               phase("lease_reconnect", sent=131 * 64, elapsed=130, payload=1), phase("cooldown_idle", elapsed=30)]
    return dict(schema=1, evidence="source_built_relay_only", source_commit=measure.SOURCE,
                fixture_sha256=FIXTURE_HASH, build_tags=measure.TAGS, target="linux-amd64", go_version="go1.27.1",
                outcome="passed", phases=phases, cleanup_complete=True, residual_connections=0,
                packet_bytes=None, scope=measure.SCOPE, workload="tailcat_tcp_echo", excludes=measure.EXCLUDES)


class RelayReportTests(unittest.TestCase):
    def source_fixture(self, root):
        source, fixtures = root / "source", root / "fixtures"
        (source / "internal/lanlink").mkdir(parents=True)
        fixtures.mkdir()
        for name, target in measure.FIXTURES.items():
            data = ("synthetic " + name).encode()
            (fixtures / name).write_bytes(data)
            (source / "internal/lanlink" / target).write_bytes(data)
        status = b"\n".join(("?? internal/lanlink/" + target).encode() for target in measure.FIXTURES.values()) + b"\n"
        def git(args, **kwargs):
            output = (measure.SOURCE + "\n").encode() if "rev-parse" in args else status if "status" in args else b""
            return subprocess.CompletedProcess(args, 0, output, b"")
        return source, fixtures, git

    def test_exact_source_and_fixture_copies_produce_stable_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, fixtures, git = self.source_fixture(Path(tmp))
            with patch.object(measure.subprocess, "run", side_effect=git):
                first = measure.verify_source(source, fixtures)
                self.assertEqual(len(first), 64)
                self.assertEqual(first, measure.verify_source(source, fixtures))

    def test_changed_overlay_copy_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, fixtures, git = self.source_fixture(Path(tmp))
            (source / "internal/lanlink/traffic_observation_test.go").write_text("changed")
            with patch.object(measure.subprocess, "run", side_effect=git):
                with self.assertRaisesRegex(measure.TrafficError, "fixture_copy_mismatch"):
                    measure.verify_source(source, fixtures)

    def test_unexpected_source_file_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, fixtures, git = self.source_fixture(Path(tmp))
            def changed(args, **kwargs):
                result = git(args, **kwargs)
                if "status" in args:
                    result.stdout += b"?? unapproved.go\n"
                return result
            with patch.object(measure.subprocess, "run", side_effect=changed):
                with self.assertRaisesRegex(measure.TrafficError, "unexpected_source_overlay"):
                    measure.verify_source(source, fixtures)

    def test_valid_complete_report_and_two_leg_ratio(self):
        value = measure.validate_report(report(), FIXTURE_HASH)
        bulk = value["phases"][2]
        self.assertAlmostEqual(bulk["relay_per_useful_byte"], 2 * bulk["relay_per_two_leg_payload"])
        self.assertIsNone(value["packet_bytes"])

    def test_public_binary_claim_is_rejected(self):
        value = report()
        value["evidence"] = "published_binary_offline"
        with self.assertRaisesRegex(measure.TrafficError, "report_identity_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_private_fields_and_strings_are_rejected(self):
        for location in ("report", "phase"):
            value = report()
            target = value if location == "report" else value["phases"][0]
            target["access_code"] = "private-fixture"
            with self.assertRaises(measure.TrafficError):
                measure.validate_report(value, FIXTURE_HASH)
        value = report()
        value["phases"][0]["name"] = "/private/fixture"
        with self.assertRaisesRegex(measure.TrafficError, "phase_order_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_single_leg_denominator_cannot_be_reported_as_two_leg(self):
        value = report()
        value["phases"][2]["relay_per_two_leg_payload"] = value["phases"][2]["relay_per_useful_byte"]
        with self.assertRaisesRegex(measure.TrafficError, "payload_ratio_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_idle_ratio_stays_undefined(self):
        value = report()
        value["phases"][1]["relay_per_useful_byte"] = 0
        with self.assertRaisesRegex(measure.TrafficError, "idle_ratio_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_negative_relay_excess_is_a_failure(self):
        value = report()
        value["phases"][2]["relay_excess_bytes"] = -1
        with self.assertRaisesRegex(measure.TrafficError, "phase_counter_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_reconnect_requires_readmission_and_new_connection(self):
        for field in ("relay_admissions", "connections_opened"):
            value = report()
            value["phases"][6][field] = 0
            with self.assertRaisesRegex(measure.TrafficError, "lease_evidence_invalid"):
                measure.validate_report(value, FIXTURE_HASH)

    def test_incomplete_phases_cannot_pass(self):
        value = report()
        value["phases"].pop()
        with self.assertRaisesRegex(measure.TrafficError, "complete_evidence_required"):
            measure.validate_report(value, FIXTURE_HASH)
        value["outcome"] = "in_progress"
        measure.validate_report(value, FIXTURE_HASH)

    def test_nan_and_boolean_counters_are_rejected(self):
        for field, replacement in (("relay_bytes_per_second", float("nan")), ("relay_total_bytes", True)):
            value = report()
            value["phases"][0][field] = replacement
            with self.assertRaises(measure.TrafficError):
                measure.validate_report(value, FIXTURE_HASH)

    def test_counter_scope_cannot_expand_to_packet_claim(self):
        value = report()
        value["packet_bytes"] = 1
        with self.assertRaisesRegex(measure.TrafficError, "report_scope_invalid"):
            measure.validate_report(value, FIXTURE_HASH)

    def test_collector_discards_raw_output_and_preserves_only_valid_schema(self):
        collector = measure.Collector(FIXTURE_HASH)
        content = b"private-fixture-diagnostic\n" + measure.PREFIX + json.dumps(report()).encode() + b"\n"
        collector.consume(io.BytesIO(content))
        self.assertFalse(collector.invalid)
        self.assertNotIn("private-fixture", json.dumps(collector.last))
        self.assertEqual(collector.last["outcome"], "passed")

    def test_collector_rejects_oversized_or_unknown_counter_record(self):
        collector = measure.Collector(FIXTURE_HASH)
        collector.consume(io.BytesIO(measure.PREFIX + b"x" * 70000 + b"\n"))
        self.assertTrue(collector.invalid)
        collector = measure.Collector(FIXTURE_HASH)
        collector.accept(measure.PREFIX + b'{"credential":"private-fixture"}')
        self.assertTrue(collector.invalid)
        self.assertIsNone(collector.last)

    def test_environment_does_not_pass_tokens_or_proxy_settings(self):
        value = measure.child_environment(FIXTURE_HASH, dict(PATH="fixture", RUNNER_TRACKING_ID="fixture",
                                                           GH_TOKEN="private", HTTP_PROXY="private", TS_PROXY="private"))
        self.assertNotIn("GH_TOKEN", value)
        self.assertNotIn("HTTP_PROXY", value)
        self.assertNotIn("TS_PROXY", value)
        self.assertEqual(value["SOBALINK_TRAFFIC_SOURCE_COMMIT"], measure.SOURCE)

    def test_real_fixture_cannot_start_outside_hosted_runner(self):
        with self.assertRaisesRegex(measure.TrafficError, "github_hosted_linux_required"):
            measure.host_guard({})

    def test_cleanup_escalates_only_the_owned_child(self):
        process = Mock()
        process.poll.return_value = None
        process.wait.side_effect = [subprocess.TimeoutExpired("fixture", 3), 0]
        measure.stop_owned(process)
        process.terminate.assert_called_once()
        process.kill.assert_called_once()
        self.assertEqual(process.wait.call_count, 2)

    def test_cleanup_does_not_signal_a_finished_child(self):
        process = Mock()
        process.poll.return_value = 0
        measure.stop_owned(process)
        process.terminate.assert_not_called()
        process.kill.assert_not_called()


if __name__ == "__main__":
    unittest.main()
