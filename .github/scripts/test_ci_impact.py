"""Minimum CI rules and authenticated event ranges using local Git fixtures."""

import contextlib
import copy
import importlib.util
import io
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).with_name("ci-impact.py")
SPEC = importlib.util.spec_from_file_location("ci_impact", SCRIPT)
impact = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(impact)


def blob(value="a", mode="100644", kind="blob"):
    return mode, kind, value * 40


def classify_paths(*paths):
    before = {path: blob() for path in impact.ROOT_DOCS | set(paths)}
    after = dict(before)
    after.update({path: blob("b") for path in paths})
    changes = [{"status": "M", "paths": [path]} for path in paths]
    return impact.classify_delta(changes, before, after)


class ImpactRulesTests(unittest.TestCase):
    def test_shared_runtime_configuration_policy_and_unknown_paths_are_full(self):
        paths = [
            "internal/core/core.go", "internal/directlan/lifecycle_test.go",
            "internal/engineadaptation/manifest.json", "internal/control/README.md",
            "cmd/soba/main.go", "web/embed.go", "extra.go", "docs/example.go",
            "go.mod", "go.sum", "mise.toml", "go.work", ".github/workflows/ci.yml",
            ".github/scripts/helper.py", "tools/helper.py", "fixtures/data.json",
            "package-lock.json", "web/package.json", "web/package-lock.json",
            "web/vite.config.ts", "web/tsconfig.json", "web/playwright.config.mjs",
            "web/public/logo.svg", "web/src/data.json", "web/dist/embedded.go",
            "AGENTS.md", "docs/AGENTS.md", "docs/SKILL.md", "web/src/AGENTS.md",
            "docs/DEVELOPMENT_PRINCIPLES.en.md", "docs/DEVELOPMENT_PRINCIPLES.ja.md",
            "docs/nested/guide.md", "LICENSE", "README.fr.md", "unknown",
            "internal/servicepresets/catalog_linux.go",
            "internal/servicepresets/catalog_arm64_test.go",
            "internal/servicepresets/catalog_windows_amd64.go",
            "internal/servicepresets/testdata/preset.go", "internal/boundedlog/config.json",
        ]
        for path in paths:
            with self.subTest(path=path):
                self.assertEqual(classify_paths(path)[0], "full")
                self.assertEqual(classify_paths("README.md", path)[0], "full")

    def test_native_short_is_a_closed_presentation_allowlist(self):
        for path in impact.NATIVE_SHORT_IMPORTS:
            self.assertEqual(classify_paths(path, "README.md"),
                             ("native-short", "reviewed_presentation_pr", []))
            for extra in ("cmd/soba/lifecycle.go", "internal/directlan/session.go", "internal/lanlink/lease.go",
                          "internal/routecat/relay.go", "internal/control/server.go", "internal/deadline/time.go",
                          "go.mod", ".github/workflows/ci.yml", "web/src/App.tsx", "internal/boundedlog/writer.go"):
                with self.subTest(path=path, extra=extra):
                    self.assertEqual(classify_paths(path, extra)[0], "full")
        for path in ("cmd/soba/help_windows.go", "cmd/soba/help/new.go", "cmd/soba/new_output.go"):
            self.assertEqual(classify_paths(path)[0], "full")
        root = pathlib.Path(__file__).resolve().parents[2]
        for path in impact.NATIVE_SHORT_IMPORTS:
            impact.validate_go_imports(path, (root / path).read_text(encoding="utf-8"))
            with self.assertRaises(impact.FailClosed):
                impact.validate_go_imports(path, 'package main; import "net/http"')

    def test_native_short_presentation_reads_are_locale_independent(self):
        original = pathlib.Path.read_text
        def explicit_utf8(path, *args, **kwargs):
            self.assertEqual(kwargs.get("encoding"), "utf-8")
            return original(path, *args, **kwargs)
        # Fail even on a UTF-8 host if the reviewed source read relies on the
        # platform default (for example Windows cp1252 for bilingual Go text).
        with mock.patch.object(pathlib.Path, "read_text", autospec=True, side_effect=explicit_utf8):
            self.test_native_short_is_a_closed_presentation_allowlist()

    def test_explicit_top_level_documentation(self):
        self.assertEqual(classify_paths("README.md", "README.en.md", "SECURITY.md",
                                        "docs/guide.md"),
                         ("docs", "documentation_only", []))
        self.assertEqual(classify_paths(), ("docs", "unchanged_tree", []))

    def test_frontend_source_tests_and_reviewed_generated_assets(self):
        for paths in [
            ("web/browser/app.spec.mjs", "README.md"),
            ("web/src/App.tsx",), ("web/src/api.ts", "web/src/api.test.ts"),
            ("web/src/components/App.test.tsx", "README.md"),
            ("web/src/styles.css", "web/dist/index.html"),
            ("web/src/App.tsx", "web/dist/assets/index-abc.js", "web/dist/assets/index-def.css"),
        ]:
            with self.subTest(paths=paths):
                self.assertEqual(classify_paths(*paths), ("frontend", "frontend_only", []))
        for paths in [("web/dist/index.html",), ("README.md", "web/dist/assets/index-abc.js"),
                      ("web/browser/app.spec.mjs", "web/dist/index.html")]:
            with self.subTest(paths=paths):
                self.assertEqual(classify_paths(*paths)[0], "full")

    def test_scoped_go_packages_and_mixed_changes(self):
        self.assertEqual(classify_paths("internal/servicepresets/catalog.go", "README.md"),
                         ("go", "scoped_go_packages", ["./internal/servicepresets"]))
        self.assertEqual(classify_paths("internal/boundedlog/writer_test.go",
                                        "internal/servicepresets/catalog.go")[2],
                         ["./internal/boundedlog", "./internal/servicepresets"])
        for extra in ["web/src/App.tsx", "web/browser/app.spec.mjs", "internal/core/core.go", "go.mod", "web/embed.go"]:
            with self.subTest(extra=extra):
                self.assertEqual(classify_paths("internal/boundedlog/writer.go", extra)[0], "full")

    def test_rename_validates_both_endpoints(self):
        roots = {path: blob() for path in impact.ROOT_DOCS}
        for old, new, expected in [
            ("internal/core/logic.go", "docs/guide.md", "full"),
            ("internal/core/logic.go", "cmd/soba/help.go", "full"),
            ("cmd/soba/help.go", "cmd/soba/new_help.go", "full"),
            ("cmd/soba/help.go", "cmd/soba/errors.go", "native-short"),
            ("docs/guide.md", "internal/core/readme.md", "full"),
            ("docs/old.md", "docs/new.md", "docs"),
            ("web/src/old.ts", "web/src/new.ts", "frontend"),
            ("internal/servicepresets/a.go", "internal/boundedlog/a.go", "go"),
        ]:
            with self.subTest(old=old, new=new):
                changes = [{"status": "R100", "paths": [old, new]}]
                result = impact.classify_delta(changes, roots | {old: blob()}, roots | {new: blob()})
                self.assertEqual(result[0], expected)

    def test_mode_type_status_and_missing_root_docs_fail_closed(self):
        roots = {path: blob() for path in impact.ROOT_DOCS}
        for entry in [blob("b", "100755"), blob("b", "120000"), blob("b", "160000", "commit")]:
            with self.subTest(entry=entry), self.assertRaises(impact.FailClosed):
                impact.classify_delta([{"status": "M", "paths": ["README.md"]}],
                                      roots, roots | {"README.md": entry})
        with self.assertRaises(impact.FailClosed):
            impact.classify_delta([{"status": "A", "paths": ["README.md"]}],
                                  roots, roots | {"README.md": blob("b")})
        with self.assertRaisesRegex(impact.FailClosed, "required_document"):
            impact.classify_delta([{"status": "D", "paths": ["README.md"]}],
                                  roots, {p: b for p, b in roots.items() if p != "README.md"})

    def test_complete_record_boundary_truncation_is_not_a_safe_delta(self):
        before = {"README.md": blob(), "internal/core/core.go": blob()}
        after = {path: blob("b") for path in before}
        with self.assertRaisesRegex(impact.FailClosed, "incomplete_tree_delta"):
            impact.validate_delta(impact.parse_name_status(b"M\0README.md\0"), before, after)


