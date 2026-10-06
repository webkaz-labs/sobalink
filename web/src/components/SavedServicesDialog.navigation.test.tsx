import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, DefinitionBundle, Locale, ServiceConfiguration, State } from '../api'
import { definitionText } from '../definitions-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { SavedServicesDialog } from './SavedServicesDialog'

const alpha: ServiceConfiguration = { id: 'fixture-alpha', name: 'Alpha-web', direction: 'forward', backend: 'direct-lan', network: 'tcp', ports: '8080', peerId: 'fixture-peer', localPort: 9080, lifetime: 'finite', ttlSeconds: 60, purpose: 'other', discoverable: false }
const beta: ServiceConfiguration = { ...alpha, id: 'fixture-beta', name: 'Beta-files', ports: '9090', localPort: 9091 }
const gamma: ServiceConfiguration = { ...alpha, id: 'fixture-gamma', name: '資料', ports: '8081', localPort: 9081 }
const profile: DefinitionBundle = { version: 1, services: [alpha, beta, gamma], groups: [{ name: 'Sample_Set', serviceIds: [alpha.id, beta.id] }, { name: 'sample_set', serviceIds: [gamma.id] }] }
const state: State = { csrfToken: 'fixture-only', self: { name: 'This device', status: 'online' }, peers: [{ id: 'fixture-peer', name: 'Sample device', networks: ['direct-lan'], online: false, bridge: true, trusted: true, verified: true, path: 'unknown' }], services: [], shares: [], transfers: [], messages: [], settings: { network: 'direct-lan' } }
function harness(locale: Locale = 'en') {
  let current = structuredClone(profile)
  let delayed: Promise<CommandResult | undefined> | undefined
  const result = (value: unknown): CommandResult => ({ ok: true, result: value as CommandResult['result'] })
  const selection = (ids = current.services.map(service => service.id)) => result({ services: current.services.filter(service => ids.includes(service.id)), states: [], revision: 'b'.repeat(64), ready: false, application: 'unverified' })
  const run = vi.fn<Server['run']>(async (name, payload) => {
    if (name === 'profile.export') return result({ profile: current, revision: 'a'.repeat(64), disabled: true })
    if (name === 'service.selection') {
      if (delayed) return delayed
      const target = payload as { group?: string; ids?: string[] }
      return selection(target.ids || current.groups?.find(group => group.name === target.group)?.serviceIds || [])
    }
    if (name === 'services.start' || name === 'services.stop') return result({ states: [], ready: name === 'services.start' })
    throw new Error(`Unexpected command: ${name}`)
  })
  const server: Server = { state, auth: 'ready', stale: false, busy: new Set(), error: null, setError: vi.fn(), run, refresh: vi.fn(), handleError: vi.fn(), updatedAt: null, messageBlock: vi.fn(), messageGuardRevision: 0 }
  const onClose = vi.fn()
  const element = () => <SavedServicesDialog server={server} locale={locale} t={translator(locale)} onClose={onClose} />
  const view = render(element())
  return {
    ...view, run, server, onClose,
    setProfile: (next: DefinitionBundle) => { current = next },
    setStale: (value: boolean) => { server.stale = value; view.rerender(element()) },
    defer: () => { let resolve!: (value: CommandResult) => void; delayed = new Promise(value => { resolve = value }); return () => resolve(selection()) },
  }
}
async function ready(locale: Locale = 'en') { await screen.findByRole('searchbox', { name: definitionText(locale, 'search') }) }
function serviceChecks() { return within(screen.getByRole('group', { name: /Selected services|選択したサービス/ })).queryAllByRole('checkbox') }
function search(query: string, locale: Locale = 'en') { fireEvent.change(screen.getByRole('searchbox', { name: definitionText(locale, 'search') }), { target: { value: query } }) }
function localCommands(run: ReturnType<typeof harness>['run']) { expect(run.mock.calls.every(([command]) => ['profile.export', 'service.selection'].includes(command))).toBe(true) }

