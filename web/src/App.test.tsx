import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { StrictMode } from 'react'
import { App } from './App'
import type { ServiceConfigResult, State } from './api'
import * as api from './api'
import { translator } from './i18n'
import { lanTranslator } from './lan-i18n'

const state: State = {
  csrfToken: 'test-csrf', self: { name: 'This device', status: 'online', networks: ['tailnet'] },
  peers: [
    { id: 'peer-a', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
    { id: 'peer-b', name: 'Notebook', networks: ['lan'], online: true, verified: true, trusted: false, bridge: true, path: 'direct' },
    { id: 'peer-c', name: 'Service host', networks: ['tailnet'], online: true, verified: true, trusted: false, bridge: false, path: 'relay' },
  ], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' },
}
function setup(initial = state, configs: Record<string, ServiceConfigResult> = {}) {
  const requests: { path: string; body: Record<string, unknown> }[] = []
  let current = structuredClone(initial)
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.body) requests.push({ path, body: JSON.parse(init.body as string) })
    const body = init?.body ? JSON.parse(init.body as string) : undefined
    return new Response(JSON.stringify(path === '/api/state' ? current : body?.name === 'discovery.refresh' ? { ok: true, result: { services: current.availableServices || [], observations: [], partial: false } } : body?.name === 'service.config' ? { ok: true, result: configs[body.payload.id] } : { ok: true }), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return { requests, fetch, setState: (next: State) => { current = next } }
}
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en') })
async function openStudio() { await userEvent.click(await screen.findByRole('button', { name: /Studio/ })); await userEvent.click(screen.getByRole('button', { name: 'Files & messages' })) }

describe('explicit and interrupted flows', () => {
  it('does not send on Enter, paste, or IME composition confirmation', async () => {
    const { requests } = setup(); render(<App />); await openStudio()
    const input = screen.getByRole('textbox', { name: 'Write a message…' })
    fireEvent.change(input, { target: { value: 'こんにちは' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.keyDown(input, { key: 'Enter', ctrlKey: true, isComposing: true })
    fireEvent.keyDown(input, { key: 'Enter', ctrlKey: true, keyCode: 229 })
    expect(requests).toHaveLength(0)
    fireEvent.keyDown(input, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(requests).toHaveLength(1))
    expect(requests[0].body).toMatchObject({ name: 'message.send', payload: { peerId: 'peer-a', text: 'こんにちは' } })
    await waitFor(() => expect(input).toHaveValue(''))
  })
  it('keeps each unsent draft through device navigation and Back', async () => {
    setup(); render(<App />); await openStudio()
    fireEvent.change(screen.getByRole('textbox', { name: 'Write a message…' }), { target: { value: 'Not sent' } })
    await userEvent.click(screen.getByRole('button', { name: /Notebook/ }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('')
    await userEvent.click(screen.getByRole('button', { name: /Studio/ }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('Not sent')
    await userEvent.click(screen.getByRole('button', { name: 'Back to devices' }))
    await userEvent.click(screen.getByRole('button', { name: /Studio/ }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('Not sent')
  })
  it('requires explicit peer permission and keeps ordinary Tailscale service access', async () => {
    const { requests } = setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: /Notebook/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Allow communication' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.trust', payload: { peerId: 'peer-b', trusted: true } }))
    await userEvent.click(screen.getByRole('button', { name: /Service host/ }))
    expect(screen.queryByRole('textbox', { name: 'Write a message…' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Connect to a service' })).toBeEnabled()
  })
  it('reviews file recipient and count without staging until send', async () => {
    const { requests } = setup(); const { container } = render(<App />); await openStudio()
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, [new File(['hello'], 'note.txt'), new File([], 'empty.txt')])
    expect(await screen.findByRole('heading', { name: 'To: Studio' })).toBeInTheDocument()
    expect(screen.getByText('note.txt')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send batch' })).toBeEnabled()
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('button', { name: 'Send batch' })).not.toBeInTheDocument()
    expect(requests).toHaveLength(0)
  })
  it('requires an explicit receive directory when no default exists', async () => {
    const incoming: State = { ...state, transfers: [{ id: 'batch-one', peerId: 'peer-a', direction: 'incoming', name: 'Notes', entries: [{ path: 'note.txt', kind: 'file', size: 5 }], totalBytes: 5, completedBytes: 0, status: 'offered', createdAt: '2026-10-02T10:00:00Z' }] }
    const { requests } = setup(incoming); render(<App />); await openStudio()
    expect(screen.getByRole('button', { name: 'Accept batch' })).toBeDisabled()
    await userEvent.type(screen.getByRole('textbox', { name: /Receive directory/ }), '/tmp/received')
    await userEvent.click(screen.getByRole('button', { name: 'Accept batch' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'transfer.accept', payload: { transferId: 'batch-one', destination: '/tmp/received' } }))
  })
  it('keeps service errors in place and restores focus after cancellation', async () => {
    const { requests } = setup(); render(<App />); await openStudio()
    const opener = screen.getByRole('button', { name: 'Connect to a service' })
    await userEvent.click(opener)
    const dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Ports' }), '8000-8100')
    expect(within(dialog).getByText(/exceeds the available listener budget/)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Start connection' })).toBeDisabled()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
    expect(requests).toHaveLength(0)
  })
  it('does not show fake peers or success when empty', async () => {
    setup({ ...state, peers: [] }); render(<App />)
    expect(await screen.findByText('Your devices will appear here')).toBeInTheDocument()
    expect(screen.queryByText('Studio')).not.toBeInTheDocument()
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  })
  it('returns to the device list when the selected peer disappears', async () => {
    const { setState } = setup(); const { container } = render(<App />); await openStudio()
    setState({ ...state, peers: [] })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await waitFor(() => expect(container.querySelector('.workspace')).not.toHaveClass('device-selected'))
    expect(screen.getByText('Your devices will appear here')).toBeInTheDocument()
  })
  it('reuses a request ID after an ambiguous send failure without clearing the draft', async () => {
    const { fetch, requests } = setup()
    let attempts = 0
    fetch.mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === '/api/command') {
        requests.push({ path, body: JSON.parse(init?.body as string) })
        if (++attempts === 1) throw new TypeError('network interrupted')
      }
      return new Response(JSON.stringify(path === '/api/state' ? state : { ok: true }), { status: 200 })
    })
    render(<App />); await openStudio()
    const input = screen.getByRole('textbox', { name: 'Write a message…' })
    fireEvent.change(input, { target: { value: 'Keep this until acknowledged' } })
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/may still have completed/)
    expect(input).toHaveValue('Keep this until acknowledged')
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() => expect(input).toHaveValue(''))
    expect(requests).toHaveLength(2)
    expect(requests[0].body.requestId).toBe(requests[1].body.requestId)
  })
  it('sends a discovered service ID with the exact advertised endpoint and shows resulting details', async () => {
    const { requests } = setup({ ...state, availableServices: [{ id: 'opaque-service', revision: 'opaque-review', checkedAt: new Date().toISOString(), purpose: 'generic', lifetime: 'until-revoked', peerId: 'peer-a', name: 'tcp 8080', ports: '8080', network: 'tcp', status: 'active', application: 'unverified' }] })
    render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), 'opaque-service')
    expect(screen.getByRole('textbox', { name: 'Ports' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    await waitFor(() => expect(requests.find(request => request.body.name === 'service.connect')?.body).toMatchObject({ name: 'service.connect', payload: { serviceId: 'opaque-service', peerId: 'peer-a', ports: '8080', network: 'tcp', name: 'tcp-8080' } }))
    expect(await screen.findByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
  })
  it('preserves long Japanese drafts and enforces the same UTF-8 byte limit as the server', async () => {
    const { requests } = setup(); render(<App />); await openStudio()
    const input = screen.getByRole('textbox', { name: 'Write a message…' })
    const text = 'あ'.repeat(6000)
    fireEvent.change(input, { target: { value: text } })
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/displayed UTF-8 byte limit/)
    expect(requests).toHaveLength(0)
    expect(input).toHaveValue(text)
  })
  it('makes peer-wide pause explicit and disables new exchanges until resumed', async () => {
    const pausedState: State = { ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: true, directory: '/tmp/received' } }] }
    const { requests } = setup(pausedState); render(<App />); await openStudio()
    expect(screen.getByText(/Messages and file transfers with this device are paused/)).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', paused: false } }))
  })
  it('turns off automatic receiving without silently resuming a paused peer', async () => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: true, directory: '/tmp/received' } }] })
    render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    await userEvent.click(screen.getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', enabled: false } }))
  })
  it('never offers unsupported local remapping for a shared service', async () => {
    const { requests } = setup(); render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    await userEvent.click(screen.getByRole('button', { name: 'Share a service' }))
    expect(screen.queryByRole('textbox', { name: 'Local starting port' })).not.toBeInTheDocument()
    await userEvent.type(screen.getByRole('textbox', { name: /^Connection name/ }), 'shared-app')
    await userEvent.type(screen.getByRole('textbox', { name: 'Ports' }), '8080')
    await userEvent.click(screen.getByRole('button', { name: 'Start sharing' }))
    await waitFor(() => expect(requests).toHaveLength(1))
    expect(requests[0].body).toMatchObject({ name: 'service.share', payload: { ports: '8080', peerIds: ['peer-a'] } })
    expect(requests[0].body.payload).not.toHaveProperty('localPort')
  })
  it('keeps a manual escape when a discovered service disappears during review', async () => {
    const observed = { id: 'opaque-service', revision: 'opaque-review', checkedAt: new Date().toISOString(), purpose: 'generic', lifetime: 'until-revoked' as const, peerId: 'peer-a', name: 'tcp 8080', ports: '8080', network: 'tcp' as const, status: 'active' as const }
    const { setState, requests } = setup({ ...state, availableServices: [observed] }); render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), observed.id)
    setState({ ...state, availableServices: [] })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByText(/The current advertisement is unavailable/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The advertised grant changed')
    expect(requests.some(request => request.body.name === 'service.connect')).toBe(false)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), '')
    expect(screen.getByRole('textbox', { name: 'Ports' })).toBeEnabled()
    expect(screen.getByRole('textbox', { name: 'Ports' })).toHaveValue('8080')
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
  })
  it('clears sensitive in-memory drafts when the local session expires', async () => {
    const { fetch } = setup(); render(<App />); await openStudio()
    fireEvent.change(screen.getByRole('textbox', { name: 'Write a message…' }), { target: { value: 'Private draft' } })
    fetch.mockImplementation(async () => new Response('{"code":"unauthenticated"}', { status: 401 }))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByRole('textbox', { name: 'Local access code' }).catch(() => screen.getByLabelText('Local access code'))).toBeInTheDocument()
    expect(screen.queryByDisplayValue('Private draft')).not.toBeInTheDocument()
  })
  it('reviews pause impact and allows cancel before discarding outgoing staging', async () => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: false, directory: '/tmp/received' } }] })
    render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    await userEvent.click(screen.getByRole('button', { name: 'Pause messages and files' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/select them again after resuming/)).toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Pause messages and files' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Pause messages and files' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', paused: true } }))
  })
  it('reviews the exact LAN pairing target and supports offline revoke', async () => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[1], online: false, verified: false, bridge: false }] })
    render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: /Notebook/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    await userEvent.click(screen.getByRole('button', { name: 'Revoke LAN pairing' }))
    expect(within(screen.getByRole('dialog')).getByText('peer-b')).toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Revoke LAN pairing' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Revoke LAN pairing' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'lan.revoke', payload: { peerId: 'peer-b' } }))
  })
  it('opens local details from the map without issuing a command', async () => {
    const { requests } = setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: 'Network graph' }))
    await userEvent.click(screen.getAllByRole('button', { name: /^Open device: Studio;/ })[0])
    expect(screen.getByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Open device' }))
    await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toBeInTheDocument()
  })

  it('keeps an explicit upload alive across map and device-list navigation', async () => {
    setup()
    let signal!: AbortSignal
    let finish!: (result: api.CommandResult) => void
    const upload = vi.spyOn(api, 'upload').mockImplementation((_peer, _files, _request, _progress, nextSignal) => {
      signal = nextSignal
      return new Promise(resolve => { finish = resolve })
    })
    const { container } = render(<App />); await openStudio()
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [new File(['notes'], 'notes.txt', { type: 'text/plain' })] } })
    await userEvent.click(await screen.findByRole('button', { name: 'Send batch' }))
    expect(upload).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: 'Network graph' }))
    expect(signal.aborted).toBe(false)
    expect(container.querySelector('.background-upload')).toHaveTextContent('Studio')
    await userEvent.click(screen.getAllByRole('button', { name: 'Back to devices' })[0])
    expect(signal.aborted).toBe(false)
    await act(async () => { finish({ ok: true }) })
    expect(container.querySelector('.background-upload')).not.toBeInTheDocument()
  })

  it.each(['idle', 'offline'])('allows saved Tailnet to change to LAN from management-only %s state', async status => {
    const { requests } = setup({ ...state, self: { ...state.self, status }, settings: { network: 'tailnet' } })
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Set up network' }))[0])
    await userEvent.click(screen.getByRole('radio', { name: /^LAN/ }))
    await userEvent.click(screen.getByText('Advanced: use an existing relay'))
    expect(screen.getByRole('button', { name: 'Create device identity' })).toBeInTheDocument()
    expect(screen.getByLabelText('Relay address', { exact: false })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Activate selected relay' })).toBeEnabled()
    expect(requests.every(request => request.body.name === 'lan.addresses')).toBe(true)
  })
  it('allows saved LAN to activate Tailnet after an offline restart', async () => {
    const { requests } = setup({ ...state, self: { ...state.self, status: 'idle', name: 'notebook' }, settings: { network: 'lan' } })
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Set up network' }))[0])
    await userEvent.click(screen.getByRole('radio', { name: /^Tailnet/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await waitFor(() => expect(requests.find(request => request.body.name === 'network.configure')?.body).toMatchObject({ name: 'network.configure', payload: { mode: 'tailnet', hostname: 'notebook' } }))
  })

  it('keeps reviewed whole-app Stop available while switching away from a running network', async () => {
    const { requests } = setup(); render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Set up network' }))[0])
    await userEvent.click(screen.getByRole('radio', { name: /^LAN/ }))
    expect(screen.queryByRole('button', { name: 'Start relay' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Stop sobalink' }))
    const review = screen.getByRole('region', { name: 'Stop this app and its connections?' })
    expect(review).toHaveFocus()
    expect(review).toHaveTextContent('active transfers, and service connections')
    await userEvent.click(within(review).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('button', { name: 'Stop sobalink' })).toBeInTheDocument()
    expect(requests).toHaveLength(0)
  })

  it.each([
    { locale: 'en', status: 'idle' },
    { locale: 'en', status: 'offline' },
    { locale: 'ja', status: 'idle' },
    { locale: 'ja', status: 'offline' },
  ] as const)('reviews and cancels whole-app Stop before network setup in $locale/$status', async ({ locale, status }) => {
    localStorage.setItem('sobalink.locale', locale)
    const t = translator(locale)
    const lt = lanTranslator(locale)
    const { requests } = setup({ ...state, self: { ...state.self, status, networks: [] }, peers: [], settings: { network: 'none' } })
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: t('addDevice') }))[0])
    expect(screen.queryByText(t('networkRestart'))).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: t('signInTailscale') })).toBeDisabled()
    for (let attempt = 0; attempt < 2; attempt++) {
      const stop = screen.getByRole('button', { name: lt('stopApplication') })
      expect(stop).toBeEnabled()
      await userEvent.click(stop)
      const review = screen.getByRole('region', { name: lt('stopReview') })
      expect(review).toHaveFocus()
      expect(review).toHaveTextContent(lt('stopImpact'))
      expect(within(review).getByRole('button', { name: lt('confirmStop') })).toBeEnabled()
      expect(requests).toHaveLength(0)
      await userEvent.click(within(review).getByRole('button', { name: t('cancel') }))
      expect(screen.queryByRole('region', { name: lt('stopReview') })).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: lt('stopApplication') })).toBeEnabled()
      expect(requests).toHaveLength(0)
    }
  })

  it('keeps a network name draft when setup closes without activating', async () => {
    const { requests } = setup({ ...state, self: { ...state.self, status: 'idle' }, settings: { network: 'none' } })
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Set up network' }))[0])
    fireEvent.change(screen.getByRole('textbox', { name: /This device name/ }), { target: { value: 'draft-device' } })
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getAllByRole('button', { name: 'Set up network' })[0])
    expect(screen.getByRole('textbox', { name: /This device name/ })).toHaveValue('draft-device')
    expect(requests).toHaveLength(0)
    expect(screen.getByRole('button', { name: 'Sign in to Tailscale' })).toBeDisabled()
  })

  it('keeps explicit network view and details in browser history', async () => {
    setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: 'Network graph' }))
    const diagramRoute = history.state
    expect(screen.getByRole('heading', { name: 'Network graph' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Show device list' }))
    const listRoute = history.state
    expect(screen.getByRole('heading', { name: 'Device list' })).toBeInTheDocument()
    expect(document.title).toBe('Device list · sobalink')
    await userEvent.click(screen.getByRole('button', { name: /^Open device: Studio;/ }))
    const detailRoute = history.state
    expect(screen.getByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
    act(() => { history.replaceState(listRoute, ''); window.dispatchEvent(new PopStateEvent('popstate')) })
    expect(screen.queryByRole('complementary', { name: 'Device details' })).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Device list' })).toBeInTheDocument()
    act(() => { history.replaceState(diagramRoute, ''); window.dispatchEvent(new PopStateEvent('popstate')) })
    expect(screen.getByRole('heading', { name: 'Network graph' })).toBeInTheDocument()
    act(() => { history.replaceState(detailRoute, ''); window.dispatchEvent(new PopStateEvent('popstate')) })
    expect(screen.getByRole('heading', { name: 'Device list' })).toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('complementary', { name: 'Device details' })).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Device list' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Open device: Studio;/ })).toHaveFocus()
  })

  it('opens the device list directly from global navigation without visiting the graph', async () => {
    const { requests } = setup(); render(<App />)
    const navigation = await screen.findByRole('navigation', { name: 'Workspace views' })
    await userEvent.click(within(navigation).getByRole('button', { name: 'Device list' }))
    expect(screen.getByRole('heading', { name: 'Device list' })).toBeInTheDocument()
    expect(document.querySelector('.network-graph-diagram')).not.toBeInTheDocument()
    expect(within(navigation).getByRole('button', { name: 'Device list' })).toHaveAttribute('aria-current', 'page')
    await userEvent.click(screen.getByRole('button', { name: /^Open device: Studio;/ }))
    expect(screen.getByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
    expect(requests).toHaveLength(0)
    await userEvent.click(within(navigation).getByRole('button', { name: 'Network graph' }))
    expect(screen.getByRole('heading', { name: 'Network graph' })).toBeInTheDocument()
    expect(screen.queryByRole('complementary', { name: 'Device details' })).not.toBeInTheDocument()
    expect(within(navigation).getByRole('button', { name: 'Network graph' })).toHaveAttribute('aria-current', 'page')
  })

})

