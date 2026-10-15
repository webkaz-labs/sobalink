#!/usr/bin/env python3
"""Required, exact-tree document checks with an independently proved render gate.

select --plan PLAN --output FILE runs all static checks and emits render_required.
check --plan PLAN --output DIRECTORY repeats that proof and performs every selected
check. There is deliberately no option to waive a required render. Render packages
live outside the checkout in DOCS_RENDER_ROOT, installed from the committed lock.
"""
import argparse
import hashlib
import html
from html.parser import HTMLParser
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import urllib.parse
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2]
VISUALS = 'docs/assets/visuals'
GENERATORS = ('build.py', 'explainers.py', 'semantic.py')
VALIDATORS = ('render.py', 'verify.py')
HELPERS = GENERATORS + VALIDATORS
SVG_NAMES = frozenset(f'{kind}-{lang}-{theme}-{size}.svg'
                      for kind in ('connection', 'permissions', 'saved-and-running', 'delivery-evidence', 'pairing-exchange', 'port-observation', 'network-topologies', 'durable-save-gates', 'restart-state', 'api-boundary', 'resource-gates')
                      for lang in ('en', 'ja') for theme in ('light', 'dark')
                      for size in ('wide', 'narrow'))
SCREENSHOTS = 'docs/assets/screenshots'
PNG_NAMES = frozenset((
    'direct-review-en-focused.png',
    'direct-review-ja-focused.png',
    'message-context-en-narrow-input.png',
    'message-context-en.png',
    'message-context-ja-narrow-input.png',
    'message-context-ja.png',
    'navigation-detail-en-narrow.png',
    'navigation-detail-ja-narrow.png',
    'navigation-graph-en-narrow.png',
    'navigation-graph-en-wide.png',
    'navigation-graph-ja-narrow.png',
    'navigation-graph-ja-wide.png',
    'receive-damaged-en-wide.png',
    'receive-damaged-ja-wide.png',
    'receive-en.png',
    'receive-ja.png',
    'receive-legacy-en-wide.png',
    'receive-legacy-ja-wide.png',
    'service-review-en.png',
    'service-review-ja.png',
    'setup-fixed-en-1180.png',
    'setup-fixed-en-390.png',
    'setup-fixed-ja-1180.png',
    'setup-fixed-ja-390.png',
    'startup-guide-en-narrow-actions.png',
    'startup-guide-en-wide.png',
    'startup-guide-ja-narrow-actions.png',
    'startup-guide-ja-wide.png',
    'stopped-review-en-focused.png',
    'stopped-review-ja-focused.png',
))
IMAGE_SUFFIXES = frozenset(('.svg', '.png', '.jpg', '.jpeg', '.gif', '.webp', '.avif'))
LIMIT = 8 * 1024 * 1024
OID = re.compile(r'[0-9a-f]{40}\Z')
SVG_NS = '{http://www.w3.org/2000/svg}'
XML_LANG = '{http://www.w3.org/XML/1998/namespace}lang'


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def git(*args):
    return subprocess.check_output(['git', '--no-pager', *args], cwd=ROOT,
                                   stderr=subprocess.DEVNULL, timeout=60)


def safe_name(name):
    require(isinstance(name, str) and name and not name.startswith('/') and '\\' not in name
            and all(part not in ('', '.', '..') for part in name.split('/'))
            and not any(ord(c) < 32 or ord(c) == 127 for c in name), 'Unsafe repository path')
    return name


def repository_path(name, *, directory=False):
    safe_name(name)
    path = ROOT
    for part in name.split('/'):
        path = path / part
        require(not path.is_symlink(), 'Symlink is not a document input: ' + name)
    require(path.resolve().is_relative_to(ROOT.resolve()), 'Path escapes repository: ' + name)
    require(path.is_dir() if directory else path.is_file(), 'Missing document input: ' + name)
    return path


def read_text(name):
    raw = repository_path(name).read_bytes()
    require(len(raw) <= LIMIT, 'Document exceeds size limit: ' + name)
    try:
        return raw.decode('utf-8')
    except UnicodeError as exc:
        raise ValueError('Document is not UTF-8: ' + name) from exc


def tracked_files(revision='HEAD'):
    require(revision == 'HEAD' or bool(OID.fullmatch(revision)), 'Invalid document revision')
    result = set()
    for record in git('ls-tree', '-r', '-z', '--full-tree', revision).split(b'\0'):
        if not record:
            continue
        metadata, raw_name = record.split(b'\t', 1)
        mode, kind, _ = metadata.decode('ascii').split()
        name = safe_name(raw_name.decode('utf-8'))
        # A symlink may never become a Markdown, SVG or linked asset input.
        if name.lower().endswith(('.md', '.markdown')):
            require(kind == 'blob' and mode in ('100644', '100755'),
                    'Non-regular tracked document: ' + name)
        if kind == 'blob' and mode in ('100644', '100755'):
            result.add(name)
    return result


