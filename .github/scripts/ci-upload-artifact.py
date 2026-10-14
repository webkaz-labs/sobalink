"""Associate required artifacts with one reviewed official producer upload.

Job logs are plain-text association evidence under immutable reviewed source,
not independent cryptographic step identity. Never emit their raw contents.
"""
import datetime
import importlib.util
from pathlib import Path
import re

REPOSITORY = 'webkaz-labs/sobalink'
UPLOAD_ACTION = 'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'
MAX_LOG_BYTES = 16 * 1024 * 1024
LOG_LINE = re.compile(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z (.*)\Z')


def require(ok):
    if not ok:
        raise ValueError('required artifact producer upload differs')


def positive(value):
    return type(value) is int and value > 0


def timestamp(value):
    require(type(value) is str and re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', value))
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))


def download_job_log(identifier):
    require(positive(identifier))
    path = Path(__file__).with_name('ci-nearby-native.py')
    spec = importlib.util.spec_from_file_location('ci_nearby_native', path)
    native = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), native.__dict__)
    # Existing read-only CI token only; no credential in argv or output.
    # Actions logs include ANSI command formatting. Permit faithful bytes only
    # inside bounded private PIPE capture; never render or echo this response.
    state, code, raw, _ = native.capture([
        'gh', 'api', '--method', 'GET', '--allow-escape-sequences',
        'repos/' + REPOSITORY + f'/actions/jobs/{identifier}/logs'],
        timeout=60, limit=MAX_LOG_BYTES)
    require(state == 'completed' and code == 0 and type(raw) is bytes and 0 < len(raw) <= MAX_LOG_BYTES)
    return raw


def validate_job_upload(raw, artifact, producer, exported, repository, run_id):
    """Validate bytes fetched from this already-validated original job's API.

    For the supported whole-second runner API step fields, compare log
    timestamps at that same precision, retaining full fractional ordering.
    Artifact-service timestamps are deliberately not bounded by step end.
    """
    require(repository == REPOSITORY and positive(run_id) and positive(producer.get('id'))
            and producer.get('run_id') == run_id and type(producer.get('run_id')) is int
            and positive(producer.get('run_attempt')) and producer.get('status') == 'completed'
            and producer.get('conclusion') == 'success' and exported in producer.get('steps', [])
            and exported.get('status') == 'completed' and exported.get('conclusion') == 'success')
    require(type(raw) is bytes and 0 < len(raw) <= MAX_LOG_BYTES and raw.endswith(b'\n'))
    try:
        text = raw.decode('utf-8')
    except UnicodeDecodeError:
        raise ValueError('required artifact producer upload differs') from None
    require('\x00' not in text and '\r' not in text.replace('\r\n', ''))
    rows = []
    for line in text.replace('\r\n', '\n').split('\n')[:-1]:
        match = LOG_LINE.fullmatch(line)
        if match:
            # Validate calendar values, and preserve all nine fractional digits.
            stamp = timestamp(match[1] + 'Z')
            rows.append((stamp, (match[1], (match[2] or '').ljust(9, '0')), match[3]))
        else:
            # Unrelated runner commands may contain untimestamped continuation
            # lines. They may never supply an upload record or boundary.
            rows.append((None, None, line))
    name, identifier, size, digest = (artifact.get(key) for key in ('name', 'id', 'size_in_bytes', 'digest'))
    require(type(name) is str and re.fullmatch(r'[A-Za-z0-9_-]{1,200}', name)
            and positive(identifier) and positive(size) and type(digest) is str
            and re.fullmatch(r'sha256:[0-9a-f]{64}', digest))
    bodies = [row[2] for row in rows]
    names = [i for i, body in enumerate(bodies) if body == '  name: ' + name]
    require(len(names) == 1)
    name_index = names[0]
    groups = [i for i, body in enumerate(bodies) if body.startswith('##[group]')]
    starts = [i for i in groups if i < name_index]
    require(starts)
    start = starts[-1]
    ends = [i for i in groups if i > start]
    require(ends)
    end = ends[0]
    require(start < name_index < end and bodies[start] == '##[group]Run ' + UPLOAD_ACTION
            and bodies[end].startswith('##[group]Run ')
            and all(rows[i][0] is not None for i in range(start, end + 1)))
    config_ends = [i for i in range(start + 1, end) if bodies[i] == '##[endgroup]']
    require(len(config_ends) == 1)
    config_end = config_ends[0]
    require(bodies[start + 1] == 'with:' and start + 1 < name_index < config_end)
    envs = [i for i in range(start + 2, config_end) if bodies[i] == 'env:']
    require(len(envs) <= 1)
    inputs_end = envs[0] if envs else config_end
    inputs = bodies[start + 2:inputs_end]
    require(name_index < inputs_end and sum(line.startswith('  name:') for line in inputs) == 1
            and [line for line in inputs if line.startswith('  overwrite:')] == ['  overwrite: false'])
    expected = [
        'Uploading artifact: ' + name + '.zip',
        'SHA256 digest of uploaded artifact is ' + digest.removeprefix('sha256:'),
        'Finalizing artifact upload',
        f'Artifact {name} successfully finalized. Artifact ID {identifier}',
        f'Artifact {name} has been successfully uploaded! Final size is {size} bytes. Artifact ID is {identifier}',
        f'Artifact download URL: https://github.com/{repository}/actions/runs/{run_id}/artifacts/{identifier}',
    ]
    families = ('Uploading artifact:', 'SHA256 digest of uploaded artifact is', 'Finalizing artifact upload',
                'Artifact download URL:')
    observed = [(i, bodies[i]) for i in range(config_end + 1, end) if bodies[i].startswith(families)
                or re.match(r'Artifact .* (?:successfully finalized|has been successfully uploaded)', bodies[i])]
    require([body for _, body in observed] == expected)
    permitted = {name_index, *(i for i, _ in observed)}
    # Reject even out-of-block copies/conflicts referring to this name or ID.
    # Other upload actions in the same job remain valid unrelated evidence.
    references = re.compile(r'(?:  name: ' + re.escape(name) + r'(?:\s|$)|'
        r'Uploading artifact: ' + re.escape(name) + r'(?:\.zip|\s|$)|'
        r'Artifact ' + re.escape(name) + r'(?:\s|$)|'
        r'Artifact ID(?: is)? ' + str(identifier) + r'(?:\D|$)|'
        r'/artifacts/' + str(identifier) + r'(?:\D|$))')
    require(all(i in permitted for i, body in enumerate(bodies) if references.search(body)))
    job_start, job_end = timestamp(producer['started_at']), timestamp(producer['completed_at'])
    step_start, step_end = timestamp(exported['started_at']), timestamp(exported['completed_at'])
    require(job_start <= step_start <= step_end <= job_end
            and all(step_start <= rows[i][0] <= step_end for i in range(start, end))
            and step_end <= rows[end][0] <= job_end
            and all(rows[i][1] <= rows[i + 1][1] for i in range(start, end)))
