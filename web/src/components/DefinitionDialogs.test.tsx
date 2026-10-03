import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { ServiceConfigResult, State } from '../api'

const service: ServiceConfigResult = { configuration: { id: 'saved-share', name: 'reviewed-share', direction: 'share', backend: 'tailnet', network: 'tcp', ports: '8080', peerIds: ['peer-one'], lifetime: 'finite', ttlSeconds: 3600, loopbackHost: '127.0.0.1', purpose: 'web', discoverable: false }, revision: 'a'.repeat(64), active: true }
const state: State = { csrfToken: 'fixture-token', self: { name: 'This device', status: 'online' }, peers: [{ id: 'peer-one', name: 'Studio', networks: ['tailnet'], online: true, trusted: true, verified: true, bridge: true, path: 'direct' }], messages: [], transfers: [], services: [], shares: [{ ...service.configuration, peerId: '', status: 'active' }], settings: { network: 'tailnet' } }
function setup(conflict = false) {
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  localStorage.setItem('sobalink.locale', 'en')
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const request = JSON.parse(init!.body as string); requests.push(request)
    if (request.name === 'service.delete' && conflict) return new Response(JSON.stringify({ code: 'profile_revision_conflict' }), { status: 409 })
    const result = request.name === 'service.config' ? service : request.name === 'group.list' ? { groups: [{ name: 'daily-tools', serviceIds: ['saved-share'] }], revision: 'b'.repeat(64) } : {}
    return new Response(JSON.stringify({ ok: true, result }))
  }))
  return requests
}
async function reviewRemoval() {
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Studio/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Remove saved rule' }))
  return await screen.findByRole('button', { name: 'Remove reviewed rule' })
}
describe('saved definition consequences', () => {
  it('requires active stop and named group review, preserving exact revisions', async () => {
    const requests = setup()
    const remove = await reviewRemoval()
    expect(screen.getByText('daily-tools')).toBeVisible()
    expect(remove).toBeDisabled()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Stop this active service before removing it' }))
    expect(remove).toBeDisabled()
    await userEvent.click(screen.getByRole('checkbox', { name: /Remove this rule from the listed groups/ }))
    expect(requests.some(item => item.name === 'service.delete')).toBe(false)
    await userEvent.click(remove)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(requests.find(item => item.name === 'service.delete')?.payload).toEqual({ id: 'saved-share', expectedRevision: 'a'.repeat(64), expectedProfileRevision: 'b'.repeat(64), stopActive: true, removeFromGroups: true })
  })
  it('cancels without deletion and requires a fresh review after a profile conflict', async () => {
    const requests = setup(true)
    await reviewRemoval()
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(item => item.name === 'service.delete')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Remove saved rule' }))
    await userEvent.click(await screen.findByRole('checkbox', { name: 'Stop this active service before removing it' }))
    await userEvent.click(screen.getByRole('checkbox', { name: /Remove this rule from the listed groups/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Remove reviewed rule' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Saved definitions or groups changed')
    expect(screen.getByRole('button', { name: 'Remove reviewed rule' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Reload saved settings' }))
    expect(await screen.findByRole('checkbox', { name: 'Stop this active service before removing it' })).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Remove reviewed rule' })).toBeDisabled()
  })
  it('reviews global sharing scope and leaves everything unchanged on cancel', async () => {
    const requests = setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: 'Preferences' }))
    await userEvent.click(screen.getByRole('button', { name: 'Stop all sharing' }))
    expect(screen.getByRole('dialog')).toHaveTextContent('reviewed-share')
    expect(screen.getByRole('dialog')).toHaveTextContent('Stop every active share on this device')
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(requests.some(item => item.name === 'service.stop-shares')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Preferences' }))
    await userEvent.click(screen.getByRole('button', { name: 'Stop all sharing' }))
    await userEvent.click(screen.getByRole('button', { name: 'Stop all active shares' }))
    await waitFor(() => expect(requests.filter(item => item.name === 'service.stop-shares')).toHaveLength(1))
  })
})
