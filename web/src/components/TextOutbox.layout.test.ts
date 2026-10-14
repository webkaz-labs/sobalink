import { expect, it } from 'vitest'

const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }
const styles = readFileSync('src/styles.css', 'utf8')
function rule(selector: string) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const matches = [...styles.matchAll(new RegExp(`^${escaped}\\s*\\{([^}]+)\\}`, 'gm'))]
  expect(matches, `One base rule for ${selector}`).toHaveLength(1)
  return Object.fromEntries(matches[0][1].split(';').filter(value => value.includes(':')).map(value => value.split(':').map(part => part.trim())))
}

// Source containment guards only. Actual 320px geometry remains a hosted browser
// assertion, including the conversation still mounted behind the outbox dialog.
it('lets the conversation shrink inside its row flex slot', () => {
  expect(rule('.conversation-slot')).toMatchObject({ display: 'flex', 'min-width': '0' })
  expect(rule('.conversation')).toMatchObject({ 'min-width': '0' })
})
it('reflows attachment controls and the long localized queue-review action', () => {
  expect(rule('.composer-tools')).toMatchObject({ display: 'flex', 'flex-wrap': 'wrap' })
  expect(rule('.send-button')).toMatchObject({ 'min-width': '0', 'max-width': '100%', 'white-space': 'normal', 'overflow-wrap': 'anywhere' })
  expect(rule('.composer-tools')).not.toHaveProperty('overflow', 'hidden')
})
it('wraps delivery status and the original-outbox action without clipping either', () => {
  expect(rule('.message-meta')).toMatchObject({ 'flex-wrap': 'wrap', 'min-width': '0', 'max-width': '100%', 'overflow-wrap': 'anywhere' })
  expect(rule('.message-meta .button')).toMatchObject({ 'min-width': '0', 'max-width': '100%', 'white-space': 'normal', 'overflow-wrap': 'anywhere' })
})
it('retains exact text whitespace and existing wrapping for long bodies, identifiers and card actions', () => {
  expect(rule('.text-queue-body')).toMatchObject({ 'white-space': 'pre-wrap', 'overflow-wrap': 'anywhere' })
  expect(rule('.code-value')).toMatchObject({ 'overflow-wrap': 'anywhere' })
  expect(rule('.outbox-card')).toMatchObject({ 'min-width': '0' })
  expect(rule('.outbox-actions button')).toMatchObject({ 'white-space': 'normal', 'overflow-wrap': 'anywhere', 'min-width': '0' })
})
