"""Offline documentation images must survive packaging, including alternatives."""
import pathlib
import shutil
import tempfile
import unittest

from document_images import image_targets, verify_document_images


REPOSITORY = pathlib.Path(__file__).resolve().parents[2]


class DocumentImageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name) / "share"
        self.root.mkdir()

    def write(self, name, contents=b"<svg xmlns='http://www.w3.org/2000/svg'/>"):
        target = self.root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(contents.encode() if isinstance(contents, str) else contents)
        return target

    def check_document(self, text):
        self.write("README.md", text)
        return verify_document_images(self.root)

    def test_markdown_inline_and_reference_forms(self):
        self.write("docs/image.svg")
        self.write("docs/image (wide).svg")
        self.assertEqual(self.check_document('''
![inline](docs/image.svg "A title")
![angle](<docs/image (wide).svg>)
![full][EXAMPLE]
![collapsed][]
![shortcut]
[example]: docs/image.svg "Example"
[collapsed]: <docs/image.svg>
[shortcut]: docs/image.svg
'''), 5)

    def test_nested_and_escaped_alt_labels_check_missing_images(self):
        for label in ("status [offline]", "status [nested [offline]]", r"status \[offline\]",
                      r"status [offline\]]", r"status [offline\\]"):
            with self.subTest(label=label), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document(f'![{label}](missing.svg)')

    def test_nested_alt_labels_resolve_inline_and_full_references(self):
        self.write("figure.svg")
        self.assertEqual(self.check_document('![status [offline]](figure.svg)'), 1)
        self.assertEqual(self.check_document('![status [offline]][figure]\n[figure]: figure.svg'), 1)
        with self.assertRaisesRegex(ValueError, "missing.svg"):
            self.check_document('![status [offline]][figure]\n[figure]: missing.svg')

    def test_image_marker_escape_parity(self):
        for count in range(6):
            text = "\\" * count + "![status [offline]](missing.svg)"
            with self.subTest(backslashes=count):
                if count % 2:
                    self.assertEqual(self.check_document(text), 0)
                else:
                    with self.assertRaisesRegex(ValueError, "missing.svg"):
                        self.check_document(text)

    def test_balanced_and_escaped_destination_parentheses(self):
        for name in ("figure(nested(wide)).svg", "figure(wide).svg", "figure wide.svg"):
            self.write(name)
        for destination in ("figure(nested(wide)).svg", r"figure\(wide\).svg", "<figure wide.svg>"):
            with self.subTest(destination=destination):
                self.assertEqual(self.check_document(f'![status [offline]]({destination})'), 1)
        for destination in ("missing(nested(wide)).svg", r"missing\(wide\).svg", "<missing wide.svg>"):
            with self.subTest(destination=destination), self.assertRaisesRegex(ValueError, "missing"):
                self.check_document(f'![status [offline]]({destination})')

    def test_escaped_and_multiline_reference_labels(self):
        for text in (r"![a\]b]" + "\n\n" + r"[a\]b]: missing.svg",
                     r"![a\]b][]" + "\n\n" + r"[a\]b]: missing.svg",
                     r"![image][a\]b]" + "\n\n" + r"[a\]b]: missing.svg",
                     "![a b]\n\n[a\nb]: missing.svg"):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document(text)

    def test_inner_images_only_render_when_outer_is_not_image(self):
        self.write("present.svg")
        self.assertEqual(self.check_document('![outer ![inner](missing.svg)](present.svg)'), 1)
        for text in ('![outer ![inner](missing.svg)]',
                     '![outer ![inner][target]]\n\n[target]: missing.svg'):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document(text)

    def test_malformed_outer_destination_does_not_hide_inner_image(self):
        self.write("present.svg")
        for suffix in ("(present.svg", "(present.svg bad title)", "(<present.svg)",
                       "(present.svg 'unfinished)", "(<pre<sent.svg>)", "(\n\npresent.svg)",
                       "(present.svg\n\n)", "(present.svg 'title\n\ncontinued')"):
            with self.subTest(suffix=suffix), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document('![outer ![inner](missing.svg)]' + suffix)
        for suffix in ("(present.svg)", "(present.svg 'title')", '(present.svg "title")',
                       "(<present.svg> (title))"):
            with self.subTest(suffix=suffix):
                self.assertEqual(self.check_document('![outer ![inner](missing.svg)]' + suffix), 1)

    def test_angle_and_bare_less_than_destination_boundaries(self):
        self.assertEqual(image_targets("![alt](pre<sent.svg)"), ["pre<sent.svg"])
        self.assertEqual(image_targets("![outer ![inner](missing.svg)](<pre<sent.svg>)"), ["missing.svg"])

    def test_unrecognized_backslash_escape_cannot_retarget_image(self):
        self.write("presenta.svg")
        with self.assertRaises(ValueError):
            self.check_document(r"![alt](present\a.svg)")

    def test_html_picture_and_srcset_all_candidates(self):
        for name in ("fallback", "narrow", "wide", "dense"):
            self.write(name + ".svg")
        self.assertEqual(self.check_document('''<picture>
<source media="(prefers-color-scheme: dark)" srcset="narrow.svg 400w, wide.svg 800w">
<img src="fallback.svg" srcset="fallback.svg 1x,dense.svg 2x" />
</picture>'''), 5)

    def test_missing_inline_and_reference_targets(self):
        for text in ('![alt](missing.svg)', '![alt][image]\n[image]: missing.svg',
                     '![alt][]\n[alt]: missing.svg', '![alt]\n[alt]: missing.svg'):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document(text)

    def test_missing_html_targets_and_each_srcset_alternative(self):
        self.write("present.svg")
        for text in ('<img src="missing.svg">', '<source src="missing.svg">',
                     '<source srcset="present.svg 1x, missing.svg 2x">',
                     '<img src="present.svg" srcset="missing.svg 1x,present.svg 2x">'):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "missing.svg"):
                self.check_document(text)

    def test_external_urls_and_embedded_data_are_not_fetched(self):
        self.assertEqual(self.check_document('''
![remote](https://example.invalid/a.svg)
<img src="//example.invalid/a.svg">
<source srcset="https://example.invalid/a.svg 1x, https://example.invalid/b.svg 2x">
<img srcset="data:image/svg+xml;base64,PHN2Zy8+ 1x">
'''), 0)

    def test_paths_relative_to_each_document_and_url_suffixes(self):
        self.write("docs/assets/figure wide.svg")
        self.write("docs/nested/guide.md", '![figure](../assets/figure%20wide.svg?version=1#view)')
        self.assertEqual(verify_document_images(self.root), 1)

    def test_traversal_absolute_and_unsafe_paths(self):
        (self.root.parent / "outside.svg").write_text("outside")
        for path in ("../outside.svg", "%2e%2e/outside.svg", "/outside.svg", "file:///outside.svg",
                     "C:/outside.svg", "docs%5coutside.svg", "docs/%00.svg", "docs/a:b.svg"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.check_document(f'<img src="{path}">')

    def symlink(self, path, target, directory=False):
        try:
            path.symlink_to(target, target_is_directory=directory)
        except (OSError, NotImplementedError) as error:
            self.skipTest(f"symlinks unavailable: {error}")

    def test_image_symlink_rejected_even_inside_share(self):
        self.write("real.svg")
        self.symlink(self.root / "linked.svg", self.root / "real.svg")
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.check_document('<img src="linked.svg">')

    def test_directory_symlink_rejected(self):
        outside = self.root.parent / "outside"
        outside.mkdir()
        (outside / "figure.svg").write_text("<svg/>")
        self.symlink(self.root / "linked", outside, directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.check_document('<img src="linked/figure.svg">')

    def test_symlink_document_tree_and_root_rejected(self):
        outside = self.root.parent / "outside"
        outside.mkdir()
        self.symlink(self.root / "docs", outside, directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            verify_document_images(self.root)
        with self.assertRaisesRegex(ValueError, "real directory"):
            verify_document_images(self.root / "docs")

    def test_symlink_document_rejected(self):
        self.write("real.md", "Text")
        self.symlink(self.root / "README.md", self.root / "real.md")
        with self.assertRaisesRegex(ValueError, "symlink"):
            verify_document_images(self.root)

    def test_empty_or_directory_image_rejected(self):
        self.write("empty.svg", b"")
        (self.root / "directory.svg").mkdir()
        for name in ("empty.svg", "directory.svg"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.check_document(f'![alt]({name})')

    def test_examples_and_comments_do_not_load_images(self):
        self.assertEqual(self.check_document('''
<!-- <img src="absent.svg"> -->
```markdown
![example](absent.svg)
```
~~~html
<img src="absent.svg">
~~~
Use `![example](absent.svg)` as syntax.
'''), 0)

    def test_data_srcset_does_not_hide_local_alternative(self):
        for srcset in ("data:image/png;base64,AA== 1x, absent.svg 2x",
                       "data:image/png;base64,AA==, absent.svg"):
            with self.subTest(srcset=srcset), self.assertRaisesRegex(ValueError, "absent.svg"):
                self.check_document(f'<img srcset="{srcset}">')

    def test_cancelled_symlink_component_is_rejected(self):
        outside = self.root.parent / "outside"
        outside.mkdir()
        self.symlink(self.root / "linked", outside, directory=True)
        self.write("present.svg")
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.check_document('<img src="linked/../present.svg">')

    def test_web_documents_are_checked_without_node_modules(self):
        self.write("web/node_modules/example/README.md", "![example](absent.svg)")
        self.assertEqual(verify_document_images(self.root), 0)
        self.write("web/README.md", "![example](absent.svg)")
        with self.assertRaisesRegex(ValueError, "web/README.md"):
            verify_document_images(self.root)

    def test_packaged_png_alternative_must_exist(self):
        self.write("docs/assets/screenshots/example-wide.png", b"synthetic PNG fixture")
        narrow = self.write("docs/assets/screenshots/example-narrow.png", b"synthetic PNG fixture")
        self.write("docs/guide.md", '<picture><source srcset="assets/screenshots/example-narrow.png"><img src="assets/screenshots/example-wide.png"></picture>')
        self.assertEqual(verify_document_images(self.root), 2)
        narrow.unlink()
        with self.assertRaisesRegex(ValueError, "example-narrow.png"):
            verify_document_images(self.root)

    def test_html_files_are_checked(self):
        self.write("docs/guide.html", '<img src="absent.svg">')
        with self.assertRaisesRegex(ValueError, "guide.html"):
            verify_document_images(self.root)

    def test_current_source_documents_and_missing_packaged_svg(self):
        for source in REPOSITORY.glob("*.md"):
            shutil.copy2(source, self.root / source.name)
        shutil.copytree(REPOSITORY / "docs", self.root / "docs")
        self.assertGreater(verify_document_images(self.root), 0)
        target = self.root / "docs/assets/visuals/connection-en-dark-narrow.svg"
        self.assertTrue(target.is_file())
        target.unlink()
        with self.assertRaisesRegex(ValueError, "connection-en-dark-narrow.svg"):
            verify_document_images(self.root)


if __name__ == "__main__":
    unittest.main()
