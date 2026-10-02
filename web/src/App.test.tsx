import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { State } from './api'

const state: State = {
  csrfToken: 'test-csrf', self: { name: 'This device', status: 'online', networks: ['tailnet'] },
  peers: [
    { id: 'peer-a', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' },
    { id: 'peer-b', name: 'Notebook', networks: ['lan'], online: true, verified: true, trusted: false, bridge: true, path: 'direct' },
    { id: 'peer-c', name: 'Service host', networks: ['tailnet'], online: true, verified: true, trusted: false, bridge: false, path: 'relay' },
  ], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' },
}
function setup(initial = state) {
  const requests: { path: string; body: Record<string, unknown> }[] = []
  let current = structuredClone(initial)
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.body) requests.push({ path, body: JSON.parse(init.body as string) })
    return new Response(JSON.stringify(path === '/api/state' ? current : { ok: true }), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return { requests, fetch, setState: (next: State) => { current = next } }
}
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en') })
async function openStudio() { await userEvent.click(await screen.findByRole('button', { name: /Studio/ })) }

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
    expect(within(dialog).getByText(/at most 64/)).toBeInTheDocument()
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
    const { requests } = setup({ ...state, availableServices: [{ id: 'opaque-service', peerId: 'peer-a', name: 'tcp 8080', ports: '8080', network: 'tcp', status: 'active', application: 'unverified' }] })
    render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), 'opaque-service')
    expect(screen.getByRole('textbox', { name: 'Ports' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'service.connect', payload: { serviceId: 'opaque-service', peerId: 'peer-a', ports: '8080', network: 'tcp', name: 'tcp-8080' } }))
    expect(await screen.findByRole('complementary', { name: 'Device details' })).toBeInTheDocument()
  })
  it('preserves long Japanese drafts and enforces the same UTF-8 byte limit as the server', async () => {
    const { requests } = setup(); render(<App />); await openStudio()
    const input = screen.getByRole('textbox', { name: 'Write a message…' })
    const text = 'あ'.repeat(6000)
    fireEvent.change(input, { target: { value: text } })
    await userEvent.click(screen.getByRole('button', { name: 'Send message' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/16 KiB/)
    expect(requests).toHaveLength(0)
    expect(input).toHaveValue(text)
  })
  it('makes peer-wide pause explicit and disables new exchanges until resumed', async () => {
    const pausedState: State = { ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: true, directory: '/tmp/received' } }] }
    const { requests } = setup(pausedState); render(<App />); await openStudio()
    expect(screen.getByText(/Messages and file transfers with this device are paused/)).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', enabled: true, paused: false, directory: '/tmp/received' } }))
  })
  it('turns off automatic receiving without silently resuming a paused peer', async () => {
    const { requests } = setup({ ...state, peers: [{ ...state.peers[0], autosave: { enabled: true, paused: true, directory: '/tmp/received' } }] })
    render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    await userEvent.click(screen.getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(requests[0].body).toMatchObject({ name: 'peer.autosave', payload: { peerId: 'peer-a', enabled: false, paused: true, directory: '/tmp/received' } }))
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
    const observed = { id: 'opaque-service', peerId: 'peer-a', name: 'tcp 8080', ports: '8080', network: 'tcp' as const, status: 'active' as const }
    const { setState } = setup({ ...state, availableServices: [observed] }); render(<App />); await openStudio()
    await userEvent.click(screen.getByRole('button', { name: 'Connect to a service' }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Available services' }), observed.id)
    setState({ ...state, availableServices: [] })
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await screen.findByText(/This service is no longer advertised/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
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
})
