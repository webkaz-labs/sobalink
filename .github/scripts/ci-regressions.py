#!/usr/bin/env python3
"""Run shared policy tests once; retain native OS contracts and periodic breadth.

This changes regression-test placement, not product/native acceptance coverage.
The common job still discovers every test. Native lanes run all regressions on
CI/toolchain changes, scheduled/forced runs, and unverifiable input. Otherwise
they exercise real filesystem/process contracts instead of repeating portable
fixtures. A hosted-runner change without a source change is covered nightly.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import platform
import re
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ROOT / '.github/scripts'
NATIVE_TESTS = (
    'test_ci_native_core.MainReceiptTests',
    'test_ci_native_core_required.RunnerTests',
    'test_ci_native_core_required.ActualContextTests',
    'test_ci_native_core_required.FixturePortabilityTests',
    'test_ci_metrics.CIMetricsTests',
    'test_ci_nearby_screenshots.ScreenshotExportTests.test_real_filesystem_requires_native_private_ownership_and_modes',
)
POSIX_TESTS = (
    'test_ci_native_core.RealPosixOwnershipTests',
    'test_ci_text_diagnostic.TextDiagnosticTests.test_receipt_and_hash_inputs_reject_unsafe_file_types',
)
DARWIN_TESTS = (
    'test_ci_dnssd_terminal.TerminalDiagnosticTests',
    'test_ci_dnssd_terminal_origin.TerminalOriginTests.test_query_cleanup_callback_observes_the_actual_child_wait',
)
# Only verified ordinary product source edits may avoid the full OS repetition.
# New path kinds, dependency manifests, policy, scripts and build inputs default
# to full. Source-contract tests still run for every such edit in the common job.
PRODUCT_PREFIXES = ('cmd/', 'internal/', 'poc/native/', 'web/src/', 'web/browser/', 'web/dist/')
PRODUCT_SUFFIXES = ('.go', '.c', '.h', '.ts', '.tsx', '.css', '.mjs', '.js', '.svg', '.html')
BUILD_PREFIXES = ('cmd/prepare-engine/', 'internal/engineadaptation/', 'internal/lifecycleadaptation/')
VERIFIED_REASONS = frozenset(('unproven_or_shared_runtime_path', 'reviewed_presentation',
                              'reviewed_safe_scope_union'))


def full_native_regressions(plan):
    """Fail closed; the caller must reproduce the exact Git/event plan first."""
    if (not isinstance(plan, dict) or plan.get('scope') not in ('full', 'native-short')
            or plan.get('reason') not in VERIFIED_REASONS
            or not isinstance(plan.get('base_sha'), str)
            or not re.fullmatch('[0-9a-f]{40}', plan['base_sha'])):
        return True
    paths = plan.get('changed_paths')
    if (not isinstance(paths, list) or not paths or not all(isinstance(path, str) for path in paths)
            or len(paths) != len(set(paths))):
        return True
    for path in paths:
        if (not isinstance(path, str) or '\\' in path
                or any(part in ('', '.', '..') for part in path.split('/'))
                or path.startswith(BUILD_PREFIXES)
                or not path.startswith(PRODUCT_PREFIXES)
                or not path.endswith(PRODUCT_SUFFIXES)):
            return True
    return False


def native_tests(platform, os_name):
    names = NATIVE_TESTS
    if os_name == 'posix':
        names += POSIX_TESTS
    if platform == 'darwin':
        names += DARWIN_TESTS
    return names


def full_suite(role, plan, host_platform, machine, environment=None):
    if role == 'common':
        return True
    # The required common job is the same Ubuntu 24.04 x64 target. Repeating
    # discovery there again would add a fifth full suite to periodic runs.
    env = os.environ if environment is None else environment
    if (host_platform == 'linux' and machine.lower() in ('x86_64', 'amd64')
            and env.get('GITHUB_ACTIONS') == 'true'
            and env.get('GITHUB_WORKFLOW') == 'Cross-platform CI'
            and env.get('GITHUB_JOB') == 'native'
            and env.get('RUNNER_ENVIRONMENT') == 'github-hosted'
            and env.get('RUNNER_OS') == 'Linux' and env.get('RUNNER_ARCH') == 'X64'):
        return False
    return full_native_regressions(plan)


def verified_plan(path):
    spec = importlib.util.spec_from_file_location('regression_coverage', SCRIPTS / 'ci-coverage.py')
    coverage = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(coverage)
    return coverage.verified_plan(path)


def suite(full):
    loader = unittest.TestLoader()
    if str(SCRIPTS) not in sys.path:
        sys.path.insert(0, str(SCRIPTS))
    selected = (loader.discover(str(SCRIPTS), pattern='test_*.py') if full else
                loader.loadTestsFromNames(native_tests(sys.platform, os.name)))
    if loader.errors or not selected.countTestCases():
        raise ValueError('regression selection did not load a nonempty suite')
    return selected


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('role', choices=('common', 'native'))
    parser.add_argument('--plan', required=True, type=Path)
    args = parser.parse_args(argv)
    plan = verified_plan(args.plan)
    full = full_suite(args.role, plan, sys.platform, platform.machine())
    selected = suite(full)
    print(f"Python regressions: {args.role}, {'full discovery' if full else 'native OS contracts'}, "
          f'{selected.countTestCases()} cases', flush=True)
    return 0 if unittest.TextTestRunner(verbosity=2).run(selected).wasSuccessful() else 1


if __name__ == '__main__':
    sys.exit(main())
