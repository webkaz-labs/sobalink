import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { App } from './App'
import type { Service, State } from './api'

function fixture(): State {
  const ad = (peerId: string, id: string, name: string, ports: string): Service => ({ id, peerId, name, ports, network: 'tcp', purpose: 'web', revision: `review-${id}`, checkedAt: new Date().toISOString(), lifetime: 'until-revoked', status: 'active' })
  return {
    csrfToken: 'synthetic-csrf', self: { name: 'Local fixture', status: 'online', networks: ['tailnet'] }, settings: { network: 'tailnet' },
    peers: ['Alpha', 'Beta'].map(name => ({ id: name.toLowerCase(), name, networks: ['tailnet'], online: true, verified: true, trusted: false, bridge: true, path: 'direct' })),
    availableServices: [ad('alpha', 'web-alpha', 'Web Alpha', '8080'), ad('alpha', 'admin-alpha', 'Admin Alpha', '9443'), ad('beta', 'web-beta', 'Web Beta', '8090')],
    messages: [], transfers: [], services: [], shares: [],
  }
}
function setup(initial = fixture()) {
  let current = initial
  let failState = false
  let discovery: (() => Promise<Response>) | undefined
  const requests: { name: string; payload: Record<string, unknown> }[] = []
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') {
      if (failState) throw new TypeError('Synthetic refresh failure')
      return new Response(JSON.stringify(current))
    }
    const request = JSON.parse(init?.body as string)
    requests.push(request)
    if (request.name === 'discovery.refresh') return discovery ? discovery() : new Response(JSON.stringify({ ok: true, result: { services: current.availableServices, observations: [], partial: false } }))
    return new Response('{"ok":true}')
  })
  vi.stubGlobal('fetch', fetch)
  return { requests, fetch, setState: (next: State) => { current = next }, fail: (value = true) => { failState = value }, discovery: (value: () => Promise<Response>) => { discovery = value } }
}
async function refresh() { await act(async () => { document.dispatchEvent(new Event('visibilitychange')) }) }


it('auth loss prevents old advertised discovery from surviving a new login', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  const initial = fixture(), view = setup(initial)
  let resolve!: (value: Response) => void
  const pending = new Promise<Response>(done => { resolve = done })
  view.discovery(() => pending)
  const original = view.fetch.getMockImplementation()!
  let locked = false
  view.fetch.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === '/api/state' && locked) return new Response('{"code":"unauthenticated"}', { status: 401 })
    if (path === '/api/session') { locked = false; return new Response('{"csrfToken":"synthetic-new-session"}') }
    return original(path, init)
  })
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  fireEvent.change(document.querySelector<HTMLInputElement>('dialog input[required][maxlength="64"]')!, { target: { value: 'private-old-draft' } })
  await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
  await waitFor(() => expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh']))
  locked = true; await refresh()
  await screen.findByLabelText('Local access code')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Local access code'), { target: { value: 'synthetic-login' } })
  await userEvent.click(screen.getByRole('button', { name: 'Open sobalink' }))
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await act(async () => { resolve(new Response(JSON.stringify({ ok: true, result: { services: initial.availableServices, observations: [], partial: false } }))); await pending })
  expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh'])
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  expect(document.querySelector<HTMLInputElement>('dialog input[required][maxlength="64"]')!).not.toHaveValue('private-old-draft')
})

it('changing the selected grant during discovery cannot apply the old grant', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  const initial = fixture(), view = setup(initial)
  let resolve!: (value: Response) => void
  const pending = new Promise<Response>(done => { resolve = done })
  view.discovery(() => pending)
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
  await waitFor(() => expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh']))
  await userEvent.selectOptions(screen.getByLabelText('Available services'), 'admin-alpha')
  await act(async () => { resolve(new Response(JSON.stringify({ ok: true, result: { services: initial.availableServices, observations: [], partial: false } }))); await pending })
  expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh'])
  expect(screen.getByLabelText('Ports')).toHaveValue('9443')
  view.discovery(async () => new Response(JSON.stringify({ ok: true, result: { services: initial.availableServices, observations: [], partial: false } })))
  await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
  await waitFor(() => expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh', 'discovery.refresh', 'service.connect']))
  expect(view.requests[2].payload).toMatchObject({ peerId: 'alpha', serviceId: 'admin-alpha', ports: '9443' })
})
