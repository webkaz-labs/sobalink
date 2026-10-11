import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { State } from './api'
const peerId = 'a'.repeat(64), resourceId = 'b'.repeat(32), grantId = 'c'.repeat(32)
const reply = { selector: { protocolVersion: 1, target: { schemaVersion: 1, resourceId }, grantId, grantRevision: 2 }, peerKey: peerId, checkedAt: new Date().toISOString(), reviewRevision: 'd'.repeat(32), services: [{ service: { id: 'e'.repeat(32), purpose: 'web', network: 'tcp', ports: '8080', lifetime: 'until-revoked', application: 'unverified' }, connectAllowed: true }] }
async function setup() {
  vi.stubGlobal('navigator', Object.defineProperties(Object.create(navigator), { languages: { value: ['en-US'] }, language: { value: 'en' } }))
  const state: State = { csrfToken: 'fixture', self: { name: 'Local example', status: 'online' }, peers: [{ id: peerId, name: 'Example target', networks: ['direct-lan'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }], messages: [], transfers: [], services: [], shares: [], settings: { network: 'direct-lan' }, resourceCatalogProcessId: 'f'.repeat(64) }
  let list = async () => new Response(JSON.stringify({ ok: true, result: reply }))
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => { if (path === '/api/state') return new Response(JSON.stringify(state)); const request = JSON.parse(init?.body as string); if (request.name === 'resource.catalog.view.list') return list(); return new Response('{"ok":true}') }))
  render(<App />)
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: /Example target/ }))
  await user.type(screen.getByLabelText('Resource ID'), resourceId); await user.type(screen.getByLabelText('View grant ID'), grantId); await user.type(screen.getByLabelText('Grant revision'), '2')
  return { user, setList: (next: typeof list) => { list = next } }
}
describe('catalog view browser navigation', () => {
  it('discards same-peer list and nested connection review on backward and forward popstate', async () => { const h = await setup(); await h.user.click(screen.getByRole('button', { name: 'Fetch services' })); await h.user.click(await screen.findByRole('button', { name: 'Review connection' })); expect(screen.getByRole('dialog')).toBeVisible(); act(() => window.dispatchEvent(new PopStateEvent('popstate'))); expect(screen.queryByRole('dialog')).toBeNull(); expect(screen.queryByRole('button', { name: 'Review connection' })).toBeNull(); expect(screen.getByLabelText('Resource ID')).toHaveValue(''); act(() => window.dispatchEvent(new PopStateEvent('popstate'))); expect(screen.queryByRole('dialog')).toBeNull(); expect(screen.queryByRole('button', { name: 'Review connection' })).toBeNull() })
  it('suppresses a same-peer late list after browser navigation', async () => { const h = await setup(); let finish!: (value: Response) => void; h.setList(() => new Promise(resolve => { finish = resolve })); await h.user.click(screen.getByRole('button', { name: 'Fetch services' })); act(() => window.dispatchEvent(new PopStateEvent('popstate'))); await act(async () => finish(new Response(JSON.stringify({ ok: true, result: reply })))); expect(screen.queryByRole('button', { name: 'Review connection' })).toBeNull(); expect(screen.getByLabelText('Resource ID')).toHaveValue('') })
})
