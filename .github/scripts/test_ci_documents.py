"""Offline, cross-platform regression tests for required document evidence."""
import copy
import base64
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('ci_documents', Path(__file__).with_name('ci-documents.py'))
documents = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(documents)
SOURCE_ROOT = documents.ROOT
SVG = '''<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50" viewBox="0 0 100 50"
role="img" aria-labelledby="title desc"><title id="title">Example</title><desc id="desc">A fictional diagram.</desc>
<style>text{font-family:-apple-system,"Noto Sans CJK JP",sans-serif}</style>
<rect x="0" y="0" width="100" height="50" fill="#fff"/><text x="10" y="25" font-size="12">Test</text></svg>'''
DIAGRAM = 'flowchart LR\n A[One] --> B[Two]'


class MarkdownTests(unittest.TestCase):
    def test_fences_are_balanced_and_diagrams_have_no_fixed_count(self):
        for marker in ('```', '~~~~', '> ```', '- ```', '    ```'):
            prose, blocks = documents.split_markdown(f'Words\n{marker}MERMAID\n{DIAGRAM}\n{marker}\nEnd', 'doc.md')
            self.assertEqual(len(blocks), 1)
            self.assertEqual(blocks[0]['source'], DIAGRAM)
            self.assertNotIn('flowchart', prose)
        self.assertEqual(documents.split_markdown('No diagrams')[1], [])
        self.assertEqual(len(documents.split_markdown(('```mermaid\n' + DIAGRAM + '\n```\n') * 37)[1]), 37)
        for text in ('```python\nexample', '~~~\n```', '```mermaid extra\n' + DIAGRAM + '\n```'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                documents.split_markdown(text, 'doc.md')

    def test_mermaid_configuration_and_external_resources_are_rejected(self):
        for source in ('%%{init: {"securityLevel":"loose"}}%%\n' + DIAGRAM,
                       '  ---\nconfig: x\n---\n' + DIAGRAM,
                       DIAGRAM + '\nclick A "https://example.invalid"',
                       'flowchart LR\n A[<img src="file:///tmp/private">]',
                       'flowchart LR\n A[&lt;img&gt;]', 'flowchart LR\n A@{img:"x.png"}',
                       DIAGRAM + '\nstyle A fill:url(#x)', DIAGRAM + '\nclassDef x color:red',
                       'flowchart LR\n A[data:image/png;base64,AAA]',
                       'flowchart LR\n A[//example.invalid/x]', 'unknownDiagram\n A --> B'):
            with self.subTest(source=source), self.assertRaises(ValueError):
                documents.mermaid_check(source)
        documents.mermaid_check(DIAGRAM)
        documents.mermaid_check('stateDiagram-v2\n [*] --> Pending: Valid fictional offer')

    def test_links_include_balanced_titles_references_html_and_srcset(self):
        text = '''[nested](docs/a(b).md "Title") [spaces](<docs/a b.md>)
![Diagram][picture] [More][doc] [doc]
[picture]: assets/one.svg
[doc]: docs/page.md#section
<a href='docs/other.md'>other</a>
<img src='assets/two.png' alt='A useful description'>
<source srcset='assets/three.svg 1x, assets/four.svg 2x'>
`[not a link](absent.md)`
'''
        links = documents.markdown_links(text, 'README.md')
        for expected in (('docs/a(b).md', False), ('docs/a b.md', False), ('assets/one.svg', True),
                         ('docs/page.md#section', False), ('docs/other.md', False),
                         ('assets/two.png', True), ('assets/three.svg', True), ('assets/four.svg', True)):
            self.assertIn(expected, links)
        self.assertNotIn(('absent.md', False), links)

    def test_link_escape_uses_backslash_parity(self):
        self.assertIn(('missing.md', False), documents.markdown_links(r'\\[link](missing.md)', 'README.md'))
        self.assertNotIn(('missing.md', False), documents.markdown_links(r'\[link](missing.md)', 'README.md'))

    def test_nested_linked_images_are_checked(self):
        links = documents.markdown_links('[![Alt](missing.svg)](README.md)', 'README.md')
        self.assertIn(('missing.svg', True), links)
        links = documents.markdown_links('[![Alt][image]](README.md)\n[image]: missing.svg', 'README.md')
        self.assertIn(('missing.svg', True), links)
        for text in ('[![](known.svg)](README.md)', '[![Alt][missing]](README.md)'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                documents.markdown_links(text, 'README.md')

    def test_missing_alt_reference_or_link_closer_fails(self):
        for text in ('![](asset.png)', '![ ](asset.png)', '![][pic]\n[pic]: image.svg',
                     '<img src="x.png">', "<img src='x.png' alt='   '>",
                     '[label][missing]', '![label][missing]', '[label](missing.md',
                     '<source srcset="x.svg absurd">',
                     "<img src='missing.svg' src='known.svg' alt='' alt='good'>",
                     "<a href='missing.md' href='known.md'>link</a>"):
            with self.subTest(text=text), self.assertRaises(ValueError):
                documents.markdown_links(text, 'doc.md')

    def test_heading_anchors_include_setext_duplicates_inline_labels_and_ids(self):
        prose, _ = documents.split_markdown('''# One
# One
# One-1
## 日本語 `test` &amp; [words](elsewhere.md)
Setext heading
--------------
<a id='explicit'></a>
```text
# Ignored
```
''')
        self.assertEqual(documents.anchors(prose), {'one', 'one-1', 'one-1-1',
                         '日本語-test--words', 'setext-heading', 'explicit'})


class RepositoryStaticTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.root_patch = patch.object(documents, 'ROOT', self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.addCleanup(self.temp.cleanup)
        self.files = set()
        for name in documents.SVG_NAMES:
            self.write(documents.VISUALS + '/' + name, SVG)
        for name in documents.PNG_NAMES:
            path = self.write(documents.SCREENSHOTS + '/' + name, '')
            path.write_bytes(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR4nGNgAAIAAAUAAXpeqz8AAAAASUVORK5CYII='))
        self.write('README.md', '# Heading\n')

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding='utf-8')
        self.files.add(name)
        return path

    def test_repository_wide_static_includes_internal_markdown_and_references(self):
        self.write('internal/component/README.md', '# Component\n[Home](../../README.md#heading)\n')
        self.write('docs/guide.md', '# Guide\n![Alt](assets/visuals/connection-en-light-wide.svg)\n')
        result = documents.static_checks(self.files)
        self.assertEqual(result['markdown_files'], 3)
        self.assertEqual(result['local_links'], 2)
        self.assertEqual(result['svg_variants'], 88)
        self.assertEqual(result['images'], {'docs/assets/visuals/connection-en-light-wide.svg'} | {documents.SCREENSHOTS + '/' + n for n in documents.PNG_NAMES})

    def test_unreferenced_known_png_still_selects_render(self):
        result = documents.static_checks(self.files)
        name = documents.SCREENSHOTS + '/' + sorted(documents.PNG_NAMES)[0]
        self.assertEqual(documents.render_selection({'scope': 'docs', 'changed_paths': [name]}, result),
                         (True, 'changed_image_asset'))
        self.files.remove(name)
        with self.assertRaisesRegex(ValueError, 'exact 30'):
            documents.static_checks(self.files)

    def test_known_png_symlink_is_rejected_without_decoder_dependency(self):
        name = documents.SCREENSHOTS + '/' + sorted(documents.PNG_NAMES)[0]
        path = self.root / name
        target = self.root / 'target.png'
        target.write_bytes(path.read_bytes())
        path.unlink()
        try:
            path.symlink_to(target)
        except (OSError, NotImplementedError):
            self.skipTest('Symlink creation unavailable on this host')
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)

    def test_missing_links_and_anchors_fail(self):
        for source in ('[bad](missing.md)', '[bad](README.md#missing)', '[bad](../outside.md)',
                       '[bad](%2e%2e/outside.md)', '[bad](file:///etc/passwd)',
                       '[bad](data:text/plain,x)', '[bad](javascript:alert(1))', '[bad](docs%5cfile.md)',
                       '[bad](README.md%00)'):
            self.write('README.md', source)
            with self.subTest(source=source), self.assertRaises(ValueError):
                documents.static_checks(self.files)

    def test_tracked_only_links_and_repository_root_links(self):
        self.write('docs/target.md', '# Target\n')
        self.write('README.md', '[root](/docs/target.md#target) [folder](docs/)')
        self.assertEqual(documents.static_checks(self.files)['local_links'], 2)
        untracked = self.write('untracked.md', '# Untracked')
        self.files.remove('untracked.md')
        self.write('README.md', '[No](untracked.md)')
        self.assertTrue(untracked.exists())
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)

    def test_symlink_asset_and_parent_directory_are_rejected(self):
        target = self.write('target.md', '# Target')
        linked = self.root / 'alias.md'
        try:
            linked.symlink_to(target)
        except (OSError, NotImplementedError):
            self.skipTest('Symlink creation unavailable on this host')
        self.files.add('alias.md')
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)
        linked.unlink()
        self.files.remove('alias.md')
        folder = self.root / 'alias'
        folder.symlink_to(self.root / 'docs', target_is_directory=True)
        with self.assertRaises(ValueError):
            documents.repository_path('alias/assets/visuals/connection-en-light-wide.svg')

    def test_unreferenced_application_svg_is_not_a_document_schema(self):
        self.write('web/public/logo.svg', '<svg><defs/></svg>')
        self.assertEqual(documents.static_checks(self.files)['svg_variants'], 88)
        self.write('README.md', '![Logo](web/public/logo.svg)')
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)

    def test_exact_svg_inventory_is_required(self):
        self.files.remove('docs/assets/visuals/connection-en-light-wide.svg')
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)
        self.files.add('docs/assets/visuals/connection-en-light-wide.svg')
        self.write('docs/assets/visuals/unreviewed.svg', SVG)
        with self.assertRaises(ValueError):
            documents.static_checks(self.files)

    def test_svg_allowlist_rejects_active_elements_attributes_namespaces_and_css(self):
        valid = self.write('test.svg', SVG)
        self.assertEqual(len(documents.svg_check(valid)), 64)
        payloads = [
            SVG.replace('<rect ', '<rect onload="alert(1)" '),
            SVG.replace('<rect ', '<rect style="fill:red" '),
            SVG.replace('<rect ', '<rect href="#title" '),
            SVG.replace('<rect ', '<rect xmlns:x="urn:unknown" x:fill="red" '),
            SVG.replace('<rect ', '<rect xml:base="https://example.invalid/" '),
            SVG.replace('<rect ', '<rect unknown="safe" '),
            SVG.replace('</svg>', '<script>bad</script></svg>'),
            SVG.replace('</svg>', '<animate attributeName="x"/></svg>'),
            SVG.replace('</svg>', '<foreignObject/></svg>'),
            SVG.replace('</svg>', '<image href="https://example.invalid/"/></svg>'),
            SVG.replace('</svg>', '<use href="#title"/></svg>'),
            SVG.replace('</svg>', '<a href="file:///tmp/private"/></svg>'),
            SVG.replace('</svg>', '<g xmlns="urn:foreign"/></svg>'),
            SVG.replace('fill="#fff"', 'fill="url(#title)"'),
            SVG.replace('text{font-family:', '@import "https://example.invalid/";text{font-family:'),
            SVG.replace('text{font-family:', 'text{filter:url(file:///tmp/private);font-family:'),
            SVG.replace('text{font-family:', 'text{f\\6fnt-family:'),
            SVG.replace('<style>', '<style>@font-face{font-family:x;src:url(x)}'),
            '<!DOCTYPE svg [<!ENTITY e SYSTEM "file:///tmp/private">]>' + SVG,
            '<?xml-stylesheet href="http://example.invalid"?>' + SVG,
            SVG.replace('width="100"', 'width="100000000"'),
            SVG.replace('aria-labelledby="title desc"', 'aria-labelledby="missing"'),
            SVG.replace('<title id="title">Example</title>', ''),
            SVG.replace('<desc id="desc">A fictional diagram.</desc>', '<desc id="desc"> </desc>'),
        ]
        for index, source in enumerate(payloads):
            valid.write_text(source, encoding='utf-8')
            with self.subTest(index=index), self.assertRaises(ValueError):
                documents.svg_check(valid)


    def test_image_allowlists_match_and_helpers_remain_explicit(self):
        spec = importlib.util.spec_from_file_location('impact_for_svg_test', Path(__file__).with_name('ci-impact.py'))
        impact = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(impact)
        self.assertEqual(impact.DOC_IMAGES, {documents.VISUALS + '/' + n for n in documents.SVG_NAMES} | impact.DOC_SCREENSHOTS)
        self.assertEqual(impact.DOC_SCREENSHOTS, {
            'docs/assets/screenshots/direct-review-en-focused.png',
            'docs/assets/screenshots/direct-review-ja-focused.png',
            'docs/assets/screenshots/message-context-en-narrow-input.png',
            'docs/assets/screenshots/message-context-en.png',
            'docs/assets/screenshots/message-context-ja-narrow-input.png',
            'docs/assets/screenshots/message-context-ja.png',
            'docs/assets/screenshots/navigation-detail-en-narrow.png',
            'docs/assets/screenshots/navigation-detail-ja-narrow.png',
            'docs/assets/screenshots/navigation-graph-en-narrow.png',
            'docs/assets/screenshots/navigation-graph-en-wide.png',
            'docs/assets/screenshots/navigation-graph-ja-narrow.png',
            'docs/assets/screenshots/navigation-graph-ja-wide.png',
            'docs/assets/screenshots/receive-damaged-en-wide.png',
            'docs/assets/screenshots/receive-damaged-ja-wide.png',
            'docs/assets/screenshots/receive-en.png',
            'docs/assets/screenshots/receive-ja.png',
            'docs/assets/screenshots/receive-legacy-en-wide.png',
            'docs/assets/screenshots/receive-legacy-ja-wide.png',
            'docs/assets/screenshots/service-review-en.png',
            'docs/assets/screenshots/service-review-ja.png',
            'docs/assets/screenshots/setup-fixed-en-1180.png',
            'docs/assets/screenshots/setup-fixed-en-390.png',
            'docs/assets/screenshots/setup-fixed-ja-1180.png',
            'docs/assets/screenshots/setup-fixed-ja-390.png',
            'docs/assets/screenshots/startup-guide-en-narrow-actions.png',
            'docs/assets/screenshots/startup-guide-en-wide.png',
            'docs/assets/screenshots/startup-guide-ja-narrow-actions.png',
            'docs/assets/screenshots/startup-guide-ja-wide.png',
            'docs/assets/screenshots/stopped-review-en-focused.png',
            'docs/assets/screenshots/stopped-review-ja-focused.png',
        })
        self.assertEqual(impact.DOC_SCREENSHOTS, {documents.SCREENSHOTS + '/' + n for n in documents.PNG_NAMES})
        self.assertEqual(len(documents.PNG_NAMES), 30)
        self.assertEqual(len(documents.SVG_NAMES), 88)
        self.assertEqual(documents.GENERATORS, ('build.py', 'explainers.py', 'semantic.py'))
        self.assertEqual(documents.VALIDATORS, ('render.py', 'verify.py'))

    def test_bounded_dash_array_is_path_presentation_only(self):
        path = self.root / 'dash.svg'
        for value in ('1', '6 4', '0.5 100', '1 2 3 4 5 6 7 8'):
            with self.subTest(valid=value):
                path.write_text(SVG.replace('</svg>', '<path d="M0 0L1 1" stroke-dasharray="'+value+'"/></svg>'))
                documents.svg_check(path)
        for value in ('0', '-1', 'NaN', 'inf', '1e2', '+1', '.5', '1.', '101',
                      '1px', 'url(x)', 'none', '1,2', '1  2', ' 1', '1 ',
                      '1 2 3 4 5 6 7 8 9', '9999999999999999999999999999999999999'):
            with self.subTest(invalid=value):
                path.write_text(SVG.replace('</svg>', '<path d="M0 0L1 1" stroke-dasharray="'+value+'"/></svg>'))
                with self.assertRaisesRegex(ValueError, "Unsupported SVG attribute value"): documents.svg_check(path)
        for body in ('<g stroke-dasharray="1 2"/>', '<style>path{stroke-dasharray:1 2}</style>'):
            path.write_text(SVG.replace('</svg>', body+'</svg>'))
            with self.assertRaisesRegex(ValueError, "Unsupported SVG (attribute|stylesheet)"): documents.svg_check(path)


