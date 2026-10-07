"""Pure workflow/source checks; no Go compiler, native fixture or network runs."""

import ast
from pathlib import Path
import re
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[2]
TEST = "TestLANCoreDeviceCardPairingCompositionIntegration"
PACKAGE = "github.com/webkaz-labs/sobalink/internal/core"
STEP = "Verify paired transport and Core applications over an isolated relay"
TAGS = "lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport"


class CardPairingSelectionTests(unittest.TestCase):
    def test_candidate_is_one_opt_in_native_test_without_initializers(self):
        source = (ROOT / "internal/core/device_card_pairing_integration_test.go").read_text()
        self.assertTrue(source.startswith("//go:build lanlink_integration\n\n"))
        self.assertEqual(re.findall(r"^func (Test\w+)\(", source, re.MULTILINE), [TEST])
        self.assertNotRegex(source, r"(?m)^func (?:init|TestMain)\(")
        self.assertIn("newNativeLANCoreFixture(t)", source)

    def test_both_existing_native_lanes_select_and_require_exact_pass(self):
        for name in ("ci.yml", "prerelease.yml"):
            with self.subTest(workflow=name):
                source = (ROOT / ".github/workflows" / name).read_text()
                marker = "      - name: " + STEP + "\n"
                self.assertEqual(source.count(marker), 1)
                block = source.split(marker, 1)[1].split("      - name:", 1)[0]
                self.assertIn("timeout-minutes: 10", block)
                python = textwrap.dedent(block.split("python - <<'PY'\n", 1)[1].rsplit("          PY", 1)[0])
                parsed = ast.parse(python)
                calls = [node for node in ast.walk(parsed) if isinstance(node, ast.Call)
                         and isinstance(node.func, ast.Attribute)
                         and isinstance(node.func.value, ast.Name)
                         and node.func.value.id == "subprocess" and node.func.attr == "run"]
                self.assertEqual(len(calls), 1)
                call = calls[0]
                self.assertEqual(ast.unparse(call.args[0].elts[0]), "sys.executable")
                args = [ast.literal_eval(node) for node in call.args[0].elts[1:]]
                self.assertEqual(args[0], ".github/scripts/ci-go-test.py")
                separator = args.index("--")
                self.assertEqual(args[1:separator:2], ["--expect"] * ((separator - 1) // 2))
                expected = args[2:separator:2]
                self.assertEqual(expected.count(PACKAGE + ":" + TEST), 1)
                command = args[separator + 1:]
                self.assertEqual(command[:4], ["go", "test", "-count=1", "-v"])
                self.assertIn("-tags=" + TAGS, command)
                self.assertIn("-timeout=5m", command)
                self.assertEqual(command[-3:], ["./internal/lanlink", "./internal/core", "./internal/routecat"])
                patterns = [arg[5:] for arg in command if arg.startswith("-run=")]
                self.assertEqual(len(patterns), 1)
                self.assertIsNotNone(re.fullmatch(patterns[0], TEST))
                self.assertIsNone(re.search(patterns[0], TEST + "Extra"))
                self.assertIsNone(re.search(patterns[0], "Extra" + TEST))
                for item in expected:
                    self.assertIsNotNone(re.fullmatch(patterns[0], item.split(":", 1)[1]))
                keywords = {item.arg: ast.unparse(item.value) for item in call.keywords}
                self.assertEqual(keywords, {"env": "env", "check": "True", "timeout": "9 * 60"})
                self.assertIn('env["SOBALINK_RUN_LAN_INTEGRATION"] = "1"', python)
                self.assertIn('("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "TS_PROXY")', python)
                self.assertIn('not k.upper().startswith("TS_")', python)


if __name__ == "__main__":
    unittest.main()