def verified_plan(path):
    """Reuse the aggregate's independent event/delta proof, never trust its artifact."""
    spec = importlib.util.spec_from_file_location('documents_coverage', ROOT / '.github/scripts/ci-coverage.py')
    coverage = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(coverage)
    plan = coverage.verified_plan(path)
    current = coverage.head()
    require(plan.get('head_sha') == current and
            plan.get('head_tree') == git('rev-parse', 'HEAD^{tree}').decode().strip(),
            'Document plan does not bind the checkout')
    # Forced/scheduled full plans do not run classifier checkout validation.
    # A dirty tracked source must still never produce exact-tree evidence.
    for args in ((), ('--cached',)):
        require(not git('diff', '--no-ext-diff', '--no-textconv', '--name-only', '-z',
                        '--ignore-submodules=none', *args, current, '--'),
                'Document checkout differs from its commit')
    for record in git('ls-files', '--cached', '-v', '-z').split(b'\0'):
        if record:
            require(record.startswith(b'H '), 'Unsafe document index flags')
    return plan


def split_markdown(text, name='<document>'):
    """Extract fenced Mermaid and visible prose, including quoted/list fences."""
    prose, diagrams, block = [], [], []
    fence = None
    start = 0
    language = ''
    for number, line in enumerate(text.splitlines(), 1):
        normalized = re.sub(r'^\s*(?:>\s*)+', '', line)
        normalized = re.sub(r'^\s*(?:[-+*]|\d+[.)])\s+', '', normalized)
        match = re.match(r'^\s*(`{3,}|~{3,})(.*)$', normalized)
        if fence is None and match:
            fence, language, start = match[1], match[2].strip().lower(), number
            require(fence[0] != '`' or '`' not in language, f'Invalid fence info: {name}:{number}')
            require(not language.startswith('mermaid') or language == 'mermaid',
                    f'Unsupported Mermaid fence options: {name}:{number}')
            block = []
            prose.append('')
        elif fence is not None:
            if match and match[1][0] == fence[0] and len(match[1]) >= len(fence) and not match[2].strip():
                if language == 'mermaid':
                    source = '\n'.join(block)
                    mermaid_check(source, name, start)
                    diagrams.append({'path': name, 'line': start, 'source': source})
                fence = None
            else:
                block.append(re.sub(r'^\s*(?:>\s*)+', '', line))
            prose.append('')
        else:
            prose.append(line)
    require(fence is None, f'Unclosed code fence: {name}:{start}')
    return re.sub(r'<!--.*?-->', '', '\n'.join(prose), flags=re.S), diagrams


def mermaid_check(source, name='<diagram>', line=1):
    where = f'{name}:{line}'
    require(source.strip() and len(source) <= 128 * 1024, 'Empty or oversized Mermaid: ' + where)
    # Restricted, inert diagram sources. No config, arbitrary CSS, HTML, icons,
    # callbacks, remote images, encoded markup or document-level frontmatter.
    forbidden = (r'%%\s*\{|(?m:^\s*---)|<|&(?:#\d+|#x[0-9a-f]+|[a-z]+);|\\|'
                 r'\b(?:https?|file|data|javascript|ftp):|//|'
                 r'(?im:^\s*(?:click|style|classDef|linkStyle|icon|image)\b)|@\{|url\s*\(')
    require(not re.search(forbidden, source, re.I), 'Unsupported Mermaid content/configuration: ' + where)
    first = next((x.strip() for x in source.splitlines() if x.strip() and not x.lstrip().startswith('%%')), '')
    require(bool(re.match(r'^(?:flowchart\s+(?:TB|TD|BT|RL|LR)|graph\s+(?:TB|TD|BT|RL|LR)|'
                          r'stateDiagram(?:-v2)?|sequenceDiagram|classDiagram|erDiagram|'
                          r'gantt|pie|journey|gitGraph|mindmap|timeline|quadrantChart|xychart-beta)\b', first)),
            'Unsupported Mermaid diagram type: ' + where)


def inline_without_code(text):
    return re.sub(r'(`+)(?!`)(.*?)\1(?!`)', lambda m: ' ' * len(m[0]), text, flags=re.S)


def anchors(prose):
    used, counts = set(), {}
    lines = prose.splitlines()
    headings = []
    for index, line in enumerate(lines):
        match = re.match(r'^\s{0,3}#{1,6}\s+(.+?)(?:\s+#+)?\s*$', line)
        if match:
            headings.append(match[1])
        elif index and re.fullmatch(r'\s{0,3}(?:=+|-+)\s*', line) and lines[index - 1].strip():
            headings.append(lines[index - 1].strip())
    for heading in headings:
        heading = re.sub(r'!?\[([^\]]*)\]\([^)]*\)', r'\1', heading)
        heading = html.unescape(re.sub(r'<[^>]*>', '', heading)).lower()
        slug = ''.join(c for c in heading if c in '-_ ' or unicodedata.category(c)[0] in 'LNM').replace(' ', '-')
        number = counts.get(slug, 0)
        candidate = slug + (f'-{number}' if number else '')
        while candidate in used:
            number += 1
            candidate = f'{slug}-{number}'
        counts[slug] = number + 1
        used.add(candidate)
    parser = HTMLLinks('<anchors>')
    parser.feed(prose)
    used.update(parser.ids)
    return used


