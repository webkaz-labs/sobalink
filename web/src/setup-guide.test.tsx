import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { State } from './api'
import { SetupGuide } from './components/SetupGuide'
import { setupNetworkStage, setupText } from './setup-guide'
import { translator } from './i18n'
import { App } from './App'
const state: State = { csrfToken: 'fixture-only', self: { name: 'This device', status: 'idle' }, settings: { network: 'none' }, peers: [], services: [], shares: [], messages: [], transfers: [] }
describe('setup guidance', () => {
  it('distinguishes no configuration, stopped, waiting, ready and stale without trusting cached readiness', () => {
    expect(setupNetworkStage(state, false)).toBe('first')
    const configured = { ...state, settings: { network: 'tailnet' as const } }
    expect(setupNetworkStage(configured, false)).toBe('waiting')
    expect(setupNetworkStage({ ...configured, self: { ...state.self, status: 'NeedsLogin' } }, false)).toBe('waiting')
    expect(setupNetworkStage({ ...configured, self: { ...state.self, status: 'running' } }, false)).toBe('ready')
    expect(setupNetworkStage({ ...configured, self: { ...state.self, status: 'running' } }, true)).toBe('stale')
    expect(setupNetworkStage({ ...configured, self: { ...state.self, guidance: { code: 'network_stopped' } as State['self']['guidance'] } }, false)).toBe('stopped')
  })
  it.each(['en', 'ja'] as const)('has navigation-only actions, truthful application guidance and stale/stop gates (%s)', locale => {
    const network = vi.fn(), saved = vi.fn(), devices = vi.fn(), refresh = vi.fn()
    const props = { state, stale: false, stopping: false, locale, t: translator(locale), network, saved, devices, refresh }
    const { rerender } = render(<SetupGuide {...props} />)
    expect(network).not.toHaveBeenCalled()
    expect(saved).not.toHaveBeenCalled()
    expect(screen.getByText(setupText(locale, 'checkHint'))).toBeInTheDocument()
    expect(screen.getByRole('button', { name: setupText(locale, 'devices') })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: setupText(locale, 'saved') }))
    expect(saved).toHaveBeenCalledTimes(1)
    rerender(<SetupGuide {...props} stale />)
    expect(screen.getByRole('button', { name: setupText(locale, 'saved') })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: setupText(locale, 'refresh') }))
    expect(refresh).toHaveBeenCalledTimes(1)
    rerender(<SetupGuide {...props} stopping />)
    expect(screen.getByRole('button', { name: translator(locale)('addDevice') })).toBeDisabled()
  })
  it.each(['en', 'ja'] as const)('opens Tailnet settings without configure/login and returns safely (%s)', async locale => {
    localStorage.setItem('sobalink.locale', locale)
    const commands: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      commands.push(JSON.parse(init?.body as string).name)
      return new Response(JSON.stringify({ ok: true }))
    }))
    render(<App />)
    const guide = await screen.findByRole('region', { name: setupText(locale, 'title') })
    fireEvent.click(within(guide).getByRole('button', { name: translator(locale)('addDevice') }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: setupText(locale, 'tailnet') }))
    expect(within(dialog).getByRole('button', { name: translator(locale)('signInTailscale') })).toBeDisabled()
    expect(commands).toEqual([])
    fireEvent(dialog, new Event('cancel', { cancelable: true }))
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    fireEvent.click(within(guide).getByRole('button', { name: translator(locale)('addDevice') }))
    expect(await screen.findByRole('button', { name: setupText(locale, 'tailnet') })).toBeInTheDocument()
    expect(commands).toEqual([])
  })
})
