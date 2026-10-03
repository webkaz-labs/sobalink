"""Pure fixture checks only: no soba execution, real PID observation, or sockets."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import resource_observer as observer


def proc_fixture(root, pid=123, start=100, state="S", rss=80, peak=96):
    directory = root / str(pid)
    directory.mkdir(exist_ok=True)
    # Field 3 through 22; comm deliberately has spaces and parentheses.
    fields = [state] + ["0"] * 18 + [str(start)]
    (directory / "stat").write_text("123 (fixture (worker)) " + " ".join(fields))
    (directory / "status").write_text(f"Name:\tprivate-fixture\nVmRSS:\t{rss} kB\nVmHWM:\t{peak} kB\nThreads:\t4\n")
    (directory / "fd").mkdir(exist_ok=True)
    (directory / "fd/0").touch()
    return directory


class ObserverTests(unittest.TestCase):
    def test_counts_sizes_without_content_or_names(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "private-fixture-name").write_bytes(b"private-fixture-contents")
            (root / "nested").mkdir()
            (root / "nested/empty").touch()
            values = observer.tree_totals(root)
            self.assertEqual(values["files"], 2)
            self.assertEqual(values["directories"], 1)
            self.assertEqual(values["logical_bytes"], 24)
            encoded = json.dumps(values)
            self.assertNotIn("private", encoded)
            self.assertNotIn(tmp, encoded)

    def test_symlink_is_not_followed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "tree").mkdir()
            (root / "outside").write_bytes(b"outside-payload")
            (root / "tree/link").symlink_to(root / "outside")
            values = observer.tree_totals(root / "tree")
            self.assertEqual(values["symlinks"], 1)
            self.assertEqual(values["logical_bytes"], 0)

    def test_scan_limit_is_explicit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "one").touch()
            self.assertFalse(observer.tree_totals(root, entry_limit=0)["scan_complete"])

    def test_log_and_outgoing_are_separate_subset_counters(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "state/outgoing").mkdir(parents=True)
            (root / "state/startup.log").write_bytes(b"x" * 10)
            (root / "state/outgoing/payload").write_bytes(b"x" * 20)
            values = observer.disk_sample(root)
            self.assertEqual(values["state_logical_bytes"], 30)
            self.assertEqual(values["outgoing_logical_bytes"], 20)
            self.assertEqual(values["startup_log_bytes"], 10)

    def test_memory_units_and_identity_without_real_proc(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            proc_fixture(root)
            values, start = observer.process_sample(123, proc_root=root)
            self.assertEqual(start, 100)
            self.assertEqual(values["rss_bytes"], 81920)
            self.assertEqual(values["os_peak_rss_bytes"], 98304)
            self.assertEqual(values["fd_count"], 1)
            self.assertTrue(values["process_alive"])
            with self.assertRaisesRegex(observer.ObservationError, "process_identity_changed"):
                observer.process_sample(123, expected_start=101, proc_root=root)

    def test_missing_and_zombie_process_are_not_alive(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertFalse(observer.process_sample(123, proc_root=root)[0]["process_alive"])
            proc_fixture(root, state="Z")
            self.assertFalse(observer.process_sample(123, proc_root=root)[0]["process_alive"])

    def test_summary_does_not_turn_exit_into_zero_memory(self):
        samples = [dict(elapsed_seconds=0, process_alive=True, rss_bytes=10, fd_count=3),
                   dict(elapsed_seconds=1, process_alive=True, rss_bytes=20, fd_count=4),
                   dict(elapsed_seconds=2, process_alive=False)]
        result = observer.summarize(samples)
        self.assertEqual(result["metrics"]["rss_bytes"], dict(baseline=10, maximum=20, end=None, samples=2))
        self.assertFalse(result["process_alive_at_end"])

    def test_local_observation_refused_before_any_process_reads(self):
        with patch.object(observer.platform, "system", return_value="Linux"):
            with self.assertRaisesRegex(observer.ObservationError, "github_hosted_fixture_required"):
                observer.verify_context(Path("/not-read"), 123, "0" * 64, env={})

    def test_duration_is_bounded_before_sampling(self):
        with patch.object(observer, "process_sample", side_effect=AssertionError("no reads")):
            with self.assertRaisesRegex(observer.ObservationError, "duration_or_interval_out_of_bounds"):
                observer.observe(Path("/not-read"), 123, 100, 901, 1)


if __name__ == "__main__":
    unittest.main()