class GitOutputParserTests(unittest.TestCase):
    def test_name_status_is_nul_delimited_and_retains_spaces(self):
        self.assertEqual(impact.parse_name_status(b"A\0docs/new guide.md\0D\0docs/old.md\0"),
                         [{"status": "A", "paths": ["docs/new guide.md"]},
                          {"status": "D", "paths": ["docs/old.md"]}])
        self.assertEqual(impact.parse_name_status(b""), [])

    def test_malformed_truncated_duplicate_or_unsupported_statuses(self):
        values = [b"M\0README.md", b"M\0", b"M\0README.md\0R100\0docs/old.md\0",
                  b"M\0README.md\0\0", b"M\tREADME.md\0", b"\0", b"M\0\0",
                  b"C100\0README.md\0docs/new.md\0", b"R101\0README.md\0docs/new.md\0",
                  b"R\0README.md\0docs/new.md\0", b"T\0docs/guide.md\0",
                  b"U\0README.md\0", b"X\0README.md\0", b"B\0README.md\0",
                  b"M\0README.md\0M\0README.md\0", b"R100\0README.md\0README.md\0",
                  b"\xff\0README.md\0"]
        for raw in values:
            with self.subTest(raw=raw), self.assertRaises(impact.FailClosed):
                impact.parse_name_status(raw)

    def test_invalid_paths_cannot_enter_the_safe_allowlist(self):
        paths = [b"/docs/a.md", b"docs/../a.md", b"docs/./a.md", b"docs//a.md",
                 b"docs\\a.md", b"docs/a\n.md", b"docs/a\t.md", b"docs/a\x7f.md",
                 b"docs/\xff.md", b"docs/"]
        for path in paths:
            with self.subTest(path=path), self.assertRaises(impact.FailClosed):
                impact.parse_name_status(b"M\0" + path + b"\0")

    def test_tree_entries_retain_modes_and_object_ids(self):
        raw = b"100644 blob " + b"a" * 40 + b"\tdocs/new guide.md\0"
        self.assertEqual(impact.parse_tree(raw), {"docs/new guide.md": blob()})
        self.assertEqual(impact.parse_tree(b""), {})
        for bad in [raw[:-1], raw + raw, raw.replace(b"100644", b"10064"),
                    raw.replace(b" blob ", b" tree "), raw.replace(b"a" * 40, b"a" * 39),
                    raw.replace(b"\t", b" "), raw.replace(b"100644", b"\xff00644")]:
            with self.subTest(raw=bad), self.assertRaises(impact.FailClosed):
                impact.parse_tree(bad)


