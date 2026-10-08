#!/usr/bin/env python3
"""Exact opt-in native activation and synthetic restart acceptance gates.

These gates establish only isolated native fixture behavior, not real-device,
OS login/suspend, or browser-session restart acceptance. Product tags stay fixed.
"""
import argparse
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile

PRODUCT_TAGS = "ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"
SUITES = {
    "activation": ("internal/core", "directlan_activation_native", "8m", {
        "TestActivationNativeBilateralReviewedController": (),
        "TestActivationNativeControllerCancellationAndDeadline": ("cancel", "deadline"),
        "TestActivationNativeLostFirstPrepareReplyRecovery": (),
        "TestActivationNativeOrdinaryCompletionHandoff": (),
        "TestActivationNativeFinalPublicationDeniesEndedLifetime": ("cancel", "deadline"),
    }),
    "restart": ("cmd/soba", "managed_restart_native", "120s", {
        "TestManagedRestartNativeCleanSuccessor": (),
        "TestManagedRestartNativeShutdownFailures": ("ipc-error", "core-error", "lock-error", "lost-ack", "wrong-ack", "no-exit", "cancel"),
        "TestManagedRestartNativeLostStopReplyStillRequiresAckAndExit": (),
        "TestManagedRestartNativeRejectsWrongOwner": (),
        "TestManagedRestartNativeUIPrivateBoundary": (),
        "TestManagedRestartNativeStartupOutcomes": ("clean", "not-ready", "cancel", "exit-before-ready"),
    }),
}


def command(suite):
    package, tag, timeout, cases = SUITES[suite]
    args = [sys.executable, str(Path(__file__).with_name("ci-go-test.py"))]
    for name, children in cases.items():
        for suffix in ("", *("/" + child for child in children)):
            args.extend(("--expect", "github.com/webkaz-labs/sobalink/" + package + ":" + name + suffix))
    return args + ["--", "go", "test", "-race", "-count=1", "-v", "-timeout=" + timeout,
                   "-tags=" + tag + "," + PRODUCT_TAGS,
                   "-run=^(" + "|".join(cases) + ")$", "./" + package]


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "error: Select one fixed activation gate; use --help.\n")


def main(argv=None):
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=SUITES)
    args = parser.parse_args(argv)
    env = {k: v for k, v in os.environ.items()
           if k.upper() not in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "TS_PROXY")
           and not k.upper().startswith("TS_")}
    env.update(GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOTELEMETRY="off")
    if args.suite == "activation":
        env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"
    # The workflow step is the cross-platform fail-stop boundary. A Python
    # timeout would kill only the wrapper and leave its Go/test descendants.
    if os.environ.get("GITHUB_ACTIONS") != "true":
        print("error: This gate requires its bounded GitHub Actions step.", file=sys.stderr)
        return 2
    tmp = None
    completed = False
    try:
        # macOS runner TMPDIR may exceed Unix socket path limits.
        base = None if os.name == "nt" else "/tmp"
        tmp = tempfile.mkdtemp(prefix="sa-", dir=base)
        env.update(TMPDIR=tmp, TMP=tmp, TEMP=tmp)
        code = subprocess.run(command(args.suite), env=env, check=False, shell=False).returncode
        completed = code == 0
        return code if code >= 0 else 128 - code
    except OSError:
        print("error: Native activation gate failed to start or complete.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130
    finally:
        if tmp is not None:
            if completed:
                shutil.rmtree(tmp)
            else:
                # Retain possible live-child files for runner teardown. Step
                # cancellation is not a joined-cleanup receipt.
                print("Native gate temporary files retained for runner cleanup.", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