class HTMLLinks(HTMLParser):
    def __init__(self, name):
        super().__init__(convert_charrefs=True)
        self.name, self.links, self.ids = name, [], set()

    def handle_starttag(self, tag, attrs):
        names = [key for key, _ in attrs]
        require(len(names) == len(set(names)), 'Duplicate HTML attributes: ' + self.name)
        values = dict(attrs)
        if 'id' in values:
            self.ids.add(values['id'])
        if tag == 'a' and values.get('name'):
            self.ids.add(values['name'])
        if tag == 'img':
            require(bool((values.get('alt') or '').strip()), 'Missing HTML image alt: ' + self.name)
            require(bool(values.get('src') or values.get('srcset')), 'Missing HTML image source: ' + self.name)
        for attr in ('href', 'src', 'poster'):
            if values.get(attr) and (attr != 'href' or tag == 'a'):
                self.links.append((values[attr], tag in ('img', 'source', 'video')))
        if values.get('srcset'):
            for candidate in values['srcset'].split(','):
                fields = candidate.strip().split()
                require(1 <= len(fields) <= 2 and (len(fields) == 1 or re.fullmatch(r'\d+(?:\.\d+)?[wx]', fields[1])),
                        'Unsupported image srcset: ' + self.name)
                self.links.append((fields[0], True))

    handle_startendtag = handle_starttag


def unescape_markdown(value):
    return html.unescape(re.sub(r'\\([!"#$%&\'()*+,\-./:;<=>?@\[\]\\^_`{|}~])', r'\1', value))


def destination(value, name):
    value = value.strip()
    if value.startswith('<'):
        end = value.find('>')
        require(end >= 0, 'Unclosed link destination: ' + name)
        target, suffix = value[1:end], value[end + 1:].strip()
    else:
        match = re.match(r'(\S+?)(?:\s+(["\']|\().*|$)', value, re.S)
        require(match is not None, 'Invalid link destination: ' + name)
        target = match[1]
        suffix = value[len(target):].strip()
    require(not suffix or (suffix[0] in '\"\'(' and suffix[-1] == {'"': '"', "'": "'", '(': ')'}[suffix[0]]),
            'Invalid link title: ' + name)
    return unescape_markdown(target)


def markdown_links(prose, name, _references=None, _depth=0):
    """Read balanced inline destinations, reference links and HTML attributes."""
    require(_depth <= 32, 'Excessively nested Markdown links: ' + name)
    visible = inline_without_code(prose)
    references = dict(_references or {})
    def reference(match):
        key = ' '.join(match[1].casefold().split())
        references.setdefault(key, destination(match[2], name))
        return '\n' * match[0].count('\n')
    visible = re.sub(r'^\s{0,3}\[([^\]\n]+)\]:[ \t]*(.+)$', reference, visible, flags=re.M)
    links = []
    index = 0
    while index < len(visible):
        image = visible.startswith('![', index)
        start = index + 1 if image else index
        escape_start = index
        while escape_start and visible[escape_start - 1] == '\\':
            escape_start -= 1
        if visible[start:start + 1] != '[' or (index - escape_start) % 2:
            index += 1
            continue
        end, depth = start + 1, 1
        while end < len(visible) and depth:
            if visible[end] == '\\':
                end += 2
                continue
            depth += (visible[end] == '[') - (visible[end] == ']')
            end += 1
        if depth:
            index += 1
            continue
        label = visible[start + 1:end - 1]
        if '![' in label:
            links.extend(markdown_links(label, name, references, _depth + 1))
        target = None
        if visible[end:end + 1] == '(':
            cursor, depth, angle, quote = end + 1, 1, False, None
            while cursor < len(visible) and depth:
                char = visible[cursor]
                if char == '\\':
                    cursor += 2
                    continue
                if char == '<' and cursor == end + 1:
                    angle = True
                elif char == '>' and angle:
                    angle = False
                elif not angle:
                    if quote:
                        if char == quote:
                            quote = None
                    elif char in '\"\'' and cursor > end + 1 and visible[cursor - 1].isspace():
                        quote = char
                    elif char in '()':
                        depth += 1 if char == '(' else -1
                cursor += 1
            require(depth == 0, 'Unclosed Markdown link: ' + name)
            target = destination(visible[end + 1:cursor - 1], name)
            end = cursor
        elif visible[end:end + 1] == '[':
            close = visible.find(']', end + 1)
            require(close >= 0, 'Unclosed reference link: ' + name)
            key = ' '.join((visible[end + 1:close] or label).casefold().split())
            require(key in references, 'Undefined reference link: ' + name)
            target, end = references[key], close + 1
        else:
            target = references.get(' '.join(label.casefold().split()))
        if image:
            require(bool(label.strip()), 'Missing Markdown image alt: ' + name)
            require(target is not None, 'Undefined image reference: ' + name)
        if target is not None:
            links.append((target, image))
        index = end
    parser = HTMLLinks(name)
    parser.feed(visible)
    links.extend(parser.links)
    # Validate definitions even if unused: future use must not revive a bad link.
    if _references is None:
        links.extend((target, False) for target in references.values())
    return links


