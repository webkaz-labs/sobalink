// Literal element roles only. Never return text, attributes, selectors or DOM.
export const LAYOUT_SELECTORS = Object.freeze([
  ['document', 'html'], ['dialog', 'dialog[open]'], ['modal-body', 'dialog[open] .modal-body'],
  ['outbox', '.outbox-view'], ['text-outbox', '.text-outbox-view'], ['card', '.outbox-card'],
  ['review', '.text-queue-review'], ['body', '.text-queue-body'], ['identifier', '.outbox-view .code-value'],
  ['actions', '.outbox-actions'], ['header', '.app-header'], ['navigation', '.primary-nav'],
  ['conversation-slot', '.conversation-slot'], ['conversation', '.conversation'], ['timeline', '.timeline'],
  ['message-meta', '.message-meta'], ['composer-area', '.composer-area'], ['composer', '.composer'],
  ['composer-tools', '.composer-tools'], ['send', '.send-button'], ['mode', '.outbox-mode'], ['preparation', '.outbox-preparation'],
].map(pair => Object.freeze(pair)))
export const LAYOUT_ROLES = Object.freeze(LAYOUT_SELECTORS.map(([role]) => role))
export const LAYOUT_LIMIT = 8192
export function fixedLayout(value) {
  const exact = (object, keys) => object && typeof object === 'object' && !Array.isArray(object) && Object.keys(object).sort().join() === [...keys].sort().join()
  const width = value => Number.isInteger(value) && value >= 0 && value <= LAYOUT_LIMIT
  const fields = ['role', 'clientWidth', 'scrollWidth', 'leftOverflow', 'rightOverflow']
  if (!exact(value, ['viewportWidth', 'regions']) || !width(value.viewportWidth) || !Array.isArray(value.regions) || value.regions.length !== LAYOUT_ROLES.length) throw Error('Invalid fixed layout')
  const regions = value.regions.map((row, index) => {
    if (!exact(row, fields) || row.role !== LAYOUT_ROLES[index] || !fields.slice(1).every(field => width(row[field]))) throw Error('Invalid fixed layout region')
    return { role: row.role, clientWidth: row.clientWidth, scrollWidth: row.scrollWidth, leftOverflow: row.leftOverflow, rightOverflow: row.rightOverflow }
  })
  return { viewportWidth: value.viewportWidth, regions }
}
export async function measureTextLayout(page) {
  return fixedLayout(await page.evaluate(({ selectors, limit }) => {
    const bounded = value => {
      if (!Number.isFinite(value)) throw Error('Invalid layout measurement')
      return Math.min(limit, Math.max(0, Math.ceil(value)))
    }
    return { viewportWidth: bounded(innerWidth), regions: selectors.map(([role, selector]) => {
      const elements = [...document.querySelectorAll(selector)]
      if (elements.length > 64) throw Error('Layout measurement exceeds bound')
      const row = { role, clientWidth: 0, scrollWidth: 0, leftOverflow: 0, rightOverflow: 0 }
      for (const element of elements) {
        const rect = element.getBoundingClientRect()
        row.clientWidth = Math.max(row.clientWidth, bounded(element.clientWidth))
        row.scrollWidth = Math.max(row.scrollWidth, bounded(element.scrollWidth))
        row.leftOverflow = Math.max(row.leftOverflow, bounded(-rect.left))
        row.rightOverflow = Math.max(row.rightOverflow, bounded(rect.right - innerWidth))
      }
      return row
    }) }
  }, { selectors: LAYOUT_SELECTORS, limit: LAYOUT_LIMIT }))
}
