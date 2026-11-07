"""Keep setup concurrency bounded, joined and separate from acceptance evidence.

The reviewed source regions intentionally bind every command, condition, version,
resource owner and wait. They do not establish hosted runtime or speed evidence.
"""
import copy
import hashlib
import json
from pathlib import Path
import re
import unittest

import ci_full_validation as full

ROOT = Path(__file__).resolve().parents[2]
HASHES = {'documents': '09c0a484aba425a640badc70546bc349dee98edb6c3b7511d9f4b7791d0ea871', 'supplemental': 'f1b1e770ffe92cee7766a9a48f88feae4e969bb5454a708d9eaf3aa7e8ef61b4', 'product-activation': '6f78614a9981e33235310af423b8a69d5f9a2d114a366a915b955d831382379f', 'web-activation': '129d7c8ef56c820ae70a5baf403c0d29805d67737628fd47fa8219816fc0cb49'}


def job(source, name):
    matches = re.findall(r'^  ' + re.escape(name) + r':\n(.*?)(?=^  [a-z][a-z-]*:\n|\Z)', source, re.S | re.M)
    if len(matches) != 1:
        raise ValueError('missing or duplicate preparation job')
    return matches[0]


def checked_region(source, kind):
    if kind in ('documents', 'supplemental'):
        pattern = r'^      - parallel:\n.*?(?=^      - |\Z)'
        consumer = ('      - name: Require document checks and selected rendering\n'
                    if kind == 'documents' else
                    '      - name: Reproduce SVGs and render every diagram\n')
        expected_controls = ['parallel']
    else:
        pattern = (r'^      - name: Prepare verified activation engine and Go dependencies\n'
                   r'.*?(?=^      - name: Verify fixed )')
        consumer = ('      - name: Verify fixed product acceptance policy\n' if kind == 'product-activation'
                    else '      - name: Verify fixed Web acceptance policy\n')
        expected_controls = ['background', 'wait']
    matches = list(re.finditer(pattern, source, re.S | re.M))
    if len(matches) != 1:
        raise ValueError('missing or duplicate joined preparation region')
    match = matches[0]
    if hashlib.sha256(match[0].encode()).hexdigest() != HASHES[kind]:
        raise ValueError('unreviewed preparation command or dependency')
    if not source[match.end():].startswith(consumer):
        raise ValueError('preparation must join before its consumer')
    # Inspect only YAML control lines, not arbitrary shell strings.
    controls = re.findall(r'^\s*(?:- )?(background|parallel|wait|wait-all|cancel):', source, re.M)
    if controls != expected_controls:
        raise ValueError('unreviewed preparation concurrency')
    return match[0]


