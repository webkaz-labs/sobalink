#!/usr/bin/env python3
"""Hosted-CI-only loopback tests. Persist an allowlisted summary, never raw logs."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

TESTS = ["TestRelaySetupExperimentLifecycle", "TestRelaySetupExperimentBootstrap", "TestTrustedRelayTwoPeerIntegration"]

def summarize(stdout, returncode, binary_hash):
    passed = [name for name in TESTS if "--- PASS: " + name + " " in stdout]
    lease = "ordered rounds across a real two-minute lease" in stdout
    ok = returncode == 0 and passed == TESTS and lease and not any("--- SKIP: " + name in stdout for name in TESTS)
    summary = {"schema":"relay-operations-experiment-v1", "binary_sha256":binary_hash, "tests_passed":passed, "loopback_lifecycle":TESTS[0] in passed, "in_memory_bootstrap_auth":TESTS[1] in passed, "native_two_minute_lease":lease, "direct_udp_underlay_compiled_out":True, "physical_lan_proven":False, "os_login_or_suspend_proven":False, "automatic_remote_deployment_proven":False, "automatic_election_proven":False, "passed":ok}
    return summary

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--report", type=Path, required=True)
    args = p.parse_args()
    if os.environ.get("GITHUB_ACTIONS") != "true":
        raise SystemExit("Native relay operations are restricted to the authorized hosted CI job")
    env = dict(os.environ, SOBALINK_RUN_RELAY_SETUP_EXPERIMENT="1", SOBALINK_RUN_LAN_INTEGRATION="1")
    # Isolated localhost fixture: never pass a proxy to a synthetic relay.
    for key in list(env):
        if key.upper() in {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "TS_PROXY"}:
            del env[key]
    result = subprocess.run([str(args.binary.resolve()), "-test.v", "-test.timeout=300s", "-test.run=^(TestRelaySetupExperiment.*|TestTrustedRelayTwoPeerIntegration)$"], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=320)
    summary = summarize(result.stdout, result.returncode, hashlib.sha256(args.binary.read_bytes()).hexdigest())
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(summary, indent=2)+"\n")
    print(json.dumps(summary, sort_keys=True))
    if not summary["passed"]:
        raise SystemExit("Relay operations failed; raw output is not published")

if __name__ == "__main__":
    main()
