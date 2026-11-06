#!/usr/bin/env python3
"""Select an acceleration-only browser cache; never supply release evidence."""
import hashlib
import json
import os
import subprocess


def cache_scope(env, event):
    repository = 'webkaz-labs/sobalink'
    metadata = event.get('repository') or {}
    if (env.get('GITHUB_REPOSITORY') != repository
            or metadata.get('full_name') != repository
            or metadata.get('default_branch') != 'main'
            or metadata.get('private') is not False
            or env.get('GITHUB_JOB') != 'browser'
            or env.get('RUNNER_OS') != 'Linux' or env.get('RUNNER_ARCH') != 'X64'):
        return ''
    ref, name = env.get('GITHUB_REF', ''), env.get('GITHUB_EVENT_NAME', '')
    if ref == 'refs/heads/main' and name in ('push', 'workflow_dispatch', 'schedule'):
        return 'main'
    if name == 'workflow_dispatch' and ref.startswith('refs/heads/') and ref != 'refs/heads/':
        return 'branch-' + hashlib.sha256(ref.encode('utf-8')).hexdigest()
    if name == 'pull_request':
        number, pull = event.get('number'), event.get('pull_request') or {}
        head = (pull.get('head') or {}).get('repo') or {}
        base = (pull.get('base') or {}).get('repo') or {}
        if (type(number) is int and number > 0 and pull.get('number') == number
                and ref == f'refs/pull/{number}/merge'
                and head.get('full_name') == repository and base.get('full_name') == repository):
            return f'pr-{number}'
    return ''


def main():
    with open(os.environ['GITHUB_EVENT_PATH'], encoding='utf-8') as source:
        scope = cache_scope(os.environ, json.load(source))
    values = subprocess.check_output(
        ['go', 'env', 'GOVERSION', 'GOOS', 'GOARCH', 'GOMODCACHE', 'GOCACHE'],
        text=True, timeout=30).splitlines()
    if len(values) != 5 or values[:3] != ['go1.27.1', 'linux', 'amd64']:
        raise SystemExit('Browser cache requires the exact Go toolchain and target')
    with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
        for key, value in (('scope', scope), ('gomodcache', values[3]), ('gocache', values[4])):
            print(f'{key}={value}', file=output)


if __name__ == '__main__':
    main()
