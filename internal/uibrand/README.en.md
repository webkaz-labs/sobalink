# Shared interface brand

[日本語](README.ja.md)

The Web UI and native companion share color tokens, the three-bar mark and icon geometry. The existing Web colors are the baseline. Density, layout, native OS accents and high-contrast behavior remain interface-specific.

- `palette.json` supplies light/dark semantic colors. Go reads them with `Palette(dark)`. After editing, run `npm run brand:generate` in `web`; `brand:check`, Web builds and tests reject stale shipped CSS.
- `icons.json` contains the existing Web paths. React imports it directly; Go `IconSVG(name)` returns the same 24×24, 1.7-stroke artwork with `currentColor` for caller-selected tint.
- `mark.svg` contains the tilted three bars. Web uses its alpha mask with the active primary color and imports the same asset for the favicon. Go `MarkSVG()` returns a copy without the standalone favicon color fallback, so it inherits the caller’s `currentColor`; `MarkOpaqueSVG()` removes the third bar's reduced opacity for small tray icons. Native renderers select their own tint.

These assets do not define screen layout or replace platform accessibility settings. Tests check the shipped palette, actual component paths and asset variants; real-device and visual acceptance remain separate.
