"""Regenerate the three existing explanatory themes from their shared primitives."""
from pathlib import Path
from runpy import run_path
D = Path(__file__).parent
_semantic = run_path(str(D / "semantic.py"))
PALETTES = _semantic["PALETTES"]
permissions_map = _semantic["permissions_map"]
saved_lifecycle = _semantic["saved_lifecycle"]
file_evidence = _semantic["file_evidence"]
GENERATORS = {'permissions': permissions_map, 'saved-and-running': saved_lifecycle,
              'delivery-evidence': file_evidence}
for kind, make in GENERATORS.items():
 for lang in ('ja', 'en'):
  for theme in PALETTES:
   for narrow in (False, True):
    size = 'narrow' if narrow else 'wide'
    (D/f'{kind}-{lang}-{theme}-{size}.svg').write_text(make(lang,theme,narrow))
print('Generated 24 localized and themed explainer SVGs.')