def resolve_link(name, target, files, documents):
    require(not any(ord(c) < 32 or ord(c) == 127 for c in target) and '\\' not in target,
            'Unsafe link destination: ' + name)
    try:
        url = urllib.parse.urlsplit(target)
    except ValueError as exc:
        raise ValueError('Malformed link destination: ' + name) from exc
    if url.scheme or url.netloc:
        require(url.scheme.lower() in ('http', 'https', 'mailto') and (not url.netloc or bool(url.hostname)),
                'Unsupported external link scheme: ' + name)
        return None
    decoded = urllib.parse.unquote(url.path)
    require('\\' not in decoded and not any(ord(c) < 32 or ord(c) == 127 for c in decoded),
            'Unsafe encoded link destination: ' + name)
    parts = [] if decoded.startswith('/') else list(PurePosixPath(name).parent.parts)
    if not decoded:
        parts = name.split('/')
    else:
        for part in decoded.split('/'):
            if part in ('', '.'):
                continue
            if part == '..':
                require(bool(parts), 'Link escapes repository: ' + name)
                parts.pop()
            else:
                parts.append(part)
    resolved = '/'.join(parts)
    if not resolved:  # Root directory links are valid, never filesystem-root links.
        return None
    is_dir = resolved not in files and any(x.startswith(resolved + '/') for x in files)
    require(resolved in files or is_dir, 'Missing local link: ' + name)
    repository_path(resolved, directory=is_dir)
    if url.fragment and resolved in documents:
        require(urllib.parse.unquote(url.fragment) in anchors(documents[resolved][0]),
                'Missing local Markdown anchor: ' + name)
    return resolved


# Every permitted SVG syntax has an inert grammar. Unknown namespaces, attributes,
# CSS properties/functions, references and foreign elements fail closed.
PAINT = re.compile(r'(?:none|currentColor|transparent|#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,24})\Z')
NUMBER = re.compile(r'-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?\Z')
NUMERIC_ATTRIBUTES = frozenset('x y x1 y1 x2 y2 cx cy r rx ry width height font-size stroke-width opacity fill-opacity stroke-opacity'.split())
COMMON_ATTRIBUTES = frozenset(('id', 'class', 'fill', 'stroke', 'stroke-width', 'opacity', 'fill-opacity',
                               'stroke-opacity', 'transform', 'aria-hidden', 'stroke-linecap', 'stroke-linejoin'))
ELEMENT_ATTRIBUTES = {
    'svg': frozenset(('viewBox', 'width', 'height', 'role', 'aria-labelledby', XML_LANG)),
    'title': frozenset(('id',)), 'desc': frozenset(('id',)), 'style': frozenset(),
    'g': COMMON_ATTRIBUTES,
    'rect': COMMON_ATTRIBUTES | frozenset(('x', 'y', 'width', 'height', 'rx', 'ry')),
    'circle': COMMON_ATTRIBUTES | frozenset(('cx', 'cy', 'r')),
    'ellipse': COMMON_ATTRIBUTES | frozenset(('cx', 'cy', 'rx', 'ry')),
    'line': COMMON_ATTRIBUTES | frozenset(('x1', 'y1', 'x2', 'y2')),
    'path': COMMON_ATTRIBUTES | frozenset(('d', 'stroke-dasharray')),
    'polyline': COMMON_ATTRIBUTES | frozenset(('points',)),
    'polygon': COMMON_ATTRIBUTES | frozenset(('points',)),
    'text': COMMON_ATTRIBUTES | frozenset(('x', 'y', 'font-size', 'font-weight', 'text-anchor', 'font-family')),
    'tspan': COMMON_ATTRIBUTES | frozenset(('x', 'y', 'font-size', 'font-weight', 'text-anchor', 'font-family')),
}
FONT_LIST = re.compile(r'(?:[A-Za-z][A-Za-z -]*|-apple-system|"[A-Za-z][A-Za-z -]*")(?:\s*,\s*(?:[A-Za-z][A-Za-z -]*|-apple-system|"[A-Za-z][A-Za-z -]*"))*\Z')