const savedShare: ServiceConfigResult = { revision: 'a'.repeat(64), active: false, configuration: { id: 'saved-share', backend: 'tailnet', name: 'Scoped-share', direction: 'share', network: 'udp', ports: '8080-8089', excludePorts: '8082,8084-8086', peerIds: ['peer-a', 'peer-c'], ttlSeconds: 120, purpose: 'custom', discoverable: true } }
const savedState: State = { ...state, shares: [{ id: 'saved-share', peerId: '', peerIds: ['peer-a', 'peer-c'], name: 'Scoped-share', network: 'udp', ports: '8080-8089', status: 'stopped' }] }
async function openDetails() { await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Open device details' })) }
describe('service drafts and authoritative saved settings', () => {
  it('offers editable unique names across connections and shares and refuses collisions', async () => {
    const { requests } = setup({ ...savedState, services: [{ id: 'existing', peerId: 'peer-c', name: 'connect-Studio', network: 'tcp', status: 'active' }] })
    render(<App />); await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    const name = screen.getByRole('textbox', { name: /^Connection name/ })
    expect(name).toHaveValue('connect-Studio-2')
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8080' } })
    fireEvent.change(name, { target: { value: 'Scoped-share' } })
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Use a new name' }))
    expect(name).toHaveValue('Scoped-share-2')
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    expect(requests[0].body.payload).toMatchObject({ name: 'Scoped-share-2', backend: 'tailnet' })
    expect(requests[0].body.payload).not.toHaveProperty('replaceId')
  })
  it('retains per-peer/mode drafts across Close and Back, then resets explicitly', async () => {
    const { requests } = setup(); render(<App />); await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '80,443' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'Local starting port' }), { target: { value: '8080' } })
    fireEvent.change(screen.getByRole('textbox', { name: /^Connection name/ }), { target: { value: 'Reviewed-name' } })
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await userEvent.click(screen.getByRole('button', { name: /Service host/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    expect(screen.getByRole('textbox', { name: 'Ports' })).toHaveValue('')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    expect(screen.getByRole('textbox', { name: 'Ports' })).toHaveValue('80,443')
    expect(screen.getByRole('textbox', { name: 'Local starting port' })).toHaveValue('8080')
    expect(screen.getByText('127.0.0.1:8081 → Studio:443')).toBeInTheDocument()
    act(() => window.dispatchEvent(new PopStateEvent('popstate')))
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    expect(screen.getByRole('textbox', { name: /^Connection name/ })).toHaveValue('Reviewed-name')
    await userEvent.click(screen.getByRole('button', { name: 'Reset form' }))
    expect(screen.getByRole('textbox', { name: 'Ports' })).toHaveValue('')
    expect(requests).toHaveLength(0)
  })
  it('keeps a manually chosen name when selecting an advertised endpoint', async () => {
    setup({ ...state, availableServices: [{ id: 'discovered', revision: 'discovered-review', checkedAt: new Date().toISOString(), purpose: 'ssh', lifetime: 'until-revoked', peerId: 'peer-a', name: 'ssh', network: 'tcp', ports: '22', status: 'active' }] })
    render(<App />); await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    fireEvent.change(screen.getByRole('textbox', { name: /^Connection name/ }), { target: { value: 'My-name' } })
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), 'discovered')
    expect(screen.getByRole('textbox', { name: /^Connection name/ })).toHaveValue('My-name')
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
    fireEvent.change(screen.getByRole('textbox', { name: 'Local starting port' }), { target: { value: '1024' } })
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
  })
  it('copies only authoritative complete settings and does not start on read or cancel under StrictMode', async () => {
    const { requests } = setup(savedState, { 'saved-share': savedShare })
    render(<StrictMode><App /></StrictMode>); await openDetails()
    await userEvent.click(screen.getByRole('button', { name: 'Copy settings' }))
    expect(await screen.findByRole('textbox', { name: /^Connection name/ })).toHaveValue('Scoped-share-2')
    expect(screen.getByRole('combobox', { name: 'Lifetime' })).toHaveValue('custom')
    expect(screen.getByText('8082,8084-8086')).toBeInTheDocument()
    expect(requests.map(request => request.body.name)).toEqual(['service.config'])
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await userEvent.click(screen.getByRole('button', { name: 'Copy settings' }))
    await screen.findByRole('textbox', { name: 'Ports' })
    await userEvent.click(screen.getByRole('button', { name: 'Start sharing' }))
    const payload = requests.find(request => request.body.name === 'service.share')?.body.payload
    expect(payload).toEqual({ backend: 'tailnet', name: 'Scoped-share-2', network: 'udp', ports: '8080-8089', excludePorts: '8082,8084-8086', peerIds: ['peer-a', 'peer-c'], ttlSeconds: 120, lifetime: 'finite', loopbackHost: '127.0.0.1', purpose: 'custom', discoverable: true })
  })
  it('edits an inactive saved rule with exact revision and rejects a changed saved draft on reopen', async () => {
    const configs = { 'saved-share': savedShare }
    const { requests } = setup(savedState, configs); render(<App />); await openDetails()
    await userEvent.click(screen.getByRole('button', { name: 'Edit and start' }))
    await screen.findByRole('textbox', { name: 'Ports' })
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8080-8090' } })
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    configs['saved-share'] = { ...savedShare, revision: 'b'.repeat(64) }
    await userEvent.click(screen.getByRole('button', { name: 'Edit and start' }))
    expect(await screen.findByText(/These saved settings have changed/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply and start' })).toBeDisabled()
    await userEvent.click(screen.getAllByRole('button', { name: 'Reload saved settings' })[0])
    expect(await screen.findByRole('textbox', { name: 'Ports' })).toHaveValue('8080-8089')
    await userEvent.click(screen.getByRole('button', { name: 'Apply and start' }))
    expect(requests.find(request => request.body.name === 'service.share')?.body.payload).toMatchObject({ replaceId: 'saved-share', expectedRevision: 'b'.repeat(64), ttlSeconds: 120, excludePorts: '8082,8084-8086', discoverable: true, peerIds: ['peer-a', 'peer-c'] })
  })
  it.each(['active', 'backend', 'legacy'] as const)('blocks unsafe saved-rule %s use until reviewed', async reason => {
    const record = { ...savedShare, active: reason === 'active', configuration: { ...savedShare.configuration, backend: reason === 'backend' ? 'lan' as const : reason === 'legacy' ? undefined : 'tailnet' as const } }
    const { requests } = setup(savedState, { 'saved-share': record }); render(<App />); await openDetails()
    await userEvent.click(screen.getByRole('button', { name: 'Edit and start' }))
    expect(await screen.findByRole('button', { name: 'Apply and start' })).toBeDisabled()
    if (reason === 'legacy') {
      await userEvent.click(screen.getByRole('checkbox', { name: /I reviewed the selected network/ }))
      expect(screen.getByRole('button', { name: 'Apply and start' })).toBeEnabled()
    }
    expect(requests.every(request => request.body.name === 'service.config')).toBe(true)
  })
  it('refuses a partial saved-rule response instead of guessing settings', async () => {
    setup(savedState); render(<App />); await openDetails()
    await userEvent.click(screen.getByRole('button', { name: 'Copy settings' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The response could not be read')
    expect(screen.queryByRole('button', { name: 'Start sharing' })).not.toBeInTheDocument()
  })
  it.each([false, true])('edits a receiving folder without changing enabled or paused=%s', async paused => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused, directory: '/tmp/old' } }] })
    render(<App />); await openDetails(); await userEvent.click(screen.getByRole('button', { name: 'Edit folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Receive directory' }), { target: { value: '/tmp/new' } })
    await userEvent.click(screen.getByRole('button', { name: 'Save folder' }))
    expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', directory: '/tmp/new' } })
    expect(requests[0].body.payload).toEqual({ peerId: 'peer-a', directory: '/tmp/new' })
  })
  it('retains unsaved default and automatic receiving folders separately on close', async () => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: true, directory: '/tmp/old' } }] })
    render(<App />); await openDetails()
    await userEvent.click(screen.getByRole('button', { name: 'Edit folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Receive directory' }), { target: { value: '/tmp/peer-draft' } })
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await userEvent.click(screen.getByRole('button', { name: 'Preferences' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Receive directory' }), { target: { value: '/tmp/default-draft' } })
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getByRole('button', { name: 'Edit folder' }))
    expect(screen.getByRole('textbox', { name: 'Receive directory' })).toHaveValue('/tmp/peer-draft')
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getByRole('button', { name: 'Preferences' }))
    expect(screen.getByRole('textbox', { name: 'Receive directory' })).toHaveValue('/tmp/default-draft')
    expect(requests).toHaveLength(0)
  })
  it('clears service drafts after session expiry and successful reauthentication', async () => {
    const { fetch } = setup(); const connected = fetch.getMockImplementation()!
    render(<App />); await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '9191' } })
    fetch.mockImplementation(async () => new Response('{"code":"unauthenticated"}', { status: 401 }))
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await screen.findByLabelText('Local access code')
    fetch.mockImplementation(connected)
    fireEvent.change(screen.getByLabelText('Local access code'), { target: { value: 'test-code' } })
    await userEvent.click(screen.getByRole('button', { name: 'Open sobalink' }))
    await openStudio(); await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    expect(screen.getByRole('textbox', { name: 'Ports' })).toHaveValue('')
  })
})