describe('saved service quick navigation', () => {
  it.each(['en', 'ja'] as const)('supports labelled keyboard filters and empty results without mutations (%s)', async locale => {
    const view = harness(locale); const d = (key: string) => definitionText(locale, key); await ready(locale)
    search('does-not-exist', locale)
    expect(serviceChecks()).toHaveLength(0)
    expect(screen.getByText(d('noMatches'))).toBeVisible()
    expect(screen.getByRole('button', { name: d('selectVisible') })).toBeDisabled()
    search('  ', locale)
    expect(serviceChecks()).toHaveLength(3)
    const filter = screen.getByRole('button', { name: d('selectedOnly') })
    filter.focus(); await userEvent.keyboard(' ')
    expect(filter).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(d('noSelection'))).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: d('resetFilters') }))
    expect(filter).toHaveAttribute('aria-pressed', 'false')
    expect(serviceChecks()).toHaveLength(3)
    expect(view.run.mock.calls.map(([command]) => command)).toEqual(['profile.export'])
  })
  it('finds exact existing groups, devices and ports while retaining user-supplied names', async () => {
    const view = harness(); await ready()
    search('sample device 9091')
    expect(screen.getByRole('checkbox', { name: /Beta-files/ })).toBeVisible()
    expect(serviceChecks()).toHaveLength(1)
    search('Sample_Set')
    expect(serviceChecks()).toHaveLength(3)
    await userEvent.selectOptions(screen.getByLabelText('Saved group'), 'Sample_Set')
    expect(screen.getByLabelText('Saved group')).toHaveValue('Sample_Set')
    expect(screen.getByRole('searchbox')).toHaveValue('')
    expect(serviceChecks()).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Selected only' })).toHaveAttribute('aria-pressed', 'true')
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    expect(await screen.findByRole('region', { name: 'Review selected services' })).toHaveTextContent('Alpha-web')
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { group: 'Sample_Set' })
    await userEvent.selectOptions(screen.getByLabelText('Saved group'), 'sample_set')
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /資料/ })).toBeChecked()
    expect(serviceChecks()).toHaveLength(1)
    localCommands(view.run)
  })
  it('adds only visible services and preserves hidden selected members through full review and cancellation', async () => {
    const view = harness(); await ready()
    search('Alpha')
    await userEvent.click(screen.getByRole('button', { name: 'Add visible services' }))
    search('Beta')
    expect(screen.getByRole('status')).toHaveTextContent('Selected outside this view: 1')
    await userEvent.click(screen.getByRole('button', { name: 'Add visible services' }))
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Alpha-web'); expect(review).toHaveTextContent('Beta-files'); expect(review).not.toHaveTextContent('資料')
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { ids: [alpha.id, beta.id] })
    search('missing')
    expect(screen.getByRole('status')).toHaveTextContent('Selected outside this view: 2')
    expect(review).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Show selection' }))
    expect(serviceChecks()).toHaveLength(2)
    expect(serviceChecks().every(box => (box as HTMLInputElement).checked)).toBe(true)
    await userEvent.click(within(review).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    localCommands(view.run)
    expect(localStorage.length).toBe(0)
  })
  it('uses the complete reviewed target and revision even when all selected members are filtered out', async () => {
    const view = harness(); await ready()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    search('absent')
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Alpha-web'); expect(review).toHaveTextContent('Beta-files'); expect(review).toHaveTextContent('資料')
    await userEvent.click(within(review).getByRole('button', { name: 'Stop reviewed services' }))
    expect(view.run).toHaveBeenCalledWith('services.stop', { ids: [alpha.id, beta.id, gamma.id], expectedRevision: 'b'.repeat(64) })
  })
  it('invalidates review and leaves group mode when an individual selection changes', async () => {
    const view = harness(); await ready()
    await userEvent.selectOptions(screen.getByLabelText('Saved group'), 'Sample_Set')
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    await screen.findByRole('region', { name: 'Review selected services' })
    await userEvent.click(screen.getByRole('checkbox', { name: /Alpha-web/ }))
    expect(screen.getByLabelText('Saved group')).toHaveValue('')
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Beta-files'); expect(review).not.toHaveTextContent('Alpha-web')
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { ids: [beta.id] })
    localCommands(view.run)
  })
  it('reconciles changed group membership and drops a removed group name on reload', async () => {
    const view = harness(); await ready()
    await userEvent.selectOptions(screen.getByLabelText('Saved group'), 'Sample_Set')
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    await screen.findByRole('region', { name: 'Review selected services' })
    view.setProfile({ ...profile, groups: [{ name: 'Sample_Set', serviceIds: [beta.id, gamma.id] }] })
    await userEvent.click(screen.getByRole('button', { name: 'Reload saved services' })); await ready()
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    expect(screen.queryByRole('checkbox', { name: /Alpha-web/ })).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /資料/ })).toBeChecked()
    expect(screen.getByRole('status')).toHaveTextContent('Selected services: 2')
    view.setProfile({ ...profile, services: [alpha, beta], groups: [{ name: 'Other_Set', serviceIds: [alpha.id] }] })
    await userEvent.click(screen.getByRole('button', { name: 'Reload saved services' })); await ready()
    expect(screen.getByLabelText('Saved group')).toHaveValue('')
    expect(serviceChecks()).toHaveLength(1)
    expect(screen.getByRole('checkbox', { name: /Beta-files/ })).toBeChecked()
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    await screen.findByRole('region', { name: 'Review selected services' })
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { ids: [beta.id] })
    localCommands(view.run)
  })
  it('keeps local search usable when stale but invalidates review until explicit rereview', async () => {
    const view = harness(); await ready()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    await screen.findByRole('region', { name: 'Review selected services' })
    view.setStale(true)
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review start' })).toBeDisabled()
    search('Beta')
    expect(screen.getByRole('checkbox', { name: /Beta-files/ })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: /Beta-files/ })).toBeDisabled()
    view.setStale(false)
    expect(screen.queryByRole('button', { name: 'Start reviewed services' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review start' })).toBeEnabled()
    localCommands(view.run)
  })
  it.each(['selection', 'reload', 'stale', 'close', 'unmount'] as const)('discards a late selection review after %s', async action => {
    const view = harness(); await ready()
    const resolve = view.defer()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    if (action === 'selection') await userEvent.click(screen.getByRole('checkbox', { name: /Alpha-web/ }))
    if (action === 'reload') { await userEvent.click(screen.getByRole('button', { name: 'Reload saved services' })); await ready() }
    if (action === 'stale') { view.setStale(true); view.setStale(false) }
    if (action === 'close') { await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0]); expect(view.onClose).toHaveBeenCalledOnce() }
    if (action === 'unmount') view.unmount()
    await act(async () => { resolve() })
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    localCommands(view.run)
  })
})
