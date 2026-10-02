"""Unit tests for strict, readable PTY restoration diagnostics."""
import copy
import importlib.util
import pathlib
import types
import unittest

spec = importlib.util.spec_from_file_location("pty_smoke", pathlib.Path(__file__).with_name("pty-smoke.py"))
pty_smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pty_smoke)


class TermiosDiagnosticsTests(unittest.TestCase):
    def setUp(self):
        self.constants = types.SimpleNamespace(
            PENDIN=0x20000000, ICANON=0x100, ECHO=0x8,
            VMIN=16, VTIME=17, B9600=9600, B19200=19200, CSIZE=0x300,
        )
        self.before = [0, 0, 0x300, 0x108, 9600, 9600, [b"\x00"] * 20]

    def test_equal_snapshot_keeps_all_fields(self):
        result = pty_smoke.termios_diagnostics(self.before, self.before, self.constants)
        self.assertEqual(result["changes"], {})
        self.assertEqual(result["before"], result["after"])
        self.assertEqual(set(result["before"]), {"iflag", "oflag", "cflag", "lflag", "ispeed", "ospeed", "cc"})
        self.assertEqual(len(result["before"]["cc"]), 20)

    def test_dynamic_and_real_mode_changes_are_both_reported(self):
        after = copy.deepcopy(self.before)
        after[3] |= self.constants.PENDIN
        after[3] &= ~self.constants.ECHO
        result = pty_smoke.termios_diagnostics(self.before, after, self.constants)
        delta = result["changes"]["lflag"]
        self.assertEqual(delta["set"], ["PENDIN"])
        self.assertEqual(delta["cleared"], ["ECHO"])
        self.assertEqual(delta["xor_hex"], hex(self.constants.PENDIN | self.constants.ECHO))
        self.assertNotEqual(result["before"], result["after"])

    def test_unknown_flag_bits_are_not_filtered(self):
        after = copy.deepcopy(self.before)
        after[0] |= 1 << 50
        result = pty_smoke.termios_diagnostics(self.before, after, self.constants)
        self.assertEqual(result["changes"]["iflag"]["xor_hex"], hex(1 << 50))
        self.assertNotEqual(result["before"], result["after"])

    def test_speeds_and_control_value_types_remain_exact(self):
        after = copy.deepcopy(self.before)
        after[4] = 19200
        after[6][self.constants.VMIN] = 0  # Same numeric byte; distinct tcgetattr representation.
        result = pty_smoke.termios_diagnostics(self.before, after, self.constants)
        self.assertEqual(result["changes"]["ispeed"]["after_names"], ["B19200"])
        change = result["changes"]["cc"][0]
        self.assertEqual(change["names"], ["VMIN"])
        self.assertEqual(change["before"], {"type": "bytes", "hex": "00"})
        self.assertEqual(change["after"], {"type": "int", "value": 0})


if __name__ == "__main__":
    unittest.main()
