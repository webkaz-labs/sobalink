"""Synthetic companion provenance regressions; no real GUI acceptance."""
import copy
import json
import pathlib
import tempfile
import unittest
from source_provenance import digest, verify_native_companion, NATIVE_UI_REF


def add_companion_fixture(build, notices, bom, root=None):
    if build['target'].startswith('linux-'):
        return
    target = build['target']
    binary = 'bin/soba-native' + ('.exe' if target.startswith('windows') else '')
    files = {'go.mod': b'module example\n', 'go.sum': b'sum\n', 'LICENSE': b'project license',
             'source/poc/native/go.mod': b'module example\n', 'source/poc/native/go.sum': b'sum\n',
             'source/poc/native/main.go': b'package main\n', 'licenses/go/LICENSE': b'go license'}
    name, version, module_sum = 'example.org/ui', 'v1.0.0', 'h1:' + 'A' * 43 + '='
    key = digest((name + '@' + version).encode())
    license_path = 'licenses/' + key + '/LICENSE'
    files[license_path] = b'ui license'
    files['go.sum'] = files['source/poc/native/go.sum'] = (name + ' ' + version + ' ' + module_sum + '\n').encode()
    inventory = {'archive_base_path': 'share/sobalink/native-ui', 'scope': 'Target-filtered CGO_ENABLED=0',
                 'modules': [{'module': name, 'version': version, 'go_module_sum': module_sum, 'notices': [{'path': license_path, 'sha256': digest(files[license_path])}]}],
                 'go_standard_library': {'module': 'Go standard library', 'version': 'go1.27.3', 'notices': [{'path': 'licenses/go/LICENSE', 'sha256': digest(files['licenses/go/LICENSE'])}]},
                 'frontend_modules': None, 'source_components': None}
    app = {'type': 'application', 'name': 'soba-native', 'version': build['version'], 'bom-ref': NATIVE_UI_REF, 'hashes': [{'alg': 'SHA-256', 'content': digest(b'companion executable')}]}
    components = [{'type': 'library', 'name': 'Go standard library', 'version': 'go1.27.3', 'bom-ref': 'native-ui:golang:stdlib'},
                  {'type': 'library', 'name': name, 'version': version, 'bom-ref': 'native-ui:golang:' + key, 'properties': [{'name': 'go:module:sum', 'value': module_sum}]}]
    edges = [{'ref': NATIVE_UI_REF, 'dependsOn': [c['bom-ref'] for c in components]}, *({'ref': c['bom-ref'], 'dependsOn': []} for c in components)]
    nested = {'bomFormat': 'CycloneDX', 'specVersion': '1.5', 'metadata': {'component': app, 'properties': [{'name': n, 'value': v} for n, v in [('build:target', target), ('source:commit', build['source_commit']), ('build:cgo', '0'), ('inventory:scope', inventory['scope'])]]}, 'components': components, 'dependencies': edges}
    files['bom.cdx.json'] = json.dumps(nested).encode()
    files['third-party-notices.json'] = json.dumps(inventory).encode()
    inputs = [{'path': p.removeprefix('source/'), 'sha256': digest(v)} for p, v in sorted(files.items()) if p.startswith('source/')]
    ui = {'project': 'github.com/webkaz-labs/sobalink/poc/native', 'product': 'soba-native', 'version': build['version'], 'source_commit': build['source_commit'], 'target': target, 'build_tags': build['build_tags'], 'go_version': 'go1.27.3', 'cgo_enabled': False, 'trimpath': True, 'buildvcs': False, 'binary': {'path': binary, 'sha256': digest(b'companion executable')}, 'source_inputs': inputs, 'source_inputs_sha256': digest(json.dumps(inputs, separators=(',', ':')).encode()), 'ldflags': '-s -w -buildid= -X main.version=' + build['version'] + ' -X main.sourceCommit=' + build['source_commit'] + (' -H=windowsgui' if target.startswith('windows') else '')}
    for file, field in [('go.mod', 'go_mod_sha256'), ('go.sum', 'go_sum_sha256'), ('bom.cdx.json', 'sbom_sha256'), ('third-party-notices.json', 'notices_sha256')]:
        ui[field] = digest(files[file])
    build['native_ui'], notices['native_ui'] = ui, inventory
    bom.setdefault('components', []).extend([app, *components])
    roots = [e for e in bom.setdefault('dependencies', []) if e['ref'] == 'application:sobalink']
    if not roots:
        roots = [{'ref': 'application:sobalink', 'dependsOn': []}]
        bom['dependencies'].extend(roots)
    roots[0]['dependsOn'].append(NATIVE_UI_REF)
    bom['dependencies'].extend(edges)
    if root is not None:
        files['build.json'] = json.dumps(ui).encode()
        for p, data in files.items():
            path = root / 'share/sobalink/native-ui' / p
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        (root / 'share/sobalink/LICENSE').write_bytes(files['LICENSE'])
        (root / 'bin').mkdir()
        (root / binary).write_bytes(b'companion executable')
        (root / 'bin' / ('soba.exe' if target.startswith('windows') else 'soba')).write_bytes(b'daemon executable')


class CompanionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.build = {'target': 'windows-amd64', 'version': '0.1.0-alpha.1', 'source_commit': 'a' * 40, 'build_tags': 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'}
        self.notices, self.bom = {}, {}
        add_companion_fixture(self.build, self.notices, self.bom, self.root)

    def check(self):
        return verify_native_companion(self.build, self.notices, self.bom, self.root)

    def test_retained_inventory(self):
        self.check()

    def test_linux_forbidden(self):
        self.build['target'] = 'linux-amd64'
        with self.assertRaises(AssertionError): self.check()

    def test_missing_companion_fails_closed(self):
        del self.build['native_ui']
        with self.assertRaises(KeyError): self.check()

    def test_toolchain_target_commit_hash_and_flags(self):
        for key, value in [('go_version', 'go1.27.1'), ('target', 'darwin-arm64'), ('source_commit', 'b'*40), ('cgo_enabled', True), ('ldflags', '-s'), ('source_inputs_sha256', '0'*64)]:
            prior = self.build['native_ui'][key]
            self.build['native_ui'][key] = value
            with self.subTest(key=key), self.assertRaises(AssertionError): self.check()
            self.build['native_ui'][key] = prior

    def test_binary_tampering(self):
        (self.root / 'bin/soba-native.exe').write_bytes(b'changed')
        with self.assertRaises(AssertionError): self.check()

    def test_extra_retained_file(self):
        (self.root / 'share/sobalink/native-ui/extra.txt').write_text('extra')
        with self.assertRaises(AssertionError): self.check()

    def test_changed_source_notice_or_metadata(self):
        for path in ['source/poc/native/main.go', 'licenses/go/LICENSE', 'go.mod', 'bom.cdx.json', 'third-party-notices.json']:
            file = self.root / 'share/sobalink/native-ui' / path
            prior = file.read_bytes()
            file.write_bytes(b'changed')
            with self.subTest(path=path), self.assertRaises((AssertionError, ValueError)): self.check()
            file.write_bytes(prior)

    def test_duplicate_or_dangling_sbom(self):
        self.bom['dependencies'][-1]['dependsOn'].append('unknown')
        with self.assertRaises(AssertionError): self.check()

    def test_notice_traversal(self):
        self.notices['native_ui']['modules'][0]['notices'][0]['path'] = '../secret'
        with self.assertRaises(AssertionError): self.check()

    def test_symlink_input(self):
        file = self.root / 'share/sobalink/native-ui/source/poc/native/main.go'
        file.unlink()
        file.symlink_to(self.root / 'bin/soba-native.exe')
        with self.assertRaises(AssertionError): self.check()


if __name__ == '__main__': unittest.main()
