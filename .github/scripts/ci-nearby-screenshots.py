"""Optional, fixed initial-dialog illustrations; never acceptance authority."""
import json
import os
from pathlib import Path
import shutil
import stat
import struct
import zlib

MAX_BYTES = 2 * 1024 * 1024
CAPTURES = [
    {'file': 'nearby-initial-en-375.png', 'locale': 'en', 'viewportWidth': 375, 'viewportHeight': 844},
    {'file': 'nearby-initial-ja-320.png', 'locale': 'ja', 'viewportWidth': 320, 'viewportHeight': 844},
]
MANIFEST = {'schema': 1, 'kind': 'nearby-initial-ui', 'acceptanceEvidence': False,
            'checkpoint': 'before-first-setup-start', 'framing': 'visible-dialog-interior-side-crop-12px',
            'masking': 'none', 'identities': 'synthetic-only', 'captures': CAPTURES}
SIGNATURE = b'\x89PNG\r\n\x1a\n'


def private_file(path, limit):
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o077 or info.st_uid != os.getuid()
            or not 0 < info.st_size <= limit):
        raise ValueError('invalid private illustration input')
    return path.read_bytes()


def png(raw, spec):
    """Validate a bounded Chromium raster and discard all ancillary metadata."""
    if type(raw) is not bytes or not 0 < len(raw) <= MAX_BYTES or not raw.startswith(SIGNATURE):
        raise ValueError('invalid illustration PNG')
    offset, chunks, compressed, ended = len(SIGNATURE), [], bytearray(), False
    width = height = channels = None
    while offset < len(raw):
        if offset + 12 > len(raw) or ended:
            raise ValueError('invalid PNG framing')
        length, kind = struct.unpack('>I4s', raw[offset:offset + 8])
        end = offset + 12 + length
        if end > len(raw):
            raise ValueError('invalid PNG chunk length')
        value = raw[offset + 8:end - 4]
        if zlib.crc32(kind + value) != struct.unpack('>I', raw[end - 4:end])[0]:
            raise ValueError('invalid PNG checksum')
        if not chunks:
            if kind != b'IHDR' or length != 13:
                raise ValueError('invalid PNG header')
            width, height, depth, color, compression, filtering, interlace = struct.unpack('>IIBBBBB', value)
            if (not spec['viewportWidth'] - 56 <= width <= spec['viewportWidth'] - 24 or not 128 <= height <= spec['viewportHeight']
                    or depth != 8 or color not in (2, 6) or (compression, filtering, interlace) != (0, 0, 0)):
                raise ValueError('invalid illustration dimensions or format')
            channels = 3 if color == 2 else 4
            chunks.append(raw[offset:end])
        elif kind == b'IDAT':
            if not length:
                raise ValueError('empty PNG raster chunk')
            compressed.extend(value)
            chunks.append(raw[offset:end])
        elif kind == b'IEND':
            if length or not compressed:
                raise ValueError('invalid PNG terminator')
            chunks.append(raw[offset:end])
            ended = True
        elif kind == b'sRGB' and value == b'\x00' and not compressed:
            pass  # Fixed renderer color tag only; no text or arbitrary metadata.
        else:
            raise ValueError('unexpected PNG chunk')
        offset = end
    if not ended:
        raise ValueError('incomplete PNG')
    expected = height * (1 + width * channels)
    inflater = zlib.decompressobj()
    pixels = inflater.decompress(bytes(compressed), expected + 1)
    if (len(pixels) != expected or not inflater.eof or inflater.unused_data or inflater.unconsumed_tail
            or any(pixels[row * (1 + width * channels)] > 4 for row in range(height))):
        raise ValueError('invalid bounded PNG raster')
    return SIGNATURE + b''.join(chunks)


def export_initial_screenshots(directory, output, *, verified):
    """Only the already-verified runner can publish these two literal images."""
    if verified is not True:
        return False
    created = False
    try:
        directory, output = Path(directory), Path(output)
        info = directory.lstat()
        if (not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid()
                or {p.name for p in directory.iterdir()} != {'screenshots.json', *(spec['file'] for spec in CAPTURES)}):
            raise ValueError('invalid private illustration directory')
        # Parsing duplicate keys must fail; neither arbitrary names nor extra
        # metadata can be made public by a browser/test-generated manifest.
        def unique(pairs):
            result = {}
            for key, value in pairs:
                if key in result:
                    raise ValueError('duplicate illustration field')
                result[key] = value
            return result
        supplied = json.loads(private_file(directory / 'screenshots.json', 2048), object_pairs_hook=unique)
        if json.dumps(supplied, sort_keys=True) != json.dumps(MANIFEST, sort_keys=True):
            raise ValueError('unexpected illustration metadata')
        images = [(spec['file'], png(private_file(directory / spec['file'], MAX_BYTES), spec)) for spec in CAPTURES]
        output.mkdir(mode=0o700, exist_ok=False)
        created = True
        for name, data in images:
            with (output / name).open('xb') as file:
                file.write(data)
        with (output / 'screenshots.json').open('x', encoding='utf-8') as file:
            file.write(json.dumps(MANIFEST, sort_keys=True) + '\n')
        return True
    except Exception:
        if created:
            try:
                shutil.rmtree(output)
            except OSError:
                pass
        return False
