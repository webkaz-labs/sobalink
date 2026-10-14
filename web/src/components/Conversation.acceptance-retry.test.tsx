import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { Locale, State } from '../api'
import { en, ja } from '../i18n'
import { transferTranslator } from '../transfer-i18n'

function offerState(): State {
  return {
    resourceCatalogProcessId: 'a'.repeat(64), csrfToken: 'synthetic-csrf', self: { name: 'Synthetic receiver', status: 'online', receiveDirectory: '/synthetic-receiving', networks: ['tailnet'] },
    peers: ['a', 'b'].map(id => ({ id: `peer-${id}`, name: `Device ${id.toUpperCase()}`, networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' })),
    messages: [], services: [], shares: [], settings: { network: 'tailnet', receiveDirectory: '/synthetic-receiving' },
    transfers: ['original', 'other'].map(id => ({ id: `delivery-${id}`, peerId: 'peer-a', direction: 'incoming', name: `${id} batch`, entries: [{ path: `${id}.txt`, kind: 'file', size: 5 }], totalBytes: 5, completedBytes: 0, status: 'awaiting-acceptance', createdAt: '2026-10-02T10:00:00Z' })),
  }
}
type Request = { requestId: string; name: string; payload: Record<string, unknown> }
function setup(code: string) {
  let state = offerState()
  const requests: Request[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    requests.push(JSON.parse(init!.body as string))
    return requests.length === 1 ? new Response(JSON.stringify({ code, error: 'Synthetic acceptance diagnostic' }), { status: 400 }) : new Response('{"ok":true}')
  }))
  return { requests, state, changeDefault: () => { state = { ...state, settings: { ...state.settings, receiveDirectory: '/new-default' }, self: { ...state.self, receiveDirectory: '/new-default' } } } }
}
async function openPeer(locale: Locale, name = 'Device A') {
  await userEvent.click(await screen.findByRole('button', { name: new RegExp(name) }))
  await userEvent.click(screen.getByRole('button', { name: (locale === 'ja' ? ja : en).filesAndMessages }))
}
const originalCard = () => screen.getByRole('article', { name: /original batch/ })

describe.each(['en', 'ja'] as const)('explicit acceptance retry (%s)', locale => {
  const copy = locale === 'ja' ? ja : en
  const ft = transferTranslator(locale)

  it.each(['default', 'override'])('retains the exact original delivery and resolved %s folder until a fresh explicit acceptance', async source => {
    localStorage.setItem('sobalink.locale', locale)
    const fixture = setup('transfer_accept_busy')
    render(<App />); await openPeer(locale)
    const original = originalCard()
    const destination = source === 'override' ? '/batch-only' : '/synthetic-receiving'
    if (source === 'override') {
      await userEvent.click(within(original).getByRole('button', { name: ft('changeFolder') }))
      fireEvent.change(within(original).getByRole('textbox', { name: new RegExp(copy.receiveDirectory) }), { target: { value: destination } })
    }
    await userEvent.click(within(original).getByRole('button', { name: copy.acceptBatch }))
    expect(await screen.findByRole('alert')).toHaveTextContent(copy.transfer_accept_busy)
    expect(within(original).getByRole('textbox', { name: new RegExp(copy.receiveDirectory) })).toHaveValue(destination)
    expect(fixture.requests).toHaveLength(1)
    fixture.changeDefault()
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await waitFor(() => expect(within(screen.getByRole('article', { name: /other batch/ })).getByText('/new-default')).toBeInTheDocument())
    await openPeer(locale, 'Device B'); await openPeer(locale)
    expect(within(originalCard()).getByRole('textbox', { name: new RegExp(copy.receiveDirectory) })).toHaveValue(destination)
    expect(fixture.requests).toHaveLength(1)
    await userEvent.click(within(originalCard()).getByRole('button', { name: copy.acceptBatch }))
    await waitFor(() => expect(fixture.requests).toHaveLength(2))
    expect(fixture.requests.map(({ name, payload }) => ({ name, payload }))).toEqual(Array(2).fill({ name: 'transfer.accept', payload: { transferId: 'delivery-original', destination } }))
    expect(fixture.requests[1].requestId).not.toBe(fixture.requests[0].requestId)
    expect(fixture.state.transfers.every(transfer => transfer.status === 'awaiting-acceptance')).toBe(true)
    expect(fixture.state.peers.every(peer => peer.trusted && peer.verified)).toBe(true)
  })

  it.each(['transfer_accept_unconfirmed', 'command_failed', 'receive_recovery_required', 'outbox_storage_uncertain', 'busy', 'unknown_acceptance_failure'])('does not offer busy retry guidance or issue another request for %s', async code => {
    localStorage.setItem('sobalink.locale', locale)
    const fixture = setup(code)
    render(<App />); await openPeer(locale)
    await userEvent.click(within(originalCard()).getByRole('button', { name: copy.acceptBatch }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(copy.transfer_accept_unconfirmed)
    expect(alert).not.toHaveTextContent(copy.transfer_accept_busy)
    expect(alert).not.toHaveTextContent(copy.request_failed)
    expect(within(originalCard()).getByRole('textbox', { name: new RegExp(copy.receiveDirectory) })).toHaveValue('/synthetic-receiving')
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(fixture.requests).toHaveLength(1)
    expect(fixture.requests[0]).toMatchObject({ name: 'transfer.accept', payload: { transferId: 'delivery-original', destination: '/synthetic-receiving' } })
  })
})
