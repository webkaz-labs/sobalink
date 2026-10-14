import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { App } from '../App'
import * as api from '../api'
import { pendingTextLocators, retainTextLocator, textDigest, type TextOutboxRecord } from '../text-outbox'
import { textQueueText } from '../text-outbox-i18n'
const processId = 'a'.repeat(64)
const base: api.State = { csrfToken: 'synthetic', resourceCatalogProcessId: processId, self: { name: 'This device', status: 'online', networks: ['tailnet'] }, peers: [
  { id: 'peer-a', name: 'Device A', networks: ['tailnet'], online: false, verified: true, trusted: true, bridge: true, path: 'unknown' },
  { id: 'peer-b', name: 'Device B', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' } }
type Request = { name: string; requestId: string; payload: Record<string, any> }
function setup(locale: api.Locale = 'en') {
  localStorage.setItem('sobalink.locale', locale)
  let state = structuredClone(base), lose = '', fail = '', bad = '', rows: TextOutboxRecord[] = []
  const requests: Request[] = []
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const request: Request = JSON.parse(init!.body as string); requests.push(request); const { payload, name } = request
    if (fail === name) { fail = ''; return new Response(JSON.stringify({ code: 'storage_busy' }), { status: 409 }) }
    let result: unknown = {}
    if (name === 'message.queue.prepare') {
      let row = rows.find(r => r.requestId === request.requestId)
      if (!row) { row = { version: 1, kind: 'text_outbox', outboxId: request.requestId, requestId: request.requestId, messageId: request.requestId, peerId: payload.peerId, processId: payload.processId, reviewRevision: 'b'.repeat(64), admission: 'prepared', status: 'prepared', deliveryOutcome: 'not_attempted', reasonCode: '', text: payload.text, textBytes: api.messageByteLength(payload.text), sha256: await textDigest(payload.text), createdAt: new Date(Number(request.requestId.slice(4, 17))).toISOString(), expiresAt: payload.expiresAt, updatedAt: new Date().toISOString() }; rows.push(row) }
      result = { ...row, ...(bad ? { [bad]: bad === 'text' ? 'changed' : 'peer-other' } : {}) }
    } else if (name === 'message.queue.commit') {
      const row = rows.find(r => r.requestId === payload.outboxId)!; delete row.processId; row.admission = 'enqueued'; row.status = 'waiting_peer'; result = { ...row }
      state.messages = [...state.messages.filter(m => m.id !== row.requestId), { id: row.requestId, peerId: row.peerId, direction: 'outgoing', text: row.text!, createdAt: row.createdAt, status: row.status }]
    } else if (name === 'message.queue.status') {
      result = rows.find(r => r.requestId === payload.outboxId)
      if (!result) return new Response(JSON.stringify({ code: 'message_not_found' }), { status: 404 })
    } else if (name === 'message.queue.discard') {
      const row = rows.find(r => r.requestId === payload.outboxId)!
      if (row.admission === 'enqueued') return new Response(JSON.stringify({ code: 'message_already_enqueued' }), { status: 409 })
      row.status = 'discarded'; delete row.text; result = { ...row }
    } else if (name === 'message.queue.cancel') {
      const row = rows.find(r => r.requestId === payload.outboxId)!; row.status = 'cancelled'; result = { ...row }
    } else if (name === 'message.queue.list') result = { version: 1, kind: 'text_outbox', status: 'ready', reasonCode: '', revision: 'c'.repeat(64), total: rows.length, items: [...rows].sort((a, b) => a.requestId.localeCompare(b.requestId)).map(({ text: _text, ...row }) => ({ ...row, id: row.requestId })) }
    else if (name === 'outbox.list') result = { version: 1, kind: 'outbox', status: 'ready', reasonCode: '', items: [] }
    if (lose === name) { lose = ''; throw new TypeError('Synthetic lost reply') }
    return new Response(JSON.stringify({ ok: true, result }))
  }); vi.stubGlobal('fetch', fetch)
  return { requests, fetch, rows: () => rows, setRows: (value: TextOutboxRecord[]) => { rows = value }, state: () => state, setState: (value: api.State) => { state = value }, lose: (name: string) => { lose = name }, fail: (name: string) => { fail = name }, bad: (field: string) => { bad = field } }
}
async function openPeer(locale: api.Locale = 'en', name = 'Device A') { await userEvent.click(await screen.findByRole('button', { name: new RegExp(name) })); await userEvent.click(screen.getByRole('button', { name: locale === 'en' ? 'Files & messages' : 'ファイルとメッセージ' })) }
async function enterQueue(locale: api.Locale = 'en', text = 'Synthetic next check: https://example.invalid/note') {
  await userEvent.click(screen.getByRole('radio', { name: textQueueText(locale, 'queue') }))
  const input = screen.getByRole('textbox', { name: locale === 'en' ? 'Write a message…' : 'メッセージを入力…' }); fireEvent.change(input, { target: { value: text } }); return input
}
async function prepare(locale: api.Locale = 'en') { await userEvent.click(screen.getByRole('button', { name: textQueueText(locale, 'review') })); return await screen.findByRole('button', { name: textQueueText(locale, 'commit') }) }
async function commit(locale: api.Locale = 'en') { await userEvent.click(screen.getByRole('checkbox', { name: textQueueText(locale, 'consent') })); await userEvent.click(screen.getByRole('button', { name: textQueueText(locale, 'commit') })) }
beforeEach(() => { sessionStorage.clear(); history.replaceState({}, '') })
it.each(['en', 'ja'] as const)('explicit offline queue uses the existing composer and reviewed consent in %s', async locale => {
  const h = setup(locale); render(<App />); await openPeer(locale)
  const input = screen.getByRole('textbox', { name: locale === 'en' ? 'Write a message…' : 'メッセージを入力…' }); expect(input).toBeDisabled()
  await enterQueue(locale); expect(input).toBeEnabled(); expect(screen.getByRole('combobox', { name: textQueueText(locale, 'deadline') })).toHaveValue('24')
  const review = screen.getByRole('button', { name: textQueueText(locale, 'review') }); fireEvent.click(review); fireEvent.click(review)
  const send = await screen.findByRole('button', { name: textQueueText(locale, 'commit') }); expect(send).toBeDisabled(); expect(input).toBeDisabled()
  expect(screen.getByText(locale === 'en' ? 'Exact message' : '送信する本文')).toBeVisible()
  expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); expect(h.requests.some(r => r.name === 'message.queue.commit')).toBe(false)
  const row = h.rows()[0]; expect(Date.parse(row.expiresAt) - Date.parse(row.createdAt)).toBe(86400000); expect(row.peerId).toBe('peer-a')
  await userEvent.click(screen.getByRole('checkbox')); fireEvent.click(send); fireEvent.click(send)
  await screen.findByText(locale === 'en' ? 'Accepted into the local queue. This does not confirm that the receiving device saved the text.' : 'ローカルの送信待ちに登録しました。相手側での本文保存はまだ確認されていません。')
  expect(input).toHaveValue(''); expect(h.requests.filter(r => r.name === 'message.queue.commit')).toHaveLength(1); expect(h.requests.some(r => r.name === 'message.send')).toBe(false)
  expect(screen.getByText(locale === 'en' ? 'Waiting for the selected device' : '選んだ相手との接続待ち')).toBeVisible()
})
it('lost prepare/status replies retain the fixed request without another preparation', async () => {
  const h = setup(); h.lose('message.queue.prepare'); render(<App />); await openPeer(); const input = await enterQueue(); await userEvent.click(screen.getByRole('button', { name: 'Review queued text' })); await screen.findByText(textQueueText('en', 'uncertain'))
  const original = h.rows()[0]; expect(input).toHaveValue(original.text); expect(pendingTextLocators()).toEqual([original.requestId])
  h.lose('message.queue.status'); await userEvent.click(screen.getByRole('button', { name: 'Check original text request' })); await waitFor(() => expect(screen.getByRole('button', { name: 'Check original text request' })).toBeEnabled())
  await userEvent.click(screen.getByRole('button', { name: 'Check original text request' })); await screen.findByRole('button', { name: 'Queue this text' }); expect(screen.getByRole('checkbox')).not.toBeChecked()
  expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); expect(h.requests.filter(r => r.name === 'message.queue.status').map(r => r.payload)).toEqual([{ outboxId: original.requestId }, { outboxId: original.requestId }])
  await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
})
it('lost commit survives reload using the original locator and never commits twice', async () => {
  const h = setup(), view = render(<App />); await openPeer(); await enterQueue(); await prepare(); h.lose('message.queue.commit'); await commit(); await screen.findByText(textQueueText('en', 'uncertain'))
  const id = h.rows()[0].requestId; expect(pendingTextLocators()).toContain(id); view.unmount(); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByText(textQueueText('en', 'localOnly'))
  expect(pendingTextLocators()).not.toContain(id); expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); expect(h.requests.filter(r => r.name === 'message.queue.commit')).toHaveLength(1)
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument()
})
it('storage busy keeps the body, key and absolute deadline for explicit original retry', async () => {
  const h = setup(); h.fail('message.queue.prepare'); render(<App />); await openPeer(); const input = await enterQueue(); await userEvent.click(screen.getByRole('button', { name: 'Review queued text' }))
  await screen.findByText(/Local storage is busy/); expect(input).toHaveValue('Synthetic next check: https://example.invalid/note')
  const first = h.requests.find(r => r.name === 'message.queue.prepare')!; await userEvent.click(screen.getByRole('button', { name: 'Retry the same preparation' })); await screen.findByRole('button', { name: 'Queue this text' })
  expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toEqual([first, first]); expect(h.requests.some(r => r.name === 'message.queue.commit')).toBe(false)
  h.fail('message.queue.commit'); await commit(); await screen.findByText(/Local storage is busy/); expect(input).toHaveValue(first.payload.text)
  await userEvent.click(screen.getByRole('button', { name: 'Check original text request' })); await screen.findByRole('checkbox'); await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
  const commits = h.requests.filter(r => r.name === 'message.queue.commit'); expect(commits).toHaveLength(2); expect(commits[0].payload).toEqual(commits[1].payload)
})
it('discard enables editing and repeat flow allocates a fresh key only after a new explicit review', async () => {
  const h = setup(); render(<App />); await openPeer(); const input = await enterQueue(); await prepare(); const first = h.rows()[0].requestId
  await userEvent.click(screen.getByRole('button', { name: 'Discard this preparation' })); await waitFor(() => expect(input).toBeEnabled()); expect(input).toHaveValue('Synthetic next check: https://example.invalid/note')
  fireEvent.change(input, { target: { value: 'Synthetic revised note' } }); expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); await prepare(); expect(h.rows()[1].requestId).not.toBe(first); await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
})
it('cancel preserves unknown delivery evidence and history cleanup is not presented as recall', async () => {
  const h = setup(); render(<App />); await openPeer(); await enterQueue(); await prepare(); await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
  const row = h.rows()[0]; row.status = 'needs_review'; row.deliveryOutcome = 'unknown'; h.setState({ ...h.state(), messages: [] })
  await userEvent.click(screen.getByRole('button', { name: /^Outbox/ })); const section = await screen.findByRole('region', { name: 'Queued text' })
  expect(within(section).getByText('Stopping prevents further sends. It cannot recall text already saved by the receiving device.')).toBeVisible(); expect(within(section).getByText(/Clearing visible message history does not erase/)).toBeVisible()
  await userEvent.click(within(section).getByRole('button', { name: 'Stop further text sends' })); await within(section).findByText('Work status: Further sends stopped · Delivery evidence: Unconfirmed')
  expect(h.requests.find(r => r.name === 'message.queue.cancel')!.payload).toEqual({ outboxId: row.requestId }); expect(h.requests.some(r => /cleanup|forget/.test(r.name))).toBe(false)
  await act(async () => { history.replaceState({}, ''); window.dispatchEvent(new PopStateEvent('popstate')) }); await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
})
it('a process change cannot commit a stale review or silently prepare again', async () => {
  const h = setup(), view = render(<App />); await openPeer(); await enterQueue(); await prepare(); const original = h.rows()[0].requestId
  view.unmount(); h.setState({ ...h.state(), resourceCatalogProcessId: 'd'.repeat(64) }); render(<App />); await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByText(textQueueText('en', 'expiredReview'))
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument(); expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); expect(pendingTextLocators()).toContain(original)
})
it.each(['peerId', 'text', 'messageId'])('malformed %s response cannot authorize delivery', async field => {
  const h = setup(); h.bad(field); render(<App />); await openPeer(); await enterQueue(); await userEvent.click(screen.getByRole('button', { name: 'Review queued text' })); await screen.findByText(textQueueText('en', 'message_invalid_response'))
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument(); expect(h.requests.some(r => r.name === 'message.queue.commit')).toBe(false); expect(pendingTextLocators()).toEqual([h.rows()[0].requestId])
})
it('missing preparation requires an explicit fresh review', async () => {
  const h = setup(), id = `mt1_${Date.now()}_${'e'.repeat(32)}`; retainTextLocator(id); render(<App />); await openPeer(); await enterQueue(); expect(screen.getByRole('button', { name: 'Review queued text' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: /^Outbox/ })); await screen.findByText(textQueueText('en', 'absent')); expect(pendingTextLocators()).toContain(id)
  await userEvent.click(screen.getByRole('button', { name: 'Start a new text review' })); expect(pendingTextLocators()).not.toContain(id); expect(h.requests.some(r => r.name === 'message.queue.prepare')).toBe(false)
})
it('keeps immediate send explicit and blocks mixed identity queue controls', async () => {
  const h = setup(); h.setState({ ...h.state(), settings: { network: 'mixed' } }); render(<App />); await openPeer('en', 'Device B')
  expect(screen.getByRole('radio', { name: 'Queue text' })).toBeDisabled(); expect(screen.getByRole('radio', { name: 'Send text now' })).toBeChecked(); const input = screen.getByRole('textbox'); expect(input).toBeEnabled()
  fireEvent.change(input, { target: { value: 'Synthetic immediate message' } }); await userEvent.click(screen.getByRole('button', { name: 'Send message' })); await waitFor(() => expect(h.requests.some(r => r.name === 'message.send')).toBe(true)); expect(h.requests.some(r => r.name.startsWith('message.queue.'))).toBe(false)
})

