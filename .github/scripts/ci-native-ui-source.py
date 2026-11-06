#!/usr/bin/env python3
"""Verify reusable native UI source; manual smoke never issues a CI receipt."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'webkaz-labs/sobalink'
CALLERS = {
    'scope': ('.github/workflows/ci.yml', 'Cross-platform CI'),
    'smoke': ('.github/workflows/native-poc.yml', 'Native UI PoC validation'),
}


def load(name):
    path = ROOT / '.github/scripts' / (name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify(mode, plan_path, environment=None):
    env = os.environ if environment is None else environment
    if mode not in CALLERS:
        raise ValueError('unknown native UI validation mode')
    workflow, name = CALLERS[mode]
    ref = env.get('GITHUB_REF', '')
    if (env.get('GITHUB_REPOSITORY') != REPOSITORY or not ref.startswith('refs/')
            or env.get('GITHUB_WORKFLOW') != name
            or env.get('GITHUB_WORKFLOW_REF') != f'{REPOSITORY}/{workflow}@{ref}'):
        raise ValueError('native UI caller identity differs')
    event = json.loads(Path(env['GITHUB_EVENT_PATH']).read_text(encoding='utf-8'))
    repo = event.get('repository', {})
    if repo.get('full_name') != REPOSITORY or repo.get('private') is not False:
        raise ValueError('native UI repository identity differs')
    impact = load('ci-impact')
    sha = impact.object_id(ROOT, 'HEAD^{commit}')
    if env.get('GITHUB_SHA') != sha:
        raise ValueError('native UI checkout differs from the run source')
    tree = impact.object_id(ROOT, 'HEAD^{tree}')
    index = impact.validate_checkout(ROOT, sha)
    inventory = impact.parse_tree(impact.git(ROOT, 'ls-tree', '-r', '-z', '--full-tree', sha))
    if index != inventory.keys():
        raise ValueError('native UI checkout index differs from the source tree')
    if mode == 'scope':
        if env.get('GITHUB_EVENT_NAME') not in ('pull_request', 'push'):
            raise ValueError('required native UI scope needs a PR or main-push event')
        # Repeat the entire event/delta/policy proof, not merely artifact fields.
        plan = load('ci-coverage').verified_plan(plan_path)
        if plan['scope'] != 'native-ui' or plan['head_sha'] != sha or plan['head_tree'] != tree:
            raise ValueError('main CI requires an eligible native UI merge plan')
    elif (env.get('GITHUB_EVENT_NAME') != 'workflow_dispatch'
          or not (event.get('inputs', {}).get('ci_lane_smoke') is True
                  or event.get('inputs', {}).get('ci_lane_smoke') == 'true')):
        raise ValueError('native UI smoke requires an explicit manual selection')
    # Logs identify this invocation only. No GITHUB_OUTPUT, plan or success
    # receipt is written; manual checks cannot satisfy the main CI aggregate.
    print(f'Native UI {mode} source: {sha}; tree: {tree}')
    if mode == 'smoke':
        print('Manual smoke only: no native-ui scope, required CI or release credit')
    return sha, tree


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('mode', choices=tuple(CALLERS))
    parser.add_argument('--plan', required=True, type=Path)
    args = parser.parse_args(argv)
    verify(args.mode, args.plan)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('Native UI source validation failed', file=sys.stderr)
        sys.exit(1)