class PreparationOverlapTests(unittest.TestCase):
    def read(self, name):
        return (ROOT / '.github/workflows' / name).read_text()

    def test_documents_join_disjoint_system_and_node_writers_before_rendering(self):
        section = job(self.read('ci.yml'), 'documents')
        region = checked_region(section, 'documents')
        self.assertEqual(region.count('          - name:'), 2)
        self.assertEqual(region.count("if: steps.document-selection.outputs.render_required == 'true'"), 2)
        system, node = region.split('          - name: Install isolated locked document rendering dependencies')
        self.assertNotIn('npm ', system)
        self.assertNotIn('apt-get', node)
        self.assertIn("/usr/bin/python3 -I -c 'from PIL import Image'", system)
        self.assertIn('npm ci --prefix "$DOCS_RENDER_ROOT"', node)
        self.assertIn('PUPPETEER_CACHE_DIR: ${{ runner.temp }}/docs-review-browser', node)
        self.assertLess(section.index('uses: actions/setup-node@'), section.index(region))
        self.assertNotIn('continue-on-error', region)

    def test_npm_install_cannot_build_assets_while_go_lists_embedded_inputs(self):
        lifecycle_hooks = {'preinstall', 'install', 'postinstall', 'prepublish',
                           'preprepare', 'prepare', 'postprepare'}
        for path in ('web/package.json', '.github/docs-render/package.json'):
            package = json.loads((ROOT / path).read_text())
            with self.subTest(path=path):
                self.assertFalse(lifecycle_hooks & package.get('scripts', {}).keys())

    def test_supplemental_documents_use_the_same_resource_partition(self):
        checked_region(job(self.read('docs-review.yml'), 'documents'), 'supplemental')

    def test_activation_go_and_web_preparation_join_before_acceptance(self):
        for filename, prefix in (('ci.yml', ''), ('activation-diagnostic.yml', 'diagnostic-')):
            for kind in ('product-activation', 'web-activation'):
                with self.subTest(filename=filename, kind=kind):
                    section = job(self.read(filename), prefix + kind)
                    region = checked_region(section, kind)
                    go, node = region.split('      - uses: actions/setup-node@')
                    self.assertNotIn('npm ', go)
                    self.assertNotIn('web/dist', go)
                    self.assertNotIn('go run ', node)
                    self.assertNotIn('go mod ', node)
                    self.assertLess(section.index('uses: actions/setup-go@'), section.index(region))
                    self.assertEqual(region.count('go mod download\n'), 1)
                    self.assertNotIn('go mod download all', region)
                    self.assertNotIn('continue-on-error', region)
                    self.assertNotIn('run: |', region.split('        wait: activation-go')[1])

    def test_document_mutations_cannot_skip_dependencies_or_lose_failure_join(self):
        section = job(self.read('ci.yml'), 'documents')
        cases = (
            section.replace('      - parallel:', '      - serial:'),
            section.replace("            if: steps.document-selection.outputs.render_required == 'true'\n", '', 1),
            section.replace('            timeout-minutes: 10', '            continue-on-error: true\n            timeout-minutes: 10', 1),
            section.replace('              npm install', '              sudo apt-get update\n              npm install', 1),
            section.replace('      - name: Require document checks', '      - run: echo unreviewed\n      - name: Require document checks', 1),
            section.replace('      - parallel:', '      - parallel:\n          - run: true', 1),
        )
        for changed in cases:
            with self.subTest(case=cases.index(changed)), self.assertRaises(ValueError):
                checked_region(changed, 'documents')

    def test_activation_mutations_cannot_race_toolchains_or_weaken_wait(self):
        for kind in ('product-activation', 'web-activation'):
            section = job(self.read('ci.yml'), kind)
            cases = (
                section.replace('        wait: activation-go', '        wait: other'),
                section.replace('        wait: activation-go', '        wait-all:'),
                section.replace('        background: true', '        background: false'),
                section.replace('        background: true', '        background: true\n        continue-on-error: true'),
                section.replace('          go mod verify', '          npm --prefix web ci\n          go mod verify'),
                section.replace('          go mod verify', '          npm --prefix web run build\n          go mod verify'),
                section.replace('      - name: Wait for activation Go preparation\n        wait: activation-go\n', ''),
                section.replace('      - name: Verify fixed ', '      - run: echo unreviewed\n      - name: Verify fixed ', 1),
            )
            for changed in cases:
                with self.subTest(kind=kind, case=cases.index(changed)), self.assertRaises(ValueError):
                    checked_region(changed, kind)


class PreparationFullProofTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        cls.workflow = cls.sources[full.WORKFLOW].decode()

    def test_exact_reviewed_jobs_and_all_async_controls_are_admitted(self):
        full.validate_preparation_overlap_workflow(self.workflow.encode())
        full.validate_parallel_workflow(self.workflow.encode())
        required = full.required_jobs(self.sources)
        for kind in ('product-activation', 'web-activation'):
            self.assertEqual(required[kind][:3], (
                'Prepare verified activation engine and Go dependencies',
                'Prepare locked dependencies and official Chromium',
                'Wait for activation Go preparation'))
            self.assertEqual(len(required[kind]), 5)
        self.assertEqual(required['product-activation'][3:], (
            'Verify fixed product acceptance policy',
            'Build normal assets and run two private production-entry cases'))
        self.assertEqual(required['web-activation'][3:], (
            'Verify fixed Web acceptance policy',
            'Compile and run seven private synthetic-owner cases'))

    def test_full_proof_rejects_relaxed_conditional_docs_and_extra_writers(self):
        section = job(self.workflow, 'documents')
        mutations = (
            section.replace("render_required == 'true'", "render_required != 'true'", 1),
            section.replace("            if: steps.document-selection.outputs.render_required == 'true'\n", '', 1),
            section.replace('      - parallel:', '      - parallel:\n          - run: echo extra'),
            section.replace('              npm ci --prefix', '              sudo apt-get update\n              npm ci --prefix'),
            section.replace('            timeout-minutes: 10', '            continue-on-error: true\n            timeout-minutes: 10', 1),
            section.replace('      - parallel:', '      - run: echo extra-writer\n      - parallel:', 1),
            section.replace('      - name: Require document checks', '      - run: echo extra-writer\n      - name: Require document checks', 1),
            section.replace('    timeout-minutes: 10', '    continue-on-error: true\n    timeout-minutes: 10', 1),
        )
        for index, changed in enumerate(mutations):
            data = self.workflow.replace(section, changed, 1).encode()
            self.assertNotEqual(data, self.sources[full.WORKFLOW])
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_preparation_overlap_workflow(data)
        changed = dict(self.sources)
        changed[full.WORKFLOW] = self.workflow.replace(section, mutations[-1], 1).encode()
        with self.assertRaises(ValueError):
            full.required_jobs(changed)

    def test_full_proof_rejects_unjoined_or_reordered_activation_and_overrides(self):
        wait = '      - name: Wait for activation Go preparation\n        wait: activation-go\n'
        for kind in ('product-activation', 'web-activation'):
            section = job(self.workflow, kind)
            without_wait = section.replace(wait, '')
            mutations = (
                without_wait,
                section.replace('        wait: activation-go', '        wait: other'),
                section.replace(wait, wait + '        if: always()\n'),
                section.replace(wait, wait + '        continue-on-error: true\n'),
                section.replace('        background: true', '        background: true\n        continue-on-error: true'),
                section.replace('        background: true', '        background: true\n        env:\n          GOFLAGS: -mod=mod'),
                section.replace('          go mod verify', '          npm --prefix web run build\n          go mod verify'),
                section.replace('          go mod verify', '          go mod verify || true'),
                section.replace('      - name: Prepare verified activation engine',
                                '      - run: echo extra-writer\n      - name: Prepare verified activation engine', 1),
                without_wait.replace('      - uses: actions/setup-node@', wait + '      - uses: actions/setup-node@', 1),
                section.replace('      - name: Verify fixed ', '      - run: echo extra-writer\n      - name: Verify fixed ', 1),
                section.replace('    steps:', '    continue-on-error: true\n    steps:', 1),
            )
            for index, changed in enumerate(mutations):
                data = self.workflow.replace(section, changed, 1).encode()
                self.assertNotEqual(data, self.sources[full.WORKFLOW])
                with self.subTest(kind=kind, mutation=index), self.assertRaises(ValueError):
                    full.validate_preparation_overlap_workflow(data)

    def test_full_proof_rejects_duplicate_missing_or_alternate_yaml_jobs(self):
        for kind in ('documents', 'product-activation', 'web-activation'):
            marker = '  ' + kind + ':\n'
            section = marker + job(self.workflow, kind)
            mutations = (
                self.workflow.replace(section, '', 1),
                self.workflow + section,
                self.workflow.replace(marker, "  '" + kind + "':\n", 1),
            )
            for index, changed in enumerate(mutations):
                with self.subTest(kind=kind, mutation=index), self.assertRaises(ValueError):
                    full.validate_preparation_overlap_workflow(changed.encode())

    def test_preparation_failure_or_missing_wait_cannot_get_runtime_credit(self):
        required = {name: steps for name, steps in full.required_jobs(self.sources).items()
                    if name in ('product-activation', 'web-activation')}
        run = dict(id=10, run_attempt=1, head_sha='a' * 40)
        original = [dict(id=index, name=name, run_id=10, run_attempt=1, head_sha='a' * 40,
                         status='completed', conclusion='success',
                         steps=[dict(name=step, status='completed', conclusion='success') for step in steps])
                    for index, (name, steps) in enumerate(required.items(), 100)]
        def audit(rows):
            return full.audit_jobs(lambda path: dict(total_count=len(rows), jobs=rows), run, required)
        self.assertEqual(len(audit(original)), 2)
        for kind, names in required.items():
            for name in names:
                for mode in ('missing', 'duplicate', 'failure', 'skipped', 'cancelled', 'in_progress'):
                    rows = copy.deepcopy(original)
                    current = next(row for row in rows if row['name'] == kind)
                    step = next(step for step in current['steps'] if step['name'] == name)
                    if mode == 'missing':
                        current['steps'].remove(step)
                    elif mode == 'duplicate':
                        current['steps'].append(copy.deepcopy(step))
                    elif mode == 'in_progress':
                        step['status'] = mode
                    else:
                        step['conclusion'] = mode
                    with self.subTest(kind=kind, step=name, mode=mode), self.assertRaises(ValueError):
                        audit(rows)


if __name__ == '__main__':
    unittest.main()
