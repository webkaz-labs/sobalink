"""Exercise the resource workflow's import prefix without API or downloads."""
import ast
import pathlib
import subprocess
import sys
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]


class ResourceWorkflowImportTests(unittest.TestCase):
    def prefix(self):
        workflow = (ROOT / '.github/workflows/release-resources.yml').read_text()
        start = workflow.index("          python - <<'PY' > /dev/null\n")
        block = workflow[start:].split('\n', 1)[1].split('          PY\n', 1)[0]
        tree = ast.parse(textwrap.dedent(block))
        guarded = next(node for node in tree.body if isinstance(node, ast.Try))
        prefix = [node for node in tree.body if isinstance(node, ast.Import)]
        for node in guarded.body:
            if isinstance(node, ast.Assign) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == 'load_validator':
                break
            prefix.append(node)
        return ast.unparse(ast.Module(body=prefix, type_ignores=[])) + '\n'

    def execute(self, source):
        return subprocess.run([sys.executable, '-I', '-B', '-c', source],
                              cwd=ROOT, capture_output=True, text=True, timeout=15)

    def test_workflow_import_resolves_sibling_validation_modules(self):
        result = self.execute(self.prefix())
        self.assertEqual(result.returncode, 0, 'workflow validation imports failed')

    def test_missing_scripts_path_is_detected(self):
        source = self.prefix()
        insertion = "sys.path.insert(0, str(pathlib.Path('.github/scripts').resolve()))\n"
        self.assertEqual(source.count(insertion), 1)
        result = self.execute(source.replace(insertion, ''))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No module named 'source_provenance'", result.stderr)


if __name__ == '__main__':
    unittest.main()
