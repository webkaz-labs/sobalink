#!/usr/bin/env python3
"""Run only the bounded context-control test gates shared by CI and prerelease.

These tags belong to the selected go test command only. They must never enter
GOFLAGS or any product build/package command. Exact parent and subtest passes
are required by ci-go-test.py; missing, skipped or failed cases fail the gate.
"""

import argparse
from pathlib import Path
import subprocess
import sys


MODULE = "github.com/webkaz-labs/sobalink"
PRODUCT_TAGS = "ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"
# Frozen test inventory. Keep the source-inventory checks in sync when changing
# a reviewed case; a passing parent cannot replace any required child event.
CORE_CASES = {
    "TestContextCoreTLSPreparePublishesBeforeReply": (),
    "TestContextCoreTLSCommitAndStatusAreReceiptBound": (
        "prepared-status",
        "commit-save",
        "duplicate-commit",
        "committed-status",
        "wrong-commit-binding",
        "wrong-status-binding",
        "reopened-prepared-status",
        "reopened-committed-status",
    ),
    "TestContextCoreTLSCompletionRejectsStaleAdmission": (
        "core-op-contention",
        "changed-process",
        "changed-store",
        "changed-current-owner",
        "wrong-association-pointer",
        "same-byte-revision",
        "genuine-proof-reuse",
    ),
    "TestContextCoreTLSPreReadCutoffRejectsLateArm": (
        "first-arm-after-cutoff",
        "replacement-arm-after-cutoff",
    ),
    "TestContextCoreTLSPostPublicationStopPreservesDisk": (),
    "TestContextCoreTLSUncertainPublicationStopsOwnerAfterUnlock": (),
    "TestContextCoreTLSResponseRejectsLaterWriteOrStop": (
        "same-byte-whole-file-write",
        "terminal-owner-stop",
    ),
    "TestContextCoreTLSBridgeJoinsOneOwnedExchange": (
        "success",
        "authenticated-peer-close",
        "canceled-context",
        "unreleased-read-gate-deadline",
        "unreleased-read-gate-close",
        "close-before-exchange",
    ),
    "TestContextTLSFixtureExcludedFromProductBuilds": (
        "linux-amd64/default",
        "linux-amd64/product",
        "linux-amd64/directlan-integration",
        "linux-amd64/directlan-lifecycle",
        "linux-amd64/lanlink-integration",
        "linux-amd64/lanlink-without-udp",
        "linux-amd64/e2e",
        "linux-arm64/default",
        "linux-arm64/product",
        "linux-arm64/directlan-integration",
        "linux-arm64/directlan-lifecycle",
        "linux-arm64/lanlink-integration",
        "linux-arm64/lanlink-without-udp",
        "linux-arm64/e2e",
        "darwin-arm64/default",
        "darwin-arm64/product",
        "darwin-arm64/directlan-integration",
        "darwin-arm64/directlan-lifecycle",
        "darwin-arm64/lanlink-integration",
        "darwin-arm64/lanlink-without-udp",
        "darwin-arm64/e2e",
        "windows-amd64/default",
        "windows-amd64/product",
        "windows-amd64/directlan-integration",
        "windows-amd64/directlan-lifecycle",
        "windows-amd64/lanlink-integration",
        "windows-amd64/lanlink-without-udp",
        "windows-amd64/e2e",
    ),
}
TCP_CASES = {
    "TestContextControlFixedTCPPrepareExchange": (),
    "TestContextControlFixedTCPWrongServerPin": (),
}
SUITES = {
    "core-tls": ("internal/core", "directlan_context_fixture", "3m", CORE_CASES),
    "fixed-tcp": ("internal/directlan", "directlan_context_tcp", "45s", TCP_CASES),
}


def command(suite):
    package, tag, timeout, cases = SUITES[suite]
    expected = [MODULE + "/" + package + ":" + test + suffix
                for test, children in cases.items()
                for suffix in ("", *("/" + child for child in children))]
    wrapper = [sys.executable, str(Path(__file__).with_name("ci-go-test.py"))]
    for case in expected:
        wrapper.extend(("--expect", case))
    return wrapper + ["--", "go", "test", "-race", "-count=1", "-v",
                      "-timeout=" + timeout, "-tags=" + tag + "," + PRODUCT_TAGS,
                      "-run=^(" + "|".join(cases) + ")$", "./" + package]


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "error: Select one fixed context-control test suite; use --help.\n")


def main(argv=None):
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=SUITES)
    args = parser.parse_args(argv)
    try:
        code = subprocess.run(command(args.suite), check=False, shell=False).returncode
        return code if code >= 0 else 128 - code
    except OSError:
        print("error: Context-control test gate could not start.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
