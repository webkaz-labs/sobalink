import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { State } from './api'
import { liveSetup, setupScopes } from './nearby-fixture'
import { nearbyText } from './nearby-i18n'

const state: State = { csrfToken: 'synthetic-csrf', self: { name: 'Synthetic local device', status: 'idle', networks: [] }, settings: { network: 'none' }, peers: [], services: [], shares: [], transfers: [], messages: [] }
beforeEach(() => { localStorage.setItem('sobalink.locale', 'en') })
describe('nearby setup navigation ownership', () => {
  it.each(['en', 'ja'] as const)('%s: entry explains request approval and optional fingerprint comparison', async locale => {
    localStorage.setItem('sobalink.locale', locale)
    const commands: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      commands.push(JSON.parse(init!.body as string).name)
      return new Response(JSON.stringify({ ok: true }))
    }))
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: locale === 'ja' ? 'ネットワークを設定' : 'Set up network' }))[0])
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByText(nearbyText(locale, 'entryIntro'))).toBeInTheDocument()
    expect(dialog.queryByText(nearbyText(locale, 'intro'))).not.toBeInTheDocument()
    expect(nearbyText(locale, 'entryIntro')).toContain(nearbyText(locale, 'requestMode'))
    expect(nearbyText(locale, 'entryIntro')).toContain(nearbyText(locale, 'receiveMode'))
    expect(nearbyText(locale, 'entryIntro')).toContain('TOFU')
    expect(nearbyText(locale, 'intro')).toContain(locale === 'ja' ? '両画面で承認' : 'approve on both screens')
    expect(nearbyText(locale, 'fullMode')).toContain('SHA-256')
    expect(commands).toEqual([])
  })
  it.each(['close', 'back', 'history', 'escape'] as const)('waits for exact-session cancellation on %s', async navigation => {
    const live = liveSetup(), commands: { name: string; payload: unknown }[] = []
    let finish!: (response: Response) => void
    const cancellation = new Promise<Response>(resolve => { finish = resolve })
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      const request = JSON.parse(init!.body as string); commands.push(request)
      if (request.name === 'setup.cancel') return cancellation
      return new Response(JSON.stringify({ ok: true, result: request.name === 'setup.scopes' ? setupScopes : live }))
    }))
    render(<App />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Set up network' }))[0])
    expect(screen.queryByRole('radio', { name: /^Tailnet/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: nearbyText('en', 'open') }))
    await screen.findByText('Peer B')
    if (navigation === 'close') await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close' }))
    if (navigation === 'back') await userEvent.click(screen.getByRole('button', { name: nearbyText('en', 'back') }))
    if (navigation === 'escape') fireEvent(screen.getByRole('dialog'), new Event('cancel', { cancelable: true }))
    if (navigation === 'history') act(() => window.dispatchEvent(new PopStateEvent('popstate')))
    await waitFor(() => expect(commands.filter(item => item.name === 'setup.cancel')).toEqual([expect.objectContaining({ name: 'setup.cancel', payload: { sessionId: live.sessionId } })]))
    expect(screen.getByRole('region', { name: nearbyText('en', 'title') })).toBeInTheDocument()
    await act(async () => finish(new Response(JSON.stringify({ ok: true, result: { ...live, state: 'cancelled', discovery: { state: 'stopped' } } }))))
    await waitFor(() => expect(screen.queryByRole('region', { name: nearbyText('en', 'title') })).not.toBeInTheDocument())
    if (navigation === 'back') expect(screen.getByRole('button', { name: nearbyText('en', 'advanced') })).toBeInTheDocument()
    else expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(commands.some(item => ['peer.trust', 'network.configure'].includes(item.name))).toBe(false)
  })
})
