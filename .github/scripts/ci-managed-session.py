#!/usr/bin/env python3
"""Run one bounded managed-session acceptance gate with exact required PASS events.

Tags apply only to selected tests. Product build flags remain unchanged.
Workflow order is TLS, outbound TCP, then three separate native invocations.
Plain synthetic admission tests remain in the normal untagged race suite.
"""

import argparse
import os
from pathlib import Path
import subprocess
import sys

MODULE = "github.com/webkaz-labs/sobalink/internal/directlan"
PRODUCT_TAGS = "ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"
SUITES = {'tls': ('directlan_managed_session_tls',
         '90s',
         {'TestManagedInboundTLSBoundSessionOnly': ('session', 'pair-context-status'),
          'TestManagedFailedCloseRetainsAndSeals': (),
          'TestManagedTLSRefusesDowngradeAndWrongPin': ('v1', 'missing-alpn', 'wrong-pin'),
          'TestManagedTLSRejectsInvalidFrames': ('wrong-binding',
                                                 'v1-frame',
                                                 'noncanonical',
                                                 'oversize',
                                                 'truncated',
                                                 'lost-reply'),
          'TestManagedUnadmittedDialCleanupHelper': ('error-with-connection',
                                                     'late-after-stop',
                                                     'stale-owner-result'),
          'TestManagedUnadmittedCleanupSuccessAndNil': (),
          'TestManagedRejectedAcceptCleanupBeforeIdentity': ('invalid-address',
                                                             'accept-error-with-connection'),
          'TestManagedDeadlineFailureRetainsThroughGenerationCompletion': ()}),
 'outbound-tcp': ('directlan_managed_session_outbound_tcp',
                  '90s',
                  {'TestManagedOutboundBoundReplyComposition': ('exact',
                                                                'wrong-binding',
                                                                'wrong-operation',
                                                                'v1',
                                                                'noncanonical',
                                                                'oversize',
                                                                'truncated',
                                                                'lost-reply',
                                                                'wrong-pin',
                                                                'cancelled',
                                                                'stale-registration',
                                                                'stale-peer',
                                                                'stopped-owner')}),
 'native-caller': ('directlan_managed_session_native',
                   '120s',
                   {'TestManagedNativeColdCallerRoles': ('caller-0', 'caller-1')}),
 'native-simultaneous': ('directlan_managed_session_native',
                         '120s',
                         {'TestManagedNativeSimultaneousColdCallers': ()}),
 'native-udp-capacity': ('directlan_managed_session_native',
                         '120s',
                         {'TestManagedNativeFreshUDPAndSingleFlowControlCapacity': ('caller-0', 'caller-1')})}


def command(suite):
    tag, timeout, cases = SUITES[suite]
    wrapper = [sys.executable, str(Path(__file__).with_name("ci-go-test.py"))]
    for name, children in cases.items():
        for suffix in ("", *("/" + child for child in children)):
            wrapper.extend(("--expect", MODULE + ":" + name + suffix))
    return wrapper + ["--", "go", "test", "-race", "-count=1", "-v",
                      "-timeout=" + timeout, "-tags=" + tag + "," + PRODUCT_TAGS,
                      "-run=^(" + "|".join(cases) + ")$", "./internal/directlan"]


class ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "error: Select one fixed managed-session gate; use --help.\n")


def main(argv=None):
    parser = ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=SUITES)
    args = parser.parse_args(argv)
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local")
    try:
        code = subprocess.run(command(args.suite), env=env, check=False, shell=False,
                              timeout=180).returncode
        return code if code >= 0 else 128 - code
    except (OSError, subprocess.TimeoutExpired):
        print("error: Managed-session gate failed to start or exceeded its bound.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