class SelectionTests(unittest.TestCase):
    def select(self, before, after, *, changes=None, scope='docs', images=()):
        plan = {'scope': scope, 'base_sha': 'a' * 40, 'changed_paths': changes or ['docs/guide.md']}
        def git(*args):
            self.assertEqual(args[0], 'show')
            return before.encode()
        with patch.object(documents, 'tracked_files', return_value={'docs/guide.md'}), \
                patch.object(documents, 'git', side_effect=git), \
                patch.object(documents, 'read_text', return_value=after):
            return documents.render_selection(plan, {'images': set(images)})

    def test_prose_only_changes_do_not_render_existing_diagrams(self):
        text = '```mermaid\n' + DIAGRAM + '\n```\n'
        self.assertEqual(self.select(text + 'Old prose', text + 'New prose'),
                         (False, 'unchanged_diagram_sources'))
        self.assertFalse(self.select('Prose', 'More prose')[0])

    def test_added_modified_removed_diagrams_render(self):
        text = '```mermaid\n' + DIAGRAM + '\n```\n'
        for before, after in (('Old', text), (text, 'New'), (text, text.replace('Two', 'Three')),
                              ('Old', text.replace('```mermaid', '~~~MERMAID').replace('```', '~~~'))):
            with self.subTest(before=before, after=after):
                self.assertTrue(self.select(before, after)[0])

    def test_svg_and_referenced_image_bytes_render(self):
        self.assertTrue(self.select('', '', changes=['docs/assets/visuals/connection-en-light-wide.svg'])[0])
        self.assertTrue(self.select('', '', changes=['docs/image.png'], images=['docs/image.png'])[0])
        self.assertTrue(self.select('', '', changes=['docs/image.png'], scope='full')[0])

    def test_image_reference_changes_render_in_every_supported_syntax(self):
        for before in ('![Alt](one.svg)', '![Alt][image]\n[image]: one.svg',
                       "<img src='one.svg' alt='Meaningful'>", "<source srcset='one.svg 1x'>",
                       '[![Alt](one.svg)](README.md)', '[![Alt][image]](README.md)\n[image]: one.svg'):
            with self.subTest(before=before):
                self.assertTrue(self.select(before, before.replace('one.svg', 'two.svg'))[0])
                self.assertFalse(self.select(before + '\nOld', before + '\nNew')[0])

    def test_full_and_unsafe_selection_fail_closed(self):
        self.assertEqual(documents.render_selection({'scope': 'full'}, {'images': set()}), (True, 'full_scope'))
        with self.assertRaises(ValueError):
            documents.render_selection({'scope': 'docs', 'changed_paths': []}, {'images': set()})
        old = '```mermaid\n%%{init:{}}%%\n' + DIAGRAM + '\n```'
        self.assertEqual(self.select(old, 'Fixed prose'), (True, 'unverified_previous_diagram_content'))


class ExactTreeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.top = Path(self.temp.name)
        self.root = self.top / 'repository'
        self.root.mkdir()
        self.root_patch = patch.object(documents, 'ROOT', self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.git('init', '-q')
        self.git('config', 'user.name', 'Synthetic CI Fixture')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.git('config', 'core.autocrlf', 'false')
        for name in ('README.md', 'README.en.md', 'SECURITY.md'):
            (self.root / name).write_text('# Example\n', encoding='utf-8')
        (self.root / '.gitignore').write_text('__pycache__/\n*.pyc\n', encoding='utf-8')
        scripts = self.root / '.github/scripts'
        scripts.mkdir(parents=True)
        for name in ('ci-coverage.py', 'ci-impact.py'):
            shutil.copyfile(SOURCE_ROOT / '.github/scripts' / name, scripts / name)
        self.git('add', '.')
        self.git('commit', '-qm', 'Synthetic baseline')
        self.base = self.git('rev-parse', 'HEAD').strip()
        (self.root / 'README.md').write_text('# Example\nMore prose.\n', encoding='utf-8')
        self.git('add', '.')
        self.git('commit', '-qm', 'Synthetic prose edit')
        self.head = self.git('rev-parse', 'HEAD').strip()
        self.event = self.top / 'event.json'
        self.event.write_text(json.dumps({'repository': {'full_name': 'webkaz-labs/sobalink'},
            'ref': 'refs/heads/main', 'before': self.base, 'after': self.head}), encoding='utf-8')
        self.environment = {'GITHUB_SHA': self.head, 'GITHUB_EVENT_NAME': 'push',
                            'GITHUB_REPOSITORY': 'webkaz-labs/sobalink', 'GITHUB_REF': 'refs/heads/main',
                            'GITHUB_EVENT_PATH': str(self.event), 'FORCE_FULL': 'false'}
        self.env_patch = patch.dict(os.environ, self.environment)
        self.env_patch.start()
        self.addCleanup(self.env_patch.stop)
        self.plan_path = self.top / 'plan.json'
        self.write_plan()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, stderr=subprocess.DEVNULL).decode()

    def write_plan(self):
        data = subprocess.check_output([os.sys.executable, str(self.root / '.github/scripts/ci-impact.py'),
                                       '--head', self.head], cwd=self.root, env=os.environ)
        self.plan_path.write_bytes(data)
        return json.loads(data)

    def test_exact_plan_reproduces_before_and_after_external_renderer_install(self):
        before = documents.verified_plan(self.plan_path)
        self.assertEqual(before['scope'], 'docs')
        render_root = self.top / 'renderer/node_modules/package'
        render_root.mkdir(parents=True)
        (render_root / 'package.json').write_text('{}', encoding='utf-8')
        self.assertEqual(documents.verified_plan(self.plan_path), before)

    def test_tampered_plan_cannot_waive_full_rendering(self):
        plan = json.loads(self.plan_path.read_text())
        for key, value in (('changed_paths', []), ('base_sha', self.head), ('head_tree', 'a' * 40),
                           ('head_sha', 'b' * 40), ('policy_sha256', 'c' * 64)):
            altered = dict(plan, **{key: value})
            self.plan_path.write_text(json.dumps(altered), encoding='utf-8')
            with self.subTest(key=key):
                verified = documents.verified_plan(self.plan_path)
                self.assertEqual(verified['scope'], 'full')
                self.assertTrue(documents.render_selection(verified, {'images': set()})[0])
        self.plan_path.write_text('not-json', encoding='utf-8')
        self.assertEqual(documents.verified_plan(self.plan_path)['scope'], 'full')

    def test_dirty_tracked_source_and_hidden_index_flags_never_get_receipts(self):
        (self.root / 'README.md').write_text('Dirty', encoding='utf-8')
        with self.assertRaises(ValueError):
            documents.verified_plan(self.plan_path)
        self.git('checkout', '--', 'README.md')
        self.git('update-index', '--assume-unchanged', 'README.md')
        with self.assertRaises(ValueError):
            documents.verified_plan(self.plan_path)

    def test_schedule_and_unknown_event_full_plans_need_no_base(self):
        for event in ('schedule', 'workflow_dispatch'):
            with patch.dict(os.environ, {'GITHUB_EVENT_NAME': event}), self.subTest(event=event):
                plan = self.write_plan()
                self.assertEqual(plan['scope'], 'full')
                self.assertIsNone(plan['base_sha'])
                verified = documents.verified_plan(self.plan_path)
                self.assertEqual(verified['head_sha'], self.head)
                self.assertTrue(documents.render_selection(verified, {'images': set()})[0])

    def test_non_regular_tracked_markdown_is_rejected(self):
        link = self.root / 'linked.md'
        try:
            link.symlink_to(self.root / 'README.md')
        except (OSError, NotImplementedError):
            self.skipTest('Symlink creation unavailable on this host')
        self.git('add', 'linked.md')
        self.git('commit', '-qm', 'Synthetic symlink')
        with self.assertRaises(ValueError):
            documents.tracked_files()


class RenderContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.top = Path(self.temp.name)
        self.root = self.top / 'repository'
        self.assets = self.root / documents.VISUALS
        self.assets.mkdir(parents=True)
        self.output = self.top / 'output'
        self.output.mkdir()
        self.files = {documents.VISUALS + '/' + name for name in documents.HELPERS}
        for name in documents.HELPERS:
            (self.assets / name).write_text('# Synthetic helper', encoding='utf-8')
        (self.assets / 'untrusted.py').write_text('raise RuntimeError()', encoding='utf-8')
        self.static = {'diagrams': [], 'images': set(), 'svg_hashes': {
            documents.VISUALS + '/' + name: documents.digest(SVG.encode()) for name in documents.SVG_NAMES}}
        self.root_patch = patch.object(documents, 'ROOT', self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.decoder_cases = ['corrupt', 'jpeg-disguised-as-png', 'oversize', 'symlink']
        self.decoder_patch = patch.object(documents, 'png_decoder_negative_checks', return_value=self.decoder_cases)
        self.decoder = self.decoder_patch.start()
        self.addCleanup(self.decoder_patch.stop)

    def test_only_exact_helpers_execute_in_scratch_after_content_verification(self):
        called = []
        def execute(command, cwd, timeout):
            name = Path(command[-1]).name
            called.append(name)
            self.assertFalse(cwd.is_relative_to(self.root))
            self.assertFalse((cwd / 'untrusted.py').exists())
            self.assertEqual(command[1:3], ['-I', '-B'])
            if name == 'build.py':
                for svg in documents.SVG_NAMES:
                    # The reproduction contract compares bytes, independent of host newlines.
                    (cwd / svg).write_bytes(SVG.encode('utf-8'))
            if name == 'render.py':
                for svg in documents.SVG_NAMES:
                    (cwd / Path(svg).with_suffix('.png')).write_bytes(b'synthetic-png')
        with patch.object(documents, 'tracked_files', return_value=self.files), \
                patch.object(documents, 'renderer_root', return_value=self.top / 'renderer'), \
                patch.object(documents, 'run_quiet', side_effect=execute), \
                patch.object(documents, 'check_png'):
            report = {}
            documents.render(self.static, self.output, report)
        self.decoder.assert_called_once()
        self.assertEqual(report['png_decoder_negative_checks'], self.decoder_cases)
        self.assertEqual(tuple(called), documents.HELPERS)
        self.assertEqual(len(list(self.output.glob('*.png'))), 88)

    def test_reproduction_fixture_is_stable_with_windows_text_newlines(self):
        original = Path.write_text
        def windows_newlines(path, data, *args, **kwargs):
            kwargs.setdefault('newline', '\r\n')
            return original(path, data, *args, **kwargs)
        with patch.object(Path, 'write_text', windows_newlines):
            self.test_only_exact_helpers_execute_in_scratch_after_content_verification()

    def test_unsafe_or_changed_generated_svg_never_reaches_renderer(self):
        for bad in (SVG.replace('</svg>', '<script/></svg>'), SVG.replace('Example', 'Changed')):
            called = []
            def execute(command, cwd, timeout):
                name = Path(command[-1]).name
                called.append(name)
                if name == 'build.py':
                    for svg in documents.SVG_NAMES:
                        (cwd / svg).write_text(bad, encoding='utf-8')
            with self.subTest(bad=bad), \
                    patch.object(documents, 'tracked_files', return_value=self.files), \
                    patch.object(documents, 'renderer_root', return_value=self.top / 'renderer'), \
                    patch.object(documents, 'run_quiet', side_effect=execute), self.assertRaises(ValueError):
                documents.render(self.static, self.output, {})
            self.assertEqual(called, list(documents.GENERATORS))

    def test_untracked_helper_cannot_be_executed(self):
        self.files.remove(documents.VISUALS + '/build.py')
        with patch.object(documents, 'tracked_files', return_value=self.files), \
                patch.object(documents, 'run_quiet') as execute, self.assertRaises(ValueError):
            documents.render(self.static, self.output, {})
        execute.assert_not_called()


class CommandTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.top = Path(self.temp.name)
        self.plan = {'scope': 'docs', 'head_sha': 'a' * 40, 'head_tree': 'b' * 40,
                     'policy_sha256': 'c' * 64, 'base_sha': 'd' * 40}
        self.static = {'markdown_files': 1, 'local_links': 0, 'mermaid_blocks': 0,
                       'svg_variants': 88, 'diagrams': [], 'images': set(), 'svg_hashes': {}}
        self.report = {'scope': 'docs', 'head_sha': 'a' * 40, 'head_tree': 'b' * 40,
                       'render_required': False, 'rendered': False}

    def test_select_and_check_recompute_instead_of_trusting_selection_artifact(self):
        selection = self.top / 'selection.json'
        selection.write_text('{"render_required":false}', encoding='utf-8')
        required = dict(self.report, render_required=True)
        with patch.object(documents, 'inspect', return_value=(self.plan, self.static, required)) as inspect, \
                patch.object(documents, 'verified_plan', return_value=self.plan), \
                patch.object(documents, 'render') as render:
            result = documents.main(['check', '--plan', str(selection), '--output', str(self.top / 'output')])
        self.assertEqual(result, 0)
        inspect.assert_called_once_with(selection)
        render.assert_called_once()
        report = json.loads((self.top / 'output/report.json').read_text())
        self.assertTrue(report['rendered'])
        self.assertEqual(report['head_tree'], self.plan['head_tree'])

    def test_static_only_check_does_not_need_node_or_pillow(self):
        with patch.object(documents, 'inspect', return_value=(self.plan, self.static, self.report)), \
                patch.object(documents, 'verified_plan', return_value=self.plan), \
                patch.object(documents, 'render', side_effect=AssertionError('Unexpected render')):
            self.assertEqual(documents.main(['check', '--plan', str(self.top / 'plan.json'),
                                            '--output', str(self.top / 'output')]), 0)
        report = json.loads((self.top / 'output/report.json').read_text())
        self.assertEqual(report['result'], 'passed')
        self.assertFalse(report['rendered'])

    def test_mutation_after_render_fails_receipt(self):
        with patch.object(documents, 'inspect', return_value=(self.plan, self.static, self.report)), \
                patch.object(documents, 'verified_plan', return_value=dict(self.plan, scope='full')):
            self.assertEqual(documents.main(['check', '--plan', str(self.top / 'plan.json'),
                                            '--output', str(self.top / 'output')]), 1)
        self.assertEqual(json.loads((self.top / 'output/report.json').read_text())['result'], 'failed')

    def test_renderer_failure_does_not_publish_process_output_or_mark_passed(self):
        with patch.object(documents, 'inspect', return_value=(self.plan, self.static, dict(self.report, render_required=True))), \
                patch.object(documents, 'render', side_effect=subprocess.CalledProcessError(1, ['private-command'], output='private-data')):
            self.assertEqual(documents.main(['check', '--plan', str(self.top / 'plan.json'),
                                            '--output', str(self.top / 'output')]), 1)
        text = (self.top / 'output/report.json').read_text()
        self.assertNotIn('private', text)
        self.assertFalse(json.loads(text)['rendered'])
        self.assertEqual(json.loads(text)['result'], 'failed')

    def test_selection_outputs_boolean_only_after_success(self):
        output = self.top / 'github-output'
        with patch.dict(os.environ, {'GITHUB_OUTPUT': str(output)}), \
                patch.object(documents, 'inspect', return_value=(self.plan, self.static, self.report)), \
                patch.object(documents, 'verified_plan', return_value=self.plan):
            self.assertEqual(documents.main(['select', '--plan', str(self.top / 'plan.json'),
                                            '--output', str(self.top / 'selection.json')]), 0)
        self.assertEqual(output.read_text(), 'render_required=false\n')
        self.assertEqual(json.loads((self.top / 'selection.json').read_text())['result'], 'selected')

    def test_only_exact_helpers_execute_and_browser_blocks_external_resources(self):
        self.assertEqual(documents.HELPERS, ('build.py', 'explainers.py', 'semantic.py', 'render.py', 'verify.py'))
        driver = documents.MERMAID_DRIVER
        self.assertIn('setOfflineMode(true)', driver)
        self.assertIn("request.abort('blockedbyclient')", driver)
        self.assertIn("!relative.startsWith('..' + path.sep)", driver)
        self.assertIn("securityLevel:'strict'", driver)
        self.assertIn("event === 'request' ? page", driver)
        self.assertNotIn('http://example', driver)


if __name__ == '__main__':
    unittest.main()
