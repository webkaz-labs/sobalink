"""Synthetic official-upload association fixtures; no service or native proof."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).with_name('ci-upload-artifact.py')
SPEC = importlib.util.spec_from_file_location('ci_upload_artifact', SCRIPT)
upload = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), upload.__dict__)


def upload_log(artifact, exported=None, *, repository=upload.REPOSITORY, run_id=10):
    """One complete action block, using only invented data and fixed fields."""
    start = (exported or {}).get('started_at', '2026-01-01T00:00:30Z').removesuffix('Z')
    end = (exported or {}).get('completed_at', '2026-01-01T00:00:50Z').removesuffix('Z')
    name, identifier, size = (artifact[key] for key in ('name', 'id', 'size_in_bytes'))
    config = ['##[group]Run ' + upload.UPLOAD_ACTION, 'with:', '  name: ' + name,
        '  path: /synthetic/receipt.json', '  if-no-files-found: error', '  retention-days: 90',
        '  compression-level: 6', '  overwrite: false', '  include-hidden-files: false',
        '  archive: true', 'env:', '  GOTOOLCHAIN: local', '##[endgroup]']
    records = ['With the provided path, there will be 1 file uploaded', 'Artifact name is valid!',
        'Root directory input is valid!', 'Uploading artifact: ' + name + '.zip',
        'Beginning upload of artifact content to blob storage', 'Uploaded bytes ' + str(size),
        'Finished uploading artifact content to blob storage!',
        'SHA256 digest of uploaded artifact is ' + artifact['digest'].removeprefix('sha256:'),
        'Finalizing artifact upload',
        f'Artifact {name} successfully finalized. Artifact ID {identifier}',
        f'Artifact {name} has been successfully uploaded! Final size is {size} bytes. Artifact ID is {identifier}',
        f'Artifact download URL: https://github.com/{repository}/actions/runs/{run_id}/artifacts/{identifier}']
    return ('\n'.join([f'{start}.{i:07d}Z {line}' for i, line in enumerate(config, 1)]
        + [f'{end}.{7000000 + i:07d}Z {line}' for i, line in enumerate(records, 1)]
        + [f'{end}.9000000Z ##[group]Run next reviewed step', f'{end}.9000001Z ##[endgroup]']) + '\n').encode()


class UploadBindingTests(unittest.TestCase):
    def setUp(self):
        self.artifact = {'id': 300, 'name': 'synthetic-required-10-1', 'size_in_bytes': 100,
                         'digest': 'sha256:' + 'c' * 64}
        self.exported = {'name': 'Preserve required proof', 'status': 'completed', 'conclusion': 'success',
            'started_at': '2026-01-01T00:00:30Z', 'completed_at': '2026-01-01T00:00:50Z'}
        self.producer = {'id': 100, 'run_id': 10, 'run_attempt': 1, 'status': 'completed', 'conclusion': 'success',
            'started_at': '2026-01-01T00:00:01Z', 'completed_at': '2026-01-01T00:01:00Z', 'steps': [self.exported]}
        self.raw = upload_log(self.artifact)

    def validate(self, raw=None):
        upload.validate_job_upload(self.raw if raw is None else raw, self.artifact, self.producer,
                                   self.exported, upload.REPOSITORY, 10)

    def test_exact_complete_pinned_upload_association(self):
        self.validate()
        self.validate(b'unrelated multiline command continuation\n' + self.raw)
        self.validate(self.raw.replace(b'\n', b'\r\n'))

    def test_changed_identity_digest_size_and_other_producer_log_fail(self):
        for key, value in [('id', 301), ('name', 'synthetic-required-10-2'), ('size_in_bytes', 101),
                           ('digest', 'sha256:' + 'd' * 64), ('id', True), ('size_in_bytes', True)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                changed = dict(self.artifact, **{key: value})
                upload.validate_job_upload(self.raw, changed, self.producer, self.exported, upload.REPOSITORY, 10)
        for key, value in [('id', True), ('run_id', 11), ('run_attempt', True),
                           ('status', 'in_progress'), ('conclusion', 'failure')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                upload.validate_job_upload(self.raw, self.artifact, dict(self.producer, **{key: value}),
                                           self.exported, upload.REPOSITORY, 10)
        with self.assertRaises(ValueError): self.validate(upload_log(dict(self.artifact, name='unrelated-proof')))

    def test_absent_truncated_invalid_utf8_and_unbounded_logs_fail(self):
        for raw in (b'', b'no evidence\n', self.raw[:-1], self.raw + b'\xff\n', self.raw + b'\x00\n',
                    self.raw + b'\rX\n', b'x' * (upload.MAX_LOG_BYTES + 1)):
            with self.subTest(length=len(raw)), self.assertRaises(ValueError): self.validate(raw)
        lines = self.raw.splitlines(keepends=True)
        for end in range(1, len(lines) - 1):
            with self.subTest(end=end), self.assertRaises(ValueError): self.validate(b''.join(lines[:end]))

    def test_only_physical_lf_lines_can_supply_upload_records(self):
        for separator in (b'\x0b', b'\x0c', '\u2028'.encode(), '\u2029'.encode()):
            with self.subTest(separator=separator), self.assertRaises(ValueError):
                self.validate(self.raw.replace(b'\n', separator) + b'\n')
            # Preserve the header and closing boundary; split only the evidence.
            marker = next(line for line in self.raw.splitlines(keepends=True) if b'SHA256 digest' in line)
            with self.subTest(separator=separator), self.assertRaises(ValueError):
                self.validate(self.raw.replace(marker, marker.rstrip(b'\n') + separator))

    def test_required_records_missing_duplicate_conflicting_and_out_of_order_fail(self):
        lines = self.raw.splitlines(keepends=True)
        for marker in (b'Uploading artifact:', b'SHA256 digest', b'Finalizing artifact upload',
                       b'successfully finalized.', b'has been successfully uploaded!', b'Artifact download URL:'):
            index = next(i for i, line in enumerate(lines) if marker in line)
            for changed in (lines[:index] + lines[index + 1:], lines[:index] + [lines[index]] + lines[index:]):
                with self.subTest(marker=marker), self.assertRaises(ValueError): self.validate(b''.join(changed))
        changes = [(b'Artifact ID 300', b'Artifact ID 301'), (b'Artifact ID is 300', b'Artifact ID is 301'),
            (b'Final size is 100', b'Final size is 101'), (b'/runs/10/', b'/runs/11/'),
            (b'/artifacts/300', b'/artifacts/301'), (b'c' * 64, b'd' * 64),
            (b'SHA256 digest of uploaded artifact is', b'SHA256 digest of uploaded artifact'),
            (b'successfully finalized.', b'successfully finaliz')]
        for old, new in changes:
            with self.subTest(old=old), self.assertRaises(ValueError): self.validate(self.raw.replace(old, new))
        final = next(i for i, line in enumerate(lines) if b'Finalizing artifact upload' in line)
        lines[final], lines[final + 1] = lines[final + 1], lines[final]
        with self.assertRaises(ValueError): self.validate(b''.join(lines))

    def test_wrong_action_input_block_or_missing_next_step_boundary_fail(self):
        changes = [(upload.UPLOAD_ACTION.encode(), b'actions/upload-artifact@' + b'd' * 40),
            (b'  name: synthetic-required-10-1', b'  name: unrelated-proof'),
            (b'  overwrite: false', b'  overwrite: true'), (b'with:', b'env:'),
            (b'  overwrite: false\n', b''), (b'##[group]Run next reviewed step', b'next step'),
            (b'##[endgroup]', b'endgroup'),
            (b'##[group]Run next reviewed step', b'##[group]arbitrary upload output')]
        for old, new in changes:
            with self.subTest(old=old), self.assertRaises(ValueError): self.validate(self.raw.replace(old, new))
        line = next(line for line in self.raw.splitlines(keepends=True) if b'  overwrite:' in line)
        with self.assertRaises(ValueError): self.validate(self.raw.replace(line, line + line))
        line = next(line for line in self.raw.splitlines(keepends=True) if b'SHA256 digest' in line)
        inserted = b'2026-01-01T00:00:50.6000000Z ##[group]Run wrong step\n'
        with self.assertRaises(ValueError): self.validate(self.raw.replace(line, inserted + line))

    def test_duplicate_block_or_out_of_block_target_records_fail(self):
        with self.assertRaises(ValueError): self.validate(self.raw + self.raw)
        for marker in (b'  name:', b'Uploading artifact:', b'successfully finalized.',
                       b'has been successfully uploaded!', b'Artifact download URL:'):
            line = next(line for line in self.raw.splitlines(keepends=True) if marker in line)
            for added in (line, line.replace(b'ID 300', b'ID 301').replace(b'ID is 300', b'ID is 301')):
                with self.subTest(marker=marker), self.assertRaises(ValueError): self.validate(self.raw + added)
        other = dict(self.artifact, id=301, name='unrelated-proof')
        self.validate(self.raw + upload_log(other))
        # Another name cannot claim this artifact's ID outside the target block.
        other['id'] = self.artifact['id']
        with self.assertRaises(ValueError): self.validate(self.raw + upload_log(other))

    def test_runner_precision_is_exact_without_expanding_named_step(self):
        # Subsecond finalization in the step's recorded final second is valid.
        self.validate()
        for old, new in ((b'00:00:30.', b'00:00:29.'), (b'00:00:50.', b'00:00:51.'),
                         (b'.7000010Z', b'.7000000Z'),
                         (b'2026-01-01T', b'2026-02-30T'), (b'Z ', b' '),
                         (b'.7000010Z', b'.70000100+00:00')):
            with self.subTest(old=old, new=new), self.assertRaises(ValueError): self.validate(self.raw.replace(old, new))
        lines = self.raw.splitlines(keepends=True)
        index = next(i for i, line in enumerate(lines) if b'successfully finalized.' in line)
        # Preserve sub-microsecond ordering instead of truncating to six digits.
        lines[index] = lines[index].replace(b'.7000010Z', b'.70000081Z')
        with self.assertRaises(ValueError): self.validate(b''.join(lines))

    def test_bounded_existing_job_log_endpoint_and_fail_closed_capture(self):
        capture = Mock(return_value=('completed', 0, self.raw, b''))
        def execute(code, namespace): namespace['capture'] = capture
        with patch('builtins.exec', side_effect=execute):
            self.assertEqual(upload.download_job_log(100), self.raw)
            capture.assert_called_with(['gh', 'api', '--method', 'GET',
                'repos/' + upload.REPOSITORY + '/actions/jobs/100/logs'], timeout=60, limit=upload.MAX_LOG_BYTES)
            for result in (('output_limit', 0, self.raw, b''), ('timeout', 0, self.raw, b''),
                           ('completed', 1, self.raw, b''), ('completed', 0, b'', b'')):
                capture.return_value = result
                with self.assertRaises(ValueError): upload.download_job_log(100)
        for identifier in (True, 0, -1, '100'):
            with self.assertRaises(ValueError): upload.download_job_log(identifier)

    def test_errors_never_include_raw_log_paths_or_credentials(self):
        private = b'synthetic-secret-value /synthetic/private-path'
        try:
            self.validate(private + b'\n')
        except ValueError as error:
            self.assertEqual(str(error), 'required artifact producer upload differs')


if __name__ == '__main__':
    unittest.main()
