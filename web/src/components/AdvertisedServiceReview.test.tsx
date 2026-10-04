import { useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Service, State } from '../api'
import { serviceText } from '../service-i18n'
import { translator } from '../i18n'
import { advertisedDraft, freshAdvertisedDraft, matchesAdvertisedDraft, newServiceDraft, servicePayload, type ServiceDraft } from '../service-form'
import type { Server } from '../useServer'
import { ServiceDialog } from './Dialogs'
const peer = { id: 'fixture-peer', name: 'Studio', networks: ['tailnet' as const], online: true, verified: true, trusted: true, bridge: true, path: 'direct' as const }
const state: State = { csrfToken: 'fixture', self: { name: 'Notebook', status: 'online' }, peers: [peer], settings: { network: 'tailnet' }, services: [], shares: [], messages: [], transfers: [] }
function observed(): Service { return { id: 'grant-current', peerId: peer.id, name: 'TCP 8080', network: 'tcp', ports: '8080', status: 'active', purpose: 'web', checkedAt: new Date().toISOString(), revision: 'opaque-reviewed-metadata', lifetime: 'finite', expiresAt: new Date(Date.now() + 300_000).toISOString() } }
function setup(current: Service, locale: 'en' | 'ja' = 'en') {
  let latestAdvertisement = current
  const run = vi.fn<Server['run']>().mockImplementation(async name => name === 'discovery.refresh' ? { ok: true, result: { services: [latestAdvertisement], observations: [], partial: false } } : { ok: true })
  let lastDraft: ServiceDraft | undefined
  function Harness({ advertisement }: { advertisement: Service }) {
    latestAdvertisement = advertisement
    const [draft, setDraft] = useState(newServiceDraft(peer, 'connect', state)); lastDraft = draft
    const next = { ...state, availableServices: [advertisement] }
    const server: Server = { state: next, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run, updatedAt: null, messageBlock: vi.fn().mockResolvedValue(null), messageGuardRevision: 0, handleError: vi.fn() }
    return <ServiceDialog server={server} t={translator(locale)} locale={locale} peer={peer} state={next} mode="connect" onClose={() => {}} draft={draft} onDraft={value => setDraft(value || newServiceDraft(peer, 'connect', state))} />
  }
  const view = render(<Harness advertisement={current} />)
  return { ...view, run, draft: () => lastDraft!, update: (advertisement: Service) => view.rerender(<Harness advertisement={advertisement} />), t: translator(locale) }
}
describe('advertised grant review binding', () => {
  it.each(['en', 'ja'] as const)('preserves purpose and review metadata until explicit manual selection (%s)', async locale => {
    const advertisement = observed(); const view = setup(advertisement, locale)
    await userEvent.selectOptions(screen.getByLabelText(view.t('availableServices')), advertisement.id)
    await userEvent.click(screen.getByText(view.t('advanced')))
    expect(screen.getByLabelText(view.t('purpose'))).toBeDisabled()
    expect(screen.getByLabelText(view.t('purpose'))).toHaveValue('web')
    expect(screen.getByText(advertisement.checkedAt!, { exact: false })).toBeVisible()
    expect(view.draft().serviceRevision).toBe(advertisement.revision)
    await userEvent.selectOptions(screen.getByLabelText(view.t('availableServices')), '')
    expect(view.draft().serviceId).toBe(''); expect(view.draft().serviceRevision).toBeUndefined()
    expect(screen.getByLabelText(view.t('purpose'))).toBeEnabled()
  })
  it.each(['purpose', 'ports', 'expiry', 'lifetime', 'grant'] as const)('blocks a changed %s without falling back to manual', async change => {
    const advertisement = observed(); const view = setup(advertisement)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), advertisement.id)
    const updated = { ...advertisement }
    if (change === 'purpose') updated.purpose = 'ssh'
    if (change === 'ports') updated.ports = '8081'
    if (change === 'expiry') updated.expiresAt = new Date(Date.now() + 60_000).toISOString()
    if (change === 'lifetime') updated.lifetime = 'until-revoked'
    if (change === 'grant') updated.id = 'replacement-grant'
    view.update(updated)
    if (change === 'grant') {
      await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('The advertised grant changed')
      expect(view.run.mock.calls.some(call => call[0] === 'service.connect')).toBe(false)
    } else { expect(screen.getByRole('button', { name: 'Start connection' })).toBeDisabled(); expect(view.run).not.toHaveBeenCalled() }
    expect(view.draft().serviceId).toBe('grant-current')
  })
  it('refreshes a long review and accepts a renewed later expiry only for the same scope', async () => {
    const advertisement = { ...observed(), checkedAt: new Date(Date.now() - 60_000).toISOString() }; const view = setup(advertisement)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), advertisement.id)
    view.update({ ...advertisement, revision: 'refreshed-review', checkedAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 600_000).toISOString() })
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    expect(view.run).toHaveBeenCalledWith('service.connect', expect.objectContaining({ serviceId: advertisement.id, serviceRevision: 'refreshed-review', purpose: 'web' }))
  })
  it('preserves a historical review when saving stopped, with no discovery request', async () => {
    const advertisement = { ...observed(), checkedAt: new Date(Date.now() - 60_000).toISOString() }; const view = setup(advertisement)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), advertisement.id)
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
    view.run.mockResolvedValueOnce(undefined)
    await userEvent.click(screen.getByRole('button', { name: 'Save without starting' }))
    expect(view.run).toHaveBeenCalledWith('service.save', expect.objectContaining({ configuration: expect.objectContaining({ serviceRevision: advertisement.revision, serviceId: advertisement.id }) }))
    view.update({ ...advertisement, checkedAt: new Date().toISOString(), revision: 'fresh-review' })
    await userEvent.click(screen.getByRole('button', { name: 'Refresh advertised review' }))
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
    expect(view.draft().serviceRevision).toBe('fresh-review')
  })

  it.each(['purpose', 'expiry', 'lifetime', 'grant'] as const)('never connects when targeted refresh changes %s', async field => {
    const original = observed(); const view = setup(original)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), original.id)
    const next = { ...original, revision: 'fresh-review' }
    if (field === 'purpose') next.purpose = 'ssh'
    if (field === 'expiry') next.expiresAt = new Date(Date.now() + 20_000).toISOString()
    if (field === 'lifetime') next.lifetime = 'until-revoked'
    if (field === 'grant') next.id = 'new-grant'
    view.run.mockResolvedValueOnce({ ok: true, result: { services: [next], observations: [], partial: false } })
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The advertised grant changed')
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['discovery.refresh'])
    expect(view.draft().serviceRevision).toBe(original.revision)
  })
  it('ignores discovery refresh after an edit or dismissal', async () => {
    let resolve!: (value: { ok: boolean; result: { services: Service[]; observations: never[] } }) => void
    const pending = new Promise<{ ok: boolean; result: { services: Service[]; observations: never[] } }>(done => { resolve = done })
    const original = observed(); const view = setup(original)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), original.id)
    view.run.mockReturnValueOnce(pending)
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    fireEvent.change(screen.getByRole('textbox', { name: /Connection name/ }), { target: { value: 'changed-name' } })
    resolve({ ok: true, result: { services: [original], observations: [] } }); await pending
    await waitFor(() => expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled())
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['discovery.refresh'])
    let resolveLate!: (value: { ok: boolean; result: { services: Service[]; observations: never[] } }) => void
    const late = new Promise<{ ok: boolean; result: { services: Service[]; observations: never[] } }>(done => { resolveLate = done })
    view.run.mockReturnValueOnce(late); await userEvent.click(screen.getByRole('button', { name: 'Start connection' })); view.unmount()
    resolveLate({ ok: true, result: { services: [original], observations: [] } }); await late
    expect(view.run.mock.calls.every(call => call[0] === 'discovery.refresh')).toBe(true)
  })

  for (const locale of ['en', 'ja'] as const) {
    it.each(['discovery_network_unavailable', 'discovery_unsupported', 'discovery_capacity'])(`localizes discovery recovery without connecting (${locale}: %s)`, async code => {
      const original = observed(); const view = setup(original, locale)
      await userEvent.selectOptions(screen.getByLabelText(view.t('availableServices')), original.id)
      view.run.mockResolvedValueOnce({ ok: true, result: { services: [], observations: [{ peerId: peer.id, state: 'unconfirmed', code, services: 0 }], partial: true } })
      await userEvent.click(screen.getByRole('button', { name: view.t('startConnection') }))
      expect(await screen.findByRole('alert')).toHaveTextContent(serviceText(locale, code))
      expect(view.run.mock.calls.map(call => call[0])).toEqual(['discovery.refresh'])
      expect(view.draft().serviceId).toBe(original.id)
    })
  }

  it('freezes the submitted scope after discovery so edits cannot hide a successful mutation', async () => {
    let resolve!: (value: { ok: boolean }) => void
    const pending = new Promise<{ ok: boolean }>(done => { resolve = done })
    const original = observed(); const view = setup(original)
    await userEvent.selectOptions(screen.getByLabelText('Available services'), original.id)
    view.run.mockResolvedValueOnce({ ok: true, result: { services: [original], observations: [], partial: false } }).mockReturnValueOnce(pending)
    await userEvent.click(screen.getByRole('button', { name: 'Start connection' }))
    await waitFor(() => expect(view.run.mock.calls.map(call => call[0])).toEqual(['discovery.refresh', 'service.connect']))
    expect(screen.getByRole('textbox', { name: /Connection name/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reset form' })).toBeDisabled()
    expect(screen.getByText(/Closing this dialog does not undo it/)).toBeVisible()
    resolve({ ok: true }); await pending
    await waitFor(() => expect(screen.getByRole('textbox', { name: /Connection name/ })).toBeEnabled())
  })
  it('uses exact same-lifetime scope comparison without treating a metadata revision as a bearer token', () => {
    const advertisement = observed(); const draft = { ...newServiceDraft(peer, 'connect', state), ...advertisedDraft(advertisement) }
    expect(matchesAdvertisedDraft(draft, { ...advertisement, revision: 'next', checkedAt: new Date().toISOString() })).toBe(true)
    expect(freshAdvertisedDraft(draft)).toBe(true)
    expect(servicePayload(draft, 'connect').serviceRevision).toBe('opaque-reviewed-metadata')
  })
})
