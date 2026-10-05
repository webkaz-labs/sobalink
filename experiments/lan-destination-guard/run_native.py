#!/usr/bin/env python3
"""Run only bounded experiment tests and publish an allowlisted aggregate.
Raw output remains local to the disposable runner and is never an artifact.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

TESTS = ["TestLANExperimentFakeEntryPoints", "TestLANExperimentFakeRetryRevocation", "TestLANExperimentFakeNilPolicy", "TestLANExperimentNativeLoopback"]

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--binary",type=Path,required=True)
    p.add_argument("--report",type=Path,required=True)
    args=p.parse_args()
    env=dict(os.environ, LAN_GUARD_REAL_SOCKETS="1")
    result=subprocess.run([str(args.binary.resolve()), "-test.v", "-test.timeout=60s", "-test.run=^TestLANExperiment"], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=75)
    successful=[name for name in TESTS if "--- PASS: "+name+" " in result.stdout]
    skipped=any("--- SKIP:"+name in result.stdout or "--- SKIP: "+name in result.stdout for name in TESTS)
    ok=result.returncode==0 and successful==TESTS and not skipped and "native_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1" in result.stdout
    report={
        "schema":"lan-write-boundary-experiment-v1",
        "engine":"tailscale.com@v1.104.0",
        "binary_sha256":hashlib.sha256(args.binary.read_bytes()).hexdigest(),
        "tests_passed":successful,
        "native_loopback": "passed" if "TestLANExperimentNativeLoopback" in successful else "not_proven",
        "batching_available": "native_batching=true" in result.stdout,
        "allowed_datagrams":7 if "native_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1" in result.stdout else 0,
        "denied_entrypoints":5 if "native_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1" in result.stdout else 0,
        "policy_revocations":1 if "native_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1" in result.stdout else 0,
        "full_engine_package":True,
        "host_no_egress_proven":False,
        "physical_lan_proven":False,
        "tcp_icmp_coverage":False,
        "passed":ok,
    }
    args.report.parent.mkdir(parents=True,exist_ok=True)
    args.report.write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps(report,sort_keys=True))
    if not ok:
        # Only synthetic test names and generic failure messages enter public logs.
        for line in result.stdout.splitlines():
            if line.startswith("--- FAIL: TestLANExperiment"):
                print(line)
        raise SystemExit("LAN experiment failed; no no-egress claim is made")
if __name__=="__main__":main()
