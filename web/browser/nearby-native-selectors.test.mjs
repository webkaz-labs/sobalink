import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'
import { JSDOM, VirtualConsole } from 'jsdom'
import { act, createElement as h } from 'react'
import { createRoot } from 'react-dom/client'
import { nearbyText } from '../src/nearby-i18n.ts'
import { translator } from '../src/i18n.ts'

// Run the pinned Playwright selector engine against a synthetic DOM only.
// This is a selector regression, never native browser or transport acceptance.
const require = createRequire(import.meta.url)
const bundle = readFileSync(require.resolve('playwright-core/lib/coreBundle'), 'utf8')
const sourceLiteral = bundle.match(/source4 = ('(?:\\.|[^'\\])*');/)
assert.ok(sourceLiteral, 'Pinned Playwright injected selector source unavailable')
const source = runInNewContext(sourceLiteral[1], Object.create(null))
const domFixture = () => {
  const virtualConsole = new VirtualConsole()
  virtualConsole.on('jsdomError', error => {
    // jsdom has no generated pseudo-element styles; this fixture has none.
    assert.equal(error.type, 'not-implemented')
    assert.equal(error.message, "Not implemented: Window's getComputedStyle() method: with pseudo-elements")
  })
  const dom = new JSDOM('<!doctype html><div id="root"></div>', { runScripts: 'outside-only', virtualConsole })
  const engine = dom.window.eval(`(() => { const module = {}; ${source}; return new (module.exports.InjectedScript())(globalThis, {isUnderTest:true,sdkLanguage:'javascript',frameSeq:1,testIdAttributeName:'data-testid',stableRafCount:1,browserName:'chromium',isUtilityWorld:false,customEngines:[]}); })()`)
  const query = selector => engine.querySelectorAll(engine.parseSelector(selector), dom.window.document)
  return { dom, query }
}
const fixture = (scope, options) => {
  const { dom, query } = domFixture()
  const label = dom.window.document.createElement('label')
  label.append(scope)
  const select = dom.window.document.createElement('select')
  for (const value of options) { const option = dom.window.document.createElement('option'); option.textContent = value; select.append(option) }
  label.append(select); dom.window.document.querySelector('#root').append(label)
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
  assert.equal(spec.includes("getByLabel("), false)
  assert.equal(spec.match(/getByRole\('combobox', \{ name: c.scope, exact: true \}\)/g)?.length, 3)
  assert.ok(spec.includes("getByRole('dialog', { name: c.dialog, exact: true })"))
  assert.ok(spec.includes("await expect(page.getByRole('dialog')).toHaveCount(0)"))
  assert.ok(spec.includes("await dialog.getByRole('button', { name: c.open, exact: true }).click()"))
})

