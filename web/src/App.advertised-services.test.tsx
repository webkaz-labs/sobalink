import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { Service, State } from './api'
import { translator } from './i18n'
import { serviceText } from './service-i18n'

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

for (const locale of ['en', 'ja'] as const) {
  describe(`advertised overview action with automatic ${locale} locale`, () => {
    it('opens the exact grant for review, preserves edits on cancel, and starts only explicitly', async () => {
      vi.stubGlobal('navigator', Object.defineProperties(Object.create(navigator), { languages: { value: [locale === 'ja' ? 'ja-JP' : 'en-US'] }, language: { value: locale } }))
      const view = setup(); render(<App />)
      const t = translator(locale), review = serviceText(locale, 'reviewConnection')
      await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
      const opener = screen.getByRole('button', { name: `${review}: Web Alpha` })
      await userEvent.click(opener)
      expect(document.documentElement.lang).toBe(locale)
      const dialog = within(screen.getByRole('dialog'))
      expect(dialog.getByLabelText(t('availableServices'))).toHaveValue('web-alpha')
      expect(dialog.getByLabelText(t('ports'))).toHaveValue('8080')
      expect(dialog.getByLabelText(t('ports'))).toBeDisabled()
      expect(dialog.getByRole('group', { name: t('previewScope') })).toHaveTextContent('Alpha:8080')
      expect(view.requests).toHaveLength(0)
      fireEvent.change(dialog.getByLabelText(t('localStart')), { target: { value: '18080' } })
      await userEvent.click(dialog.getByRole('button', { name: t('cancel') }))
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(opener).toHaveFocus()
      expect(view.requests).toHaveLength(0)
      await userEvent.click(opener)
      expect(screen.getByLabelText(t('localStart'))).toHaveValue('18080')
      await userEvent.click(screen.getByRole('button', { name: t('startConnection') }))
      await waitFor(() => expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh', 'service.connect']))
      expect(view.requests[1].payload).toMatchObject({ peerId: 'alpha', serviceId: 'web-alpha', serviceRevision: 'review-web-alpha', ports: '8080', localPort: 18080, purpose: 'web', lifetime: 'until-stopped' })
      expect(view.requests.some(item => item.name === 'peer.trust')).toBe(false)
      expect(await screen.findByRole('complementary', { name: t('details') })).toBeInTheDocument()
    })
  })
}

it('keeps manual drafts separate and selects another advertised peer/service exactly', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  const view = setup(); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
  fireEvent.change(screen.getByLabelText('Ports'), { target: { value: '12345' } })
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Admin Alpha' }))
  expect(screen.getByLabelText('Available services')).toHaveValue('admin-alpha')
  expect(screen.getByLabelText('Ports')).toHaveValue('9443')
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
  expect(screen.getByLabelText('Ports')).toHaveValue('12345')
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await userEvent.click(screen.getByRole('button', { name: /Beta/ }))
  expect(screen.queryByRole('button', { name: 'Review connection: Web Alpha' })).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Beta' }))
  expect(screen.getByLabelText('Available services')).toHaveValue('web-beta')
  expect(screen.getByRole('group', { name: translator('en')('previewScope') })).toHaveTextContent('Beta:8090')
  expect(view.requests).toHaveLength(0)
})

it.each(['cancel', 'back', 'navigation'] as const)('ignores late discovery after %s and never repeats a pending start', async dismissal => {
  localStorage.setItem('sobalink.locale', 'en')
  const initial = fixture(), view = setup(initial)
  let resolve!: (value: Response) => void
  const pending = new Promise<Response>(done => { resolve = done })
  view.discovery(() => pending)
  render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  const start = screen.getByRole('button', { name: 'Start connection' })
  fireEvent.click(start); fireEvent.click(start)
  await waitFor(() => expect(view.requests).toHaveLength(1))
  if (dismissal === 'cancel') await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  if (dismissal === 'back') act(() => { history.replaceState({ sobalinkPeer: 'beta' }, ''); window.dispatchEvent(new PopStateEvent('popstate')) })
  if (dismissal === 'navigation') fireEvent.click(screen.getByRole('button', { name: /Beta/ }))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await act(async () => { resolve(new Response(JSON.stringify({ ok: true, result: { services: initial.availableServices, observations: [], partial: false } }))); await pending })
  expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh'])
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  if (dismissal !== 'cancel') expect(screen.getByRole('heading', { name: 'Beta' })).toBeInTheDocument()
})

it.each(['changed', 'withdrawn'] as const)('does not start a %s grant from the overview shortcut', async change => {
  localStorage.setItem('sobalink.locale', 'en')
  const initial = fixture(), view = setup(initial); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  view.setState({ ...initial, availableServices: change === 'withdrawn' ? [] : initial.availableServices!.map(item => item.id === 'web-alpha' ? { ...item, ports: '9090' } : item) })
  await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('The advertised grant changed')
  expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh'])
  expect(screen.getByLabelText('Available services')).toHaveValue('web-alpha')
  expect(screen.getByLabelText('Ports')).toHaveValue('8080')
})

it('blocks overview launch and removes stale route claims until a fresh snapshot arrives', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  const initial = fixture(), view = setup(initial); const { container } = render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  expect(container.querySelector('.conversation-header')).toHaveTextContent('Direct')
  view.fail(); await refresh()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Review connection: Web Alpha' })).toBeDisabled())
  expect(container.querySelector('.conversation-header')).toHaveTextContent('Response not confirmed')
  expect(container.querySelector('.conversation-header')).toHaveTextContent('Path unknown')
  expect(container.querySelector('.conversation-header')).not.toHaveTextContent('Direct')
  expect(container.querySelectorAll('.online-dot, .status-dot.is-online')).toHaveLength(0)
  await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
  expect(screen.getByRole('complementary')).toHaveTextContent('Path unknown')
  view.fail(false); await refresh()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Review connection: Web Alpha' })).toBeEnabled())
  expect(container.querySelector('.conversation-header')).toHaveTextContent('Direct')
  expect(view.requests).toHaveLength(0)
})

it('does not continue discovery into a connection when the follow-up snapshot fails', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  const view = setup(); render(<App />)
  await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Review connection: Web Alpha' }))
  view.fail()
  await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled())
  expect(view.requests.map(item => item.name)).toEqual(['discovery.refresh'])
  expect(screen.getByLabelText('Available services')).toHaveValue('web-alpha')
})

it('expires a retained LAN route while state polling is still pending', async () => {
  localStorage.setItem('sobalink.locale', 'en')
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  try {
    const initial = fixture()
    initial.settings = { network: 'lan' }
    initial.peers = [{ ...initial.peers[0], networks: ['lan'], route: { state: 'ready', path: 'direct', observedAt: new Date(Date.now() - 29900).toISOString() } }]
    const view = setup(initial), { container } = render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: /Alpha/ }))
    expect(container.querySelector('.conversation-header')).toHaveTextContent('Direct')
    view.fetch.mockImplementation(() => new Promise<Response>(() => {}))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); vi.advanceTimersByTime(1000) })
    expect(container.querySelector('.conversation-header')).toHaveTextContent('Path unknown')
    expect(container.querySelector('.conversation-header')).not.toHaveTextContent('Direct')
    expect(initial.peers[0].path).toBe('direct')
    expect(initial.peers[0].route?.state).toBe('ready')
    expect(view.requests).toHaveLength(0)
  } finally { vi.useRealTimers() }
})