it('restores a process-local preparation after reload but requires inspecting the body and new consent', async () => {
  const h = setup(), view = render(<App />); await openPeer(); await enterQueue(); await prepare(); const row = h.rows()[0]; view.unmount(); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByRole('button', { name: 'Inspect original text' })
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument(); expect(h.requests.some(r => r.name === 'message.queue.commit')).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'Inspect original text' })); const send = await screen.findByRole('button', { name: 'Queue this text' }); expect(send).toBeDisabled(); expect(screen.getByText(row.text!)).toBeVisible()
  await userEvent.click(screen.getByRole('checkbox', { name: textQueueText('en', 'consent') })); await userEvent.click(send); await screen.findByText(textQueueText('en', 'localOnly'))
  expect(h.requests.filter(r => r.name === 'message.queue.commit')).toHaveLength(1)
})
it('navigating away from an ambiguous preparation keeps its original peer and text', async () => {
  const h = setup(); h.lose('message.queue.prepare'); render(<App />); await openPeer(); await enterQueue(); await userEvent.click(screen.getByRole('button', { name: 'Review queued text' })); await screen.findByText(textQueueText('en', 'uncertain'))
  const row = h.rows()[0]; await openPeer('en', 'Device B'); await openPeer(); expect(screen.getByRole('textbox')).toHaveValue(row.text)
  await userEvent.click(screen.getByRole('button', { name: 'Check original text request' })); await screen.findByRole('button', { name: 'Queue this text' })
  expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1); expect(h.requests.find(r => r.name === 'message.queue.status')?.payload).toEqual({ outboxId: row.requestId })
})
it('a lost cancellation reply is reconciled using the original request without claiming non-delivery', async () => {
  const h = setup(); render(<App />); await openPeer(); await enterQueue(); await prepare(); await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
  const row = h.rows()[0]; row.status = 'needs_review'; row.deliveryOutcome = 'unknown'; await userEvent.click(screen.getByRole('button', { name: /^Outbox/ }))
  const section = await screen.findByRole('region', { name: 'Queued text' }); h.lose('message.queue.cancel'); await userEvent.click(within(section).getByRole('button', { name: 'Stop further text sends' })); await within(section).findByText(textQueueText('en', 'uncertain'))
  expect(pendingTextLocators()).toContain(row.requestId); await userEvent.click(within(section).getByRole('button', { name: 'Inspect original text' })); await within(section).findByText('Work status: Further sends stopped · Delivery evidence: Unconfirmed')
  expect(h.requests.filter(r => r.name === 'message.queue.cancel')).toHaveLength(1); expect(pendingTextLocators()).not.toContain(row.requestId)
})
it('a retired locator never enables a replacement review', async () => {
  const h = setup(), id = `mt1_${Date.now() - 9 * 86400000}_${'e'.repeat(32)}`, original = h.fetch.getMockImplementation()!; retainTextLocator(id)
  h.fetch.mockImplementation(async (path, init) => { if (init?.body && JSON.parse(init.body as string).name === 'message.queue.status') return new Response(JSON.stringify({ code: 'message_retired' }), { status: 409 }); return original(path, init) })
  render(<App />); await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByText(textQueueText('en', 'uncertain'))
  expect(pendingTextLocators()).toContain(id); expect(screen.queryByRole('button', { name: 'Start a new text review' })).not.toBeInTheDocument(); expect(h.requests.some(r => r.name === 'message.queue.prepare')).toBe(false)
})
it('conversation links focus their exact original queue record', async () => {
  setup(); render(<App />); await openPeer(); await enterQueue(); await prepare(); await commit(); await screen.findByText(textQueueText('en', 'localOnly'))
  const timeline = screen.getByRole('log', { name: 'Device A' }); await userEvent.click(within(timeline).getByRole('button', { name: 'View text in outbox' }))
  const card = await screen.findByRole('article', { name: 'Queued text: Device A' }); await waitFor(() => expect(card).toHaveFocus())
})

