"""Only the immutable 0.3.0-alpha.2 resource-observation baseline.

That release predates Darwin CGO provenance. Keep its hash-pinned asset policy
without relaxing current release gates, strict JSON or cryptographic checks.
"""
import argparse
import base64
import hashlib
import os
import pathlib
import types
import urllib.request
from source_provenance import read_json

VERSION = '0.3.0-alpha.2'
COMMIT = '00cc6a99809df77bf1754936ea7bf5ca4c5d0741'
RELEASE_ID = '402850401'
VALIDATOR_SHA256 = 'cd82d293da01ce947bcd78e79494a498e8058d6c495f77cbb03fd3d7b84d7bcd'
VALIDATOR_URL = ('https://raw.githubusercontent.com/webkaz-labs/sobalink/' + COMMIT
                 + '/.github/scripts/release-validation.py')


def require(condition, code):
    if not condition:
        raise ValueError(code)


def baseline(environment):
    require(environment.get('RELEASE_VERSION') == VERSION
            and environment.get('TESTED_COMMIT') == COMMIT
            and environment.get('RELEASE_ID') == RELEASE_ID, 'historical-baseline-mismatch')


def fetch_source():
    # Public source fetch never carries the Actions token.
    with urllib.request.urlopen(VALIDATOR_URL, timeout=60) as response:
        return response.read(65537)


def strict_assets(root, validator, version, commit, bundle):
    require(version == VERSION and commit == COMMIT, 'historical-baseline-mismatch')
    expected = validator.filenames(version) | ({'packslip.sigstore.json'} if bundle else set())
    files = list(root.iterdir())
    require({p.name for p in files} == expected
            and all(p.is_file() and not p.is_symlink() for p in files), 'historical-asset-inventory')
    for path in files:
        if path.suffix == '.json':
            value = read_json(path.read_bytes())
            if path.name == 'packslip.sigstore.json':
                read_json(base64.b64decode(value['dsseEnvelope']['payload'], validate=True))


def load_validator(environment):
    baseline(environment)
    source = fetch_source()
    # No import/compile/execution of downloaded code before exact hash admission.
    require(len(source) <= 65536 and hashlib.sha256(source).hexdigest() == VALIDATOR_SHA256,
            'historical-validator-digest')
    validator = types.ModuleType('sobalink_historical_resource_validator')
    exec(compile(source, '<hash-pinned-historical-validator>', 'exec'), validator.__dict__)
    old_state, old_assets, old_bundle = validator.release_state, validator.check_assets, validator.check_bundle

    def release_state(version, commit, required=False, published=False):
        require(version == VERSION and commit == COMMIT and required and published,
                'historical-baseline-mismatch')
        ref, release = old_state(version, commit, required=True, published=True)
        require(type(release['id']) is int and release['id'] == int(RELEASE_ID), 'historical-release-id')
        return ref, release

    def check_assets(root, version, commit, bundle=False):
        strict_assets(root, validator, version, commit, bundle)
        return old_assets(root, version, commit, bundle=bundle)

    def check_bundle(root, version, commit):
        strict_assets(root, validator, version, commit, True)
        return old_bundle(root, version, commit)

    validator.release_state = release_state
    validator.check_assets = check_assets
    validator.check_bundle = check_bundle
    return validator


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=('bundle',))
    parser.add_argument('--root', type=pathlib.Path, required=True)
    args = parser.parse_args()
    try:
        load_validator(os.environ).check_bundle(args.root, VERSION, COMMIT)
    except Exception:
        raise SystemExit('Historical resource bundle verification failed; details withheld') from None


if __name__ == '__main__':
    main()
