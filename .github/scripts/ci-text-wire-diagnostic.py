#!/usr/bin/env python3
"""Private capture of one exact diagnostic; emit only fixed enum records."""
import re
import subprocess
import sys
import tempfile
from pathlib import Path

OP = r'(?:status|deliver|offer|wrong-epoch|other)'
CAT = r'(?:none|deadline|canceled|eof|unexpected_eof|network_timeout|message_conflict|message_unknown_lease|message_storage_busy|message_permission|other)'
RECORDS = [
    re.compile(r'\s+[A-Za-z0-9_]+\.go:[0-9]+: (SOBA_TEXT_WIRE operation=' + OP + r' stage=(?:lease_lookup|peer_json|peer_json_returned) category=' + CAT + r' context=' + CAT + r')\s*'),
    re.compile(r'\s+[A-Za-z0-9_]+\.go:[0-9]+: (SOBA_TEXT_REPLAY index=[0-3] operation=(?:status|deliver|offer) stage=begin)\s*'),
]

def run_private(command):
    # Neither stdout nor stderr is inherited; never upload this private capture.
    with tempfile.TemporaryFile() as private:
        try:
            result = subprocess.run(command, stdout=private, stderr=private, check=False)
        except OSError:
            print('TEXT_WIRE_DIAGNOSTIC status=launch_failed release_validation=excluded')
            return 1
        private.seek(0)
        for raw in private:
            try:
                line = raw.decode('utf-8').rstrip('\r\n')
            except UnicodeError:
                continue
            for pattern in RECORDS:
                match = pattern.fullmatch(line)
                if match:
                    print(match.group(1))
                    break
    status = 'passed' if result.returncode == 0 else 'failed'
    print('TEXT_WIRE_DIAGNOSTIC status=' + status + ' release_validation=excluded')
    return 0 if result.returncode == 0 else 1

if __name__ == '__main__':
    package = 'github.com/webkaz-labs/sobalink/internal/core'
    test = 'TestDurableTextProcessRestartACKLossAndHiddenReplay'
    command = [sys.executable, str(Path(__file__).with_name('ci-go-test.py')), '--exact', '--expect', package + ':' + test, '--', 'go', 'test', '-race', '-count=1', '-timeout=4m', '-tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy', './internal/core', '-run', '^' + test + '$']
    raise SystemExit(run_private(command))
