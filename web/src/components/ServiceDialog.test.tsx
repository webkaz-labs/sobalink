import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import { translator } from '../i18n'
import { serviceText } from '../service-i18n'
import { newServiceDraft, type ServiceDraft } from '../service-form'
import type { Server } from '../useServer'
import { ServiceDialog } from './Dialogs'

function setup(mode: 'connect' | 'share', locale: Locale = 'en', budget = 100) {
  const state: State = { csrfToken: 'fixture', self: { name: 'Fixture device', status: 'online' }, peers: [{ id: 'fixture-peer', name: 'Fixture target', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }], messages: [], transfers: [], services: [], shares: [], settings: { network: 'tailnet' }, servicePresets: [{ id: 'server-example', purpose: 'custom', network: 'tcp', port: 8123, localPort: 18123, label: { en: 'Server example', ja: 'サーバーの例' } }], limits: { effective: { resources: { materializedListeners: { mode: 'limited', value: budget + 2 } } }, usage: { materializedListeners: 2 } } }
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true })
  const onClose = vi.fn()
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn().mockResolvedValue(state), run, updatedAt: null, handleError: vi.fn() }
  let savedDraft: ServiceDraft | undefined
  function Harness() {
    const [draft, onDraft] = useState<ServiceDraft | undefined>(newServiceDraft(state.peers[0], mode, state))
    savedDraft = draft
    return <ServiceDialog t={translator(locale)} locale={locale} onClose={onClose} server={server} peer={state.peers[0]} state={state} mode={mode} draft={draft} onDraft={onDraft} />
  }
  const view = render(<Harness />)
  return { ...view, run, onClose, user: userEvent.setup(), t: translator(locale), s: (key: string) => serviceText(locale, key), draft: () => savedDraft }
}

describe('service setup mirrors', () => {
  it.each(['en', 'ja'] as const)('uses the supplied catalog and keeps example ports editable (%s)', async locale => {
    const view = setup('connect', locale)
    const example = screen.getByRole('combobox', { name: new RegExp(view.s('examples')) })
    expect(screen.getByRole('option', { name: /TCP 8123/ })).toBeInTheDocument()
    await view.user.selectOptions(example, 'server-example')
    expect(screen.getByRole('textbox', { name: view.t('ports') })).toHaveValue('8123')
    expect(screen.getByRole('textbox', { name: view.t('localStart') })).toHaveValue('18123')
    fireEvent.change(screen.getByRole('textbox', { name: view.t('ports') }), { target: { value: '8124' } })
    await view.user.click(screen.getByRole('button', { name: view.t('startConnection') }))
    expect(view.run).toHaveBeenCalledWith('service.connect', expect.objectContaining({ ports: '8124', localPort: 18123, lifetime: 'until-stopped', ttlSeconds: 0 }))
  })
  it('requires deliberate no-expiry selection for a mapped IPv6 share and previews direction', async () => {
    const view = setup('share')
    expect(screen.getByRole('combobox', { name: 'Lifetime' })).toHaveValue('3600')
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8080' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'Application port' }), { target: { value: '3000' } })
    await view.user.click(screen.getByText(view.t('advanced')))
    await view.user.selectOptions(screen.getByRole('combobox', { name: 'Loopback address' }), '::1')
    await view.user.selectOptions(screen.getByRole('combobox', { name: 'Lifetime' }), 'until-revoked')
    expect(screen.getByText('Tailnet:8080').parentElement!.textContent).toBe('Tailnet:8080[::1]:3000')
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: 'Start sharing' }))
    expect(view.run).toHaveBeenCalledWith('service.share', expect.objectContaining({ ports: '8080', localPort: 3000, loopbackHost: '::1', lifetime: 'until-revoked', ttlSeconds: 0, peerIds: ['fixture-peer'] }))
  })
  it('validates custom whole seconds and permits finite periods longer than a day', async () => {
    const view = setup('connect')
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8080' } })
    await view.user.selectOptions(screen.getByRole('combobox', { name: 'Lifetime' }), 'custom')
    const duration = screen.getByRole('spinbutton', { name: 'Duration in seconds' })
    for (const value of ['0', '1.5', '9223372037']) {
      fireEvent.change(duration, { target: { value } })
      expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
      fireEvent.submit(duration.closest('form')!)
      expect(view.run).not.toHaveBeenCalled()
    }
    fireEvent.change(duration, { target: { value: '259200' } })
    await view.user.click(screen.getByRole('button', { name: 'Start connection' }))
    expect(view.run).toHaveBeenCalledWith('service.connect', expect.objectContaining({ lifetime: 'finite', ttlSeconds: 259200 }))
  })
  it('uses the remaining aggregate capacity above the old threshold', () => {
    setup('connect', 'en', 100)
    const ports = screen.getByRole('textbox', { name: 'Ports' })
    fireEvent.change(ports, { target: { value: '8000-8099' } })
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
    expect(screen.getByText('Available listener budget: 100')).toBeInTheDocument()
    fireEvent.change(ports, { target: { value: '8000-8100' } })
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
  })
  it('does not start or renew a service on cancel', async () => {
    const view = setup('share')
    fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8080' } })
    await view.user.selectOptions(screen.getByRole('combobox', { name: 'Lifetime' }), 'until-revoked')
    await view.user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(view.onClose).toHaveBeenCalledOnce()
    expect(view.run).not.toHaveBeenCalled()
    expect(view.draft()?.lifetime).toBe('until-revoked')
  })
})

it('saves a stopped definition while current listener resources are exhausted', async () => {
  const view = setup('connect', 'en', 0)
  fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: '8000-8100' } })
  expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Save without starting' })).toBeEnabled()
  await view.user.click(screen.getByRole('button', { name: 'Save without starting' }))
  expect(view.run).toHaveBeenCalledOnce()
  expect(view.run).toHaveBeenCalledWith('service.save', { configuration: expect.objectContaining({ direction: 'forward', ports: '8000-8100', lifetime: 'until-stopped', ttlSeconds: 0, peerId: 'fixture-peer' }) })
  expect(view.run.mock.calls[0][1]).not.toHaveProperty('replaceId')
  expect(view.onClose).toHaveBeenCalledOnce()
})
it('does not save or start an invalid definition', async () => {
  const view = setup('share')
  fireEvent.change(screen.getByRole('textbox', { name: 'Ports' }), { target: { value: 'not a port' } })
  expect(screen.getByRole('button', { name: 'Start sharing' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Save without starting' })).toBeDisabled()
  expect(view.run).not.toHaveBeenCalled()
})