@unittest.skipUnless(shutil.which("git"), "Git is required for tree integration tests")
class GitHistoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.event_path = pathlib.Path(self.temp.name) / "event.json"
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "CI Fixture")
        self.git("config", "user.email", "ci@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.autocrlf", "false")
        self.write(".gitignore", "/.sobalink-deps/\n/bin/\n/dist/\n/.build/\n__pycache__/\n*.log\n")
        self.write("web/.gitignore", "node_modules/\n.vite/\ncoverage/\n")
        self.write(".github/scripts/fixture.py", "# synthetic tracked script\n")
        for path in impact.ROOT_DOCS:
            self.write(path, "fixture documentation\n")
        self.write("internal/core/core.go", "package core\n")
        self.write("internal/servicepresets/catalog.go", "package servicepresets\n")
        self.write("internal/boundedlog/writer.go", 'package boundedlog\nimport "os"\nvar _ *os.File\n')
        self.write("web/src/App.tsx", "export const fixture = 1\n")
        self.write("cmd/soba/errors.go", "package main\n")
        self.base = self.commit()

    def git(self, *args):
        env = os.environ.copy()
        env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_TERMINAL_PROMPT": "0", "LC_ALL": "C"})
        return subprocess.check_output(["git", *args], cwd=self.root, env=env,
                                       stderr=subprocess.DEVNULL).decode().strip()

    def write(self, path, text):
        dest = self.root / path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(text, encoding="utf-8")

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "Synthetic fixture change")
        return self.git("rev-parse", "HEAD")

    def push_event(self, head=None, base=None):
        return {"repository": {"full_name": impact.REPOSITORY}, "ref": "refs/heads/main",
                "before": base or self.base, "after": head or self.git("rev-parse", "HEAD")}

    def environment(self, event, name="push"):
        self.event_path.write_text(json.dumps(event), encoding="utf-8")
        return {"GITHUB_EVENT_NAME": name, "GITHUB_EVENT_PATH": str(self.event_path),
                "GITHUB_SHA": self.git("rev-parse", "HEAD"), "GITHUB_REPOSITORY": impact.REPOSITORY,
                "GITHUB_REF": event["ref"] if name == "push" else "refs/pull/42/merge"}

    def classify(self, head=None, event=None, name="push", env_changes=None, **options):
        environment = self.environment(self.push_event() if event is None else event, name)
        environment.update(env_changes or {})
        return impact.classify(self.root, head=head or self.git("rev-parse", "HEAD"),
                               environment=environment, **options)

    def docs_change(self):
        self.write("README.md", "fixture documentation changed\n")
        return self.commit()

    def pull_request_event(self, change=None):
        self.git("checkout", "-qb", "fixture-topic")
        if change is None:
            self.docs_change()
        else:
            change()
            self.commit()
        source = self.git("rev-parse", "HEAD")
        self.git("checkout", "main")
        self.git("merge", "--no-ff", "-m", "Synthetic tested merge", source)
        head = self.git("rev-parse", "HEAD")
        return {"repository": {"full_name": impact.REPOSITORY}, "number": 42,
                "pull_request": {"merge_commit_sha": head,
                    "base": {"sha": self.base, "ref": "main", "repo": {"full_name": impact.REPOSITORY}},
                    "head": {"sha": source, "repo": {"full_name": "fixture-fork/sobalink"}}}}

    def test_native_short_pr_and_identical_main_delta_stays_full(self):
        event = self.pull_request_event(lambda: self.write("cmd/soba/errors.go", 'package main\nimport "errors"\nvar _ = errors.New\n'))
        report = self.classify(event=event, name="pull_request")
        self.assertEqual(report["scope"], "native-short")
        self.assertEqual(report["changed_paths"], ["cmd/soba/errors.go"])
        self.assertEqual(self.classify()["reason"], "native_short_requires_pr")
        self.assertEqual(self.classify()["scope"], "full")
        self.assertEqual(self.classify(event=event, name="pull_request", force_full=True)["scope"], "full")

    def test_native_short_new_import_falls_back_to_full(self):
        event = self.pull_request_event(lambda: self.write("cmd/soba/errors.go", 'package main\nimport "net/http"\n'))
        self.assertEqual(self.classify(event=event, name="pull_request")["reason"], "unreviewed_go_dependency")
        self.assertEqual(self.classify(event=event, name="pull_request")["scope"], "full")

    def test_native_short_build_directive_falls_back_to_full(self):
        event = self.pull_request_event(lambda: self.write("cmd/soba/errors.go", '//go:build linux\n\npackage main\n'))
        self.assertEqual(self.classify(event=event, name="pull_request")["reason"], "platform_or_special_go_source")
        self.assertEqual(self.classify(event=event, name="pull_request")["scope"], "full")

    def test_native_short_mixed_lifecycle_pr_remains_full(self):
        def change():
            self.write("cmd/soba/errors.go", "package main\n// presentation\n")
            self.write("internal/core/core.go", "package core\n// lifecycle change\n")
        event = self.pull_request_event(change)
        self.assertEqual(self.classify(event=event, name="pull_request")["scope"], "full")

    def test_docs_without_prior_baseline_emit_versioned_evidence(self):
        head = self.docs_change()
        report = self.classify()
        self.assertEqual(report["scope"], "docs")
        self.assertEqual(report["reason"], "documentation_only")
        self.assertEqual(report["version"], 3)
        self.assertEqual(report["policy_id"], "minimum-ci-v3")
        self.assertEqual(report["head_sha"], head)
        self.assertEqual(report["base_sha"], self.base)
        self.assertEqual(report["head_tree"], self.git("rev-parse", "HEAD^{tree}"))
        self.assertEqual(report["changed_paths"], ["README.md"])
        self.assertEqual(report["go_packages"], [])
        self.assertEqual(report["policy_sha256"], impact.digest(SCRIPT.read_bytes()))
        self.assertNotIn("baseline_run_id", report)

    def test_push_range_keeps_hidden_code_change_before_latest_doc_commit(self):
        self.write("internal/core/core.go", "package core\n// changed\n")
        self.commit()
        self.docs_change()
        report = self.classify()
        self.assertEqual(report["scope"], "full")
        self.assertEqual(report["changed_paths"], ["README.md", "internal/core/core.go"])

    def test_push_range_uses_event_before_not_last_commit(self):
        self.write("docs/first.md", "first documentation change\n")
        self.commit()
        self.docs_change()
        report = self.classify()
        self.assertEqual(report["scope"], "docs")
        self.assertEqual(report["changed_paths"], ["README.md", "docs/first.md"])
        self.assertEqual(report["base_sha"], self.base)

    def test_pr_merge_authenticates_both_parents_and_fork_repository(self):
        event = self.pull_request_event()
        report = self.classify(event=event, name="pull_request")
        self.assertEqual(report["scope"], "docs")
        self.assertEqual(report["base_sha"], self.base)
        self.assertEqual(report["changed_paths"], ["README.md"])
        for field, value in [("head", "f" * 40), ("base", "e" * 40)]:
            bad = copy.deepcopy(event)
            bad["pull_request"][field]["sha"] = value
            with self.subTest(field=field):
                self.assertEqual(self.classify(event=bad, name="pull_request")["reason"], "unverified_pr_merge")

    def test_pr_head_checkout_instead_of_tested_merge_is_full(self):
        event = self.pull_request_event()
        self.git("checkout", "--detach", event["pull_request"]["head"]["sha"])
        event["pull_request"]["merge_commit_sha"] = None
        self.assertEqual(self.classify(event=event, name="pull_request")["reason"], "unverified_pr_merge")

    def test_pr_branch_repository_merge_sha_and_ref_mismatch_are_full(self):
        event = self.pull_request_event()
        for path, value in [(('base', 'ref'), 'other'), (('base', 'repo', 'full_name'), 'fixture/other'),
                            (('head', 'repo', 'full_name'), 'malformed'), (('merge_commit_sha',), 'f' * 40)]:
            bad = copy.deepcopy(event)
            target = bad['pull_request']
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = value
            with self.subTest(path=path):
                self.assertEqual(self.classify(event=bad, name='pull_request')['scope'], 'full')
        self.assertEqual(self.classify(event=event, name='pull_request',
                                      env_changes={'GITHUB_REF': 'refs/pull/43/merge'})['scope'], 'full')

    def test_main_push_event_mismatches_and_unsupported_events_are_full(self):
        self.docs_change()
        event = self.push_event()
        for field, value in [('ref', 'refs/heads/feature'), ('after', 'f' * 40),
                             ('before', '0' * 40), ('before', 'HEAD'), ('before', 'f' * 40),
                             ('forced', True), ('created', True), ('deleted', True)]:
            with self.subTest(field=field, value=value):
                self.assertEqual(self.classify(event=event | {field: value})['scope'], 'full')
        for field, value in [('GITHUB_SHA', 'f' * 40), ('GITHUB_REPOSITORY', 'fixture/other'),
                             ('GITHUB_REF', 'refs/heads/other'), ('GITHUB_EVENT_NAME', 'workflow_dispatch'),
                             ('GITHUB_EVENT_NAME', 'schedule'), ('GITHUB_EVENT_NAME', 'pull_request_target')]:
            with self.subTest(field=field, value=value):
                self.assertEqual(self.classify(env_changes={field: value})['scope'], 'full')
        for field in ('before', 'after', 'repository'):
            bad = copy.deepcopy(event)
            del bad[field]
            self.assertEqual(self.classify(event=bad)['scope'], 'full')

    def test_nonancestor_push_base_is_full(self):
        other = self.docs_change()
        self.git('reset', '--hard', self.base)
        self.write('docs/other.md', 'other history\n')
        self.commit()
        self.assertEqual(self.classify(event=self.push_event(base=other))['reason'], 'event_base_not_ancestor')

    def test_missing_malformed_or_truncated_event_is_full(self):
        self.docs_change()
        environment = self.environment(self.push_event())
        for raw in ('{', '[]', '{}', '{"before":"truncated"}'):
            self.event_path.write_text(raw, encoding='utf-8')
            self.assertEqual(impact.classify(self.root, head=self.git('rev-parse', 'HEAD'),
                                            environment=environment)['scope'], 'full')
        self.event_path.unlink()
        self.assertEqual(impact.classify(self.root, head=self.git('rev-parse', 'HEAD'),
                                        environment=environment)['reason'], 'invalid_event')
        self.assertEqual(impact.classify(self.root, head=self.git('rev-parse', 'HEAD'),
                                        environment={})['scope'], 'full')

    def test_force_full_and_identical_tree(self):
        self.assertEqual(self.classify()['scope'], 'docs')
        report = self.classify(force_full=True)
        self.assertEqual(report['scope'], 'full')
        self.assertEqual(report['reason'], 'forced_full')
        self.assertEqual(report['head_sha'], self.base)

    def test_docs_addition_deletion_and_rename_preserve_required_docs(self):
        self.write('docs/old.md', 'ordinary prose\n')
        added = self.commit()
        self.assertEqual(self.classify()['scope'], 'docs')
        self.git('mv', 'docs/old.md', 'docs/new.md')
        self.commit()
        self.assertEqual(self.classify(event=self.push_event(base=added))['scope'], 'docs')
        (self.root / 'docs/new.md').unlink()
        self.commit()
        self.assertEqual(self.classify(event=self.push_event(base=added))['scope'], 'docs')
        (self.root / 'README.md').unlink()
        self.commit()
        self.assertEqual(self.classify()['reason'], 'required_document_missing_or_non_regular')

    def test_actual_rename_from_runtime_into_docs_is_full(self):
        (self.root / 'docs').mkdir()
        self.git('mv', 'internal/core/core.go', 'docs/guide.md')
        self.commit()
        report = self.classify()
        self.assertEqual(report['scope'], 'full')
        self.assertEqual(report['changed_paths'], ['docs/guide.md', 'internal/core/core.go'])

    def test_nontext_prose_and_source_are_full_including_deleted_blobs(self):
        for path in ['docs/binary.md', 'web/src/binary.ts']:
            dest = self.root / path
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(b'bad\x00content')
            self.commit()
            self.assertEqual(self.classify()['reason'], 'non_text_content')
            self.base = self.git('rev-parse', 'HEAD')
            dest.unlink()
            self.commit()
            self.assertEqual(self.classify()['reason'], 'non_text_content')
            self.base = self.git('rev-parse', 'HEAD')

    def test_frontend_source_and_generated_output(self):
        self.write('web/src/App.tsx', 'export const fixture = 2\n')
        self.write('web/dist/index.html', '<html>fixture</html>\n')
        self.write('web/dist/assets/index-fixture.js', 'export const fixture = 2\n')
        changed = self.commit()
        self.assertEqual(self.classify()['scope'], 'frontend')
        self.write('web/dist/index.html', '<html>generated only</html>\n')
        self.commit()
        self.assertEqual(self.classify(event=self.push_event(base=changed))['reason'],
                         'generated_assets_without_source_change')

    def test_scoped_go_files_tests_and_two_packages(self):
        self.write('internal/servicepresets/catalog.go', 'package servicepresets\n// changed\n')
        self.write('internal/servicepresets/catalog_test.go', 'package servicepresets\n')
        self.docs_change()
        self.assertEqual(self.classify()['go_packages'], ['./internal/servicepresets'])
        self.write('internal/boundedlog/writer.go', 'package boundedlog\n// changed\n')
        self.commit()
        self.assertEqual(self.classify()['go_packages'], ['./internal/boundedlog', './internal/servicepresets'])
        self.write('internal/core/core.go', 'package core\n// changed\n')
        self.commit()
        self.assertEqual(self.classify()['scope'], 'full')

    def test_go_build_constraints_cgo_unsafe_and_directives_are_full(self):
        for source in ['//go:build windows\npackage servicepresets\n',
                       '// +build linux\npackage servicepresets\n',
                       'package servicepresets\nimport "C"\n',
                       'package servicepresets\nimport (`C`)\n',
                       'package servicepresets\nimport "unsafe"\n',
                       'package servicepresets\n//go:embed fixture\n']:
            with self.subTest(source=source):
                self.write('internal/servicepresets/catalog.go', source)
                self.commit()
                self.assertEqual(self.classify()['reason'], 'platform_or_special_go_source')

    def test_new_go_dependency_and_escaped_imports_require_full_scope(self):
        for source in ['package servicepresets\nimport "net"\n',
                       'package servicepresets\nimport (alias "example.invalid/shared")\n',
                       'package servicepresets\nimport "\\x43"\n']:
            with self.subTest(source=source):
                self.write('internal/servicepresets/catalog.go', source)
                self.commit()
                self.assertEqual(self.classify()['reason'], 'unreviewed_go_dependency')

    def test_go_import_parser_handles_comments_aliases_raw_strings_and_groups(self):
        source = ('package boundedlog\nimport /* comment */ (\n'
                  ' _ "bytes"; alias `fmt`\n // comment\n "os"\n)\n'
                  'var text = "import something"\n')
        self.write('internal/boundedlog/writer.go', source)
        self.commit()
        self.assertEqual(self.classify()['scope'], 'go')

    def test_browser_specs_and_fixtures_use_frontend_scope(self):
        self.write('web/browser/fixture.mjs', 'export const fixture = 1\n')
        changed = self.commit()
        self.assertEqual(self.classify()['scope'], 'frontend')
        self.write('web/browser/fixture.mjs', 'export const fixture = 2\n')
        self.write('web/dist/index.html', '<html>fixture</html>\n')
        self.commit()
        self.assertEqual(self.classify(event=self.push_event(base=changed))['reason'],
                         'generated_assets_without_source_change')

    def test_unchanged_unsupported_package_content_prevents_scoped_go(self):
        self.write('internal/boundedlog/writer_windows.go', 'package boundedlog\n')
        self.base = self.commit()
        self.write('internal/boundedlog/writer_test.go', 'package boundedlog\n')
        self.commit()
        self.assertEqual(self.classify()['reason'], 'unsupported_go_package_file')

    def test_checkout_mismatch_dirty_source_and_untracked_source_are_full(self):
        head = self.docs_change()
        self.assertEqual(self.classify(head=self.base)['reason'], 'head_checkout_mismatch')
        self.write('internal/core/core.go', 'package core\n// dirty\n')
        self.assertEqual(self.classify(head=head)['reason'], 'dirty_or_unverifiable_checkout')
        self.git('add', 'internal/core/core.go')
        self.assertEqual(self.classify()['scope'], 'full')
        self.git('reset', '--hard', head)
        self.write('internal/core/untracked.go', 'package core\n')
        self.assertEqual(self.classify()['reason'], 'untracked_checkout_paths')

    def test_machine_local_ignore_cannot_hide_source(self):
        self.docs_change()
        self.write('.git/info/exclude', '*.go\n')
        self.write('internal/core/hidden.go', 'package core\n')
        self.assertEqual(self.classify()['reason'], 'unrecognized_ignored_checkout_paths')

    def test_known_ignored_outputs_are_permitted(self):
        self.docs_change()
        for path in ['.build/result', '.sobalink-deps/fixture/source.go', 'bin/tool',
                     'dist/archive', 'web/node_modules/fixture/index.js',
                     '.github/scripts/__pycache__/ci-impact.pyc']:
            self.write(path, 'synthetic build output\n')
        self.assertEqual(self.classify()['scope'], 'docs')

    def test_index_flags_cannot_hide_dirty_bytes(self):
        self.docs_change()
        path = 'internal/core/core.go'
        for flag, reset in [('--assume-unchanged', '--no-assume-unchanged'),
                            ('--skip-worktree', '--no-skip-worktree')]:
            self.git('update-index', flag, '--', path)
            self.assertEqual(self.classify()['reason'], 'unsafe_index_flags')
            self.write(path, 'package core\n// hidden\n')
            self.assertEqual(self.classify()['reason'], 'unsafe_index_flags')
            self.git('update-index', reset, '--', path)
            self.git('checkout', '--', path)

    def test_shallow_history_is_full(self):
        (self.root / '.git/shallow').write_text(self.base + '\n', encoding='ascii')
        self.assertEqual(self.classify()['reason'], 'incomplete_repository_history')

    def test_actual_mode_and_type_changes_are_full(self):
        self.git('update-index', '--chmod=+x', 'README.md')
        self.git('commit', '-qm', 'Synthetic mode change')
        self.git('checkout-index', '--force', '--', 'README.md')
        self.assertEqual(self.classify()['reason'], 'non_regular_or_changed_file_mode')
        self.git('reset', '--hard', self.base)
        oid = self.git('hash-object', '-w', 'README.md')
        self.git('update-index', '--cacheinfo', '120000', oid, 'README.md')
        self.git('commit', '-qm', 'Synthetic symlink')
        self.assertEqual(self.classify()['scope'], 'full')

    def test_missing_dishonest_and_truncated_diff_are_full(self):
        self.write('internal/core/core.go', 'package core\n// changed\n')
        self.docs_change()
        original = impact.git
        for raw in [b'', b'M\0README.md\0', b'A\0README.md\0M\0internal/core/core.go\0', b'M\0README.md']:
            def dishonest(root, *args):
                if args[0] == 'diff' and '--name-status' in args:
                    return raw
                return original(root, *args)
            with self.subTest(raw=raw), mock.patch.object(impact, 'git', side_effect=dishonest):
                self.assertEqual(self.classify()['scope'], 'full')
        with mock.patch.object(impact, 'git', side_effect=impact.FailClosed('git_query_failed')):
            self.assertEqual(self.classify()['scope'], 'full')
        with mock.patch.object(impact, 'parse_name_status', side_effect=ValueError('unexpected')):
            self.assertEqual(self.classify()['reason'], 'classification_failed')

    def test_truncated_tree_or_index_is_full(self):
        self.docs_change()
        original = impact.git
        for command in ('ls-tree', 'ls-files'):
            def truncated(root, *args):
                raw = original(root, *args)
                if args[0] == command and (command == 'ls-tree' or '--cached' in args):
                    return raw.split(b'\0')[0] + b'\0'
                return raw
            with self.subTest(command=command), mock.patch.object(impact, 'git', side_effect=truncated):
                self.assertEqual(self.classify()['reason'], 'incomplete_checkout_index')

    def test_cli_reads_github_event_and_writes_same_json(self):
        head = self.docs_change()
        environment = os.environ | self.environment(self.push_event())
        output = pathlib.Path(self.temp.name) / 'plan.json'
        result = subprocess.run([os.sys.executable, str(SCRIPT), '--repo', str(self.root),
                                 '--head', head, '--output', str(output)],
                                check=True, capture_output=True, text=True, env=environment)
        report = json.loads(result.stdout)
        self.assertEqual(report, json.loads(output.read_text(encoding='utf-8')))
        self.assertEqual(report['scope'], 'docs')


