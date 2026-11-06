// The shared palette is authoritative. Keep the shipped CSS reviewable and checked.
import { readFileSync, writeFileSync } from 'node:fs'
const palette = JSON.parse(readFileSync(new URL('../../internal/uibrand/palette.json', import.meta.url), 'utf8'))
const block = (selector, colors) => `${selector} {\n${Object.entries(colors).map(([name, value]) => `  --${name}: ${value};`).join('\n')}\n}\n`
const css = '/* Generated from internal/uibrand/palette.json; run npm run brand:generate. */\n' + block(':root', palette.light) + block('html[data-theme="dark"]', palette.dark) + '@media (prefers-color-scheme: dark) {\n' + block('html[data-theme="system"]', palette.dark) + '}\n'
const destination = new URL('../src/brand-tokens.css', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(destination, 'utf8') !== css) throw new Error('Shared brand CSS is stale: run npm run brand:generate')
} else writeFileSync(destination, css)
