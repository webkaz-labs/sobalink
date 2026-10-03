import { useState } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, Locale, ServiceConfiguration, StartupList, StartupReview, State } from '../api'
import { translator } from '../i18n'
import { startupEnglish, startupJapanese, startupText } from '../startup-i18n'
import type { Server } from '../useServer'
import { StartupProfilesDialog } from './StartupProfilesDialog'
const service: ServiceConfiguration = { id: 'fixture-outbound', name: 'fixture-ssh', direction: 'forward', backend: 'tailnet', peerId: 'fixture-peer', network: 'tcp', ports: '22', localPort: 2222, loopbackHost: '127.0.0.1', lifetime: 'finite', ttlSeconds: 120, purpose: 'ssh', discoverable: false }
const state: State = { csrfToken: 'fictional-control', self: { name: 'Notebook', status: 'online' }, peers: [], settings: { network: 'tailnet' }, services: [], shares: [], transfers: [], messages: [] }
const empty: StartupList = { entries: [], revision: 'a'.repeat(64), suppressed: false }
const planned: StartupReview = { name: 'fixture-startup', ids: [service.id], services: [service], enabled: false, network: 'tailnet', hostname: 'Notebook', revision: 'b'.repeat(64), storeRevision: empty.revision, selectionRevision: 'c'.repeat(64) }
function harness(locale: Locale = 'en', initial = empty, failure = '', selected = [service]) {
  let stored = structuredClone(initial)
  const run = vi.fn<Server['run']>(); const onClose = vi.fn()
  function Harness({ snapshot }: { snapshot?: StartupList }) {
    const [error, setError] = useState<unknown>(null)
    run.mockImplementation(async (name, payload) => {
      setError(null)
      if (name === failure) { setError({ code: 'private_settings_unavailable' }); return }
      let value: unknown
      if (name === 'startup.list') value = stored
      if (name === 'startup.preview') value = { ...planned, name: (payload as { name: string }).name, storeRevision: stored.revision }
      if (name === 'startup.save') { stored = { ...stored, revision: 'd'.repeat(64), entries: [{ ...planned, enabled: true, valid: true, state: 'saved' }] }; value = stored }
      if (name === 'startup.disable') { stored = { ...stored, revision: 'e'.repeat(64), entries: stored.entries.map(entry => ({ ...entry, enabled: false, state: 'disabled' })) }; value = stored }
      return { ok: true, result: value as CommandResult['result'] }
    })
    const server = { state: { ...state, startup: snapshot }, error, setError, run, auth: 'ready', stale: false, busy: new Set(), refresh: vi.fn(), handleError: vi.fn(), updatedAt: null } as Server
    return <StartupProfilesDialog server={server} locale={locale} t={translator(locale)} onClose={onClose} target={{ ids: selected.map(item => item.id) }} selected={selected} />
  }
  const view = render(<Harness />)
  return { ...view, run, onClose, update: (snapshot: StartupList) => view.rerender(<Harness snapshot={snapshot} />) }
}
async function prepare(locale: Locale = 'en') {
  const s = (key: string) => startupText(locale, key)
  await waitFor(() => expect(screen.getByRole('button', { name: s('reload') })).toBeEnabled())
  fireEvent.change(screen.getByLabelText(s('name')), { target: { value: 'fixture-startup' } })
  await userEvent.click(screen.getByRole('button', { name: s('review') }))
  return screen.findByRole('region', { name: s('scope') })
}
describe('explicit future outbound startup', () => {
  it.each(['en', 'ja'] as const)('reviews exact finite scope before save and never starts now (%s)', async locale => {
    const s = (key: string) => startupText(locale, key); const view = harness(locale)
    expect(screen.queryByText(s('operationPending'))).not.toBeInTheDocument()
    const review = await prepare(locale)
    expect(review).toHaveTextContent('fixture-peer'); expect(review).toHaveTextContent('127.0.0.1:2222 → 22'); expect(review).toHaveTextContent('120')
    expect(review).toHaveTextContent(s('effect')); expect(review).toHaveTextContent(s('frozen'))
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['startup.list', 'startup.preview'])
    await userEvent.click(within(review).getByRole('button', { name: s('confirm') }))
    expect(await screen.findByRole('status')).toHaveTextContent(s('saved'))
    expect(view.run).toHaveBeenCalledWith('startup.save', { name: planned.name, ids: [service.id], expectedRevision: planned.revision, expectedStoreRevision: empty.revision })
    expect(view.run.mock.calls.some(call => ['services.start', 'service.connect', 'network.configure'].includes(call[0]))).toBe(false)
  })
  it('cancels reviewed approval without mutation and rejects selections containing shares', async () => {
    const view = harness(); await prepare(); await userEvent.click(screen.getByRole('button', { name: 'Back to selection' }))
    expect(view.run.mock.calls.some(call => call[0] === 'startup.save')).toBe(false)
    view.unmount(); harness('en', empty, '', [{ ...service, direction: 'share' }])
    expect(await screen.findByText(startupText('en', 'selectionRequired'))).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Review future startup' })).not.toBeInTheDocument()
  })
  it('requires explicit replacement of an existing approval', async () => {
    const stored = { ...empty, entries: [{ ...planned, enabled: true, valid: true, state: 'saved' }] }; const view = harness('en', stored)
    const review = await prepare()
    expect(within(review).getByRole('button', { name: startupText('en', 'confirm') })).toBeDisabled()
    await userEvent.click(within(review).getByRole('checkbox', { name: startupText('en', 'replace') }))
    await userEvent.click(within(review).getByRole('button', { name: startupText('en', 'confirm') }))
    await screen.findByText(startupText('en', 'saved')); expect(view.run.mock.calls.some(call => call[0] === 'startup.save')).toBe(true)
  })
  it('invalidates review when another session changes the private store', async () => {
    const view = harness(); await prepare(); view.update({ ...empty, revision: 'f'.repeat(64) })
    expect(await screen.findByText(startupText('en', 'scopeChanged'))).toBeVisible()
    expect(screen.queryByRole('button', { name: startupText('en', 'confirm') })).not.toBeInTheDocument()
    expect(view.run.mock.calls.some(call => call[0] === 'startup.save')).toBe(false)
  })
  it.each(['startup.save', 'startup.disable'])('does not claim success after failed persistence in %s', async command => {
    const initial = command === 'startup.disable' ? { ...empty, entries: [{ ...planned, enabled: true, valid: true, state: 'started' }] } : empty
    const view = harness('en', initial, command)
    if (command === 'startup.save') { await prepare(); await userEvent.click(screen.getByRole('button', { name: startupText('en', 'confirm') })) }
    else { await userEvent.click(await screen.findByRole('button', { name: startupText('en', 'disable') })); await userEvent.click(screen.getByRole('button', { name: startupText('en', 'disableConfirm') })) }
    expect(await screen.findByRole('alert')).toHaveTextContent(startupText('en', 'private_settings_unavailable'))
    expect(screen.queryByText(startupText('en', command === 'startup.save' ? 'saved' : 'disabledDone'))).not.toBeInTheDocument()
    expect(view.run.mock.calls.some(call => ['service.stop', 'services.stop'].includes(call[0]))).toBe(false)
  })
  it('reviews disabling without stopping services and shows offline suppression', async () => {
    const view = harness('ja', { ...empty, suppressed: true, entries: [{ ...planned, enabled: true, valid: true, state: 'started' }] }); const s = (key: string) => startupText('ja', key)
    expect(await screen.findByText(s('suppressed'))).toBeVisible()
    expect(screen.getByText(s('started'), { exact: false })).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: s('disable') }))
    expect(screen.getByRole('region', { name: s('disableTitle') })).toHaveTextContent(s('disableHint'))
    await userEvent.click(screen.getByRole('button', { name: 'キャンセル' }))
    expect(view.run.mock.calls.some(call => call[0] === 'startup.disable')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: s('disable') })); await userEvent.click(screen.getByRole('button', { name: s('disableConfirm') }))
    expect(await screen.findByRole('status')).toHaveTextContent(s('disabledDone'))
    expect(view.run).toHaveBeenCalledWith('startup.disable', { name: planned.name, expectedStoreRevision: empty.revision })
  })
  it('keeps every new help and recovery label bilingual', () => expect(Object.keys(startupJapanese)).toEqual(Object.keys(startupEnglish)))
})