// Bind independent acceptance labels to the product strings, then exercise
// every control shape used by that case with the pinned React DOM renderer.
// This does not substitute for the actual Core/browser acceptance.
const spec = readFileSync(new URL('./nearby-setup-native.acceptance.mjs', import.meta.url), 'utf8')
const literal = spec.match(/const labels = ([\s\S]*?)\nconst commandResult/)
assert.ok(literal)
const labels = runInNewContext(`(${literal[1]})`, Object.create(null))
const names = { open: 'open', back: 'back', alias: 'alias', scope: 'scope', ports: 'ports', bootstrapPort: 'bootstrapPort', relayPort: 'relayPort', host: 'allowHost', consent: 'consent', start: 'start', local: 'localExchange', peer: 'peerExchange', import: 'importPeer', unavailable: 'discoveryUnavailable', review: 'review', fingerprint: 'fingerprint', otherFingerprint: 'peerFingerprint', save: 'saveConsent', verified: 'verified', approve: 'approve', connected: 'connected', trust: 'trustSeparate' }
for (const locale of ['en', 'ja']) {
  test(`all nearby acceptance labels match current production strings: ${locale}`, () => {
    const c = labels[locale], t = translator(locale)
    assert.equal(c.network, t('addDevice')); assert.equal(c.dialog, t('chooseNetwork'))
    for (const [key, product] of Object.entries(names)) assert.equal(c[key], nearbyText(locale, product))
  })
  test(`nearby controlled edits, read-only values, repeated consent and navigation keep exact role targets: ${locale}`, async () => {
    const { dom, query } = domFixture(), c = labels[locale]
    const previousWindow = globalThis.window, previousDocument = globalThis.document, previousAct = globalThis.IS_REACT_ACT_ENVIRONMENT
    globalThis.window = dom.window; globalThis.document = dom.window.document; globalThis.IS_REACT_ACT_ENVIRONMENT = true
    const root = createRoot(dom.window.document.querySelector('#root'))
    const role = (role, name) => query(`internal:role=${role}[name=${JSON.stringify(name)}s]`)
    const one = (roleName, name) => { const matches = role(roleName, name); assert.equal(matches.length, 1); return matches[0] }
    const field = (name, type, value, extra = {}) => h('label', { key: name }, name, h(type === 'textarea' ? 'textarea' : 'input', { ...(type === 'textarea' ? {} : { type }), value, onChange() {}, ...extra }))
    const render = (value, checked) => act(async () => root.render(h('div', null,
      h('button', null, c.network),
      h('dialog', { open: true, 'aria-label': c.dialog }, h('button', null, c.open), h('button', null, c.back),
        h('label', null, c.alias, h('input', { value: 'Synthetic peer', onChange() {} }), h('small', null, nearbyText(locale, 'aliasHint'))),
        h('label', null, c.scope, h('select', { value: 'synthetic', onChange() {} }, h('option', { value: '' }, nearbyText(locale, 'chooseScope')), h('option', { value: 'synthetic' }, '127.0.0.1 · 127.0.0.0/8'))),
        h('details', { open: true }, h('summary', null, c.ports), field(c.bootstrapPort, 'number', checked ? '48446' : '48445'), field(c.relayPort, 'number', checked ? '48444' : '48443')),
        field(c.local, 'textarea', 'synthetic-contact-local', { readOnly: true }),
        field(c.peer, 'textarea', value), h('button', null, c.import),
        h('div', { role: 'region', 'aria-label': c.review },
          field(c.fingerprint, 'textarea', 'synthetic-full-fingerprint', { readOnly: true }), field(c.otherFingerprint, 'textarea', value),
          ...[c.host, c.consent, c.save, c.verified].map(name => h('label', { key: name }, h('input', { type: name === c.host ? 'radio' : 'checkbox', checked, onChange() {} }), name)),
          h('button', null, c.start), h('button', null, c.approve), h('button', null, nearbyText(locale, 'checkStatus'))),
        h('p', null, c.unavailable), h('p', null, c.trust), h('p', null, c.connected)))))
    try {
      // Actual React updates textarea textContent/defaultValue on controlled
      // edits. Old exact-label lookup works empty, then fails after first fill.
      for (const [value, checked] of [['', false], ['synthetic-wrong', false], ['synthetic-correct', true], ['synthetic-correct', false], ['synthetic-correct', true], ['', false], ['synthetic-reentered', true]]) {
        await render(value, checked)
        for (const name of [c.peer, c.otherFingerprint]) {
          const control = one('textbox', name)
          assert.equal(control.value, value); assert.equal(control.textContent, value)
          assert.equal(query(`internal:label=${JSON.stringify(name)}s`).length, value ? 0 : 1)
        }
        assert.equal(one('textbox', c.local).value, 'synthetic-contact-local')
        assert.equal(one('textbox', c.fingerprint).value, 'synthetic-full-fingerprint')
        assert.equal(query(`internal:role=textbox[name=/^${c.alias}/]`).length, 1)
        assert.equal(one('combobox', c.scope).value, 'synthetic')
        assert.equal(one('spinbutton', c.bootstrapPort).value, checked ? '48446' : '48445')
        assert.equal(one('spinbutton', c.relayPort).value, checked ? '48444' : '48443')
        for (const name of [c.host, c.consent, c.save, c.verified]) assert.equal(one(name === c.host ? 'radio' : 'checkbox', name).checked, checked)
        for (const name of [c.network, c.open, c.back, c.import, c.start, c.approve, nearbyText(locale, 'checkStatus')]) one('button', name)
        one('dialog', c.dialog); one('region', c.review)
        for (const value of [c.ports, c.unavailable, c.trust, c.connected]) assert.equal(query(`internal:text=${JSON.stringify(value)}s`).length, 1)
        assert.equal(role('textbox', 'unrelated').length, 0)
      }
    } finally {
      await act(async () => root.unmount()); dom.window.close()
      globalThis.window = previousWindow; globalThis.document = previousDocument; globalThis.IS_REACT_ACT_ENVIRONMENT = previousAct
    }
  })
}
test('controlled DOM regression stays bound to the production field shapes', () => {
  const ui = readFileSync(new URL('../src/components/NearbySetup.tsx', import.meta.url), 'utf8')
  assert.match(ui, /<label className="field">\{label\}<textarea value=\{value\}/)
  assert.match(ui, /<label className="field">\{label\}<textarea ref=\{field\} rows=\{3\} readOnly value=\{text\}/)
  assert.match(ui, /<label className="field">\{c\('alias'\)\}<input[\s\S]*?<small className="muted">\{c\('aliasHint'\)\}<\/small><\/label>/)
  for (const port of ['bootstrapPort', 'relayPort']) assert.ok(ui.includes(`<label className="field">{c('${port}')}<input type="number"`))
  assert.ok(ui.includes('<input type="radio" name="nearby-host-role" checked={allowHost === true}'))
  assert.ok(ui.includes('<input type="radio" name="nearby-host-role" checked={allowHost === false}'))
  assert.ok(spec.includes("getByRole('radio', { name: c.host, exact: true }).check()"))
  for (const key of ['peer', 'otherFingerprint']) assert.ok(spec.includes(`getByRole('textbox', { name: c.${key}, exact: true })`))
  for (const key of ['bootstrapPort', 'relayPort']) assert.ok(spec.includes(`getByRole('spinbutton', { name: c.${key}, exact: true })`))
})