def svg_check(path):
    raw = path.read_bytes()
    name = path.name
    require(not path.is_symlink() and len(raw) <= LIMIT, 'Unsafe SVG file: ' + name)
    try:
        text = raw.decode('utf-8')
        require(not re.search(r'<!|<\?(?!xml\s)', text, re.I), 'Unsupported SVG declaration: ' + name)
        root = ET.fromstring(text)
    except (UnicodeError, ET.ParseError) as exc:
        raise ValueError('Malformed SVG: ' + name) from exc
    require(root.tag == SVG_NS + 'svg', 'Invalid SVG namespace/root: ' + name)
    ids = set()
    for node in root.iter():
        require(isinstance(node.tag, str) and node.tag.startswith(SVG_NS), 'Foreign SVG namespace: ' + name)
        tag = node.tag[len(SVG_NS):]
        require(tag in ELEMENT_ATTRIBUTES, 'Unsupported SVG element: ' + name)
        require(set(node.attrib) <= ELEMENT_ATTRIBUTES[tag], 'Unsupported SVG attribute: ' + name)
        for attr, value in node.attrib.items():
            if attr in NUMERIC_ATTRIBUTES:
                valid = bool(NUMBER.fullmatch(value)) and abs(float(value)) <= 16384
                if attr in ('width', 'height'):
                    valid = valid and 0 < float(value) <= 8192
                if 'opacity' in attr:
                    valid = valid and 0 <= float(value) <= 1
            elif attr == 'stroke-dasharray':
                # Only bounded decimal presentation values; no CSS, units or references.
                parts = value.split(' ')
                valid = (1 <= len(parts) <= 8 and all(
                    re.fullmatch(r'[0-9]+(?:\.[0-9]+)?', part) and 0 < float(part) <= 100
                    for part in parts))
            elif attr in ('fill', 'stroke'):
                valid = bool(PAINT.fullmatch(value))
            elif attr in ('id', 'class'):
                valid = bool(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]*', value))
                if attr == 'id':
                    valid = valid and value not in ids
                    ids.add(value)
            elif attr == 'transform':
                valid = bool(re.fullmatch(r'(?:(?:translate|scale|rotate|matrix|skewX|skewY)\([0-9eE+., \-]+\)\s*)+', value))
            elif attr == 'd':
                valid = bool(re.fullmatch(r'[MmZzLlHhVvCcSsQqTtAa0-9eE+., \-]+', value))
            elif attr in ('points', 'viewBox'):
                valid = bool(re.fullmatch(r'[0-9eE+., \-]+', value))
            elif attr == 'font-family':
                valid = bool(FONT_LIST.fullmatch(value))
            elif attr == 'font-weight':
                valid = bool(re.fullmatch(r'(?:[1-9]\d{0,2}|1000|normal|bold)', value))
            elif attr == 'text-anchor':
                valid = value in ('start', 'middle', 'end')
            elif attr == 'stroke-linecap':
                valid = value in ('butt', 'round', 'square')
            elif attr == 'stroke-linejoin':
                valid = value in ('miter', 'round', 'bevel')
            elif attr == 'aria-hidden':
                valid = value in ('true', 'false')
            elif attr == XML_LANG:
                valid = bool(re.fullmatch(r'[a-zA-Z]{2,8}(?:-[a-zA-Z0-9]{1,8})*', value))
            elif attr == 'role':
                valid = value == 'img'
            elif attr == 'aria-labelledby':
                valid = bool(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]*(?: [A-Za-z_][A-Za-z0-9_-]*)*', value))
            else:
                valid = False
            require(valid, 'Unsupported SVG attribute value: ' + name)
        if tag == 'style':
            css = node.text or ''
            match = re.fullmatch(r'\s*text\s*\{\s*font-family\s*:\s*([^{};]+)\s*;?\s*}\s*', css)
            require(match is not None and bool(FONT_LIST.fullmatch(match[1].strip())),
                    'Unsupported SVG stylesheet: ' + name)
        elif tag not in ('title', 'desc', 'text', 'tspan'):
            require(not (node.text or '').strip(), 'Unexpected SVG text content: ' + name)
        require(not (node.tail or '').strip(), 'Unexpected SVG trailing content: ' + name)
    for tag in ('title', 'desc'):
        nodes = root.findall(SVG_NS + tag)
        require(len(nodes) == 1 and ''.join(nodes[0].itertext()).strip(), 'Missing SVG title/description: ' + name)
    require(root.attrib.get('role') == 'img' and root.attrib.get('aria-labelledby')
            and all(key in ids for key in root.attrib['aria-labelledby'].split()), 'Missing SVG accessibility binding: ' + name)
    require('width' in root.attrib and 'height' in root.attrib and 'viewBox' in root.attrib,
            'Missing SVG viewport: ' + name)
    return digest(raw)


def static_checks(files=None):
    files = tracked_files() if files is None else set(files)
    documents = {}
    diagrams, images, links = [], set(), 0
    for name in sorted(files):
        if name.lower().endswith(('.md', '.markdown')):
            documents[name] = split_markdown(read_text(name), name)
            diagrams.extend(documents[name][1])
    require(bool(documents), 'No tracked Markdown documents')
    for name, (prose, _) in documents.items():
        for target, image in markdown_links(prose, name):
            resolved = resolve_link(name, target, files, documents)
            if resolved:
                links += 1
                if image or PurePosixPath(resolved).suffix.lower() in IMAGE_SUFFIXES:
                    images.add(resolved)
    expected_pngs = {SCREENSHOTS + '/' + name for name in PNG_NAMES}
    actual_pngs = {name for name in files if name.startswith(SCREENSHOTS + '/') and name.endswith('.png')}
    require(actual_pngs == expected_pngs, 'Expected exact 30 named documentation PNG variants')
    for name in expected_pngs:
        repository_path(name)
    images.update(expected_pngs)
    expected = {VISUALS + '/' + name for name in SVG_NAMES}
    actual = {name for name in files if name.startswith(VISUALS + '/') and name.endswith('.svg')}
    require(actual == expected, 'Expected exact 88 named documentation SVG variants')
    checked_svgs = expected | {name for name in images if name.lower().endswith('.svg')}
    svgs = {name: svg_check(repository_path(name)) for name in sorted(checked_svgs)}
    return {'markdown_files': len(documents), 'local_links': links, 'mermaid_blocks': len(diagrams),
            'svg_variants': len(svgs), 'diagrams': diagrams, 'images': images, 'svg_hashes': svgs}


