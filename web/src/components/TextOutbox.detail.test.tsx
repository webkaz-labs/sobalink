import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import * as api from '../api'
import { textDigest, type TextOutboxRecord } from '../text-outbox'
import type { Server } from '../useServer'
import { TextOutboxSection } from './TextOutbox'

const processId = 'a'.repeat(64)
const state: api.State = { csrfToken: 'synthetic', resourceCatalogProcessId: processId,
  self: { name: 'Synthetic sender', status: 'online', networks: ['tailnet'] },
  peers: [{ id: 'peer-a', name: 'Synthetic receiver', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }],
  messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' } }
const body = '  synthetic exact text\n合成\n'
async function fixture(prepared = false): Promise<TextOutboxRecord> {
  const now = Date.now() - 1000, id = `mt1_${now}_${'b'.repeat(32)}`, digest = await textDigest(body)
  const record: TextOutboxRecord = { version: 1, kind: 'text_outbox', outboxId: id, requestId: id, messageId: id,
    peerId: 'peer-a', reviewRevision: 'c'.repeat(64), admission: prepared ? 'prepared' : 'enqueued',
    status: prepared ? 'prepared' : 'completed', deliveryOutcome: prepared ? 'not_attempted' : 'confirmed', reasonCode: '',
    text: body, textBytes: api.messageByteLength(body), sha256: digest, createdAt: new Date(now).toISOString(),
    expiresAt: new Date(now + 3600000).toISOString(), updatedAt: new Date(now).toISOString() }
  if (prepared) record.processId = processId
  else record.receipt = { lease: { version: 1, deliveryId: id, digest: 'd'.repeat(64), receiverEpoch: 'e'.repeat(32), token: 'f'.repeat(32), recipientId: 'peer-a', expiresAt: record.expiresAt, receiptUntil: new Date(now + 8 * 86400000 + 300000).toISOString() }, messageId: id, textBytes: record.textBytes, textSHA256: digest, savedAt: new Date(now + 1).toISOString() }
  return record
}
const advance = (record: TextOutboxRecord) => ({ ...record, updatedAt: new Date(Date.parse(record.updatedAt) + 10).toISOString() })
function setup(initial: TextOutboxRecord) {
  let listed = initial, detail: TextOutboxRecord | Promise<TextOutboxRecord> = initial
  const server = { auth: 'ready', stale: false, refresh: vi.fn(async () => {}), handleError: vi.fn() } as unknown as Server
  const commands = vi.spyOn(api, 'command').mockImplementation(async name => {
    if (name === 'message.queue.list') {
      const { text: _body, ...row } = listed
      return { ok: true, result: { version: 1, kind: 'text_outbox', status: 'ready', reasonCode: '', revision: '1'.repeat(64), total: 1, items: [{ ...row, id: row.requestId }] } }
    }
    if (name === 'message.queue.status') return { ok: true, result: { ...await detail } }
    throw Error('Unexpected synthetic command')
  })
  const view = render(<TextOutboxSection state={state} server={server} locale="en" />)
  return { ...view, commands, server, setListed: (record: TextOutboxRecord) => { listed = record },
    setDetail: (record: TextOutboxRecord | Promise<TextOutboxRecord>) => { detail = record },
    renderState: (value: api.State) => view.rerender(<TextOutboxSection state={value} server={server} locale="en" />) }
}
async function inspect() { await userEvent.click(await screen.findByRole('button', { name: 'Inspect original text' })) }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => sessionStorage.clear())

it.each([false, true])('keeps verified detail when its own response advances updatedAt (prepared=%s)', async prepared => {
  const initial = await fixture(prepared), h = setup(initial)
  h.setDetail(advance(initial)); await inspect()
  await waitFor(() => expect(h.container.querySelector('.text-queue-body')?.textContent).toBe(body))
  expect(screen.getByRole('button', { name: 'Check original text request' })).toBeEnabled()
  expect(h.commands.mock.calls.map(([name]) => name)).toEqual(['message.queue.list', 'message.queue.status'])
  if (prepared) {
    expect(screen.getByRole('checkbox')).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Queue this text' })).toBeDisabled()
  }
})

