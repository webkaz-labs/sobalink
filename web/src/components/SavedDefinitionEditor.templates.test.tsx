import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, Locale, ServiceConfiguration, State } from '../api'
import { savedEditorText } from '../saved-editor-i18n'
import { serviceText } from '../service-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { SavedDefinitionEditor } from './SavedDefinitionEditor'

const revision = 'a'.repeat(64)
const saved: ServiceConfiguration = { id: 'template-one', name: 'example-share', direction: 'share', backend: 'lan', peerIds: ['absent-peer', 'current-peer'], network: 'udp', ports: '8080-8082', excludePorts: '8081', loopbackHost: '::1', lifetime: 'finite', ttlSeconds: 259200, purpose: 'custom', discoverable: true }
const second: ServiceConfiguration = { ...saved, id: 'template-two', name: 'another-share', ports: '9090', excludePorts: '', localPort: 9091, lifetime: 'until-revoked', ttlSeconds: 0, discoverable: false }
const state: State = { csrfToken: 'fixture', self: { name: 'Notebook', status: 'offline' }, settings: { network: 'none' }, peers: [{ id: 'current-peer', name: 'Current device', networks: ['lan'], online: false, verified: false, trusted: false, bridge: true, path: 'unknown' }], services: [], shares: [], transfers: [], messages: [] }
function setup(locale: Locale = 'en') {
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload) => name === 'service.config' ? { ok: true, result: { configuration: (payload as { id: string }).id === saved.id ? saved : second, revision, active: true } } : { ok: true, result: { configuration: { ...(payload as any).configuration, id: 'saved-result' }, revision, active: false } })
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run, handleError: vi.fn(), updatedAt: null, messageBlock: vi.fn().mockResolvedValue(null), messageGuardRevision: 0 }
  return { props: { server, locale, t: translator(locale), mode: 'share' as const, services: [saved, second], onSaved: vi.fn(), onClose: vi.fn() }, e: (key: string) => savedEditorText(locale, key), run }
}
function pending() { let resolve!: (value: CommandResult | undefined) => void; const promise = new Promise<CommandResult | undefined>(done => { resolve = done }); return { promise, resolve } }

