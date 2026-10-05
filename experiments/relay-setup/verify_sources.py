#!/usr/bin/env python3
"""Fail closed if the audited production/fixture sources have drifted."""
import hashlib
import json
from pathlib import Path

root=Path(__file__).resolve().parents[2]
hashes=json.loads(Path(__file__).with_name('source_hashes.json').read_text())
for path, expected in hashes.items():
    content=(root/path).read_bytes().replace(b'\r\n', b'\n')
    if hashlib.sha256(content).hexdigest() != expected:
        raise SystemExit('Audited source changed; review and refresh relay experiment fixture hashes')
print('Relay experiment source hashes verified')
