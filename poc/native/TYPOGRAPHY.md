# Japanese punctuation correction

A fixed-text three-way headless comparison reproduced the reported defect:

1. Generic/system font, no text language: Japanese U+3002/U+3001 appeared raised,
   undersized and unlike normal horizontal Japanese punctuation.
2. Generic/system font with Lang("ja"): the defect remained in this environment.
3. Explicit Noto Sans CJK JP with Lang("ja"): Japanese punctuation returned to
   the normal lower-left position within the full-width glyph cell.

This distinguishes an actual fallback-font problem from normal Japanese
punctuation positioning. The comparison does not establish that the original
face was a Chinese font; no such claim is made. System fallback selection was
not instrumented at individual glyph level.

Fix: set Japanese text language and an explicit installed Japanese family list:
Hiragino Sans, Yu Gothic UI, Meiryo, Noto Sans CJK JP, Noto Sans JP. English keeps
its system default. Headless rendering also sets the Tester's locale. No text or
punctuation was removed or replaced, and no font-system setting was changed.

The verified renderer used the existing Noto Sans CJK JP face from the installed
Noto CJK collection. Its font license is SIL Open Font License 1.1 according to
the installed distribution copyright file; upstream is
https://github.com/notofonts/noto-cjk . No font binary is bundled in this PoC.
Hiragino/Yu Gothic/Meiryo are referenced by installed family name only, not copied
or redistributed. Their selection and punctuation require native macOS/Windows
checks. Font availability can differ on other Linux systems; this is not a
universal fallback guarantee.

The diagnostic TestJapaneseTypographyProbe writes only fixed fictional text when
SOBA_POC_TYPOGRAPHY_OUTPUT is explicitly provided. Fixed punctuation checks use
「確認します。端末、設定。句読点『、。』」. The regular UI tests and expanded narrow
Japanese screenshot were rerun with the corrected font.

日本語句読点の不自然な位置を固定文で再現し、日本語フォントの明示で修正しました。
言語指定だけでは再現環境の問題は直りませんでした。句読点は削除していません。
これはheadless描画の確認であり、macOS/Windows実機の字体・IME確認とは別です。
