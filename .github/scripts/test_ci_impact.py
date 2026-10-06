"""Conservative CI impact rules, malformed deltas, and real Git histories."""

import contextlib
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
    before = {path: blob("a") for path in paths}
    after = {path: blob("b") for path in paths}
    changes = [{"status": "M", "paths": [path]} for path in paths]
    return impact.classify_delta(changes, before, after)


class ImpactRulesTests(unittest.TestCase):
    def test_default_for_runtime_dependencies_tools_configuration_and_unknowns(self):
        paths = [
            "internal/core/core.go", "internal/directlan/lifecycle_test.go",
            "internal/engineadaptation/manifest.json", "internal/control/README.md",
            "cmd/soba/main.go", "cmd/helper/readme.md", "web/embed.go", "extra.go",
            "docs/example.go", "go.mod", "go.sum", "mise.toml", "go.work",
            ".github/workflows/ci.yml", ".github/scripts/helper.py",
            ".github/fixtures/test.go", ".github/impact-policy.json",
            "tools/helper.py", "helpers/file.css", "fixtures/data.json",
            "buildtags/native.txt", "config/app.json", "package-lock.json",
            "web/package.json", "web/package-lock.json", "web/vite.config.ts",
            "web/src/App.tsx", "web/src/api.ts", "web/src/useServer.ts",
            "web/src/routes/route.tsx", "web/src/setup/Setup.tsx",
            "web/src/auth.ts", "web/src/i18n.ts", "web/src/testfixture.css.ts",
            "AGENTS.md", "docs/AGENTS.md", "docs/nested/SKILL.md",
            "docs/DEVELOPMENT_PRINCIPLES.en.md", "docs/DEVELOPMENT_PRINCIPLES.ja.md",
            "LICENSE", "README.fr.md", "documentation/guide.md", "unknown",
        ]
        for path in paths:
            with self.subTest(path=path):
                self.assertTrue(classify_paths(path)[0])
                self.assertTrue(classify_paths("README.md", path)[0])

    def test_only_explicit_documentation_paths(self):
        self.assertEqual(classify_paths("README.md", "README.en.md", "SECURITY.md",
                                        "docs/guide.md", "docs/deep/example.md"),
                         (False, "documentation_only", False))

    def test_css_and_generated_outputs_require_safe_source(self):
        for paths in [
            ("web/src/styles.css",),
            ("web/src/components/App.css", "web/dist/index.html"),
            ("README.md", "web/src/styles.css", "web/dist/assets/index-abc.js",
             "web/dist/assets/index-def.css"),
        ]:
            with self.subTest(paths=paths):
                self.assertEqual(classify_paths(*paths), (False, "presentation_only", True))
        for paths in [("web/dist/index.html",), ("README.md", "web/dist/index.html"),
                      ("web/src/App.tsx", "web/src/styles.css", "web/dist/index.html"),
                      ("web/src/styles.css", "web/dist/embedded.go")]:
            with self.subTest(paths=paths):
                self.assertTrue(classify_paths(*paths)[0])

    def test_unmodified_complete_tree_can_reuse_verified_full_coverage(self):
        tree = {"internal/core/core.go": blob()}
        self.assertEqual(impact.classify_delta([], tree, tree),
                         (False, "unchanged_tree_from_verified_full_baseline", False))

    def test_deletions_check_the_old_path(self):
        for path, expected in [("docs/old.md", False), ("web/src/old.css", False),
                               ("internal/core/old.go", True), ("web/src/api.ts", True),
                               ("web/dist/old.css", True)]:
            with self.subTest(path=path):
                result = impact.classify_delta([{"status": "D", "paths": [path]}],
                                               {path: blob()}, {})
                self.assertEqual(result[0], expected)

    def test_renames_check_both_endpoints(self):
        for old, new, expected in [
            ("internal/core/logic.go", "docs/guide.md", True),
            ("web/src/api.ts", "web/src/styles.css", True),
            ("docs/guide.md", "internal/core/readme.md", True),
            ("docs/old.md", "docs/new.md", False),
            ("web/src/old.css", "web/src/new.css", False),
            ("web/dist/old.css", "web/dist/new.css", True),
        ]:
            with self.subTest(old=old, new=new):
                changes = impact.parse_name_status(
                    b"R100\0" + old.encode() + b"\0" + new.encode() + b"\0")
                result = impact.classify_delta(changes, {old: blob()}, {new: blob()})
                self.assertEqual(result[0], expected)

    def test_modes_symlinks_submodules_and_status_mismatches_fail_closed(self):
        cases = [
            ([{"status": "M", "paths": ["docs/guide.md"]}],
             {"docs/guide.md": blob()}, {"docs/guide.md": blob("b", "100755")}),
            ([{"status": "M", "paths": ["docs/guide.md"]}],
             {"docs/guide.md": blob("a", "120000")}, {"docs/guide.md": blob("b", "120000")}),
            ([{"status": "A", "paths": ["docs/guide.md"]}], {},
             {"docs/guide.md": blob("a", "160000", "commit")}),
            ([{"status": "A", "paths": ["docs/guide.md"]}],
             {"docs/guide.md": blob()}, {"docs/guide.md": blob("b")}),
            ([{"status": "D", "paths": ["docs/guide.md"]}], {},
             {"docs/guide.md": blob()}),
        ]
        for changes, before, after in cases:
            with self.subTest(changes=changes, before=before, after=after):
                with self.assertRaises(impact.FailClosed):
                    impact.classify_delta(changes, before, after)

    def test_complete_record_boundary_truncation_is_not_a_safe_delta(self):
        before = {"README.md": blob(), "internal/core/core.go": blob()}
        after = {path: blob("b") for path in before}
        changes = impact.parse_name_status(b"M\0README.md\0")
        with self.assertRaisesRegex(impact.FailClosed, "incomplete_tree_delta"):
            impact.classify_delta(changes, before, after)


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
        self.root = pathlib.Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI Fixture")
        self.git("config", "user.email", "ci@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.autocrlf", "false")
        self.write(".gitignore", "/.sobalink-deps/\n/bin/\n/dist/\n/.build/\n__pycache__/\n*.log\n")
        self.write("web/.gitignore", "node_modules/\n.vite/\ncoverage/\n")
        self.write(".github/scripts/fixture.py", "# synthetic tracked script\n")
        self.write("README.md", "fixture documentation\n")
        self.write("internal/core/core.go", "package core\n")
        self.baseline = self.commit()

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

    def classify(self, head=None, **options):
        inputs = {"head": head or self.git("rev-parse", "HEAD"),
                  "baseline": self.baseline, "baseline_run_id": "42"}
        inputs.update(options)
        return impact.classify(self.root, **inputs)

    def test_real_document_delta_reports_immutable_evidence(self):
        self.write("README.md", "fixture documentation changed\n")
        head = self.commit()
        report = self.classify(head)
        self.assertFalse(report["long_required"])
        self.assertEqual(report["reason"], "documentation_only")
        self.assertEqual(report["head_sha"], head)
        self.assertEqual(report["baseline_sha"], self.baseline)
        self.assertEqual(report["baseline_run_id"], 42)
        self.assertEqual(report["head_tree"], self.git("rev-parse", "HEAD^{tree}"))
        self.assertEqual(report["baseline_tree"], self.git("rev-parse", self.baseline + "^{tree}"))
        self.assertEqual(report["changed_paths"], ["README.md"])
        self.assertEqual(report["paths_sha256"], impact.digest(impact.canonical(["README.md"])))
        self.assertEqual(report["policy_sha256"], impact.digest(SCRIPT.read_bytes()))
        self.assertEqual(report["version"], 1)

    def test_cumulative_delta_includes_runtime_change_before_latest_doc_commit(self):
        self.write("internal/core/core.go", "package core\n// changed\n")
        self.commit()
        self.write("README.md", "more docs\n")
        report = self.classify(self.commit())
        self.assertTrue(report["long_required"])
        self.assertEqual(report["changed_paths"], ["README.md", "internal/core/core.go"])

    def test_cumulative_workflow_change_requires_full_adoption_run(self):
        self.write(".github/workflows/ci.yml", "name: fixture\n")
        self.commit()
        self.write("README.md", "more docs\n")
        self.assertTrue(self.classify(self.commit())["long_required"])

    def test_actual_rename_from_runtime_into_allowlist_is_full(self):
        (self.root / "docs").mkdir()
        self.git("mv", "internal/core/core.go", "docs/guide.md")
        report = self.classify(self.commit())
        self.assertTrue(report["long_required"])
        self.assertEqual(report["changes"], [{"status": "R100", "paths": [
            "internal/core/core.go", "docs/guide.md"]}])

    def test_absent_untrusted_invalid_and_nonancestor_baselines_are_full(self):
        for inputs in [{"baseline": None}, {"baseline_run_id": None},
                       {"baseline_run_id": "0"}, {"baseline_run_id": 42},
                       {"baseline_run_id": "42\n"}, {"baseline": "HEAD"},
                       {"baseline": "f" * 40}, {"head": "HEAD"}, {"head": None}]:
            with self.subTest(inputs=inputs):
                call = {"head": self.baseline, "baseline": self.baseline,
                        "baseline_run_id": "42"}
                call.update(inputs)
                self.assertTrue(impact.classify(self.root, **call)["long_required"])
        self.write("docs/branch.md", "branch\n")
        other = self.commit()
        self.git("reset", "--hard", self.baseline)
        self.write("docs/head.md", "head\n")
        report = self.classify(self.commit(), baseline=other)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "baseline_not_proven_ancestor")

    def test_verified_identical_tree_and_force_full(self):
        self.assertFalse(self.classify()["long_required"])
        report = self.classify(force_full=True)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "forced_full")
        self.assertEqual(report["head_sha"], self.baseline)

    def test_head_and_tracked_checkout_must_match_the_classified_commit(self):
        self.write("README.md", "changed\n")
        head = self.commit()
        report = self.classify(self.baseline)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "head_checkout_mismatch")
        self.write("internal/core/core.go", "package core\n// dirty source\n")
        report = self.classify(head)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "dirty_or_unverifiable_checkout")
        self.git("add", "internal/core/core.go")
        self.assertTrue(self.classify(head)["long_required"])
        self.write("internal/core/core.go", "package core\n")
        self.assertEqual(self.classify(head)["reason"], "dirty_or_unverifiable_checkout")

    def test_untracked_source_prevents_documentation_skip(self):
        self.write("README.md", "changed docs\n")
        head = self.commit()
        self.write("internal/core/untracked.go", "package core\n// untracked source\n")
        report = self.classify(head)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "untracked_checkout_paths")

    def test_machine_local_ignore_cannot_hide_untracked_runtime_source(self):
        self.write("README.md", "changed docs\n")
        head = self.commit()
        self.write(".git/info/exclude", "*.go\n")
        self.write("internal/core/untracked.go", "package core\n// ignored source\n")
        self.assertEqual(self.git("ls-files", "--others", "--exclude-standard"), "")
        report = self.classify(head)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "unrecognized_ignored_checkout_paths")

    def test_known_ignored_build_outputs_do_not_change_the_source_tree(self):
        self.write("README.md", "changed docs\n")
        head = self.commit()
        for path in [".build/result", ".sobalink-deps/fixture/source.go", "bin/tool",
                     "dist/archive.tar.gz", "web/node_modules/fixture/index.js",
                     ".github/scripts/__pycache__/ci-impact.pyc"]:
            self.write(path, "synthetic build output\n")
        self.assertFalse(self.classify(head)["long_required"])

    def test_arbitrary_ignored_files_and_cache_lookalikes_are_not_build_outputs(self):
        self.write("README.md", "changed docs\n")
        head = self.commit()
        self.write(".git/info/exclude", "*__pycache__/\n")
        self.write("internal/core/extra.log", "unexpected ignored content\n")
        report = self.classify(head)
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "unrecognized_ignored_checkout_paths")
        (self.root / "internal/core/extra.log").unlink()
        self.write("internal/core/fake__pycache__/source.go", "package hidden\n")
        self.assertEqual(self.classify(head)["reason"], "unrecognized_ignored_checkout_paths")

    def test_assume_unchanged_and_skip_worktree_flags_cannot_hide_dirty_bytes(self):
        self.write("README.md", "changed docs\n")
        head = self.commit()
        path = "internal/core/core.go"
        for flag, reset in [("--assume-unchanged", "--no-assume-unchanged"),
                            ("--skip-worktree", "--no-skip-worktree")]:
            with self.subTest(flag=flag):
                self.git("update-index", flag, "--", path)
                # Even a clean hidden entry is insufficient checkout evidence.
                self.assertEqual(self.classify(head)["reason"], "unsafe_index_flags")
                self.write(path, "package core\n// hidden changed source\n")
                self.assertEqual(self.git("diff", "--name-only", "HEAD"), "")
                report = self.classify(head)
                self.assertTrue(report["long_required"])
                self.assertEqual(report["reason"], "unsafe_index_flags")
                self.git("update-index", reset, "--", path)
                self.git("checkout", "--", path)

    def test_shallow_history_does_not_establish_coverage(self):
        # A valid shallow marker is sufficient to exercise Git's history check.
        (self.root / ".git/shallow").write_text(self.baseline + "\n", encoding="ascii")
        report = self.classify()
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "incomplete_repository_history")

    def test_actual_mode_change_is_full(self):
        self.git("update-index", "--chmod=+x", "README.md")
        self.git("commit", "-qm", "Synthetic mode change")
        self.git("checkout-index", "--force", "--", "README.md")
        report = self.classify()
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "non_regular_or_changed_file_mode")

    def test_actual_symlink_is_full_without_os_symlink_support(self):
        oid = self.git("hash-object", "-w", "README.md")
        self.git("update-index", "--add", "--cacheinfo", "120000", oid, "docs/link.md")
        self.git("commit", "-qm", "Synthetic symlink entry")
        self.assertTrue(self.classify()["long_required"])

    def test_actual_submodule_entry_is_full(self):
        self.git("update-index", "--add", "--cacheinfo", "160000", self.baseline, "docs/module.md")
        self.git("commit", "-qm", "Synthetic submodule entry")
        self.assertTrue(self.classify()["long_required"])

    def test_git_failure_and_complete_record_truncation_are_full(self):
        self.write("README.md", "changed\n")
        self.write("internal/core/core.go", "package core\n// changed\n")
        head = self.commit()
        original = impact.git

        def truncated(root, *args):
            if args[0] == "diff" and "--name-status" in args:
                return b"M\0README.md\0"
            return original(root, *args)

        with mock.patch.object(impact, "git", side_effect=truncated):
            report = self.classify(head)
            self.assertTrue(report["long_required"])
            self.assertEqual(report["reason"], "incomplete_tree_delta")
        with mock.patch.object(impact, "git", side_effect=impact.FailClosed("git_query_failed")):
            self.assertTrue(self.classify(head)["long_required"])
        with mock.patch.object(impact, "parse_name_status", side_effect=ValueError("unexpected")):
            report = self.classify(head)
            self.assertTrue(report["long_required"])
            self.assertEqual(report["reason"], "classification_failed")

    def test_truncated_index_inventory_is_not_checkout_proof(self):
        original = impact.git

        def truncated(root, *args):
            if args[0] == "ls-files" and "--cached" in args:
                return b"H README.md\0"
            return original(root, *args)

        with mock.patch.object(impact, "git", side_effect=truncated):
            report = self.classify()
        self.assertTrue(report["long_required"])
        self.assertEqual(report["reason"], "incomplete_checkout_index")

    def test_cli_json_and_output_file(self):
        self.write("README.md", "docs changed\n")
        head = self.commit()
        output = self.root / "impact.json"
        result = subprocess.run([
            os.sys.executable, str(SCRIPT), "--repo", str(self.root), "--head", head,
            "--verified-baseline-commit", self.baseline,
            "--verified-baseline-run-id", "42", "--output", str(output),
        ], check=True, capture_output=True, text=True)
        report = json.loads(result.stdout)
        self.assertEqual(report, json.loads(output.read_text(encoding="utf-8")))
        self.assertFalse(report["long_required"])


