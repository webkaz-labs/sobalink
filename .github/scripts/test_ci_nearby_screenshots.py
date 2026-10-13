"""Illustration export policy only; generated pixels here are unit inputs."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
import zlib

SPEC = importlib.util.spec_from_file_location('nearby_screenshots', Path(__file__).with_name('ci-nearby-screenshots.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
ROOT = Path(__file__).resolve().parents[2]


def chunk(kind, data):
    return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))


def fixture_png(width=335, height=128, ancillary=b'', pixels=None):
    header = chunk(b'IHDR', struct.pack('>IIBBBBB', width, height, 8, 2, 0, 0, 0))
    pixels = pixels if pixels is not None else b'\0' * (height * (1 + width * 3))
    return gate.SIGNATURE + header + ancillary + chunk(b'IDAT', zlib.compress(pixels)) + chunk(b'IEND', b'')


class ScreenshotExportTests(unittest.TestCase):
    def stage(self, root, manifest=None):
        directory = root / 'private'
        directory.mkdir(mode=0o700)
        for spec in gate.CAPTURES:
            path = directory / spec['file']
            path.write_bytes(fixture_png(spec['viewportWidth'] - 40))
            path.chmod(0o600)
        path = directory / 'screenshots.json'
        path.write_text(json.dumps(gate.MANIFEST if manifest is None else manifest))
        path.chmod(0o600)
        return directory

    def test_fixed_manifest_matches_browser_helper(self):
        import subprocess
        result = subprocess.run(['node', '--input-type=module', '-e',
                                 "import {screenshotManifest} from './web/browser/nearby-native-screenshots.mjs'; console.log(JSON.stringify(screenshotManifest()))"],
                                cwd=ROOT, check=True, capture_output=True, text=True)
        self.assertEqual(json.loads(result.stdout), gate.MANIFEST)

    def test_only_verified_flow_exports_both_literal_images(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = self.stage(root)
            for rejected in (False, None, 1, 'true'):
                self.assertFalse(gate.export_initial_screenshots(directory, root / 'public', verified=rejected))
                self.assertFalse((root / 'public').exists())
            self.assertTrue(gate.export_initial_screenshots(directory, root / 'public', verified=True))
            self.assertEqual({p.name for p in (root / 'public').iterdir()}, {'screenshots.json', *(s['file'] for s in gate.CAPTURES)})
            self.assertEqual(json.loads((root / 'public/screenshots.json').read_text()), gate.MANIFEST)
            self.assertFalse(gate.export_initial_screenshots(directory, root / 'public', verified=True))

    def test_invalid_metadata_and_paths_never_publish_partial_artifacts(self):
        for mutation in ('extra', 'private-name', 'wrong-checkpoint', 'acceptance', 'wrong-type', 'duplicate', 'extra-file', 'symlink', 'hardlink', 'permissions', 'corrupt'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                manifest = copy.deepcopy(gate.MANIFEST)
                if mutation == 'extra': manifest['private'] = 'not-exportable'
                if mutation == 'private-name': manifest['captures'][0]['file'] = '../private.png'
                if mutation == 'wrong-checkpoint': manifest['checkpoint'] = 'connected'
                if mutation == 'acceptance': manifest['acceptanceEvidence'] = True
                if mutation == 'wrong-type': manifest['schema'] = True
                directory = self.stage(root, manifest)
                image = directory / gate.CAPTURES[0]['file']
                if mutation == 'duplicate': (directory / 'screenshots.json').write_text('{"schema":1,"schema":1}')
                if mutation == 'extra-file': (directory / 'private.log').write_text('not-exportable')
                if mutation == 'symlink':
                    image.unlink(); image.symlink_to(directory / gate.CAPTURES[1]['file'])
                if mutation == 'hardlink': os.link(image, root / 'linked.png')
                if mutation == 'permissions': image.chmod(0o644)
                if mutation == 'corrupt': image.write_bytes(b'not a PNG')
                self.assertFalse(gate.export_initial_screenshots(directory, root / 'public', verified=True))
                self.assertFalse((root / 'public').exists())

    def test_png_bounds_metadata_crc_trailing_data_and_decompression(self):
        spec = gate.CAPTURES[0]
        valid = fixture_png()
        self.assertEqual(gate.png(valid, spec), valid)
        self.assertEqual(gate.png(fixture_png(ancillary=chunk(b'sRGB', b'\0')), spec), valid)
        invalid = [b'not-png', valid[:-1], valid + b'private', fixture_png(width=376), fixture_png(height=845),
                   fixture_png(width=1), fixture_png(height=1), fixture_png(ancillary=chunk(b'tEXt', b'private')),
                   fixture_png(ancillary=chunk(b'iTXt', b'private')), fixture_png(ancillary=chunk(b'sRGB', b'private')),
                   fixture_png(pixels=b'\0'), fixture_png(pixels=b'\0' * (128 * (1 + 335 * 3) + 1)),
                   fixture_png(pixels=b'\5' * (128 * (1 + 335 * 3))), valid[:-8] + b'bad CRC!', b'x' * (gate.MAX_BYTES + 1)]
        for data in invalid:
            with self.assertRaises((ValueError, zlib.error)):
                gate.png(data, spec)

    def test_final_exact_source_and_original_receipt_precede_export(self):
        source = (ROOT / '.github/scripts/ci-nearby-diagnostic.py').read_text()
        call = source.index("screenshots_ready = module('ci-nearby-screenshots')")
        self.assertGreater(call, source.index("report['browser'] = browser_summary"))
        self.assertGreater(call, source.rindex('require_source_inventory(log, after_build=True)'))
        self.assertGreater(call, source.index("raise ValueError('source or executable changed')"))
        self.assertIn("if report['diagnosticPassed']:\n            # Illustrations", source)
        workflow = (ROOT / '.github/workflows/activation-diagnostic.yml').read_text().split('  diagnostic-nearby:\n')[1].split('  diagnostic-nearby-native:\n')[0]
        step = workflow.split('      - name: Preserve verified-flow initial UI illustrations separately\n')[1]
        self.assertIn("if: success() && steps.nearby.outputs.screenshots_ready == 'true'", step)
        for filename in ['screenshots.json'] + [spec['file'] for spec in gate.CAPTURES]:
            self.assertIn('${{ runner.temp }}/soba-nearby-ui-sanitized/' + filename, step)
        self.assertNotIn('*', step)
        self.assertNotIn('matrix:', workflow)
        self.assertEqual(workflow.count('run: python -B .github/scripts/ci-nearby-diagnostic.py'), 1)


if __name__ == '__main__':
    unittest.main()
