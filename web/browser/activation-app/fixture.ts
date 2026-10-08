// Acceptance presentation only; production auth/API/opener are imported intact.
// The backend is explicitly synthetic, never a real account or Core workflow.
import { login, setCSRFToken, command, type Locale } from '../../src/api'
import { openUpgradeHandoff } from '../../src/upgrade-handoff'
import { validUpgradeReview, type UpgradeReview } from '../../src/direct-lan-upgrade'
const element = <T extends HTMLElement>(id: string) => document.getElementById(id) as T
let csrf = '', review: UpgradeReview | undefined, handoff: (() => void) | undefined
const status = (value: string) => { element('status').textContent = value }
element<HTMLFormElement>('login').onsubmit = async event => {
  event.preventDefault()
  const input = element<HTMLInputElement>('code'), code = input.value
  input.value = ''
  try {
    const result = await login(code)
    csrf = result.csrfToken || ''; setCSRFToken(csrf)
    const response = await fetch('/api/state', { cache: 'no-store' })
    const state = await response.json()
    if (!response.ok || state.syntheticOwner !== true) throw Error('fixture')
    element('login').hidden = true; element('workspace').hidden = false
    element('owner').textContent = state.successor ? 'Synthetic successor owner' : 'Synthetic old owner'
    status('Authenticated synthetic owner')
  } catch { status('Private fixture sign-in failed') }
}
element('review').onclick = async () => {
  const deadline = new Date(Date.now() + 45000).toISOString()
  try {
    const result = await command('direct-lan.upgrade.review', { peerId: 'synthetic-web-peer', deadline })
    if (!validUpgradeReview(result.result, 'synthetic-web-peer', deadline)) throw Error('review')
    review = result.result; element('scope').textContent = JSON.stringify(review)
    element<HTMLButtonElement>('apply').disabled = false
  } catch { status('Synthetic review failed') }
}
element('apply').onclick = () => {
  if (!review || handoff) return
  element<HTMLButtonElement>('apply').disabled = true
  const approved = review; review = undefined
  let failed = false
  const cleanup = openUpgradeHandoff(approved, element<HTMLSelectElement>('locale').value as Locale, () => {
    failed = true; handoff = undefined; status('Restart window ended; review again')
  })
  if (!failed) handoff = cleanup
}
element('logout').onclick = async () => {
  await fetch('/api/session', { method: 'DELETE', headers: { 'X-CSRF-Token': csrf } })
  csrf = ''; setCSRFToken(''); status('Signed out')
}
window.addEventListener('pagehide', () => handoff?.())
