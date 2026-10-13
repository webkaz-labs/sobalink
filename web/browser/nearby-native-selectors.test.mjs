import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'
import { JSDOM, VirtualConsole } from 'jsdom'

// Run the pinned Playwright selector engine against a synthetic DOM only.
// This is a selector regression, never native browser or transport acceptance.
const require = createRequire(import.meta.url)
const bundle = readFileSync(require.resolve('playwright-core/lib/coreBundle'), 'utf8')
const sourceLiteral = bundle.match(/source4 = ('(?:\\.|[^'\\])*');/)
assert.ok(sourceLiteral, 'Pinned Playwright injected selector source unavailable')
const source = runInNewContext(sourceLiteral[1], Object.create(null))
const fixture = (scope, options) => {
  const virtualConsole = new VirtualConsole()
  virtualConsole.on('jsdomError', error => {
    // jsdom has no generated pseudo-element styles; this fixture has none.
    assert.equal(error.type, 'not-implemented')
    assert.equal(error.message, "Not implemented: Window's getComputedStyle() method: with pseudo-elements")
  })
  const dom = new JSDOM('<!doctype html><label id="scope"></label>', { runScripts: 'outside-only', virtualConsole })
  const label = dom.window.document.querySelector('label')
  label.append(scope)
  const select = dom.window.document.createElement('select')
  for (const value of options) { const option = dom.window.document.createElement('option'); option.textContent = value; select.append(option) }
  label.append(select)
  const engine = dom.window.eval(`(() => { const module = {}; ${source}; return new (module.exports.InjectedScript())(globalThis, {isUnderTest:true,sdkLanguage:'javascript',frameSeq:1,testIdAttributeName:'data-testid',stableRafCount:1,browserName:'chromium',isUtilityWorld:false,customEngines:[]}); })()`)
  const query = selector => engine.querySelectorAll(engine.parseSelector(selector), dom.window.document)
  return { dom, select, query }
}
for (const [scope, choices] of [['Network to use', ['Choose a network', '127.0.0.1 · 127.0.0.0/8']], ['使用するネットワーク', ['ネットワークを選択', '127.0.0.1 · 127.0.0.0/8']]]) {
  test('nearby scope uses accessible combobox name with nested options: ' + scope, () => {
    const { dom, select, query } = fixture(scope, choices)
    try {
      assert.equal(query(`internal:label=${JSON.stringify(scope)}s`).length, 0, 'Old exact label must fail for the real markup shape')
      const matches = query(`internal:role=combobox[name=${JSON.stringify(scope)}s]`)
      assert.equal(matches.length, 1); assert.equal(matches[0], select)
      assert.equal(query('internal:role=combobox[name="unrelated"s]').length, 0)
    } finally { dom.window.close() }
  })
}
test('native acceptance retains named-dialog navigation and scope role lookup', () => {
  const spec = readFileSync(new URL('./nearby-setup-native.acceptance.mjs', import.meta.url), 'utf8')
  const ui = readFileSync(new URL('../src/components/NearbySetup.tsx', import.meta.url), 'utf8')
  assert.match(ui, /<label className="field">\{c\('scope'\)\}<select/)
  assert.equal(spec.includes("getByLabel(c.scope"), false)
  assert.equal(spec.match(/getByRole\('combobox', \{ name: c.scope, exact: true \}\)/g)?.length, 3)
  assert.ok(spec.includes("getByRole('dialog', { name: c.dialog, exact: true })"))
  assert.ok(spec.includes("await expect(page.getByRole('dialog')).toHaveCount(0)"))
  assert.ok(spec.includes("await dialog.getByRole('button', { name: c.open, exact: true }).click()"))
})