describe('inert share templates', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`${locale}: reads current settings, preserves every scope choice and missing recipient, then saves only after edited review`, async () => {
      const { props, e, run } = setup(locale); const t = translator(locale); render(<SavedDefinitionEditor {...props} />)
      // Catalog metadata is a choice label, never the data used for the new draft.
      run.mockResolvedValueOnce({ ok: true, result: { configuration: { ...saved, ports: '8180-8182', excludePorts: '8181', owner: 'old-owner', leaseSeconds: 30, startOnLaunch: true, replaceId: saved.id, expectedRevision: revision }, revision, active: true } })
      await userEvent.selectOptions(screen.getByRole('combobox', { name: new RegExp(e('template')) }), saved.id)
      await screen.findByDisplayValue('example-share-2')
      expect(screen.getByLabelText(t('ports'))).toHaveValue('8180-8182')
      expect(screen.getByLabelText(t('excludePorts'))).toHaveValue('8181')
      expect(screen.getByLabelText(e('duration'))).toHaveValue(259200)
      expect(screen.getByLabelText(new RegExp(e('peers')))).toHaveValue('absent-peer, current-peer')
      expect(screen.getByText(e('unavailable'))).toBeVisible()
      expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config'])
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      const review = screen.getByRole('region', { name: e('reviewTitle') })
      for (const value of ['UDP', 'lan', 'absent-peer, current-peer', '8180-8182', '8181', '[::1]:8180, 8182', '259200', 'custom', t('enabled')]) expect(review).toHaveTextContent(value)
      await userEvent.click(screen.getByRole('button', { name: e('back') }))
      expect(screen.queryByRole('region', { name: e('reviewTitle') })).not.toBeInTheDocument()
      fireEvent.change(screen.getByLabelText(t('ports')), { target: { value: '8280' } })
      fireEvent.change(screen.getByLabelText(t('excludePorts')), { target: { value: '' } })
      fireEvent.change(screen.getByLabelText(serviceText(locale, 'shareLocalPort')), { target: { value: '8281' } })
      await userEvent.selectOptions(screen.getByLabelText(t('protocol')), 'tcp')
      fireEvent.change(screen.getByLabelText(new RegExp(e('peers'))), { target: { value: 'absent-peer' } })
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      expect(screen.getByRole('region', { name: e('reviewTitle') })).toHaveTextContent('8280 → [::1]:8281')
      await userEvent.click(screen.getByRole('button', { name: e('confirm') }))
      const payload = run.mock.calls.find(([name]) => name === 'service.save')?.[1] as any
      expect(payload).toEqual({ configuration: { name: 'example-share-2', direction: 'share', backend: 'lan', peerIds: ['absent-peer'], peerId: undefined, network: 'tcp', ports: '8280', excludePorts: '', localPort: 8281, loopbackHost: '::1', lifetime: 'finite', ttlSeconds: 259200, purpose: 'custom', discoverable: true } })
      expect(run.mock.calls.map(([name]) => name)).toEqual(['service.config', 'service.save'])
      expect(props.onSaved).toHaveBeenCalledOnce()
    })
    it(`${locale}: a new template, an edit and blank choice invalidate review without saving`, async () => {
      const { props, e, run } = setup(locale); render(<SavedDefinitionEditor {...props} />)
      const template = screen.getByRole('combobox', { name: new RegExp(e('template')) })
      await userEvent.selectOptions(template, saved.id); await screen.findByDisplayValue('example-share-2')
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      fireEvent.change(screen.getByLabelText(e('duration')), { target: { value: '129600' } })
      expect(screen.queryByRole('button', { name: e('confirm') })).not.toBeInTheDocument()
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      await userEvent.selectOptions(template, second.id); await screen.findByDisplayValue('another-share-2')
      expect(screen.queryByRole('button', { name: e('confirm') })).not.toBeInTheDocument()
      expect(screen.getByLabelText(serviceText(locale, 'lifetime'))).toHaveValue('until-revoked')
      await userEvent.click(screen.getByRole('button', { name: e('review') }))
      await userEvent.selectOptions(template, '')
      expect(screen.getByLabelText(translator(locale)('ruleName'))).toHaveValue('')
      expect(screen.queryByRole('button', { name: e('confirm') })).not.toBeInTheDocument()
      await userEvent.click(screen.getByRole('button', { name: translator(locale)('cancel') }))
      expect(run.mock.calls.every(([name]) => name === 'service.config')).toBe(true)
      expect(props.onClose).toHaveBeenCalledOnce()
    })
  }
  it('ignores late reads after a newer template or blank choice', async () => {
    const { props, e, run } = setup(); const first = pending(); run.mockReturnValueOnce(first.promise)
    render(<SavedDefinitionEditor {...props} />)
    const template = screen.getByRole('combobox', { name: new RegExp(e('template')) })
    await userEvent.selectOptions(template, saved.id); await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    await userEvent.selectOptions(template, second.id); await screen.findByDisplayValue('another-share-2')
    await act(async () => first.resolve({ ok: true, result: { configuration: saved, revision, active: false } }))
    expect(screen.getByLabelText('Connection name')).toHaveValue('another-share-2')
    const next = pending(); run.mockReturnValueOnce(next.promise)
    await userEvent.selectOptions(template, saved.id); await waitFor(() => expect(run).toHaveBeenCalledTimes(3))
    await userEvent.selectOptions(template, '')
    await act(async () => next.resolve({ ok: true, result: { configuration: saved, revision, active: false } }))
    expect(screen.getByLabelText('Connection name')).toHaveValue('')
    expect(run.mock.calls.every(([name]) => name === 'service.config')).toBe(true)
  })
  it('ignores a pending template after close and does not repeat the same selection read', async () => {
    const { props, e, run } = setup(); const read = pending(); run.mockReturnValueOnce(read.promise)
    render(<SavedDefinitionEditor {...props} />)
    const template = screen.getByRole('combobox', { name: new RegExp(e('template')) })
    await userEvent.selectOptions(template, saved.id); await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    fireEvent.change(template, { target: { value: saved.id } })
    expect(run).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    await act(async () => read.resolve({ ok: true, result: { configuration: saved, revision, active: false } }))
    expect(screen.queryByDisplayValue('example-share-2')).not.toBeInTheDocument()
    expect(props.onClose).toHaveBeenCalledOnce(); expect(props.onSaved).not.toHaveBeenCalled()
  })
  it('preserves exact references when a visible device belongs to a different network', async () => {
    const { props, e, run } = setup()
    run.mockResolvedValueOnce({ ok: true, result: { configuration: { ...saved, backend: 'tailnet', peerIds: ['current-peer'] }, revision, active: false } })
    render(<SavedDefinitionEditor {...props} />)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: new RegExp(e('template')) }), saved.id)
    await screen.findByDisplayValue('example-share-2')
    expect(screen.getByLabelText(new RegExp(e('peers')))).toHaveValue('current-peer')
    expect(screen.getByText(e('unavailable'))).toBeVisible()
    expect(screen.getByRole('button', { name: e('review') })).toBeEnabled()
    expect(run.mock.calls.every(([name]) => name === 'service.config')).toBe(true)
  })
  it('does not reuse missing or mismatched template data and permits explicit reload', async () => {
    const { props, e, run } = setup(); run.mockResolvedValueOnce(undefined)
    render(<SavedDefinitionEditor {...props} />)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: new RegExp(e('template')) }), saved.id)
    await screen.findByRole('button', { name: e('reload') })
    expect(screen.queryByRole('button', { name: e('review') })).not.toBeInTheDocument()
    run.mockResolvedValueOnce({ ok: true, result: { configuration: second, revision, active: false } })
    await userEvent.click(screen.getByRole('button', { name: e('reload') }))
    await waitFor(() => expect(props.server.setError).toHaveBeenCalledWith({ code: 'invalid_response' }))
    expect(screen.queryByRole('button', { name: e('review') })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: e('reload') })); await screen.findByDisplayValue('example-share-2')
    expect(run.mock.calls.every(([name]) => name === 'service.config')).toBe(true)
  })
  it('invalidates review on lost contact and rejects a read interrupted even if contact returns', async () => {
    const { props, e, run } = setup(); const view = render(<SavedDefinitionEditor {...props} />)
    const template = screen.getByRole('combobox', { name: new RegExp(e('template')) })
    await userEvent.selectOptions(template, saved.id); await screen.findByDisplayValue('example-share-2')
    await userEvent.click(screen.getByRole('button', { name: e('review') }))
    view.rerender(<SavedDefinitionEditor {...props} server={{ ...props.server, stale: true }} />)
    expect(screen.queryByRole('button', { name: e('confirm') })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: e('review') })).toBeDisabled()
    view.rerender(<SavedDefinitionEditor {...props} />)
    const read = pending(); run.mockReturnValueOnce(read.promise)
    await userEvent.selectOptions(template, second.id); await waitFor(() => expect(run).toHaveBeenCalledTimes(2))
    view.rerender(<SavedDefinitionEditor {...props} server={{ ...props.server, stale: true }} />)
    view.rerender(<SavedDefinitionEditor {...props} />)
    await act(async () => read.resolve({ ok: true, result: { configuration: second, revision, active: false } }))
    expect(screen.queryByRole('button', { name: e('review') })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: e('reload') })).toBeEnabled()
  })
  it('prevents repeated save clicks and ignores completion after closing', async () => {
    const { props, e, run } = setup(); render(<SavedDefinitionEditor {...props} />)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: new RegExp(e('template')) }), saved.id); await screen.findByDisplayValue('example-share-2')
    await userEvent.click(screen.getByRole('button', { name: e('review') }))
    const save = pending(); run.mockReturnValueOnce(save.promise)
    const confirm = screen.getByRole('button', { name: e('confirm') })
    fireEvent.click(confirm); fireEvent.click(confirm)
    expect(run.mock.calls.filter(([name]) => name === 'service.save')).toHaveLength(1)
    expect(screen.getByRole('button', { name: e('review') })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent(e('saving'))
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close' }))
    await act(async () => save.resolve({ ok: true, result: { configuration: { ...saved, id: 'new-id' }, revision, active: false } }))
    expect(props.onClose).toHaveBeenCalledOnce(); expect(props.onSaved).not.toHaveBeenCalled()
  })
})
