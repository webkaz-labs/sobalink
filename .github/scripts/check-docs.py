#!/usr/bin/env python3
"""One-off documentation evidence; never replaces required application CI."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import urllib.parse
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2]
BASE = '46dcd363b3eafc8ed9b92c7403937cf4d11d65af'
# Explicit reviewed publication boundary, not a general docs-only classifier.
REVIEWED = set('''
README.en.md
README.md
SECURITY.md
docs/ARCHITECTURE.md
docs/BATCH_SEND_ACCEPTANCE.en.md
docs/BATCH_SEND_ACCEPTANCE.ja.md
docs/CAPACITY.en.md
docs/CAPACITY.ja.md
docs/CATALOG_VIEW_CONTRACT.en.md
docs/CATALOG_VIEW_CONTRACT.ja.md
docs/CI_EFFICIENCY.en.md
docs/CI_EFFICIENCY.ja.md
docs/CLIENT_HELPERS.en.md
docs/CLIENT_HELPERS.ja.md
docs/CLI_GUIDE.en.md
docs/CLI_GUIDE.ja.md
docs/CONNECTION_SETUP.en.md
docs/CONNECTION_SETUP.ja.md
docs/CONVENIENCE_PLAN.en.md
docs/CONVENIENCE_PLAN.ja.md
docs/DEVELOPMENT_PRINCIPLES.en.md
docs/DEVELOPMENT_PRINCIPLES.ja.md
docs/DEVICE_CARDS.en.md
docs/DEVICE_CARDS.ja.md
docs/DIRECT_LAN.en.md
docs/DIRECT_LAN.ja.md
docs/DISTRIBUTION.md
docs/DURABLE_OUTBOX.en.md
docs/DURABLE_OUTBOX.ja.md
docs/DURABLE_TEXT.en.md
docs/DURABLE_TEXT.ja.md
docs/FEATURE_PARITY.en.md
docs/FEATURE_PARITY.ja.md
docs/GENERIC.en.md
docs/GENERIC.ja.md
docs/LAN.en.md
docs/LAN.ja.md
docs/LAN_DESTINATIONS.en.md
docs/LAN_DESTINATIONS.ja.md
docs/LIFECYCLE.en.md
docs/LIFECYCLE.ja.md
docs/MIXED_CONNECTIONS.en.md
docs/MIXED_CONNECTIONS.ja.md
docs/PORT_PROPOSALS.en.md
docs/PORT_PROPOSALS.ja.md
docs/PROXY_DIAGNOSTICS.en.md
docs/PROXY_DIAGNOSTICS.ja.md
docs/README.en.md
docs/README.md
docs/RELAY_OPERATIONS.en.md
docs/RELAY_OPERATIONS.ja.md
docs/RELAY_TRAFFIC_MEASUREMENT.en.md
docs/RELAY_TRAFFIC_MEASUREMENT.ja.md
docs/RESOURCE_CATALOG.en.md
docs/RESOURCE_CATALOG.ja.md
docs/RESOURCE_GROUP.en.md
docs/RESOURCE_GROUP.ja.md
docs/RESOURCE_INSPECTION.en.md
docs/RESOURCE_INSPECTION.ja.md
docs/RESOURCE_MANAGEMENT.en.md
docs/RESOURCE_MANAGEMENT.ja.md
docs/RESOURCE_MEASUREMENT.en.md
docs/RESOURCE_MEASUREMENT.ja.md
docs/RESOURCE_SETTINGS.en.md
docs/RESOURCE_SETTINGS.ja.md
docs/ROADMAP.en.md
docs/ROADMAP.ja.md
docs/ROUTE_RECOVERY_DESIGN.en.md
docs/ROUTE_RECOVERY_DESIGN.ja.md
docs/SAVED_SERVICES.en.md
docs/SAVED_SERVICES.ja.md
docs/SECURITY.ja.md
docs/SIGN_IN.en.md
docs/SIGN_IN.ja.md
docs/STARTUP.en.md
docs/STARTUP.ja.md
docs/USE_CASES.ja.md
docs/VERIFICATION.en.md
docs/VERIFICATION.md
docs/WEB_ACTIVATION_ACCEPTANCE_PROPOSAL.md
docs/WEB_ACTIVATION_HANDOFF.en.md
docs/WEB_ACTIVATION_HANDOFF.ja.md
docs/WEB_CONTROLS.en.md
docs/WEB_CONTROLS.ja.md
internal/backendworker/README.md
internal/connectionroute/README.md
internal/endpointmeta/README.md
internal/endpointmeta/testdata/README.md
internal/nearby/DNSSD.en.md
internal/nearby/DNSSD.ja.md
internal/nearby/README.md
web/API.md
web/BROWSER_ACCEPTANCE.md
web/UX_ACCEPTANCE.md
web/browser/png-fixtures/README.md
web/src/catalog/INTEGRATION.md
web/src/group/INTEGRATION.md
web/src/resource/INTEGRATION.md
docs/FAQ.en.md
docs/FAQ.ja.md
docs/TROUBLESHOOTING.en.md
docs/TROUBLESHOOTING.ja.md
docs/USE_CASES.en.md
docs/assets/visuals/.gitignore
docs/assets/visuals/README.md
docs/assets/visuals/build.py
docs/assets/visuals/connection-en-dark-narrow.svg
docs/assets/visuals/connection-en-dark-wide.svg
docs/assets/visuals/connection-en-light-narrow.svg
docs/assets/visuals/connection-en-light-wide.svg
docs/assets/visuals/connection-ja-dark-narrow.svg
docs/assets/visuals/connection-ja-dark-wide.svg
docs/assets/visuals/connection-ja-light-narrow.svg
docs/assets/visuals/connection-ja-light-wide.svg
docs/assets/visuals/delivery-evidence-en-dark-narrow.svg
docs/assets/visuals/delivery-evidence-en-dark-wide.svg
docs/assets/visuals/delivery-evidence-en-light-narrow.svg
docs/assets/visuals/delivery-evidence-en-light-wide.svg
docs/assets/visuals/delivery-evidence-ja-dark-narrow.svg
docs/assets/visuals/delivery-evidence-ja-dark-wide.svg
docs/assets/visuals/delivery-evidence-ja-light-narrow.svg
docs/assets/visuals/delivery-evidence-ja-light-wide.svg
docs/assets/visuals/explainers.py
docs/assets/visuals/permissions-en-dark-narrow.svg
docs/assets/visuals/permissions-en-dark-wide.svg
docs/assets/visuals/permissions-en-light-narrow.svg
docs/assets/visuals/permissions-en-light-wide.svg
docs/assets/visuals/permissions-ja-dark-narrow.svg
docs/assets/visuals/permissions-ja-dark-wide.svg
docs/assets/visuals/permissions-ja-light-narrow.svg
docs/assets/visuals/permissions-ja-light-wide.svg
docs/assets/visuals/render.py
docs/assets/visuals/saved-and-running-en-dark-narrow.svg
docs/assets/visuals/saved-and-running-en-dark-wide.svg
docs/assets/visuals/saved-and-running-en-light-narrow.svg
docs/assets/visuals/saved-and-running-en-light-wide.svg
docs/assets/visuals/saved-and-running-ja-dark-narrow.svg
docs/assets/visuals/saved-and-running-ja-dark-wide.svg
docs/assets/visuals/saved-and-running-ja-light-narrow.svg
docs/assets/visuals/saved-and-running-ja-light-wide.svg
docs/assets/visuals/verify.py
.github/workflows/docs-review.yml
.github/scripts/check-docs.py
.github/docs-render/package.json
.github/docs-render/package-lock.json
'''.split())
VISUALS = ROOT / 'docs/assets/visuals'
SVG_NAMES = {f'{kind}-{lang}-{theme}-{size}.svg'
             for kind in ('connection', 'permissions', 'saved-and-running', 'delivery-evidence')
             for lang in ('en', 'ja') for theme in ('light', 'dark')
             for size in ('wide', 'narrow')}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, text=True).strip()


def scope(local=False):
    head = git('rev-parse', 'HEAD')
    if not local:
        require(os.environ.get('GITHUB_EVENT_NAME') == 'create', 'Expected create event')
        require(os.environ.get('GITHUB_REPOSITORY') == 'webkaz-labs/sobalink', 'Unexpected repository')
        require(os.environ.get('GITHUB_SHA') == head, 'Checkout does not equal event SHA')
        event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
        ref = event.get('ref', '')
        require(event.get('ref_type') == 'branch' and re.fullmatch(r'docs-review/[A-Za-z0-9._-]+', ref),
                'Expected a fresh docs-review branch')
        require(os.environ.get('GITHUB_REF') == 'refs/heads/' + ref, 'Event ref mismatch')
        require(not git('status', '--porcelain', '--untracked-files=no'), 'Dirty checkout')
    subprocess.run(['git', 'merge-base', '--is-ancestor', BASE, head], cwd=ROOT, check=True)
    changed = set(git('diff', '--name-only', '--no-renames', BASE, *([] if local else [head])).splitlines())
    if local:
        changed.update(git('ls-files', '--others', '--exclude-standard').splitlines())
    require(changed and changed <= REVIEWED, 'Change outside reviewed document/asset/CI boundary')
    for name in changed:
        path = ROOT / name
        require(path.is_file() and not path.is_symlink(), 'Deleted or non-regular reviewed file: ' + name)
    return head, len(changed)


def slugs(text):
    found, counts = set(), {}
    fence = None
    for line in text.splitlines():
        match = re.match(r'^\s{0,3}(`{3,}|~{3,})', line)
        if match:
            token = match[1]
            if fence is None:
                fence = token
            elif token[0] == fence[0] and len(token) >= len(fence):
                fence = None
            continue
        if fence:
            continue
        match = re.match(r'^#{1,6}\s+(.+?)(?:\s+#+)?\s*$', line)
        if match:
            slug = re.sub(r'<[^>]+>', '', match[1]).lower()
            slug = ''.join(c for c in slug if c in '-_ ' or unicodedata.category(c)[0] in 'LN').replace(' ', '-')
            n = counts.get(slug, 0)
            counts[slug] = n + 1
            found.add(slug + (f'-{n}' if n else ''))
    found.update(re.findall(r'<(?:a|h[1-6])[^>]+(?:id|name)="([^"]+)"', text))
    return found


def markdown():
    names = set(git('ls-files', '*.md').splitlines()) | {p for p in REVIEWED if p.endswith('.md')}
    diagrams, links = [], 0
    for name in sorted(names):
        path = ROOT / name
        text = path.read_text()
        fence, block, language = None, [], ''
        prose = []
        for line in text.splitlines():
            match = re.match(r'^\s{0,3}(`{3,}|~{3,})(.*)$', line)
            if match and fence is None:
                fence, language = match[1], match[2].strip()
                block = []
            elif match and fence and match[1][0] == fence[0] and len(match[1]) >= len(fence) and not match[2].strip():
                if language == 'mermaid':
                    diagrams.append('\n'.join(block))
                fence = None
            elif fence:
                block.append(line)
            else:
                prose.append(line)
        require(fence is None, 'Unclosed fence: ' + name)
        prose = '\n'.join(prose)
        require(not re.search(r'!\[\s*\](?:\(|\[)', prose), 'Missing image alt: ' + name)
        for tag in re.findall(r'<img\b[^>]*>', prose, re.I):
            require(re.search(r'\balt=["\'][^"\']+?["\']', tag), 'Missing HTML image alt: ' + name)
        targets = re.findall(r'\]\(([^\s\)]+)(?:\s+"[^"]*")?\)', prose)
        targets += re.findall(r'^\s*\[[^\]]+\]:\s*(\S+)', prose, re.M)
        targets += re.findall(r'(?:src|srcset)="([^"]+)"', prose)
        for target in targets:
            target = target.strip('<>')
            url = urllib.parse.urlsplit(target)
            if url.scheme or url.netloc:
                continue
            dest = (path.parent / urllib.parse.unquote(url.path)).resolve() if url.path else path
            require(dest.is_relative_to(ROOT), 'Link escapes repository: ' + name)
            require(dest.exists(), 'Missing local link: ' + name)
            if url.fragment and dest.suffix == '.md':
                require(urllib.parse.unquote(url.fragment) in slugs(dest.read_text()), 'Missing local anchor: ' + name)
            links += 1
    require(len(diagrams) == 31, 'Expected 31 Mermaid diagrams')
    return len(names), links, diagrams


def svg_check(path):
    text = path.read_text()
    require(not re.search(r'<!DOCTYPE|<!ENTITY|<\?(?!xml\s)|@import', text, re.I), 'Unsafe SVG declaration: ' + path.name)
    root = ET.fromstring(text)
    ns = '{http://www.w3.org/2000/svg}'
    require(root.tag == ns + 'svg', 'Invalid SVG root: ' + path.name)
    require(root.find(ns + 'title') is not None and root.find(ns + 'desc') is not None,
            'Missing SVG title/description: ' + path.name)
    for node in root.iter():
        require(node.tag.split('}')[-1] not in ('script', 'foreignObject', 'image', 'iframe'),
                'Active or external SVG element: ' + path.name)
        for key, value in node.attrib.items():
            attr = key.split('}')[-1].lower()
            require(not attr.startswith('on'), 'SVG event handler: ' + path.name)
            if attr in ('href', 'src'):
                require(value.startswith('#'), 'External SVG reference: ' + path.name)
        for value in list(node.attrib.values()) + [node.text or '']:
            for url in re.findall(r'url\(\s*["\']?([^\)"\']+)', value, re.I):
                require(url.startswith('#'), 'External SVG URL: ' + path.name)
    return hashlib.sha256(path.read_bytes()).hexdigest()


def render(diagrams, output, report):
    require({p.name for p in VISUALS.glob('*.svg')} == SVG_NAMES, 'Expected exact 32 SVG variants')
    before = {name: svg_check(VISUALS / name) for name in SVG_NAMES}
    with tempfile.TemporaryDirectory() as temp:
        scratch = Path(temp)
        for name in ('build.py', 'explainers.py', 'render.py', 'verify.py'):
            shutil.copyfile(VISUALS / name, scratch / name)
        for name in ('build.py', 'explainers.py'):
            report['stage'] = 'svg-regenerate-' + name
            subprocess.run([sys.executable, '-B', str(scratch / name)], check=True, timeout=60, stdout=subprocess.DEVNULL)
        after = {p.name: svg_check(p) for p in scratch.glob('*.svg')}
        require(before == after, 'SVG regeneration differs from committed assets')
        for name in ('render.py', 'verify.py'):
            report['stage'] = 'svg-' + name
            subprocess.run([sys.executable, '-B', str(scratch / name)], check=True, timeout=120, stdout=subprocess.DEVNULL)
        for path in scratch.glob('*.png'):
            shutil.copyfile(path, output / path.name)
        # Strict Mermaid configuration; no frontmatter/directives/remote references.
        config = scratch / 'mermaid.json'
        config.write_text(json.dumps({'securityLevel': 'strict', 'fontFamily': 'Noto Sans CJK JP'}))
        puppeteer = scratch / 'puppeteer.json'
        # Ephemeral read-only CI worker; Chrome sandbox is unavailable on hosted Ubuntu.
        puppeteer.write_text(json.dumps({'args': ['--no-sandbox']}))
        mmdc = ROOT / '.github/docs-render/node_modules/.bin/mmdc'
        for index, source in enumerate(diagrams, 1):
            report['stage'] = 'mermaid-render'
            report['mermaid_index'] = index
            require(not re.search(r'https?://|%%\{|^---|\bclick\s|<', source, re.M | re.I),
                    'Unsupported Mermaid external content or configuration')
            source_file = scratch / 'diagram.mmd'
            source_file.write_text(source)
            dest = output / f'mermaid-{index:02}.png'
            subprocess.run([str(mmdc), '-i', str(source_file), '-o', str(dest), '-c', str(config), '-p', str(puppeteer)],
                           check=True, timeout=60, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            from PIL import Image
            with Image.open(dest) as image:
                require(image.width > 0 and image.height > 0, 'Empty rendered Mermaid image')
                image.verify()
    return len(before)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--local', action='store_true', help='Check uncommitted edits; not CI evidence')
    parser.add_argument('--static', action='store_true', help='Do not claim rendering')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    report = {'scope': 'supplemental documentation only', 'required_application_checks': 'not evaluated',
              'rendered': False, 'local': args.local}
    try:
        report['stage'] = 'scope'
        report['head'], report['changed_files'] = scope(args.local)
        report['stage'] = 'markdown'
        report['markdown_files'], report['local_links'], diagrams = markdown()
        report['mermaid_blocks'] = len(diagrams)
        report['stage'] = 'svg-static'
        require({p.name for p in VISUALS.glob('*.svg')} == SVG_NAMES, 'Expected exact 32 SVG variants')
        for name in SVG_NAMES:
            svg_check(VISUALS / name)
        if not args.static:
            report['svg_variants'] = render(diagrams, args.output, report)
            report['rendered'] = True
        report['stage'] = 'complete'
        report.pop('mermaid_index', None)
        report['result'] = 'passed'
    except Exception as error:
        # Do not publish source contents, runner state, environment or command output.
        report['result'] = 'failed'
        report['error_type'] = type(error).__name__
        if isinstance(error, ValueError):
            report['reason'] = str(error)
        raise
    finally:
        (args.output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