it('external record revision changes clear detail and consent before another send', async () => {
  const initial = await fixture(true), h = setup(initial); await inspect()
  await screen.findByRole('checkbox'); await userEvent.click(screen.getByRole('checkbox'))
  expect(screen.getByRole('button', { name: 'Queue this text' })).toBeEnabled()
  const next = advance(initial); h.setListed(next); h.setDetail(next)
  await userEvent.click(screen.getByRole('button', { name: 'Refresh queued text' }))
  await screen.findByRole('button', { name: 'Inspect original text' })
  expect(h.container.querySelector('.text-queue-body')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument()
  await inspect(); await screen.findByRole('checkbox')
  expect(screen.getByRole('checkbox')).not.toBeChecked()
  expect(screen.getByRole('button', { name: 'Queue this text' })).toBeDisabled()
  expect(h.commands.mock.calls.some(([name]) => name === 'message.queue.commit')).toBe(false)
})

it('a same-timestamp external record change still invalidates the exact inspected revision', async () => {
  const initial = await fixture(true), h = setup(initial); await inspect(); await screen.findByRole('checkbox')
  await userEvent.click(screen.getByRole('checkbox'))
  h.setListed({ ...initial, reviewRevision: '2'.repeat(64) })
  await userEvent.click(screen.getByRole('button', { name: 'Refresh queued text' }))
  await screen.findByRole('button', { name: 'Inspect original text' })
  expect(h.container.querySelector('.text-queue-body')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument()
  expect(h.commands.mock.calls.some(([name]) => name === 'message.queue.commit')).toBe(false)
})

it('a current Core process change clears inspected detail and checked consent', async () => {
  const h = setup(await fixture(true)); await inspect(); await screen.findByRole('checkbox')
  await userEvent.click(screen.getByRole('checkbox'))
  h.renderState({ ...state, resourceCatalogProcessId: '3'.repeat(64) })
  await screen.findByRole('button', { name: 'Inspect original text' })
  expect(h.container.querySelector('.text-queue-body')).toBeNull()
  expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  expect(h.commands.mock.calls.some(([name]) => name === 'message.queue.commit')).toBe(false)
})

it.each(['process', 'revision', 'process-restored', 'revision-restored'] as const)('ignores a detail response arriving after an external %s change', async changed => {
  const initial = await fixture(changed.startsWith('process')), h = setup(initial), reply = deferred<TextOutboxRecord>()
  h.setDetail(reply.promise); await inspect()
  await waitFor(() => expect(h.commands.mock.calls.filter(([name]) => name === 'message.queue.status')).toHaveLength(1))
  if (changed.startsWith('process')) {
    h.renderState({ ...state, resourceCatalogProcessId: '4'.repeat(64) })
    if (changed === 'process-restored') h.renderState(state)
  } else { h.setListed(advance(initial)); await userEvent.click(screen.getByRole('button', { name: 'Refresh queued text' })); await waitFor(() => expect(h.commands.mock.calls.filter(([name]) => name === 'message.queue.list')).toHaveLength(2))
    if (changed === 'revision-restored') { await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh queued text' })).toBeEnabled()); h.setListed(initial); await userEvent.click(screen.getByRole('button', { name: 'Refresh queued text' })); await waitFor(() => expect(h.commands.mock.calls.filter(([name]) => name === 'message.queue.list')).toHaveLength(3)) }
  }
  await act(async () => { reply.resolve(initial); await reply.promise })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Inspect original text' })).toBeEnabled())
  expect(h.container.querySelector('.text-queue-body')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument()
  expect(h.commands.mock.calls.some(([name]) => name === 'message.queue.commit')).toBe(false)
})

it('a response changing the original peer or body cannot retain detail or authorize sending', async () => {
  const initial = await fixture(true), h = setup(initial)
  h.setDetail({ ...advance(initial), peerId: 'peer-other', text: 'changed synthetic' }); await inspect()
  await screen.findByText('The response did not match the original text request. Keep its ID and check the original status; do not create a replacement.')
  expect(h.container.querySelector('.text-queue-body')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Queue this text' })).not.toBeInTheDocument()
  expect(h.commands.mock.calls.some(([name]) => name === 'message.queue.commit')).toBe(false)
})
