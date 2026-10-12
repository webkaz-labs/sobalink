import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { App } from '../App'
import * as api from '../api'
import { transferEnglish, transferJapanese } from '../transfer-i18n'

const processId = 'a'.repeat(64)
const state: api.State = { resourceCatalogProcessId: processId, csrfToken: 'synthetic-csrf', self: { name: 'This device', status: 'online', networks: ['tailnet'] }, peers: [
  { id: 'peer-a', name: 'Device A', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
  { id: 'peer-b', name: 'Device B', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
  { id: 'peer-c', name: 'Device C', networks: ['tailnet'], online: false, verified: true, trusted: true, bridge: true, path: 'unknown' },
], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' } }
function setup(locale: api.Locale = 'en') {
  localStorage.setItem('sobalink.locale', locale)
  let current = structuredClone(state)
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  let id = ''
  const prepared = () => ({ version: 1, processId, preparationId: id, peerIds: ['peer-a', 'peer-b'], status: 'prepared', reviewRevision: 'b'.repeat(64), entries: [{ id: '1', path: 'sample.txt', kind: 'file', size: 3, sha256: 'c'.repeat(64) }], totalBytes: 3 })
  const committed = () => ({ version: 1, processId, preparationId: id, dispatchId: id, status: 'committed', children: [{ peerId: 'peer-a', transferId: 'child-a' }, { peerId: 'peer-b', transferId: 'child-b' }] })
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(current))
    const request = JSON.parse(init!.body as string); requests.push(request)
    const result = request.name === 'transfer.dispatch.commit' || request.name === 'transfer.dispatch.status' ? committed() : { version: 1, processId, preparationId: id, status: 'discarded' }
    return new Response(JSON.stringify({ ok: true, result }))
  })
  vi.stubGlobal('fetch', fetch)
  const upload = vi.spyOn(api, 'uploadDispatch').mockImplementation(async (_peers, _process, _selection, request) => { id = request; return { ok: true, result: prepared() } })
  return { requests, fetch, upload, prepared, committed, setState: (next: api.State) => { current = next }, setId: (next: string) => { id = next } }
}
async function openPeer(name: string, locale: api.Locale = 'en') {
  await userEvent.click(await screen.findByRole('button', { name: new RegExp(name) }))
  await userEvent.click(screen.getByRole('button', { name: locale === 'ja' ? 'ファイルとメッセージ' : 'Files & messages' }))
}
async function selectBatch(container: HTMLElement, locale: api.Locale = 'en') {
  const file = new File(['abc'], 'sample.txt')
  fireEvent.change(container.querySelector('input[type="file"]:not([webkitdirectory])')!, { target: { files: [file] } })
  const text = locale === 'ja' ? transferJapanese : transferEnglish
  const recipients = await screen.findByRole('group', { name: text.recipients })
  expect(within(recipients).getByRole('checkbox', { name: /Device A/ })).toBeChecked()
  expect(within(recipients).getByRole('checkbox', { name: /Device B/ })).not.toBeChecked()
  expect(within(recipients).queryByRole('checkbox', { name: /Device C/ })).not.toBeInTheDocument()
  await userEvent.click(within(recipients).getByRole('checkbox', { name: /Device B/ }))
  return file
}
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en') })
it.each(['en', 'ja'] as const)('prepares once and commits the exact checked recipients within the existing review in %s', async locale => {
  const h = setup(locale), oldUpload = vi.spyOn(api, 'upload')
  const { container } = render(<App />); await openPeer('Device A', locale)
  const file = await selectBatch(container, locale)
  const send = screen.getByRole('button', { name: locale === 'ja' ? 'まとめて送信' : 'Send batch' })
  fireEvent.click(send); fireEvent.click(send)
  await waitFor(() => expect(h.requests.filter(request => request.name === 'transfer.dispatch.commit')).toHaveLength(1))
  expect(h.upload).toHaveBeenCalledTimes(1); expect(oldUpload).not.toHaveBeenCalled()
  expect(h.upload.mock.calls[0].slice(0, 3)).toEqual([['peer-a', 'peer-b'], processId, { entries: [{ path: 'sample.txt', kind: 'file', size: 3 }], files: [{ path: 'sample.txt', file }] }])
  expect(h.requests.find(request => request.name === 'transfer.dispatch.commit')!.payload).toEqual({ processId, preparationId: h.upload.mock.calls[0][3], reviewRevision: 'b'.repeat(64), confirm: true })
  const text = locale === 'ja' ? transferJapanese : transferEnglish
  await screen.findByRole('region', { name: text.deliverySummary })
  expect(screen.queryByRole('region', { name: locale === 'ja' ? '確認' : 'Review' })).not.toBeInTheDocument()
})
it('retains the original request after a lost commit response and resolves it without preparing or sending again', async () => {
  const h = setup(), real = h.fetch.getMockImplementation()!
  h.fetch.mockImplementation(async (path, init) => {
    if (init?.body && JSON.parse(init.body as string).name === 'transfer.dispatch.commit') { h.requests.push(JSON.parse(init.body as string)); throw new TypeError('Synthetic lost response') }
    return real(path, init)
  })
  const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  await screen.findByText(transferEnglish.dispatchUnknown)
  expect(screen.getByRole('button', { name: 'Send batch' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: transferEnglish.checkDispatch }))
  await screen.findByRole('region', { name: transferEnglish.deliverySummary })
  expect(h.upload).toHaveBeenCalledTimes(1)
  expect(h.requests.filter(request => request.name === 'transfer.dispatch.commit')).toHaveLength(1)
  expect(h.requests.filter(request => request.name === 'transfer.dispatch.status')[0].payload).toEqual({ processId, preparationId: h.upload.mock.calls[0][3] })
  expect(h.requests.some(request => request.name === 'transfer.dispatch.discard')).toBe(false)
})
it('does not commit a delayed preparation after switching away and back', async () => {
  const h = setup(); let finish!: () => void
  h.upload.mockImplementation((_peers, _process, _selection, request) => { h.setId(request); return new Promise(resolve => { finish = () => resolve({ ok: true, result: h.prepared() }) }) })
  const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  await openPeer('Device B'); await openPeer('Device A')
  await act(async () => finish())
  await waitFor(() => expect(h.requests.some(request => request.name === 'transfer.dispatch.discard')).toBe(true))
  expect(h.requests.some(request => request.name === 'transfer.dispatch.commit')).toBe(false)
  expect(h.upload.mock.calls[0][5].aborted).toBe(true)
})
it('keeps an unavailable selected recipient visible and prevents silently sending only to the other one', async () => {
  const h = setup(); const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  h.setState({ ...state, peers: state.peers.map(peer => peer.id === 'peer-b' ? { ...peer, online: false } : peer) })
  await act(async () => document.dispatchEvent(new Event('visibilitychange')))
  await screen.findByText(transferEnglish.recipientUnavailable)
  expect(screen.getByRole('checkbox', { name: /Device B/ })).toBeChecked()
  expect(screen.getByRole('button', { name: 'Send batch' })).toBeDisabled()
  expect(h.upload).not.toHaveBeenCalled()
})
it('rejects a mismatched preparation without requesting delivery', async () => {
  const h = setup()
  h.upload.mockImplementation(async (_p, _g, _s, id) => { h.setId(id); return { ok: true, result: { ...h.prepared(), peerIds: ['peer-a', 'peer-c'] } } })
  const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  await screen.findByText(transferEnglish.prepareUnknown)
  expect(h.requests.some(request => request.name === 'transfer.dispatch.commit')).toBe(false)
})
it('allows explicitly closing a rejected preparation without creating another send', async () => {
  const h = setup()
  h.upload.mockRejectedValue(new api.ApiError('transfer_dispatch_invalid', 'Synthetic rejection'))
  const real = h.fetch.getMockImplementation()!
  h.fetch.mockImplementation(async (path, init) => init?.body && JSON.parse(init.body as string).name === 'transfer.dispatch.status'
    ? new Response(JSON.stringify({ code: 'transfer_dispatch_stale' }), { status: 409 }) : real(path, init))
  const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  await screen.findByText(transferEnglish.prepareUnknown)
  await userEvent.click(screen.getByRole('button', { name: transferEnglish.checkDispatch }))
  await waitFor(() => expect(screen.getByRole('button', { name: transferEnglish.abandonDraft })).toBeEnabled())
  await userEvent.click(screen.getByRole('button', { name: transferEnglish.abandonDraft }))
  expect(screen.queryByText('sample.txt')).not.toBeInTheDocument()
  expect(h.upload).toHaveBeenCalledTimes(1)
  expect(h.requests.some(request => request.name === 'transfer.dispatch.commit')).toBe(false)
})
it('requires an explicit outcome review before closing an uncertain committed request after restart', async () => {
  const h = setup(), real = h.fetch.getMockImplementation()!
  h.fetch.mockImplementation(async (path, init) => {
    if (init?.body && JSON.parse(init.body as string).name === 'transfer.dispatch.commit') { h.requests.push(JSON.parse(init.body as string)); throw new TypeError('Synthetic lost response') }
    return real(path, init)
  })
  const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  await screen.findByText(transferEnglish.dispatchUnknown)
  h.setState({ ...state, resourceCatalogProcessId: 'd'.repeat(64) })
  await act(async () => document.dispatchEvent(new Event('visibilitychange')))
  await screen.findByText(transferEnglish.dispatchExpired)
  expect(screen.getByRole('button', { name: transferEnglish.checkDispatch })).toBeDisabled()
  expect(screen.getByRole('button', { name: transferEnglish.abandonDraft })).toBeDisabled()
  await userEvent.click(screen.getByRole('checkbox', { name: transferEnglish.acknowledgeUnknown }))
  await userEvent.click(screen.getByRole('button', { name: transferEnglish.abandonDraft }))
  expect(screen.queryByText('sample.txt')).not.toBeInTheDocument()
  expect(h.upload).toHaveBeenCalledTimes(1)
  expect(h.requests.filter(request => request.name === 'transfer.dispatch.commit')).toHaveLength(1)
  expect(h.requests.some(request => request.name === 'transfer.dispatch.discard')).toBe(false)
})
it('reuses existing retry only for the failed recipient and keeps completed recipients untouched', async () => {
  const h = setup(); const { container } = render(<App />); await openPeer('Device A'); await selectBatch(container)
  const transfer = (peerId: string, id: string, status: api.Transfer['status']): api.Transfer => ({ id, peerId, direction: 'outgoing', name: 'Synthetic batch', status, entries: [], totalBytes: 3, completedBytes: status === 'completed' ? 3 : 0, retryable: status === 'failed', createdAt: '2026-01-01T00:00:00Z' })
  h.setState({ ...state, transfers: [transfer('peer-a', 'child-a', 'completed'), transfer('peer-b', 'child-b', 'failed')] })
  await userEvent.click(screen.getByRole('button', { name: 'Send batch' }))
  const summary = await screen.findByRole('region', { name: transferEnglish.deliverySummary })
  await waitFor(() => expect(within(summary).getByRole('checkbox', { name: /Select for retry/ })).toBeEnabled())
  await userEvent.click(within(summary).getByRole('checkbox', { name: /Select for retry/ }))
  await userEvent.click(within(summary).getByRole('button', { name: /Retry selected failed transfers/ }))
  await waitFor(() => expect(h.requests.filter(request => request.name === 'transfer.retry')).toHaveLength(1))
  expect(h.requests.find(request => request.name === 'transfer.retry')!.payload).toEqual({ transferId: 'child-b', expected: { processId, peerId: 'peer-b', direction: 'outgoing' } })
  expect(h.upload).toHaveBeenCalledTimes(1)
})
