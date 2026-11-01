import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, DefinitionBundle, Locale, ServiceConfiguration, State } from '../api'
import { definitionText } from '../definitions-i18n'
import { translator } from '../i18n'
import { savedPeerText, savedServicePeers } from '../saved-peer-filter'
import type { Server } from '../useServer'
import { SavedServicesDialog } from './SavedServicesDialog'

const web: ServiceConfiguration = { id: 'web', name: 'Sample web', direction: 'forward', network: 'tcp', ports: '8080', peerId: 'device-a', localPort: 9080, ttlSeconds: 60, purpose: 'web', discoverable: false }
const ssh: ServiceConfiguration = { ...web, id: 'ssh', name: 'Sample SSH', peerId: 'device-b', ports: '22', localPort: 9022 }
const share: ServiceConfiguration = { ...web, id: 'share', name: 'Sample share', direction: 'share', peerId: undefined, peerIds: ['device-a', 'device-b'] }
const profile: DefinitionBundle = { version: 1, services: [web, ssh, share], groups: [{ name: 'Sample group', serviceIds: ['web', 'ssh'] }] }
function harness(locale: Locale) {
  const state: State = { csrfToken: 'fixture', self: { name: 'This device', status: 'online' }, peers: [{ id: 'device-a', name: 'Sample device', networks: ['tailnet'], online: false, bridge: true, trusted: true, verified: true, path: 'unknown' }], services: [], shares: [], transfers: [], messages: [], settings: { network: 'tailnet' } }
  const run = vi.fn<Server['run']>(async (name, payload) => {
    let result: unknown
    if (name === 'profile.export') result = { profile, revision: 'a'.repeat(64), disabled: true }
    else if (name === 'service.selection') {
      const target = payload as { ids?: string[]; group?: string }
      const ids = target.group ? ['web', 'ssh'] : target.ids || []
      result = { services: profile.services.filter(item => ids.includes(item.id)), states: [], revision: 'b'.repeat(64), ready: false, application: 'unverified' }
    } else throw new Error(`Unexpected mutation: ${name}`)
    return { ok: true, result } as CommandResult
  })
  const onClose = vi.fn()
  const server: Server = { state, auth: 'ready', stale: false, busy: new Set(), error: null, setError: vi.fn(), run, refresh: vi.fn(), handleError: vi.fn(), updatedAt: null, messageBlock: vi.fn(), messageGuardRevision: 0 }
  render(<SavedServicesDialog server={server} locale={locale} t={translator(locale)} onClose={onClose} />)
  return { run, onClose }
}
const visibleChecks = (locale: Locale) => within(screen.getByRole('group', { name: new RegExp(definitionText(locale, 'selected')) })).queryAllByRole('checkbox')

describe('saved service device filter', () => {
  it('matches exact saved identities without inventing a default peer', () => {
    expect(savedServicePeers(web)).toEqual(['device-a'])
    expect(savedServicePeers(share)).toEqual(['device-a', 'device-b'])
    expect(savedServicePeers({ ...web, peerId: undefined })).toEqual([])
    expect(savedServicePeers({ ...share, peerIds: [] })).toEqual([])
  })
  it.each(['en', 'ja'] as const)('filters without mutation, preserves hidden selection, and reviews exact group scope (%s)', async locale => {
    const { run, onClose } = harness(locale)
    const d = (key: string) => definitionText(locale, key)
    const filter = await screen.findByRole('combobox', { name: savedPeerText(locale, 'label') })
    expect(within(filter).getByRole('option', { name: `${savedPeerText(locale, 'unavailable')} · device-b` })).toBeVisible()
    await userEvent.selectOptions(filter, 'device-a')
    expect(visibleChecks(locale)).toHaveLength(2)
    await userEvent.click(screen.getByRole('checkbox', { name: /Sample web/ }))
    await userEvent.selectOptions(filter, 'device-b')
    expect(visibleChecks(locale)).toHaveLength(2)
    expect(screen.queryByRole('checkbox', { name: /Sample web/ })).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(`${d('hiddenSelected')}: 1`)
    await userEvent.click(screen.getByRole('button', { name: d('showSelection') }))
    expect(filter).toHaveValue('')
    expect(screen.getByRole('checkbox', { name: /Sample web/ })).toBeChecked()
    await userEvent.selectOptions(filter, 'device-a')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: d('group') }), 'Sample group')
    expect(filter).toHaveValue('')
    expect(visibleChecks(locale)).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: d('reviewStart') }))
    expect(run).toHaveBeenLastCalledWith('service.selection', { group: 'Sample group' })
    // Filtering during a review changes presentation, never its exact target.
    await userEvent.selectOptions(filter, 'device-a')
    const review = screen.getByRole('region', { name: d('scope') })
    expect(review).toHaveTextContent('Sample web')
    expect(review).toHaveTextContent('Sample SSH')
    expect(run.mock.calls.map(([name]) => name)).toEqual(['profile.export', 'service.selection'])
    await userEvent.click(screen.getByRole('button', { name: d('resetFilters') }))
    expect(filter).toHaveValue('')
    expect(visibleChecks(locale)).toHaveLength(3)
    const dialog = screen.getByRole('dialog')
    fireEvent(dialog, new Event('cancel', { bubbles: false, cancelable: true }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(run.mock.calls.map(([name]) => name)).toEqual(['profile.export', 'service.selection'])
  })
})
