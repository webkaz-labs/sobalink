import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import { lifecycleEnglish, lifecycleJapanese, lifecycleText } from '../lifecycle-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { LogoutControl, ServiceOwnership, StartupGuide } from './LifecycleControls'
const state: State = { csrfToken: 'fictional-token', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet' }, peers: [], services: [{ id: 'service-one', name: 'Example', peerId: 'missing-peer', network: 'tcp', status: 'active' }], shares: [], proxies: [], transfers: [], messages: [] }
function setup(locale: Locale = 'en', failure?: string) {
  const requests: { name: string; payload: unknown }[] = []
  const server = { state, stale: false, handleError: vi.fn() } as unknown as Server
  vi.stubGlobal('fetch', vi.fn(async (_path: string, init: RequestInit) => {
    requests.push(JSON.parse(init.body as string))
    if (failure === 'network_error') throw new Error('Private diagnostic text')
    if (failure) return new Response(JSON.stringify({ code: failure, error: 'Private diagnostic text' }), { status: 409 })
    return new Response(JSON.stringify({ ok: true, result: { state: 'logged-out', logoutConfirmed: true, localTrafficStopped: true, applicationState: 'stopping' } }))
  }))
  return { requests, props: { server, locale, t: translator(locale), blocked: false, onStopping: vi.fn() }, l: (key: string) => lifecycleText(locale, key) }
}
describe('reviewed lifecycle controls', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: distinguishes explicit ownership without a renewable lease`, () => {
      const l = (key: string) => lifecycleText(locale, key)
      render(<ServiceOwnership locale={locale} service={{ owner: 'manual-owner', leaseSeconds: 0 }} />)
      expect(screen.getByText(l('ownerHint'))).toBeVisible()
      expect(screen.queryByText(l('taskHint'))).not.toBeInTheDocument()
      expect(document.body).not.toHaveTextContent(`${l('lease')}: 0`)
    })
  }
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: reviews logout impact, cancels without a command, and explicitly confirms once`, async () => {
      const { props, l, requests } = setup(locale); render(<LogoutControl {...props} />)
      await userEvent.click(screen.getByRole('button', { name: l('logout') }))
      expect(screen.getByRole('region')).toHaveTextContent(l('logoutImpact'))
      expect(screen.getByRole('region')).toHaveTextContent(l('logoutPreserved'))
      expect(screen.getByRole('region')).toHaveTextContent(`${l('activeServices')}1`)
      await userEvent.click(screen.getByRole('button', { name: translator(locale)('cancel') }))
      expect(requests).toHaveLength(0)
      await userEvent.click(screen.getByRole('button', { name: l('logout') }))
      fireEvent.click(screen.getByRole('button', { name: l('confirmLogout') }))
      await screen.findByText(l('confirmed'))
      expect(requests).toHaveLength(1)
      expect(requests[0]).toMatchObject({ name: 'network.logout', payload: {} })
      expect(props.onStopping).toHaveBeenCalledWith(true)
      expect(screen.queryByRole('button', { name: l('confirmLogout') })).not.toBeInTheDocument()
    })
    it(`${locale}: explains CLI-only startup, previews selected mode/action, and copies without executing`, async () => {
      const l = (key: string) => lifecycleText(locale, key)
      const close = vi.fn(); const copy = vi.fn().mockResolvedValue(undefined)
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: copy } })
      const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
      render(<StartupGuide locale={locale} t={translator(locale)} onClose={close} />)
      expect(screen.getByText(l('registrationStatus'))).toBeVisible()
      expect(screen.getByText(l('startupIntro'))).toBeVisible()
      await userEvent.selectOptions(screen.getByLabelText(l('mode')), 'offline')
      await userEvent.selectOptions(screen.getByLabelText(l('action')), 'disable')
      expect(screen.getByLabelText(l('previewCLI'))).toHaveValue('soba --state-dir "STATE_DIRECTORY" autostart disable --startup offline --json')
      expect(screen.getByLabelText(l('applyCLI'))).toHaveValue('soba --state-dir "STATE_DIRECTORY" autostart disable --startup offline --apply --review REVIEW_TOKEN')
      for (const key of ['mode', 'action']) expect(screen.getByRole('combobox', { name: l(key) })).toHaveAccessibleName(l(key))
      for (const key of ['previewCLI', 'applyCLI']) {
        const input = screen.getByRole('textbox', { name: l(key) }) as HTMLTextAreaElement
        expect(input.labels).toHaveLength(1)
        expect(input.labels![0].textContent).toBe(l(key))
        expect(input.labels![0].control).toBe(input)
      }
      await userEvent.click(screen.getAllByRole('button', { name: l('copy') })[0])
      expect(copy).toHaveBeenCalledWith('soba --state-dir "STATE_DIRECTORY" autostart disable --startup offline --json')
      expect(fetch).not.toHaveBeenCalled()
      await userEvent.click(screen.getAllByRole('button', { name: translator(locale)('close') })[0])
      expect(close).toHaveBeenCalledOnce()
    })
  }
  it.each(['tailnet_logout_unconfirmed', 'network_error', 'logout_cleanup_unconfirmed'])('keeps %s truthful and offers explicit same-profile recovery', async failure => {
    const { props, l, requests } = setup('en', failure); render(<LogoutControl {...props} />)
    await userEvent.click(screen.getByRole('button', { name: l('logout') }))
    await userEvent.click(screen.getByRole('button', { name: l('confirmLogout') }))
    expect(await screen.findByRole('status')).toHaveTextContent(l(failure === 'logout_cleanup_unconfirmed' ? 'cleanup' : 'unconfirmed'))
    expect(document.body).not.toHaveTextContent('Private diagnostic text')
    expect(screen.getByLabelText(l('logout'))).toHaveValue('soba --state-dir "STATE_DIRECTORY" logout')
    expect(requests).toHaveLength(1)
  })
  it('allows correction when the saved Tailnet network is unavailable', async () => {
    const { props, l } = setup('en', 'tailnet_logout_unavailable'); render(<LogoutControl {...props} />)
    await userEvent.click(screen.getByRole('button', { name: l('logout') }))
    await userEvent.click(screen.getByRole('button', { name: l('confirmLogout') }))
    await screen.findByText(l('unavailable'))
    expect(props.onStopping).toHaveBeenLastCalledWith(false)
    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.getByRole('button', { name: l('logout') })).toBeEnabled()
  })
  it('keeps LAN pairing separate and renders supplied ownership without inventing a lease', () => {
    expect(Object.keys(lifecycleJapanese)).toEqual(Object.keys(lifecycleEnglish))
    const { props, l } = setup()
    render(<><LogoutControl {...props} server={{ ...props.server, state: { ...state, settings: { network: 'lan' } } }} /><ServiceOwnership locale="en" service={{ owner: 'task-fictional', leaseSeconds: 30, leaseExpiresAt: '2026-10-03T05:00:00Z' }} /></>)
    expect(screen.queryByRole('button', { name: l('logout') })).not.toBeInTheDocument()
    expect(screen.getByText('task-fictional')).toBeVisible()
    expect(screen.getByText(l('taskHint'))).toBeVisible()
    expect(screen.getByText('2026-10-03T05:00:00Z')).toBeVisible()
  })
  it('leaves copyable commands visible when clipboard permission is unavailable', async () => {
    const copy = vi.fn().mockRejectedValue(new Error('denied')); Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: copy } })
    render(<StartupGuide locale="en" t={translator('en')} onClose={() => {}} />)
    await userEvent.click(screen.getAllByRole('button', { name: 'Copy command' })[0])
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(lifecycleText('en', 'copyFailed')))
    expect(screen.getByLabelText(lifecycleText('en', 'previewCLI'))).toHaveValue('soba --state-dir "STATE_DIRECTORY" autostart enable --startup saved --json')
  })
})