def render_selection(plan, static):
    if plan['scope'] == 'full':
        return True, 'full_scope'
    changes = plan.get('changed_paths')
    require(isinstance(changes, list) and all(isinstance(p, str) for p in changes), 'Invalid changed document paths')
    if any(PurePosixPath(p).suffix.lower() == '.svg' or p in static['images'] for p in changes):
        return True, 'changed_image_asset'
    base = plan.get('base_sha')
    require(isinstance(base, str) and bool(OID.fullmatch(base)), 'Narrow document plan lacks verified base')
    old_files, new_files = tracked_files(base), tracked_files()
    for name in changes:
        safe_name(name)
        if name.lower().endswith(('.md', '.markdown')):
            old = git('show', base + ':' + name).decode('utf-8') if name in old_files else ''
            new = read_text(name) if name in new_files else ''
            new_prose, new_blocks = split_markdown(new, name)
            try:
                old_prose, old_blocks = split_markdown(old, name)
                old_images = [(target, image) for target, image in markdown_links(old_prose, name)
                              if image or PurePosixPath(urllib.parse.urlsplit(target).path).suffix.lower() in IMAGE_SUFFIXES]
            except ValueError:
                return True, 'unverified_previous_diagram_content'
            if [block['source'] for block in old_blocks] != [block['source'] for block in new_blocks]:
                return True, 'changed_mermaid_source'
            new_images = [(target, image) for target, image in markdown_links(new_prose, name)
                          if image or PurePosixPath(urllib.parse.urlsplit(target).path).suffix.lower() in IMAGE_SUFFIXES]
            if old_images != new_images:
                return True, 'changed_image_reference'
    return False, 'unchanged_diagram_sources'


# Mermaid CLI 12 exposes renderMermaid(browser, source, format, options). Our
# browser adapter owns request interception; its renderer cannot replace the
# allowlist with a more permissive handler. All pseudo-HTTPS asset reads are
# served from the pinned dependency tree, never sent to a network or source file.
MERMAID_DRIVER = r'''
import fs from 'node:fs/promises';
import path from 'node:path';
import {createRequire} from 'node:module';
import {pathToFileURL, fileURLToPath} from 'node:url';
const [root, input, output, progress] = process.argv.slice(2);
const modules = await fs.realpath(path.join(root, 'node_modules'));
const require = createRequire(path.join(root, 'package.json'));
const {default: puppeteer} = await import(pathToFileURL(require.resolve('puppeteer')).href);
const {renderMermaid} = await import(pathToFileURL(require.resolve('@mermaid-js/mermaid-cli')).href);
const index = await fs.realpath(path.join(modules, '@mermaid-js/mermaid-cli/dist/index.html'));
const browser = await puppeteer.launch({headless: 'shell', args: ['--no-sandbox', '--disable-background-networking',
  '--proxy-server=http://127.0.0.1:9', '--proxy-bypass-list=<-loopback>', '--host-resolver-rules=MAP * ~NOTFOUND']});
let blocked = false;
const isolated = {newPage: async () => {
  const page = await browser.newPage();
  await page.setOfflineMode(true);
  await page.setRequestInterception(true);
  page.on('request', async request => {
    try {
      const url = new URL(request.url());
      if (url.protocol === 'file:' && await fs.realpath(fileURLToPath(url)) === index) {
        await request.continue();
        return;
      }
      if (url.origin === 'https://mermaid-cli-intercept.invalid' && !url.search && !url.hash) {
        const filename = await fs.realpath(fileURLToPath(new URL('file://' + url.pathname)));
        const relative = path.relative(modules, filename);
        const types = {'.js':'application/javascript', '.mjs':'application/javascript', '.css':'text/css;charset=UTF-8',
          '.woff2':'font/woff2', '.woff':'font/woff', '.ttf':'font/ttf', '.json':'application/json'};
        const contentType = types[path.extname(filename)];
        if (relative && !relative.startsWith('..' + path.sep) && !path.isAbsolute(relative) && contentType) {
          await request.respond({status:200, headers:{'Access-Control-Allow-Origin':'*'}, contentType,
            body:await fs.readFile(filename)});
          return;
        }
      }
    } catch {}
    blocked = true;
    await request.abort('blockedbyclient');
  });
  const on = page.on.bind(page);
  page.on = (event, handler) => event === 'request' ? page : on(event, handler);
  return page;
}};
try {
  const diagrams = JSON.parse(await fs.readFile(input, 'utf8'));
  for (const [index, diagram] of diagrams.entries()) {
    await fs.writeFile(progress, JSON.stringify({index}));
    const result = await renderMermaid(isolated, diagram.source, 'png', {fontEmbed:false,
      mermaidConfig:{securityLevel:'strict', fontFamily:'Noto Sans CJK JP', htmlLabels:false,
        flowchart:{htmlLabels:false}, maxTextSize:131072}});
    if (blocked) throw new Error('Renderer attempted an unsupported resource request');
    await fs.writeFile(path.join(output, diagram.output), result.data);
  }
} finally { await browser.close(); }
'''


