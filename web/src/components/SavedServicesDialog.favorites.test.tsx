import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, DefinitionBundle, FavoriteReference, FavoritesView, Locale, ServiceConfiguration, State } from '../api'
import { definitionText } from '../definitions-i18n'
import { favoriteKey } from '../favorites'
import { favoriteText } from '../favorites-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { SavedServicesDialog } from './SavedServicesDialog'

const alpha: ServiceConfiguration = { id: 'sample-alpha', name: 'Alpha-web', direction: 'forward', backend: 'direct-lan', network: 'tcp', ports: '8080', peerId: 'sample-peer', localPort: 9080, lifetime: 'finite', ttlSeconds: 60, purpose: 'other', discoverable: false }
const beta = { ...alpha, id: 'sample-beta', name: 'Beta-files', ports: '9090', localPort: 9091 }
const gamma = { ...alpha, id: 'sample-gamma', name: '資料', ports: '8081', localPort: 9081 }
const profile: DefinitionBundle = { version: 1, services: [alpha, beta, gamma], groups: [{ name: 'Sample_Set', serviceIds: [alpha.id, beta.id] }, { name: 'sample_set', serviceIds: [gamma.id] }] }
const state: State = { csrfToken: 'fixture-only', self: { name: 'This device', status: 'online' }, peers: [], services: [], shares: [], transfers: [], messages: [], settings: { network: 'direct-lan' } }
const initialFavorites: FavoritesView = { version: 1, revision: 'a'.repeat(64), entries: [{ kind: 'service', serviceId: alpha.id, available: true }, { kind: 'group', groupName: 'Sample_Set', available: true }], durabilityUncertain: false }
const result = (value: unknown): CommandResult => ({ ok: true, result: value as CommandResult['result'] })
function deferred() { let resolve!: (value: CommandResult | undefined) => void; const promise = new Promise<CommandResult | undefined>(done => { resolve = done }); return { promise, resolve } }
function harness(locale: Locale = 'en') {
  let current = structuredClone(profile)
  let favorites: unknown = structuredClone(initialFavorites)
  let pendingList: ReturnType<typeof deferred> | undefined
  let pendingChange: ReturnType<typeof deferred> | undefined
  let changeFailure = false
  const run = vi.fn<Server['run']>(async (name, payload) => {
    if (name === 'profile.export') return result({ profile: current, revision: 'b'.repeat(64), disabled: true })
    if (name === 'favorites.list') return pendingList ? pendingList.promise : favorites === undefined ? undefined : result(favorites)
    if (name === 'favorites.add' || name === 'favorites.remove') {
      if (pendingChange) return pendingChange.promise
      if (changeFailure) return undefined
      const request = payload as { reference: FavoriteReference; expectedRevision: string }
      const next = structuredClone(favorites) as FavoritesView
      next.entries = next.entries.filter(entry => favoriteKey(entry) !== favoriteKey(request.reference))
      if (name === 'favorites.add') next.entries.push({ ...request.reference, available: true })
      next.revision = 'c'.repeat(64); favorites = next
      return result(next)
    }
    if (name === 'service.selection') {
      const target = payload as { group?: string; ids?: string[] }
      const ids = target.ids || current.groups?.find(group => group.name === target.group)?.serviceIds || []
      return result({ services: current.services.filter(service => ids.includes(service.id)), states: [], revision: 'd'.repeat(64), ready: false, application: 'unverified' })
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
    setFavorites: (next: unknown) => { favorites = next },
    setStale: (value: boolean) => { server.stale = value; view.rerender(element()) },
    setBusy: (value: string[]) => { server.busy = new Set(value); view.rerender(element()) },
    setChangeFailure: (code = 'favorites_revision_conflict') => { changeFailure = true; server.error = { code }; view.rerender(element()) },
    deferList: () => { pendingList = deferred(); return pendingList },
    resumeList: () => { pendingList = undefined },
    deferChange: () => { pendingChange = deferred(); return pendingChange },
  }
}
async function ready(locale: Locale = 'en') { await screen.findByRole('searchbox', { name: definitionText(locale, 'search') }) }
async function open(locale: Locale = 'en') { await ready(locale); await userEvent.click(screen.getByRole('button', { name: favoriteText(locale, 'title') })) }
async function loaded(locale: Locale = 'en') { await screen.findByLabelText(favoriteText(locale, 'addTarget')) }
function panel(locale: Locale = 'en') { return within(screen.getByRole('region', { name: favoriteText(locale, 'title') })) }
function checks() { return within(screen.getByRole('group', { name: /Selected services|選択したサービス/ })).queryAllByRole('checkbox') }
function noAuthorityChanges(run: ReturnType<typeof harness>['run']) { expect(run.mock.calls.every(([name]) => ['profile.export', 'favorites.list', 'favorites.add', 'favorites.remove', 'service.selection'].includes(name))).toBe(true) }
function uniqueKeys(run: ReturnType<typeof harness>['run']) { const keys = run.mock.calls.filter(([name]) => name.startsWith('favorites.')).map(([, , key]) => key); expect(keys.every(key => /^favorites:/.test(key || ''))).toBe(true); expect(new Set(keys).size).toBe(keys.length) }

describe('saved favorites controls', () => {
  it('does not read preferences until opened, or change ordinary navigation', async () => {
    const view = harness(); await ready()
    expect(view.run.mock.calls.map(([name]) => name)).toEqual(['profile.export'])
    expect(screen.getByRole('button', { name: 'Favorites' })).toHaveAttribute('aria-expanded', 'false')
    expect(checks()).toHaveLength(3)
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    expect(checks().every(box => (box as HTMLInputElement).checked)).toBe(true)
  })
  it.each(['en', 'ja'] as const)('adds an exact group and service using inert revision-bound marks (%s)', async locale => {
    const view = harness(locale); await ready(locale)
    const toggle = screen.getByRole('button', { name: favoriteText(locale, 'title') })
    toggle.focus(); await userEvent.keyboard(' '); await loaded(locale)
    await userEvent.selectOptions(screen.getByLabelText(favoriteText(locale, 'addTarget')), favoriteKey({ kind: 'group', groupName: 'sample_set' }))
    await userEvent.click(panel(locale).getByRole('button', { name: favoriteText(locale, 'add') })); await loaded(locale)
    expect(view.run).toHaveBeenCalledWith('favorites.add', { reference: { kind: 'group', groupName: 'sample_set' }, expectedRevision: 'a'.repeat(64) }, expect.any(String))
    await userEvent.selectOptions(screen.getByLabelText(favoriteText(locale, 'addTarget')), favoriteKey({ kind: 'service', serviceId: gamma.id }))
    await userEvent.click(panel(locale).getByRole('button', { name: favoriteText(locale, 'add') })); await loaded(locale)
    expect(view.run).toHaveBeenCalledWith('favorites.add', { reference: { kind: 'service', serviceId: gamma.id }, expectedRevision: 'c'.repeat(64) }, expect.any(String))
    expect(view.run.mock.calls.filter(([name]) => name.startsWith('favorites.')).map(([name]) => name)).toEqual(['favorites.list', 'favorites.add', 'favorites.list', 'favorites.add', 'favorites.list'])
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0)
    noAuthorityChanges(view.run); uniqueKeys(view.run)
  })
  it('uses current full group scope and revision after navigating an inert group favorite', async () => {
    const view = harness(); await open(); await loaded()
    view.setProfile({ ...profile, groups: [{ name: 'Sample_Set', serviceIds: [beta.id, gamma.id] }] })
    await userEvent.click(panel().getByRole('button', { name: 'Select favorite: Group · Sample_Set' }))
    expect(screen.getByLabelText('Saved group')).toHaveValue('Sample_Set')
    expect(view.run.mock.calls.filter(([name]) => name === 'services.start')).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Review start' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Beta-files'); expect(review).toHaveTextContent('資料'); expect(review).not.toHaveTextContent('Alpha-web')
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { group: 'Sample_Set' })
    await userEvent.click(within(review).getByRole('button', { name: 'Start reviewed services' }))
    expect(view.run).toHaveBeenCalledWith('services.start', { group: 'Sample_Set', expectedRevision: 'd'.repeat(64) })
  })
  it('preserves hidden selections during favorite filtering and always reviews the full target', async () => {
    const view = harness(); await ready()
    await userEvent.click(screen.getByRole('button', { name: 'Select all' })); await open(); await loaded()
    await userEvent.click(panel().getByRole('button', { name: 'Favorite services only' }))
    expect(checks()).toHaveLength(1); expect(screen.getByText(/Selected outside this view: 2/)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Review stop' }))
    const review = await screen.findByRole('region', { name: 'Review selected services' })
    expect(review).toHaveTextContent('Alpha-web'); expect(review).toHaveTextContent('Beta-files'); expect(review).toHaveTextContent('資料')
    expect(view.run).toHaveBeenLastCalledWith('service.selection', { ids: [alpha.id, beta.id, gamma.id] })
    await userEvent.click(screen.getByRole('button', { name: 'Show selection' }))
    expect(checks()).toHaveLength(3)
    expect(panel().getByRole('button', { name: 'Favorite services only' })).toHaveAttribute('aria-pressed', 'false')
    await userEvent.click(panel().getByRole('button', { name: 'Select favorite: Service · Alpha-web' }))
    expect(screen.queryByRole('region', { name: 'Review selected services' })).not.toBeInTheDocument()
    expect(checks()).toHaveLength(1); expect(checks()[0]).toBeChecked()
    noAuthorityChanges(view.run)
  })
  it('retains missing service and exact group references until explicit removal', async () => {
    const view = harness()
    view.setFavorites({ ...initialFavorites, entries: [{ kind: 'service', serviceId: 'missing-service', available: false }, { kind: 'group', groupName: 'Missing_Set', available: false }] })
    await open(); await loaded()
    expect(panel().getByRole('button', { name: 'Select favorite: Service · missing-service' })).toBeDisabled()
    expect(panel().getByRole('button', { name: 'Select favorite: Group · Missing_Set' })).toBeDisabled()
    expect(panel().getAllByText('Definition missing')).toHaveLength(2)
    await userEvent.click(panel().getByRole('button', { name: 'Remove favorite: Group · Missing_Set' })); await loaded()
    expect(view.run).toHaveBeenCalledWith('favorites.remove', { reference: { kind: 'group', groupName: 'Missing_Set' }, expectedRevision: initialFavorites.revision }, expect.any(String))
    expect(panel().getByRole('button', { name: 'Remove favorite: Service · missing-service' })).toBeEnabled()
    noAuthorityChanges(view.run)
  })
  it.each([undefined, { ...initialFavorites, entries: null }])('leaves ordinary saved navigation usable when preference loading fails', async value => {
    const view = harness(); view.setFavorites(value); await open()
    expect(await screen.findByText(favoriteText('en', 'loadFailed'))).toBeVisible()
    expect(screen.queryByText(favoriteText('en', 'empty'))).not.toBeInTheDocument()
    expect(checks()).toHaveLength(3)
    await userEvent.click(screen.getByRole('button', { name: 'Select all' }))
    expect(checks().every(box => (box as HTMLInputElement).checked)).toBe(true)
    view.setFavorites(initialFavorites)
    await userEvent.click(panel().getByRole('button', { name: 'Reload favorites' })); await loaded()
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it('requires reload and review after a revision conflict, with no automatic replay', async () => {
    const view = harness(); await open(); await loaded(); view.setChangeFailure()
    await userEvent.click(panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' }))
    expect(await screen.findByText(favoriteText('en', 'conflict'))).toBeVisible()
    expect(screen.queryByLabelText(favoriteText('en', 'addTarget'))).not.toBeInTheDocument()
    expect(view.run.mock.calls.filter(([name]) => name.startsWith('favorites.')).map(([name]) => name)).toEqual(['favorites.list', 'favorites.remove'])
    view.setFavorites({ ...initialFavorites, revision: 'e'.repeat(64), entries: [] })
    await userEvent.click(panel().getByRole('button', { name: 'Reload favorites' })); await loaded()
    expect(screen.getByText(favoriteText('en', 'empty'))).toBeVisible()
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it('never uses a successful mutation response instead of a fresh list', async () => {
    const view = harness(); await open(); await loaded()
    const pending = view.deferChange()
    const remove = panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' })
    fireEvent.click(remove); fireEvent.click(remove)
    expect(view.run.mock.calls.filter(([name]) => name === 'favorites.remove')).toHaveLength(1)
    const concurrent = { ...initialFavorites, revision: 'e'.repeat(64), entries: [{ kind: 'group', groupName: 'sample_set', available: true }] }
    view.setFavorites(concurrent)
    await act(async () => pending.resolve(result({ ...initialFavorites, entries: [] }))); await loaded()
    expect(panel().getByRole('button', { name: 'Select favorite: Group · sample_set' })).toBeVisible()
    expect(panel().queryByRole('button', { name: 'Select favorite: Service · Alpha-web' })).not.toBeInTheDocument()
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it('requires fresh loading when the read after a mutation fails', async () => {
    const view = harness(); await open(); await loaded(); const pending = view.deferChange()
    await userEvent.click(panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' }))
    view.setFavorites(undefined)
    await act(async () => pending.resolve(result({ ...initialFavorites, entries: [] })))
    expect(await screen.findByText(favoriteText('en', 'changeFailed'))).toBeVisible()
    expect(screen.queryByLabelText(favoriteText('en', 'addTarget'))).not.toBeInTheDocument()
    expect(checks()).toHaveLength(3); uniqueKeys(view.run)
  })
  it('shows uncertain persistence and permits only a deliberate fresh-revision reconciliation', async () => {
    const view = harness(); view.setFavorites({ ...initialFavorites, durabilityUncertain: true }); await open(); await loaded()
    expect(screen.getByText(favoriteText('en', 'uncertain'))).toBeVisible()
    expect(view.run.mock.calls.filter(([name]) => name.startsWith('favorites.')).map(([name]) => name)).toEqual(['favorites.list'])
    await userEvent.click(panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' })); await loaded()
    expect(view.run).toHaveBeenCalledWith('favorites.remove', { reference: { kind: 'service', serviceId: alpha.id }, expectedRevision: initialFavorites.revision }, expect.any(String))
    noAuthorityChanges(view.run)
  })
  it('blocks preference edits and navigation during ordinary saved-service work', async () => {
    const view = harness(); await open(); await loaded(); view.setBusy(['services.start'])
    expect(panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' })).toBeDisabled()
    expect(panel().getByRole('button', { name: 'Select favorite: Group · Sample_Set' })).toBeDisabled()
    expect(panel().getByRole('button', { name: 'Reload favorites' })).toBeDisabled()
    // The optional panel can always be dismissed without changing selection.
    await userEvent.click(screen.getByRole('button', { name: 'Favorites' }))
    expect(screen.queryByRole('region', { name: 'Favorites' })).not.toBeInTheDocument()
    noAuthorityChanges(view.run)
  })
  it('waits for an older preference request before freshly loading a reopened panel', async () => {
    const view = harness(); await ready(); view.setBusy(['favorites:previous'])
    await open()
    expect(screen.getByText(favoriteText('en', 'loading'))).toBeVisible()
    expect(view.run.mock.calls.filter(([name]) => name === 'favorites.list')).toHaveLength(0)
    view.setBusy([]); await loaded()
    expect(view.run.mock.calls.filter(([name]) => name === 'favorites.list')).toHaveLength(1)
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it('clears a stale preference filter and needs an explicit fresh read on recovery', async () => {
    const view = harness(); await open(); await loaded()
    await userEvent.click(panel().getByRole('button', { name: 'Favorite services only' }))
    expect(checks()).toHaveLength(1)
    view.setStale(true)
    expect(checks()).toHaveLength(3)
    expect(panel().getByRole('button', { name: 'Reload favorites' })).toBeDisabled()
    view.setStale(false)
    expect(screen.queryByLabelText(favoriteText('en', 'addTarget'))).not.toBeInTheDocument()
    expect(view.run.mock.calls.filter(([name]) => name === 'favorites.list')).toHaveLength(1)
    await userEvent.click(panel().getByRole('button', { name: 'Reload favorites' })); await loaded()
    expect(view.run.mock.calls.filter(([name]) => name === 'favorites.list')).toHaveLength(2)
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it.each(['collapse', 'close', 'unmount', 'stale', 'profile reload'] as const)('discards late favorites after %s', async action => {
    const view = harness(); const pending = view.deferList(); await open()
    if (action === 'collapse') await userEvent.click(screen.getByRole('button', { name: 'Favorites' }))
    if (action === 'close') { await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0]); expect(view.onClose).toHaveBeenCalledOnce() }
    if (action === 'unmount') view.unmount()
    if (action === 'stale') { view.setStale(true); view.setStale(false) }
    if (action === 'profile reload') { view.resumeList(); view.setFavorites({ ...initialFavorites, entries: [] }); await userEvent.click(screen.getByRole('button', { name: 'Reload saved services' })); await loaded() }
    await act(async () => pending.resolve(result(initialFavorites)))
    expect(screen.queryByRole('button', { name: 'Select favorite: Service · Alpha-web' })).not.toBeInTheDocument()
    if (action === 'stale') {
      expect(screen.getByText(favoriteText('en', 'stale'))).toBeVisible()
      view.resumeList(); await userEvent.click(panel().getByRole('button', { name: 'Reload favorites' })); await loaded()
      expect(panel().getByRole('button', { name: 'Select favorite: Service · Alpha-web' })).toBeVisible()
    }
    uniqueKeys(view.run); noAuthorityChanges(view.run)
  })
  it('does not follow a late mutation with another list after closing', async () => {
    const view = harness(); await open(); await loaded(); const pending = view.deferChange()
    await userEvent.click(panel().getByRole('button', { name: 'Remove favorite: Service · Alpha-web' }))
    await userEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    await act(async () => pending.resolve(result({ ...initialFavorites, entries: [] })))
    expect(view.run.mock.calls.filter(([name]) => name.startsWith('favorites.')).map(([name]) => name)).toEqual(['favorites.list', 'favorites.remove'])
    expect(screen.queryByRole('region', { name: 'Favorites' })).not.toBeInTheDocument()
  })
})