class CommandLineFailureTests(unittest.TestCase):
    def test_missing_conflicting_and_unknown_inputs_emit_full_json(self):
        for argv in [[], ["--head"], ["--not-an-option"], ["--hea", "a" * 40],
                     ["--head", "a" * 40, "--head", "b" * 40],
                     ["--head=" + "a" * 40, "--head=" + "b" * 40]]:
            with self.subTest(argv=argv), contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(impact.main(argv), 0)
                report = json.loads(output.getvalue())
                self.assertTrue(report["long_required"])
                self.assertNotEqual(report["reason"], "documentation_only")

    def test_policy_read_failure_is_full(self):
        with mock.patch.object(impact.pathlib.Path, "read_bytes", side_effect=OSError("private/path")):
            report = impact.classify(pathlib.Path.cwd(), head="a" * 40)
        self.assertTrue(report["long_required"])
        self.assertIsNone(report["policy_sha256"])
        self.assertEqual(report["reason"], "policy_unavailable")

    def test_report_write_failure_emits_full_and_nonzero(self):
        with mock.patch.object(impact.pathlib.Path, "write_text", side_effect=OSError("private/path")), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(impact.main(["--output", "report.json"]), 1)
        report = json.loads(output.getvalue())
        self.assertTrue(report["long_required"])
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