it('follows bounded text pagination with its exact revision and no sending side effects', async () => {
  const h = setup(), view = render(<App />); await openPeer(); await enterQueue(); await prepare(); await commit(); await screen.findByText(textQueueText('en', 'localOnly')); view.unmount(); sessionStorage.clear()
  const originalRow = h.rows()[0], baseKey = originalRow.requestId.slice(0, 18)
  const first = { ...originalRow, requestId: baseKey + 'a'.repeat(32), outboxId: baseKey + 'a'.repeat(32), messageId: baseKey + 'a'.repeat(32) }, second = { ...originalRow, requestId: baseKey + 'b'.repeat(32), outboxId: baseKey + 'b'.repeat(32), messageId: baseKey + 'b'.repeat(32), peerId: 'peer-b' }
  h.setRows([first, second]); const originalFetch = h.fetch.getMockImplementation()!
  h.fetch.mockImplementation(async (path, init) => {
    if (init?.body) { const request = JSON.parse(init.body as string); if (request.name === 'message.queue.list') { h.requests.push(request); const { text: _text, ...row } = request.payload.cursor ? second : first; return new Response(JSON.stringify({ ok: true, result: { version: 1, kind: 'text_outbox', status: 'ready', reasonCode: '', revision: 'c'.repeat(64), total: 2, items: [{ ...row, id: row.requestId }], ...(request.payload.cursor ? {} : { nextCursor: first.requestId }) } })) } }
    return originalFetch(path, init)
  })
  render(<App />); await userEvent.click(await screen.findByRole('button', { name: /^Outbox/ })); await screen.findByRole('article', { name: 'Queued text: Device A' }); await userEvent.click(screen.getByRole('button', { name: 'Load more text requests' })); await screen.findByRole('article', { name: 'Queued text: Device B' })
  expect(h.requests.filter(r => r.name === 'message.queue.list').at(-1)?.payload).toEqual({ cursor: first.requestId, revision: 'c'.repeat(64) }); expect(screen.queryByRole('button', { name: 'Load more text requests' })).not.toBeInTheDocument()
  expect(h.requests.filter(r => r.name === 'message.queue.commit')).toHaveLength(1)
})

