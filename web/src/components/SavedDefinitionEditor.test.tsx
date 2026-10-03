import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { Locale, ServiceConfiguration, State } from '../api'
import { savedEditorEnglish, savedEditorJapanese, savedEditorText } from '../saved-editor-i18n'
import { translator } from '../i18n'
import { serviceText } from '../service-i18n'
import type { Server } from '../useServer'
import { SavedDefinitionEditor } from './SavedDefinitionEditor'
const revision = 'a'.repeat(64)
const configuration: ServiceConfiguration = { id: 'saved-one', name: 'offline-example', direction: 'forward', backend: 'tailnet', peerId: 'missing-peer', network: 'tcp', ports: '8443', loopbackHost: '::1', lifetime: 'until-stopped', ttlSeconds: 0, purpose: 'custom', discoverable: false }
const state: State = { csrfToken: 'fixture', self: { name: 'Notebook', status: 'offline' }, settings: { network: 'none' }, peers: [], services: [], shares: [], transfers: [], messages: [] }
function setup(locale: Locale = 'en', source?: 'edit' | 'copy') {
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload) => name === 'service.config' ? { ok: true, result: { configuration, revision, active: false } } : { ok: true, result: { configuration: { ...(payload as any).configuration, id: 'saved-result' }, revision, active: false } })
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run, handleError: vi.fn(), updatedAt: null }
  return { props: { server, locale, t: translator(locale), mode: 'connect' as const, services: source ? [configuration] : [], source: source ? { id: configuration.id, intent: source } : undefined, onSaved: vi.fn(), onClose: vi.fn() }, e: (key: string) => savedEditorText(locale, key), run }
}
describe('offline saved-definition management', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: creates a reviewed stopped definition without any visible peer or active network`, async () => {
      const { props, e, run } = setup(locale); render(<SavedDefinitionEditor {...props} />)
      await userEvent.type(screen.getByLabelText(translator(locale)('ruleName')), 'new-offline')
      await userEvent.selectOptions(screen.getByLabelText(new RegExp(e('backend'))), 'lan')
      await userEvent.type(screen.getByLabelText(new RegExp(e('peers'))), 'missing-device')
      await userEvent.type(screen.getByLabelText(translator(locale)('ports')), '8080')
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      const review = screen.getByRole('region', { name: e('reviewTitle') })
      expect(review).toHaveTextContent('missing-device'); expect(review).toHaveTextContent('lan')
      expect(run).not.toHaveBeenCalled()
      await userEvent.click(screen.getByRole('button', { name: e('back') }))
      expect(run).not.toHaveBeenCalled()
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
      expect(run).toHaveBeenCalledWith('service.save', expect.objectContaining({ configuration: expect.objectContaining({ backend: 'lan', peerId: 'missing-device', ports: '8080', direction: 'forward' }) }))
      expect(run.mock.calls.some(([name]) => ['service.connect', 'service.share', 'services.start'].includes(name))).toBe(false)
      expect(props.onSaved).toHaveBeenCalledOnce()
    })
    it(`${locale}: copies orphaned definitions without losing metadata and edits only the authoritative revision`, async () => {
      const { props, e, run } = setup(locale, 'copy'); const view = render(<SavedDefinitionEditor {...props} />)
      await waitFor(() => expect(screen.getByLabelText(translator(locale)('ruleName'))).toHaveValue('offline-example-2'))
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
      const copy = run.mock.calls.find(([name]) => name === 'service.save')?.[1] as any
      expect(copy.configuration).toMatchObject({ peerId: 'missing-peer', loopbackHost: '::1', purpose: 'custom' })
      expect(copy.configuration.id).toBeUndefined(); expect(copy.expectedRevision).toBeUndefined()
      view.unmount()
      const edit = setup(locale, 'edit'); render(<SavedDefinitionEditor {...edit.props} />)
      await screen.findByDisplayValue('offline-example')
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
      expect(edit.run).toHaveBeenCalledWith('service.save', expect.objectContaining({ expectedRevision: revision, configuration: expect.objectContaining({ id: 'saved-one', peerId: 'missing-peer' }) }))
    })
  }
  it('keeps missing identities explicit, prevents duplicate share references and lets cancellation leave storage unchanged', async () => {
    expect(Object.keys(savedEditorJapanese)).toEqual(Object.keys(savedEditorEnglish))
    const { props, e, run } = setup(); render(<SavedDefinitionEditor {...props} mode="share" />)
    await userEvent.type(screen.getByLabelText(translator('en')('ruleName')), 'offline-share')
    await userEvent.selectOptions(screen.getByLabelText(/Saved network/), 'tailnet')
    await userEvent.type(screen.getByLabelText(/Exact device IDs/), 'missing-peer, missing-peer')
    await userEvent.type(screen.getByLabelText('Ports'), '8080')
    expect(screen.getByRole('button', { name: e('review') })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(run).not.toHaveBeenCalled(); expect(props.onClose).toHaveBeenCalledOnce()
  })
  it('keeps failed revision saves open and requires reloading before another reviewed save', async () => {
    const { props, e, run } = setup('en', 'edit'); const view = render(<SavedDefinitionEditor {...props} />)
    await screen.findByDisplayValue('offline-example')
    run.mockResolvedValueOnce(undefined)
    await userEvent.click(screen.getByRole('button', { name: e('review') }))
    await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
    view.rerender(<SavedDefinitionEditor {...props} server={{ ...props.server, error: { code: 'service_revision_conflict' } }} />)
    expect(screen.getByRole('button', { name: e('review') })).toBeDisabled()
    expect(props.onSaved).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: e('reload') })).toBeEnabled()
  })
  it('exposes edit, copy and delete for orphaned definitions from the global catalog and cancels deletion', async () => {
    const requests: { name: string; payload: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      const request = JSON.parse(init!.body as string); requests.push(request)
      const result = request.name === 'profile.export' ? { profile: { version: 1, services: [configuration], groups: [] }, revision, disabled: true } : request.name === 'service.config' ? { configuration, revision, active: false } : { groups: [], revision }
      return new Response(JSON.stringify({ ok: true, result }))
    }))
    localStorage.setItem('sobalink.locale', 'en'); render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: 'Saved services' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Copy saved definition' }))
    await screen.findByDisplayValue('offline-example-2')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await userEvent.click(screen.getByRole('button', { name: 'Remove definition' }))
    await screen.findByRole('button', { name: serviceText('en', 'removeConfirm') })
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(requests.some(request => request.name === 'service.delete')).toBe(false)
    await userEvent.click(await screen.findByRole('button', { name: 'Remove definition' }))
    await userEvent.click(await screen.findByRole('button', { name: serviceText('en', 'removeConfirm') }))
    await waitFor(() => expect(requests.filter(request => request.name === 'service.delete')).toHaveLength(1))
    expect(requests.find(request => request.name === 'service.delete')?.payload).toEqual({ id: 'saved-one', expectedRevision: revision, expectedProfileRevision: revision, stopActive: false, removeFromGroups: false })
  })
})
