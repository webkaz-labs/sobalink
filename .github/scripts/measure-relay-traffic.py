#!/usr/bin/env python3
"""Run the hosted relay-only fixture and retain strictly validated counters."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import threading

SOURCE = "8e6cbb00d60757f701d7d453adb92590cc5d2544"
TAGS = "lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport"
PREFIX = b"SOBALINK_RELAY_TRAFFIC_REPORT "
SCOPE = "successful_encrypted_stream_writes_at_one_proxy_boundary_per_relay_leg"
EXCLUDES = ["outer_tcp_ip_ethernet_headers", "tcp_ack_only_packets", "kernel_retransmissions", "icmp_diagnostics",
            "direct_udp", "separate_local_admission_http", "real_wan_nat", "released_binary_memory",
            "sobalink_file_message_protocol", "peer_service_discovery", "web_ui_polling"]
PHASES = ["startup_pairing", "idle", "bulk_1048576", "bulk_16777216", "bulk_67108864", "small_tcp_frames", "lease_reconnect", "cooldown_idle"]
FIXTURES = {"counters.go": "traffic_observation_counter_test.go",
            "counters_test.go": "traffic_observation_counter_unit_test.go",
            "integration_test.go": "traffic_observation_test.go"}
REPORT_KEYS = {"schema", "evidence", "source_commit", "fixture_sha256", "build_tags", "target", "go_version", "outcome",
               "phases", "cleanup_complete", "residual_connections", "packet_bytes", "scope", "workload", "excludes"}
INTEGER_METRICS = {"useful_sent_bytes", "useful_received_bytes", "useful_total_bytes", "relay_to_bytes", "relay_from_bytes",
                   "relay_total_bytes", "two_leg_payload_bytes", "relay_excess_bytes", "connections_opened",
                   "active_connections_start", "active_connections_end", "dial_failures", "relay_admissions"}
FLOAT_METRICS = {"observation_seconds", "payload_seconds", "relay_bytes_per_second", "useful_bytes_per_second"}
RATIOS = {"relay_per_useful_byte", "relay_per_two_leg_payload"}


class TrafficError(Exception):
    pass


class Cancelled(TrafficError):
    pass


def require(condition, code):
    if not condition:
        raise TrafficError(code)


def host_guard(env=os.environ):
    require(env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted"
            and platform.system() == "Linux" and platform.machine() in ("x86_64", "AMD64"), "github_hosted_linux_required")


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def verify_source(source, fixtures):
    def git(*args):
        result = subprocess.run(["git", "-C", str(source), *args], capture_output=True, timeout=15)
        require(result.returncode == 0, "source_verification_failed")
        return result.stdout
    require(git("rev-parse", "HEAD").strip().decode("ascii") == SOURCE, "source_commit_mismatch")
    git("diff", "--exit-code")
    git("diff", "--cached", "--exit-code")
    expected_status = {("?? internal/lanlink/" + target).encode() for target in FIXTURES.values()}
    require(set(git("status", "--porcelain", "--untracked-files=all").splitlines()) == expected_status, "unexpected_source_overlay")
    combined = hashlib.sha256()
    for name, target in sorted(FIXTURES.items()):
        original = fixtures / name
        copied = source / "internal/lanlink" / target
        require(not original.is_symlink() and not copied.is_symlink() and digest(original) == digest(copied), "fixture_copy_mismatch")
        combined.update(name.encode("ascii") + b"\0" + original.read_bytes() + b"\0")
    return combined.hexdigest()


def close_number(actual, expected):
    return type(actual) in (int, float) and math.isfinite(actual) and math.isclose(actual, expected, rel_tol=1e-9, abs_tol=1e-9)


def validate_report(value, fixture_hash):
    require(isinstance(value, dict) and set(value) == REPORT_KEYS, "report_fields_invalid")
    require(type(value["schema"]) is int and value["schema"] == 1 and value["evidence"] == "source_built_relay_only"
            and value["source_commit"] == SOURCE and value["fixture_sha256"] == fixture_hash and value["build_tags"] == TAGS
            and value["target"] == "linux-amd64" and value["go_version"] == "go1.27.1", "report_identity_invalid")
    require(value["outcome"] in ("in_progress", "passed", "failed") and type(value["cleanup_complete"]) is bool
            and type(value["residual_connections"]) is int and -1 <= value["residual_connections"] <= 1024,
            "report_outcome_invalid")
    require(value["packet_bytes"] is None and value["scope"] == SCOPE and value["workload"] == "tailcat_tcp_echo"
            and value["excludes"] == EXCLUDES, "report_scope_invalid")
    phases = value["phases"]
    require(isinstance(phases, list) and len(phases) <= len(PHASES), "report_phases_invalid")
    for index, phase in enumerate(phases):
        require(isinstance(phase, dict) and set(phase) == {"name"} | INTEGER_METRICS | FLOAT_METRICS | RATIOS,
                "phase_fields_invalid")
        require(phase["name"] == PHASES[index], "phase_order_invalid")
        require(all(type(phase[key]) is int and 0 <= phase[key] <= 1 << 40 for key in INTEGER_METRICS), "phase_counter_invalid")
        require(all(type(phase[key]) in (int, float) and math.isfinite(phase[key]) and 0 <= phase[key] <= 1 << 40
                    for key in FLOAT_METRICS), "phase_duration_invalid")
        elapsed, payload = phase["observation_seconds"], phase["payload_seconds"]
        require(0 < elapsed <= 600 and payload <= elapsed, "phase_duration_invalid")
        sent, received = phase["useful_sent_bytes"], phase["useful_received_bytes"]
        useful, relay = phase["useful_total_bytes"], phase["relay_total_bytes"]
        require(sent == received and useful == sent + received and relay == phase["relay_to_bytes"] + phase["relay_from_bytes"]
                and phase["two_leg_payload_bytes"] == 2 * useful and phase["relay_excess_bytes"] == relay - 2 * useful
                and phase["dial_failures"] == 0, "phase_arithmetic_invalid")
        require(close_number(phase["relay_bytes_per_second"], relay / elapsed), "phase_rate_invalid")
        if useful:
            require(payload > 0 and close_number(phase["relay_per_useful_byte"], relay / useful)
                    and close_number(phase["relay_per_two_leg_payload"], relay / (2 * useful))
                    and close_number(phase["useful_bytes_per_second"], useful / payload), "payload_ratio_invalid")
        else:
            require(payload == 0 and phase["relay_per_useful_byte"] is None and phase["relay_per_two_leg_payload"] is None
                    and phase["useful_bytes_per_second"] == 0, "idle_ratio_invalid")
        if phase["name"].startswith("bulk_"):
            require(sent == int(phase["name"][5:]), "bulk_size_invalid")
        elif phase["name"] == "small_tcp_frames":
            require(sent == 256000, "small_message_size_invalid")
        elif phase["name"] == "lease_reconnect":
            require(elapsed >= 130 and 128 <= sent <= 64 * 600 and sent % 64 == 0
                    and phase["connections_opened"] > 0 and phase["relay_admissions"] > 0, "lease_evidence_invalid")
        else:
            require(useful == 0, "idle_payload_invalid")
            if phase["name"] in ("idle", "cooldown_idle"):
                require(elapsed >= (60 if phase["name"] == "idle" else 30), "idle_duration_invalid")
    if value["outcome"] == "passed":
        require(len(phases) == len(PHASES) and value["cleanup_complete"] and value["residual_connections"] == 0,
                "complete_evidence_required")
    return value


class Collector:
    def __init__(self, fixture_hash):
        self.fixture_hash, self.last, self.invalid = fixture_hash, None, False

    def accept(self, line):
        if not line.startswith(PREFIX):
            return
        try:
            self.last = validate_report(json.loads(line[len(PREFIX):]), self.fixture_hash)
        except (ValueError, TypeError, KeyError, TrafficError):
            self.invalid = True

    def consume(self, stream):
        pending, dropping = bytearray(), False
        while True:
            block = stream.read(4096)
            if not block:
                break
            for byte in block:
                if byte == 10:
                    if not dropping:
                        self.accept(bytes(pending))
                    pending.clear()
                    dropping = False
                elif not dropping:
                    pending.append(byte)
                    if len(pending) > 65536:
                        if pending.startswith(PREFIX):
                            self.invalid = True
                        pending.clear()
                        dropping = True
        if pending and not dropping:
            self.accept(bytes(pending))


def child_environment(fixture_hash, env=os.environ):
    clean = {name: env[name] for name in ("PATH", "RUNNER_TRACKING_ID") if name in env}
    clean.update(LANG="C.UTF-8", LC_ALL="C.UTF-8", TZ="UTC", GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted",
                 SOBALINK_RUN_TRAFFIC_MEASUREMENT="1", SOBALINK_TRAFFIC_FIXTURE_SHA256=fixture_hash,
                 SOBALINK_TRAFFIC_SOURCE_COMMIT=SOURCE)
    return clean


def interrupt(signum, frame):
    raise Cancelled("measurement_cancelled")


def stop_owned(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--fixtures", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    process, reader, collector = None, None, None
    result = dict(schema=1, evidence="source_built_relay_only", source_commit=SOURCE, outcome="failed", failure_code=None,
                  fixture_sha256=None, test_binary_sha256=None, execution_completed=False, report=None)
    try:
        host_guard()
        require(args.binary.is_file() and not args.binary.is_symlink(), "compiled_fixture_required")
        fixture_hash = verify_source(args.source, args.fixtures)
        result.update(fixture_sha256=fixture_hash, test_binary_sha256=digest(args.binary))
        collector = Collector(fixture_hash)
        signal.signal(signal.SIGINT, interrupt)
        signal.signal(signal.SIGTERM, interrupt)
        process = subprocess.Popen([str(args.binary.resolve()), "-test.v", "-test.run=^TestRelayTrafficObservation$", "-test.timeout=10m"],
                                   cwd=args.source, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                   env=child_environment(fixture_hash))
        reader = threading.Thread(target=collector.consume, args=(process.stdout,), daemon=True)
        reader.start()
        code = process.wait(timeout=11 * 60)
        result["execution_completed"] = True
        reader.join(timeout=3)
        require(not reader.is_alive() and not collector.invalid and collector.last is not None, "aggregate_report_unavailable")
        require(code == 0 and collector.last["outcome"] == "passed", "relay_fixture_failed")
        result["outcome"] = "passed"
    except Cancelled:
        result.update(outcome="cancelled", failure_code="measurement_cancelled")
    except subprocess.TimeoutExpired:
        result["failure_code"] = "measurement_timeout"
    except TrafficError as error:
        result["failure_code"] = str(error)
    except Exception:
        result["failure_code"] = "measurement_unavailable"
    finally:
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        try:
            stop_owned(process)
            if reader is not None:
                reader.join(timeout=3)
        except Exception:
            result.update(outcome="failed", failure_code="owned_process_cleanup_incomplete")
        if collector is not None and not collector.invalid:
            result["report"] = collector.last
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(result, sort_keys=True) + "\n", encoding="utf-8")
    print("Relay-only aggregate observation " + result["outcome"] + "; see the content-free report.")
    return 0 if result["outcome"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