it.each(['en', 'ja'] as const)('does not display receiver-saved confirmation without a receipt in %s', async locale => {
  const h = setup(locale), created = Date.now(), id = `mt1_${created}_${'a'.repeat(32)}`
  h.setRows([{ version: 1, kind: 'text_outbox', outboxId: id, requestId: id, messageId: id, peerId: 'peer-a', reviewRevision: 'b'.repeat(64), admission: 'enqueued', status: 'completed', deliveryOutcome: 'confirmed', reasonCode: '', textBytes: 3, sha256: await textDigest('abc'), createdAt: new Date(created).toISOString(), expiresAt: new Date(created + 86400000).toISOString(), updatedAt: new Date(created).toISOString() }])
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: locale === 'en' ? /^Outbox/ : /^送信待ち/ }))
  const region = await screen.findByRole('region', { name: textQueueText(locale, 'title') })
  await within(region).findByText(textQueueText(locale, 'message_invalid_response'))
  expect(within(region).queryByText(textQueueText(locale, 'completed'), { exact: false })).not.toBeInTheDocument()
  expect(within(region).queryByText(textQueueText(locale, 'confirmed'), { exact: false })).not.toBeInTheDocument()
  expect(h.requests.some(r => r.name === 'message.queue.commit')).toBe(false)
})