def run_quiet(command, cwd, timeout):
    subprocess.run(command, cwd=cwd, check=True, timeout=timeout,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def check_png(path):
    from PIL import Image
    require(path.is_file() and not path.is_symlink(), 'Missing rendered PNG: ' + path.name)
    with Image.open(path) as image:
        require(image.format == 'PNG' and 0 < image.width <= 16384 and 0 < image.height <= 16384,
                'Invalid rendered PNG: ' + path.name)
        image.verify()


def png_decoder_negative_checks(directory):
    """Mandatory real-decoder negative tests in the PIL-equipped document gate."""
    from PIL import Image, UnidentifiedImageError
    path = directory / 'decoder-negative.png'
    cases = ('corrupt', 'jpeg-disguised-as-png', 'oversize', 'symlink')
    for case in cases:
        if case == 'corrupt':
            path.write_bytes(b'not an image')
        elif case == 'jpeg-disguised-as-png':
            Image.new('RGB', (1, 1)).save(path, format='JPEG')
        elif case == 'oversize':
            Image.new('L', (16385, 1)).save(path, format='PNG')
        else:
            path.unlink()
            target = directory / 'decoder-symlink-target.png'
            Image.new('L', (1, 1)).save(target, format='PNG')
            path.symlink_to(target)
        try:
            check_png(path)
        except (ValueError, UnidentifiedImageError):
            pass
        else:
            raise ValueError('PNG decoder accepted invalid fixture: ' + case)
    path.unlink()
    target.unlink()
    return list(cases)


def renderer_root():
    tracked = tracked_files()
    require({'.github/docs-render/package.json', '.github/docs-render/package-lock.json'} <= tracked,
            'Renderer dependency lock must be tracked in the checked commit')
    value = os.environ.get('DOCS_RENDER_ROOT', '')
    require(bool(value), 'DOCS_RENDER_ROOT must identify the external pinned renderer install')
    root = Path(value).resolve()
    require(not root.is_relative_to(ROOT.resolve()), 'Renderer dependencies must be outside checkout')
    for name in ('package.json', 'package-lock.json'):
        require((root / name).is_file() and not (root / name).is_symlink()
                and (root / name).read_bytes() == repository_path('.github/docs-render/' + name).read_bytes(),
                'Renderer install does not match committed dependency lock')
    package = json.loads((root / 'package.json').read_text())
    installed = json.loads((root / 'node_modules/@mermaid-js/mermaid-cli/package.json').read_text())
    require(installed['version'] == package['devDependencies']['@mermaid-js/mermaid-cli'],
            'Unexpected installed Mermaid CLI version')
    for program in ('node', 'npm'):
        version = subprocess.check_output([program, '--version'], stderr=subprocess.DEVNULL, timeout=30).decode().strip().removeprefix('v')
        require(version == package['engines'][program], 'Unexpected document toolchain version: ' + program)
    return root


def render(static, output, report):
    tracked = tracked_files()
    require({VISUALS + '/' + name for name in HELPERS} <= tracked,
            'Exact SVG generator helpers must be tracked in the checked commit')
    require(output.is_dir() and not output.is_symlink() and not any(p.is_symlink() for p in output.iterdir()),
            'Rendered output must be a regular directory without symlinks')
    tools = renderer_root()
    with tempfile.TemporaryDirectory(prefix='ci-documents-') as temp:
        scratch = Path(temp)
        report['stage'] = 'png-decoder-negative-checks'
        report['png_decoder_negative_checks'] = png_decoder_negative_checks(scratch)
        for name in HELPERS:
            shutil.copyfile(repository_path(VISUALS + '/' + name), scratch / name)
        expected = {name: static['svg_hashes'][VISUALS + '/' + name] for name in SVG_NAMES}
        for name in GENERATORS:
            report['stage'] = 'svg-regenerate-' + name
            run_quiet([sys.executable, '-I', '-B', str(scratch / name)], scratch, 60)
        generated = {path.name: svg_check(path) for path in scratch.glob('*.svg')}
        require(generated == expected, 'SVG regeneration differs from exact committed variants')
        for name in VALIDATORS:
            report['stage'] = 'svg-' + name
            run_quiet([sys.executable, '-I', '-B', str(scratch / name)], scratch, 120)
        for name in SVG_NAMES:
            png = scratch / Path(name).with_suffix('.png')
            check_png(png)
            shutil.copyfile(png, output / png.name)
        # Extra documentation SVGs use the same fixed raster helper, separately
        # from the named variants' layout-specific overflow assertions.
        extras = sorted(set(static['svg_hashes']) - {VISUALS + '/' + name for name in SVG_NAMES})
        for index, name in enumerate(extras, 1):
            extra = scratch / f'extra-{index}'
            extra.mkdir()
            shutil.copyfile(repository_path(VISUALS + '/render.py'), extra / 'render.py')
            shutil.copyfile(repository_path(name), extra / 'document.svg')
            svg_check(extra / 'document.svg')
            report['stage'] = 'linked-svg-render'
            run_quiet([sys.executable, '-I', '-B', str(extra / 'render.py')], extra, 60)
            check_png(extra / 'document.png')
            shutil.copyfile(extra / 'document.png', output / f'linked-svg-{index:03}.png')
        # Other referenced raster assets are decoded without opening a browser.
        for name in sorted(static['images']):
            if PurePosixPath(name).suffix.lower() in IMAGE_SUFFIXES - {'.svg'}:
                from PIL import Image
                if name.startswith(SCREENSHOTS + '/'):
                    check_png(repository_path(name))
                else:
                    with Image.open(repository_path(name)) as image:
                        require(0 < image.width <= 16384 and 0 < image.height <= 16384, 'Invalid image dimensions: ' + name)
                        image.verify()
        driver = scratch / 'render-mermaid.mjs'
        driver.write_text(MERMAID_DRIVER, encoding='utf-8')
        manifest = []
        for index, diagram in enumerate(static['diagrams'], 1):
            mermaid_check(diagram['source'], diagram['path'], diagram['line'])
            manifest.append({'source': diagram['source'], 'output': f'mermaid-{index:03}.png'})
        if manifest:
            report['stage'] = 'mermaid-render'
            source = scratch / 'diagrams.json'
            source.write_text(json.dumps(manifest), encoding='utf-8')
            progress = scratch / 'progress.json'
            try:
                run_quiet(['node', str(driver), str(tools), str(source), str(output), str(progress)], scratch,
                          min(600, 30 + 30 * len(manifest)))
            except subprocess.SubprocessError:
                if progress.is_file():
                    index = json.loads(progress.read_text(encoding='utf-8')).get('index')
                    if type(index) is int and 0 <= index < len(static['diagrams']):
                        report['document'] = static['diagrams'][index]['path']
                        report['line'] = static['diagrams'][index]['line']
                raise
            for diagram in manifest:
                check_png(output / diagram['output'])


def inspect(plan_path):
    plan = verified_plan(plan_path)
    static = static_checks()
    selected, reason = render_selection(plan, static)
    report = {'version': 1, 'kind': 'required-documents', 'scope': plan['scope'],
              'head_sha': plan['head_sha'], 'head_tree': plan['head_tree'],
              'policy_sha256': plan['policy_sha256'], 'base_sha': plan.get('base_sha'),
              'checker_sha256': digest(Path(__file__).read_bytes()),
              'render_required': selected, 'render_reason': reason, 'rendered': False,
              **{key: static[key] for key in ('markdown_files', 'local_links', 'mermaid_blocks', 'svg_variants')}}
    return plan, static, report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('command', choices=('select', 'check'))
    parser.add_argument('--plan', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    # Artifacts/dependencies in the checkout would poison delta verification.
    require(not args.output.resolve().is_relative_to(ROOT.resolve()), 'Document outputs must be outside checkout')
    output = args.output if args.command == 'check' else args.output.parent
    output.mkdir(parents=True, exist_ok=True)
    report_file = output / 'report.json' if args.command == 'check' else args.output
    require(not report_file.is_symlink(), 'Document report cannot be a symlink')
    report = {'version': 1, 'kind': 'required-documents', 'result': 'failed', 'stage': 'static', 'rendered': False}
    try:
        plan, static, checked = inspect(args.plan)
        report.update(checked)
        if args.command == 'check' and report['render_required']:
            render(static, output, report)
            report['rendered'] = True
        # Prove helper execution and dependency installation did not change scope
        # or bytes. A selected plan is only a setup hint, never reusable evidence.
        require(verified_plan(args.plan) == plan, 'Document plan changed during checks')
        report['stage'] = 'complete'
        report['result'] = 'selected' if args.command == 'select' else 'passed'
        if args.command == 'select' and os.environ.get('GITHUB_OUTPUT'):
            with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
                stream.write('render_required=' + str(report['render_required']).lower() + '\n')
        print('Documents: ' + report['result'] + '; render_required=' + str(report['render_required']).lower())
        return 0
    except Exception as error:
        report['result'] = 'failed'
        report['error_type'] = type(error).__name__
        # Never publish source text, subprocess output, environment or host paths.
        if isinstance(error, ValueError):
            report['reason'] = str(error)
            print('::error::' + str(error), file=sys.stderr)
        else:
            location = (': ' + report['document'] + ':' + str(report['line'])) if 'document' in report else ''
            print('::error::Document checks failed at ' + report['stage'] + location, file=sys.stderr)
        return 1
    finally:
        report_file.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n', encoding='utf-8')


if __name__ == '__main__':
    sys.exit(main())