describe('connectivity-first device view', () => {
  it('opens services before exchange and keeps the message draft through both views', async () => {
    setup(); render(<App />)
    expect(await screen.findByText('Close, even from afar.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /Studio/ }))
    expect(screen.getByRole('region', { name: 'Services and connection' })).toBeVisible()
    expect(screen.queryByRole('textbox', { name: 'Write a message…' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send files' })).toBeEnabled()
    await userEvent.click(screen.getByRole('button', { name: 'Write a message' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Write a message…' }), { target: { value: 'Draft to keep' } })
    await userEvent.click(screen.getByRole('button', { name: 'Services' }))
    expect(screen.queryByRole('textbox', { name: 'Write a message…' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toHaveValue('Draft to keep')
  })
  it('uses increased effective message limits and preserves UTF-8 overflow for review', async () => {
    const limits = { effective: { logical: { messageBytes: { mode: 'unlimited' as const } }, resources: { messageTextBytes: { mode: 'limited' as const, value: 20000 } } }, usage: { materializedListeners: 0 } }
    const { requests } = setup({ ...state, limits }); render(<App />); await openStudio()
    const composer = screen.getByRole('textbox', { name: 'Write a message…' })
    expect(composer).not.toHaveAttribute('maxlength')
    fireEvent.change(composer, { target: { value: 'あ'.repeat(6000) } })
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    await waitFor(() => expect(requests).toHaveLength(1))
    fireEvent.change(composer, { target: { value: 'あ'.repeat(7000) } })
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('displayed UTF-8 byte limit')
    expect(composer).toHaveValue('あ'.repeat(7000))
    expect(requests).toHaveLength(1)
  })
})