it.each(['en', 'ja'] as const)('retains the underlying queue controls and exact long text through outbox close/reopen (%s)', async locale => {
  const h = setup(locale), { container } = render(<App />)
  const body = '  synthetic-' + 'x'.repeat(256) + '\n合成テキスト\n'
  await openPeer(locale); await enterQueue(locale, body); await prepare(locale)
  expect(container.querySelector('.text-queue-body')?.textContent).toBe(body)
  await commit(locale); await screen.findByText(textQueueText(locale, 'localOnly'))
  const id = h.rows()[0].requestId
  const open = () => userEvent.click(within(container.querySelector('.primary-nav') as HTMLElement).getByRole('button', { name: locale === 'en' ? /^Outbox/ : /^送信待ち/ }))
  for (let attempt = 0; attempt < 2; attempt++) {
    await open()
    const dialog = await screen.findByRole('dialog')
    const card = await within(dialog).findByRole('article', { name: `${textQueueText(locale, 'title')}: Device A` })
    expect(card).toHaveAttribute('data-text-request-id', id)
    await userEvent.click(within(card).getByRole('button', { name: textQueueText(locale, 'details') }))
    await waitFor(() => expect(card.querySelector('.text-queue-body')?.textContent).toBe(body))
    const tools = container.querySelector('.composer-tools') as HTMLElement
    expect(tools.querySelectorAll('.icon-button')).toHaveLength(2)
    expect(within(tools).getByRole('button', { name: textQueueText(locale, 'review') })).toBeDisabled()
    expect(container.querySelector('.message-meta .button')?.textContent).toBe(textQueueText(locale, 'view'))
    fireEvent(dialog, new Event('cancel', { bubbles: false, cancelable: true }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  }
  expect(h.requests.filter(r => r.name === 'message.queue.prepare')).toHaveLength(1)
  expect(h.requests.filter(r => r.name === 'message.queue.commit')).toHaveLength(1)
})
