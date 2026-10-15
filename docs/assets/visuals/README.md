# Documentation diagrams

[Documentation index](../../README.en.md) · [日本語](../../README.md)

Four concepts, one question per diagram: service connection, independent permissions, saved versus started services, and durable-file save evidence. Each has Japanese/English, light/dark and wide/narrow variants. These are explanatory illustrations, not screenshots or test evidence.

## Embed and read

Use a `picture` element with narrow sources below 960px for the spatial connection overview (840px for the other diagrams) and dark sources for a dark color preference. Include descriptive `alt` text and a short paragraph explaining the same boundary. Renderers that ignore `picture` still receive the fallback image and text. Never treat color or a connecting arrow as proof of success. The wide connection overview is 1100 × 650; its narrow counterpart is 360 × 816. The Markdown host controls the available column width and may ignore media hints. Keep the text alternative visible and use the narrow asset when a constrained host makes the wide labels too small.

- `connection`: two sobalink devices sharing a service. Ordinary Tailnet targets do not require sobalink.
- `permissions`: network connection, message/file trust and scoped service permission are independent.
- `saved-and-running`: saving does not start a service or revive authority; application authentication remains separate.
- `delivery-evidence`: durable files/folders only. Local acceptance, receiver saving and sender confirmation of save evidence differ. Neither reading nor continued existence is guaranteed.

## Regenerate and check

Run from this directory with Python 3:

```sh
python3 build.py
python3 explainers.py
XDG_CACHE_HOME=/tmp python3 render.py
XDG_CACHE_HOME=/tmp python3 verify.py
```

Generation uses only the Python standard library. Optional PNG rendering needs librsvg and Cairo; review sheets also need Pillow. Text-bound verification uses librsvg. Generated PNG review sheets and HTML previews stay untracked. Commit the generator and all changed SVG variants together.

The palette follows the existing UI. Text labels accompany state colors. Japanese and English copy lives in the generators, not translated identifiers. Review all eight variants for each changed concept, narrow legibility and plain-text fallback. SVG text-bound checks do not establish browser layout, screen-reader support, 200% text zoom or application acceptance.
