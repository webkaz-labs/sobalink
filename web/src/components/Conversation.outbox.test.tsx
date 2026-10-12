import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { App } from '../App'
import * as api from '../api'
import { fixtureOutbox, fixtureOutboxID } from '../outbox-fixture'
import { outboxText } from '../outbox-i18n'
import { pendingOutboxLocators, retainOutboxLocator, type OutboxRecord } from '../outbox'
const absentID = `ob1_${Date.parse('2026-10-12T10:00:00Z')}_${'b'.repeat(32)}`, retiredID = `ob1_${Date.parse('2026-10-01T10:00:00Z')}_${'c'.repeat(32)}`
const processId = 'a'.repeat(64)
const base: api.State = { resourceCatalogProcessId: processId, csrfToken: 'synthetic', outboxSupported: true, outboxReady: true, outboxPendingCount: 0, self: { name: 'This device', status: 'online', networks: ['tailnet'] }, peers: [
 { id: 'peer-a', name: 'Device A', networks: ['tailnet'], online: false, verified: true, trusted: true, bridge: true, path: 'unknown', outboxEligibility: { allowed: true, reasonCode: 'outbox_capability_unknown' } },
 { id: 'peer-b', name: 'Device B', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct', outboxEligibility: { allowed: false, reasonCode: 'outbox_peer_upgrade_required' } },
], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' } }
function setup(locale: api.Locale = 'en') {
 localStorage.setItem('sobalink.locale', locale)
 let state = structuredClone(base), prepared: OutboxRecord | null = null, items: OutboxRecord[] = [], loseCommit = false
 const requests: { name: string; payload: Record<string, unknown> }[] = []
 const fetch = vi.fn(async (path: string, init?: RequestInit) => {
  if (path === '/api/state') return new Response(JSON.stringify(state))
  const request = JSON.parse(init!.body as string); requests.push(request); let result: unknown
  if (request.name === 'outbox.list') result = { version: 1, kind: 'outbox', status: state.outboxReady ? 'ready' : 'blocked_recovery', reasonCode: '', items }
  else if (request.name === 'outbox.commit') {
   const record: OutboxRecord = { ...prepared!, admission: 'enqueued', processId: undefined, status: 'waiting_for_peer', children: prepared!.children.map(c => ({ ...c, status: 'waiting_for_peer' })) }; items = [record]; state.outboxPendingCount = 1
   if (loseCommit) throw new TypeError('Synthetic response loss'); result = record
  } else if (request.name === 'outbox.status') result = items.find(r => r.requestId === request.payload.requestId) || prepared
  else if (request.name === 'outbox.cancel') { items = items.map(r => ({ ...r, status: 'cancelled', children: r.children.map(c => ({ ...c, status: 'cancelled' })) })); result = items[0] }
  else if (request.name === 'outbox.discard') result = { ...prepared, status: 'discarded', children: prepared!.children.map(c => ({ ...c, status: 'discarded' })) }
  else result = {}
  return new Response(JSON.stringify({ ok: true, result }))
 }); vi.stubGlobal('fetch', fetch)
 const upload = vi.spyOn(api, 'uploadOutbox').mockImplementation(async (peers, process, expiresAt, _selection, id) => {
  prepared = { ...fixtureOutbox(), outboxId: id, requestId: id, processId: process, peerIds: peers, createdAt: new Date().toISOString(), expiresAt, updatedAt: new Date().toISOString() }; return { ok: true, result: { ...prepared } }
 })
 return { requests, upload, fetch, prepared: () => prepared!, setState: (next: api.State) => { state = next }, setItems: (next: OutboxRecord[]) => { items = next }, loseCommit: () => { loseCommit = true } }
}
async function openPeer(locale: api.Locale) { await userEvent.click(await screen.findByRole('button', { name: /Device A/ })); await userEvent.click(screen.getByRole('button', { name: locale === 'ja' ? 'ファイルとメッセージ' : 'Files & messages' })) }
async function chooseQueue(container: HTMLElement, locale: api.Locale) {
 await userEvent.click(screen.getByRole('radio', { name: outboxText(locale, 'queue') })); fireEvent.change(container.querySelector('input[type=file]:not([webkitdirectory])')!, { target: { files: [new File(['abc'], 'sample.txt')] } }); await screen.findByRole('button', { name: outboxText(locale, 'review') })
}
beforeEach(() => { sessionStorage.clear(); history.replaceState({}, '') })
it.each(['en', 'ja'] as const)('offline queue requires staged review and preserves composer gating in %s', async locale => {
 const h = setup(locale), immediate = vi.spyOn(api, 'upload'), { container } = render(<App />); await openPeer(locale)
 expect(container.querySelector('textarea')).toBeDisabled(); expect(screen.getByRole('radio', { name: outboxText(locale, 'immediate') })).toBeChecked()
 await chooseQueue(container, locale); expect(container.querySelector('textarea')).toBeDisabled()
 const prepare = screen.getByRole('button', { name: outboxText(locale, 'review') }); fireEvent.click(prepare); fireEvent.click(prepare)
 const commit = await screen.findByRole('button', { name: new RegExp(outboxText(locale, 'queueUntil')) })
 expect(h.upload).toHaveBeenCalledTimes(1); expect(h.requests.some(r => r.name === 'outbox.commit')).toBe(false); expect(screen.getByText(outboxText(locale, 'consent'))).toBeVisible()
 fireEvent.click(commit); fireEvent.click(commit); await screen.findByText(outboxText(locale, 'localOnly'))
 expect(h.requests.filter(r => r.name === 'outbox.commit')).toHaveLength(1); expect(immediate).not.toHaveBeenCalled()
})
it('recovers lost commit from the original locator after reload without another send', async () => {
 const h = setup(); h.loseCommit(); const view = render(<App />); await openPeer('en'); await chooseQueue(view.container, 'en')
 await userEvent.click(screen.getByRole('button', { name: 'Prepare queue review' })); await userEvent.click(await screen.findByRole('button', { name: /Queue until/ })); await screen.findByText(outboxText('en', 'uncertain'))
 const id = h.prepared().requestId; expect(pendingOutboxLocators()).toContain(id); view.unmount(); render(<App />)
 await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByText(outboxText('en', 'localOnly'))
 expect(pendingOutboxLocators()).not.toContain(id); expect(h.upload).toHaveBeenCalledTimes(1); expect(h.requests.filter(r => r.name === 'outbox.commit')).toHaveLength(1)
})
it('looks up original missing admission and prevents blind replacement', async () => {
 const h = setup(); retainOutboxLocator(absentID); const view = render(<App />); await openPeer('en'); await chooseQueue(view.container, 'en')
 expect(screen.getByRole('button', { name: 'Prepare queue review' })).toBeDisabled(); await userEvent.click(screen.getByRole('button', { name: /^Outbox/ })); await screen.findByText(absentID)
 expect(h.requests.find(r => r.name === 'outbox.status')?.payload).toEqual({ requestId: absentID }); expect(h.upload).not.toHaveBeenCalled()
})
it('survives process change and preserves partial evidence through cancel and Back', async () => {
 const h = setup(), r = fixtureOutbox(); r.admission = 'enqueued'; delete r.processId; r.status = 'blocked_recovery'; r.deliveryOutcome = 'partial'; r.children[0].status = 'blocked_recovery'; r.children[0].deliveryOutcome = 'partial'
 h.setItems([r]); h.setState({ ...base, resourceCatalogProcessId: 'd'.repeat(64) }); render(<App />)
 const open = await screen.findByRole('button', { name: /^Outbox/ }); await userEvent.click(open); await screen.findByText(/Work status: Delivery needs review/); expect(screen.getByText(outboxText('en', 'stopHint'))).toBeVisible()
 await userEvent.click(screen.getByRole('button', { name: 'Stop further sends' })); await screen.findByText(/Further sends stopped · Delivery evidence: Partly saved/)
 expect(h.requests.find(r => r.name === 'outbox.cancel')?.payload).toEqual({ outboxId: fixtureOutboxID })
 await act(async () => { history.replaceState({}, ''); window.dispatchEvent(new PopStateEvent('popstate')) }); await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument()); expect(open).toHaveFocus()
})
it('known unsupported peer cannot queue but ordinary online composer remains enabled', async () => {
 setup(); const view = render(<App />); await userEvent.click(await screen.findByRole('button', { name: /Device B/ })); await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
 expect(screen.getByRole('radio', { name: 'Queue for later' })).toBeDisabled(); expect(screen.getByText(outboxText('en', 'unsupported'))).toBeVisible(); expect(view.container.querySelector('textarea')).toBeEnabled()
})
it('keeps a prepared review discoverable after browser reload without granting delivery', async () => {
 const h = setup(), view = render(<App />); await openPeer('en'); await chooseQueue(view.container, 'en'); await userEvent.click(screen.getByRole('button', { name: 'Prepare queue review' })); await screen.findByRole('button', { name: /Queue until/ })
 const id = h.prepared().requestId; expect(pendingOutboxLocators()).toContain(id); view.unmount(); render(<App />)
 await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByRole('button', { name: /Queue until/ })
 expect(h.requests.some(r => r.name === 'outbox.commit')).toBe(false); expect(pendingOutboxLocators()).toContain(id)
})
it('allows a new explicit review only after authoritative absent status, never for a retired key', async () => {
 const h = setup(), real = h.fetch.getMockImplementation()!; retainOutboxLocator(absentID); retainOutboxLocator(retiredID)
 h.fetch.mockImplementation(async (path, init) => {
  if (init?.body) { const request = JSON.parse(init.body as string); if (request.name === 'outbox.status') return new Response(JSON.stringify({ ok: false, code: request.payload.requestId === absentID ? 'outbox_not_found' : 'outbox_retired' }), { status: 400 }) }
  return real(path, init)
 })
 render(<App />); await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByText(outboxText('en', 'absent'))
 expect(pendingOutboxLocators()).toEqual([retiredID]); expect(h.upload).not.toHaveBeenCalled()
})
it('allocates durable identity only at explicit preparation after a draft has aged', async () => {
 const h = setup(), now = Date.now(), clock = vi.spyOn(Date, 'now').mockReturnValue(now - 10 * 60000)
 const view = render(<App />); await openPeer('en'); await chooseQueue(view.container, 'en')
 expect(h.upload).not.toHaveBeenCalled(); expect(pendingOutboxLocators()).toEqual([])
 clock.mockReturnValue(now)
 await userEvent.click(screen.getByRole('button', { name: 'Prepare queue review' })); await screen.findByRole('button', { name: /Queue until/ })
 expect(h.prepared().requestId).toMatch(new RegExp(`^ob1_${now}_[a-f0-9]{32}$`))
})
it('keeps the exact original canonical key when status is checked after the admission window', async () => {
 const h = setup(); h.loseCommit(); const view = render(<App />); await openPeer('en'); await chooseQueue(view.container, 'en')
 await userEvent.click(screen.getByRole('button', { name: 'Prepare queue review' })); await userEvent.click(await screen.findByRole('button', { name: /Queue until/ })); await screen.findByText(outboxText('en', 'uncertain'))
 const original = h.prepared().requestId
 vi.spyOn(Date, 'now').mockReturnValue(Date.now() + 9 * 86400000)
 await userEvent.click(screen.getByRole('button', { name: 'Check original request' })); await screen.findByText(outboxText('en', 'localOnly'))
 expect(h.requests.find(r => r.name === 'outbox.status')?.payload).toEqual({ requestId: original }); expect(h.upload).toHaveBeenCalledTimes(1)
})
