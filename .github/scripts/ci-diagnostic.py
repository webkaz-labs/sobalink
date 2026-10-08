#!/usr/bin/env python3
"""Select an exact-head, diagnostic-only suite from a newly created branch."""
import json
import os
from pathlib import Path
import re
import subprocess

REPOSITORY = 'webkaz-labs/sobalink'
REF = re.compile(r'refs/heads/diagnostic/(web|product|activation)/([0-9a-f]{12})-([a-z0-9][a-z0-9-]{0,31})\Z')


def select(event_name, event, repository, ref, sha, checkout_sha):
    match = REF.fullmatch(ref)
    metadata = event.get('repository') or {}
    if (event_name != 'create' or event.get('ref_type') != 'branch'
            or repository != REPOSITORY or metadata.get('full_name') != repository
            or metadata.get('private') is not False
            or not re.fullmatch(r'[0-9a-f]{40}', sha)
            or checkout_sha != sha or not match
            or match[2] != sha[:12]
            or event.get('ref') != ref.removeprefix('refs/heads/')):
        raise ValueError('not a valid exact-head diagnostic branch creation')
    return match[1]


def main():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text(encoding='utf-8'))
    sha = os.environ['GITHUB_SHA']
    checkout_sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    suite = select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                   os.environ['GITHUB_REF'], sha, checkout_sha)
    with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
        print('suite=' + suite, file=output)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as output:
        print('Diagnostic suite: ' + suite + '\n\nExact source: ' + sha +
              '\n\nDiagnostic feedback only. This does not establish ci-required, '
              'full native coverage, package validation, or release readiness.', file=output)


if __name__ == '__main__':
    main()
