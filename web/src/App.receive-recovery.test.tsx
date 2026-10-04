import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import * as api from './api'
import { recoveryText } from './receive-recovery-i18n'

const recovery: api.ReceiveRecovery = { state: 'blocked', code: 'legacy_review_required', reservedBytes: null, applied: false, review: [...api.RECEIVE_RECOVERY_REVIEW] }
const initial: api.State = {
  csrfToken: 'fixture-csrf', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet', receiveDirectory: '/fixture/received' },
  peers: [{ id: 'fixture-studio', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct', autosave: { enabled: true, paused: false } }],
  receiveRecovery: recovery, messages: [], services: [], shares: [], transfers: [
    { id: 'offer', peerId: 'fixture-studio', direction: 'incoming', name: 'Fixture notes', entries: [{ path: 'notes.txt', kind: 'file', size: 5 }], totalBytes: 5, completedBytes: 0, status: 'offered', createdAt: '2026-10-02T10:00:00Z' },
    { id: 'retry', peerId: 'fixture-studio', direction: 'incoming', name: 'Failed receive', entries: [], totalBytes: 5, completedBytes: 0, status: 'failed', error: 'receive_recovery_required', createdAt: '2026-10-02T10:01:00Z' },
    { id: 'outgoing', peerId: 'fixture-studio', direction: 'outgoing', name: 'Failed outgoing', entries: [], totalBytes: 5, completedBytes: 0, status: 'failed', createdAt: '2026-10-02T10:02:00Z' },
  ],
}
function setup() {
  let state = structuredClone(initial)
  const commands: { name: string; payload: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    const body = JSON.parse(init?.body as string); commands.push(body)
    return new Response(JSON.stringify({ ok: true, ...(body.name === 'receive.recovery.confirm' ? { result: state.receiveRecovery } : {}) }))
  }))
  return { commands, change: (next: api.ReceiveRecovery) => { state = { ...state, receiveRecovery: next } } }
}
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en'); history.replaceState(null, '') })

describe('receive recovery integration', () => {
  it('blocks only incoming accept/retry and labels saved autosave separately', async () => {
    const { commands } = setup(); render(<App />)
    expect(await screen.findByText(recoveryText('en', 'blocked'))).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /Studio/ }))
    expect(screen.getByRole('button', { name: 'Connect to a service' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Send files' })).toBeEnabled()
    await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
    expect(screen.getByRole('button', { name: 'Accept batch' })).toBeDisabled()
    expect(within(screen.getByRole('article', { name: 'Failed receive · Failed' })).getByRole('button', { name: 'Retry transfer' })).toBeDisabled()
    expect(within(screen.getByRole('article', { name: 'Failed outgoing · Failed' })).getByRole('button', { name: 'Retry transfer' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Decline' })).toBeEnabled()
    expect(screen.getByRole('textbox', { name: 'Write a message…' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Attach files' })).toBeEnabled()
    await userEvent.click(screen.getByRole('button', { name: 'Open device details' }))
    const details = screen.getByRole('complementary')
    expect(within(details).getByText(recoveryText('en', 'savedSetting'))).toBeInTheDocument()
    expect(within(details).getByText('Enabled')).toHaveClass('badge-neutral')
    expect(within(details).getByText(recoveryText('en', 'blocked'))).toBeInTheDocument()
    expect(commands).toHaveLength(0)
  })
  it('opens from receiving preferences and returns without changing settings', async () => {
    const { commands } = setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: 'Preferences' }))
    let dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Review receiving' }))
    dialog = screen.getByRole('dialog', { name: 'Receive recovery' })
    await within(dialog).findByRole('checkbox')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Back to preferences' }))
    expect(screen.getByRole('dialog', { name: 'Preferences' })).toBeInTheDocument()
    expect(commands).toHaveLength(1)
    expect(commands[0]).toMatchObject({ name: 'receive.recovery.confirm', payload: { reviewed: false } })
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Review receiving' }))
    await screen.findByRole('checkbox')
    await act(async () => fireEvent(window, new PopStateEvent('popstate', { state: history.state })))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(commands.every(command => command.name === 'receive.recovery.confirm' && !(command.payload as { reviewed: boolean }).reviewed)).toBe(true)
  })
  it('updates actual receive controls after fresh recovery without altering stored permission', async () => {
    const { change, commands } = setup(); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: /Studio/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Files & messages' }))
    expect(screen.getByRole('button', { name: 'Accept batch' })).toBeDisabled()
    change({ ...recovery, state: 'ready', code: '', reservedBytes: 0 })
    await act(async () => document.dispatchEvent(new Event('visibilitychange')))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Accept batch' })).toBeEnabled())
    expect(screen.queryByText(recoveryText('en', 'blocked'))).not.toBeInTheDocument()
    expect(commands).toHaveLength(0)
  })
})
