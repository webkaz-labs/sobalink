import { act, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { OutboxDialog } from './Outbox'
import type { State } from '../api'
import { translator } from '../i18n'
import { pendingOutboxLocators, retainOutboxLocator, type OutboxRecord } from '../outbox'
import { fixtureOutbox, fixtureOutboxID } from '../outbox-fixture'
import type { Server } from '../useServer'

const secondID = fixtureOutboxID.replace(/a{32}$/, 'd'.repeat(32))
const state: State = { csrfToken: 'synthetic', self: { name: 'Synthetic device', status: 'online', networks: [] }, peers: [], messages: [], transfers: [], services: [], shares: [] }
const list = (items: OutboxRecord[] = []) => new Response(JSON.stringify({ ok: true, result: { version: 1, kind: 'outbox', status: 'ready', reasonCode: '', items } }))
function enqueued() {
  const record = fixtureOutbox()
  delete record.processId
  record.admission = 'enqueued'; record.status = 'queued'
  record.children = record.children.map(child => ({ ...child, status: 'queued' }))
  return record
}
function deferred() {
  let resolve!: (response: Response) => void
  const promise = new Promise<Response>(done => { resolve = done })
  return { promise, resolve }
}
function setup() {
  const lists: ReturnType<typeof deferred>[] = [], statuses: ReturnType<typeof deferred>[] = []
  const requests: { name: string; payload: { requestId?: string } }[] = []
  vi.stubGlobal('fetch', vi.fn((_path: string, init: RequestInit) => {
    const request = JSON.parse(init.body as string); requests.push(request)
    const pending = deferred()
    if (request.name === 'outbox.list') lists.push(pending)
    else if (request.name === 'outbox.status') statuses.push(pending)
    else throw new Error('Unexpected synthetic command')
    return pending.promise
  }))
  const server = { auth: 'ready', stale: false, updatedAt: new Date(0), handleError: vi.fn() } as unknown as Server
  const dialog = (updatedAt = new Date(0)) => <OutboxDialog state={state} server={{ ...server, updatedAt }} locale="en" t={translator('en')} onClose={() => {}} />
  const view = render(dialog())
  const invalidate = (kind: 'unmount' | 'supersede') => kind === 'unmount' ? view.unmount() : view.rerender(dialog(new Date(1)))
  const settle = async (pending: ReturnType<typeof deferred>, response: Response) => { await act(async () => { pending.resolve(response) }) }
  return { lists, statuses, requests, invalidate, settle }
}
beforeEach(() => { sessionStorage.clear() })

it.each(['unmount', 'supersede'] as const)('ignores a late empty list after %s without looking up a newly retained locator', async kind => {
  const h = setup()
  expect(h.lists).toHaveLength(1)
  h.invalidate(kind)
  retainOutboxLocator(fixtureOutboxID)
  await h.settle(h.lists[0], list())
  expect(h.statuses).toHaveLength(0)
  expect(pendingOutboxLocators()).toEqual([fixtureOutboxID])
})

it.each(['unmount', 'supersede'] as const)('does not resolve locators from a late populated list after %s', async kind => {
  retainOutboxLocator(fixtureOutboxID)
  const h = setup(); h.invalidate(kind)
  await h.settle(h.lists[0], list([enqueued()]))
  expect(pendingOutboxLocators()).toEqual([fixtureOutboxID])
  expect(h.statuses).toHaveLength(0)
})

it.each([
  ['unmount', 'enqueued'], ['supersede', 'enqueued'],
  ['unmount', 'not-found'], ['supersede', 'not-found'],
] as const)('ignores a status completing after %s with %s and does not request the next locator', async (kind, outcome) => {
  retainOutboxLocator(fixtureOutboxID); retainOutboxLocator(secondID)
  const h = setup()
  await h.settle(h.lists[0], list())
  await waitFor(() => expect(h.statuses).toHaveLength(1))
  expect(h.requests.at(-1)?.payload).toEqual({ requestId: fixtureOutboxID })
  h.invalidate(kind)
  const response = outcome === 'enqueued'
    ? new Response(JSON.stringify({ ok: true, result: enqueued() }))
    : new Response(JSON.stringify({ code: 'outbox_not_found' }), { status: 404 })
  await h.settle(h.statuses[0], response)
  expect(pendingOutboxLocators()).toEqual([fixtureOutboxID, secondID])
  expect(h.statuses).toHaveLength(1)
})

it('allows the latest refresh to resolve its original locator and show the authoritative record', async () => {
  retainOutboxLocator(fixtureOutboxID)
  const h = setup(); h.invalidate('supersede')
  await h.settle(h.lists[1], list())
  await waitFor(() => expect(h.statuses).toHaveLength(1))
  await h.settle(h.statuses[0], new Response(JSON.stringify({ ok: true, result: enqueued() })))
  expect(pendingOutboxLocators()).toEqual([])
  expect(screen.getByText('Queued on this device. Delivery is not yet confirmed.')).toBeVisible()
  await h.settle(h.lists[0], list())
  expect(h.statuses).toHaveLength(1)
})