class CommandLineFailureTests(unittest.TestCase):
    def test_missing_conflicting_and_unknown_inputs_emit_full_json(self):
        for argv in [[], ["--head"], ["--not-an-option"], ["--hea", "a" * 40],
                     ["--head", "a" * 40, "--head", "b" * 40],
                     ["--head=" + "a" * 40, "--head=" + "b" * 40]]:
            with self.subTest(argv=argv), contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(impact.main(argv), 0)
                report = json.loads(output.getvalue())
                self.assertEqual(report["scope"], "full")
                self.assertNotEqual(report["reason"], "documentation_only")

    def test_policy_read_failure_is_full(self):
        with mock.patch.object(impact.pathlib.Path, "read_bytes", side_effect=OSError("private/path")):
            report = impact.classify(pathlib.Path.cwd(), head="a" * 40)
        self.assertEqual(report["scope"], "full")
        self.assertIsNone(report["policy_sha256"])
        self.assertEqual(report["reason"], "policy_unavailable")

    def test_report_write_failure_emits_full_and_nonzero(self):
        with mock.patch.object(impact.pathlib.Path, "write_text", side_effect=OSError("private/path")), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(impact.main(["--output", "report.json"]), 1)
        report = json.loads(output.getvalue())
        self.assertEqual(report["scope"], "full")
        self.assertEqual(report["reason"], "report_write_failed")
        self.assertNotIn("private/path", output.getvalue())

    def test_git_errors_timeouts_output_limits_and_arguments_are_safe(self):
        with mock.patch.object(impact.subprocess, "run", side_effect=OSError("private/path")):
            with self.assertRaisesRegex(impact.FailClosed, "^git_query_failed$"):
                impact.git(pathlib.Path.cwd(), "status")
        with mock.patch.object(impact.subprocess, "run", side_effect=subprocess.TimeoutExpired("git", 60)):
            with self.assertRaisesRegex(impact.FailClosed, "^git_query_failed$"):
                impact.git(pathlib.Path.cwd(), "status")
        result = subprocess.CompletedProcess([], 0, b"x" * 20)
        with mock.patch.object(impact, "MAX_GIT_OUTPUT", 10), \
                mock.patch.object(impact.subprocess, "run", return_value=result) as runner:
            with self.assertRaisesRegex(impact.FailClosed, "^git_output_limit$"):
                impact.git(pathlib.Path.cwd(), "diff", "--name-status")
            self.assertEqual(runner.call_args.args[0], ["git", "--no-pager", "diff", "--name-status"])
            self.assertNotIn("shell", runner.call_args.kwargs)
            self.assertEqual(runner.call_args.kwargs["env"]["GIT_NO_REPLACE_OBJECTS"], "1")


if __name__ == "__main__":
    unittest.main()
