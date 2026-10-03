import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import * as api from '../api'
import { readSavedProxies } from '../startup-proxy'
import { startupText } from '../startup-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { SaveProxyReview, SavedProxiesDialog } from './SavedProxyControls'
const scope: api.ProxyScope = { name: 'fictional-proxy', backend: 'tailnet', loopbackHost: '127.0.0.1', localPort: 1080, lifetime: 'finite', ttlSeconds: 120, targets: [{ peerId: 'fixture-peer', port: 443 }] }
const review: api.ProxyReview = { scope, revision: 'b'.repeat(64), endpoint: '127.0.0.1:1080', targets: [{ peerId: 'fixture-peer', host: '100.64.0.2', port: 443 }], authentication: 'username-password-required', application: 'unverified' }
const entry: api.SavedProxyView = { name: scope.name, scope, revision: 'record-revision', startOnLaunch: true, credentialsSaved: true, valid: true, state: 'saved' }
const empty: api.SavedProxyList = { entries: [], revision: 'a'.repeat(64), suppressed: false }
const stored: api.SavedProxyList = { ...empty, entries: [entry] }
const state: api.State = { csrfToken: 'fictional-control', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet' }, peers: [{ id: 'fixture-peer', name: 'Studio', networks: ['tailnet'], online: true, trusted: true, verified: true, bridge: true, path: 'direct' }], services: [], shares: [], proxies: [], transfers: [], messages: [] }
function mock(initial = empty, failure = '') {
  let list = structuredClone(initial)
  const calls: { name: string; payload: Record<string, unknown> }[] = []
  const fetch = vi.fn(async (_path: string, options?: RequestInit) => {
    const request = JSON.parse(options!.body as string); calls.push(request)
    if (request.name === failure) return new Response(JSON.stringify({ code: 'private_settings_unavailable', error: 'fictional-password-never-display' }), { status: 500 })
    let result: unknown
    if (request.name === 'proxy.saved.list') result = list
    if (request.name === 'proxy.preview') result = review
    if (['proxy.generate', 'proxy.save'].includes(request.name)) { const next = { ...entry, startOnLaunch: request.payload.startOnLaunch }; list = { ...list, revision: 'c'.repeat(64), entries: [next] }; result = next }
    if (request.name === 'proxy.reveal') result = { username: 'fictional-user', password: 'fictional-password' }
    if (request.name === 'proxy.saved.disable') { list = { ...list, revision: 'd'.repeat(64), entries: [{ ...entry, startOnLaunch: false, revision: 'disabled-record' }] }; result = list }
    if (request.name === 'proxy.saved.delete') { list = { ...empty, revision: 'e'.repeat(64) }; result = list }
    if (request.name === 'proxy.saved.start') result = { id: 'runtime-proxy', name: scope.name, status: 'active', application: 'unverified' }
    return new Response(JSON.stringify({ ok: true, result }))
  })
  vi.stubGlobal('fetch', fetch)
  const server = { state, stale: false, run: vi.fn(), refresh: vi.fn().mockResolvedValue(state), handleError: vi.fn(), setError: vi.fn(), busy: new Set(), error: null, auth: 'ready', updatedAt: null } as unknown as Server
  return { calls, fetch, server }
}
async function ready() { await waitFor(() => expect(screen.getByRole('button', { name: 'Generate and save privately' })).toBeEnabled()) }
describe('explicit private proxy persistence', () => {
  it.each(['en', 'ja'] as const)('defaults launch off and generates without revealing or starting (%s)', async locale => {
    const context = mock(); const onSaved = vi.fn(); const s = (key: string) => startupText(locale, key)
    render(<SaveProxyReview review={review} server={context.server} locale={locale} t={translator(locale)} onBack={vi.fn()} onSaved={onSaved} />)
    await waitFor(() => expect(screen.getByRole('button', { name: s('generate') })).toBeEnabled())
    expect(screen.getByRole('checkbox', { name: s('startOnLaunch') })).not.toBeChecked()
    expect(screen.queryByDisplayValue('fictional-password')).not.toBeInTheDocument()
    expect(context.calls.map(call => call.name)).toEqual(['proxy.saved.list'])
    await userEvent.click(screen.getByRole('button', { name: s('generate') }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(context.calls.find(call => call.name === 'proxy.generate')?.payload).toEqual({ scope, expectedRevision: review.revision, expectedStoreRevision: empty.revision, startOnLaunch: false })
    expect(context.calls.some(call => ['proxy.start', 'proxy.saved.start', 'proxy.reveal'].includes(call.name))).toBe(false)
    expect(context.server.run).not.toHaveBeenCalled()
  })
  it('clears supplied private values before awaiting save, on back, and when launch choice changes', async () => {
    const context = mock(); const onBack = vi.fn(); const onSaved = vi.fn()
    let finish!: (value: Response) => void
    const response = new Promise<Response>(resolve => { finish = resolve })
    const view = render(<SaveProxyReview review={review} server={context.server} locale="en" t={translator('en')} onBack={onBack} onSaved={onSaved} />)
    await ready(); await userEvent.selectOptions(screen.getByRole('combobox'), 'supplied')
    const username = screen.getByLabelText('Runtime username') as HTMLInputElement; const password = screen.getByLabelText('Runtime password') as HTMLInputElement
    fireEvent.change(username, { target: { value: 'fictional-user' } }); fireEvent.change(password, { target: { value: 'fictional-password' } })
    await userEvent.click(screen.getByRole('checkbox', { name: startupText('en', 'startOnLaunch') }))
    expect(username.value).toBe(''); expect(password.value).toBe('')
    fireEvent.change(username, { target: { value: 'fictional-user' } }); fireEvent.change(password, { target: { value: 'fictional-password' } })
    context.fetch.mockImplementationOnce((_path, options) => { context.calls.push(JSON.parse(options!.body as string)); return response })
    await userEvent.click(screen.getByRole('button', { name: 'Save entered credentials privately' }))
    expect(username.value).toBe(''); expect(password.value).toBe(''); expect(onSaved).not.toHaveBeenCalled()
    expect(context.calls.at(-1)?.payload).toMatchObject({ username: 'fictional-user', password: 'fictional-password', startOnLaunch: true })
    expect(context.server.run).not.toHaveBeenCalled(); expect(localStorage.getItem('fictional-password')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Edit scope' })); expect(onBack).toHaveBeenCalledOnce()
    view.unmount(); finish(new Response(JSON.stringify({ ok: true, result: entry }))); await response
    expect(onSaved).not.toHaveBeenCalled()
  })
  it('does not claim storage success after failure and never renders echoed private error text', async () => {
    const context = mock(empty, 'proxy.save'); const onSaved = vi.fn()
    render(<SaveProxyReview review={review} server={context.server} locale="en" t={translator('en')} onBack={vi.fn()} onSaved={onSaved} />)
    await ready(); await userEvent.selectOptions(screen.getByRole('combobox'), 'supplied')
    fireEvent.change(screen.getByLabelText('Runtime username'), { target: { value: 'fictional-user' } }); fireEvent.change(screen.getByLabelText('Runtime password'), { target: { value: 'fictional-password' } })
    await userEvent.click(screen.getByRole('button', { name: 'Save entered credentials privately' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(startupText('en', 'private_settings_unavailable'))
    expect(screen.queryByText('fictional-password-never-display')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Runtime password')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Save entered credentials privately' })).toBeDisabled()
    expect(onSaved).not.toHaveBeenCalled()
  })
  it('requires explicit replacement and erases private input on authoritative store revision change', async () => {
    const context = mock(stored)
    const view = render(<SaveProxyReview review={review} server={context.server} locale="en" t={translator('en')} onBack={vi.fn()} onSaved={vi.fn()} />)
    const checkbox = await screen.findByRole('checkbox', { name: startupText('en', 'replaceProxy') })
    expect(screen.getByRole('button', { name: 'Generate and save privately' })).toBeDisabled()
    await userEvent.click(checkbox); await userEvent.selectOptions(screen.getByRole('combobox'), 'supplied')
    fireEvent.change(screen.getByLabelText('Runtime password'), { target: { value: 'fictional-password' } })
    view.rerender(<SaveProxyReview review={review} server={{ ...context.server, state: { ...state, savedProxies: { ...stored, revision: 'f'.repeat(64) } } }} locale="en" t={translator('en')} onBack={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.getByLabelText('Runtime password')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Save entered credentials privately' })).toBeDisabled()
    expect(context.calls.some(call => call.name === 'proxy.save')).toBe(false)
  })
  it('excludes unexpected credential fields from sanitized saved state', () => {
    const parsed = readSavedProxies({ ...stored, password: 'fictional-top-secret', entries: [{ ...entry, username: 'fictional-user', scope: { ...scope, password: 'fictional-scope-secret' } }] })
    expect(JSON.stringify(parsed)).not.toContain('fictional-top-secret'); expect(JSON.stringify(parsed)).not.toContain('fictional-user'); expect(JSON.stringify(parsed)).not.toContain('fictional-scope-secret')
  })
})
describe('saved proxy lifecycle and temporary reveal', () => {
  it.each(['en', 'ja'] as const)('requires explicit reveal, clears on hide/close, and never uses the shared retry cache (%s)', async locale => {
    const context = mock(stored); const onClose = vi.fn(); const s = (key: string) => startupText(locale, key)
    const view = render(<SavedProxiesDialog server={context.server} locale={locale} t={translator(locale)} onClose={onClose} />)
    await screen.findByRole('button', { name: s('reveal') }); expect(context.calls.some(call => call.name === 'proxy.reveal')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: s('reveal') }))
    const password = await screen.findByDisplayValue('fictional-password') as HTMLInputElement
    expect(password).toHaveAttribute('data-private', 'proxy-credential'); expect(password.closest('.proxy-credentials')).not.toBeNull()
    expect(context.server.run).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: s('hide') }))
    expect(password.value).toBe(''); expect(screen.queryByDisplayValue('fictional-password')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: s('reveal') }))
    const shown = await screen.findByDisplayValue('fictional-password') as HTMLInputElement
    view.unmount(); expect(shown.value).toBe('')
  })
  it('discards a late reveal after hiding it and after a scope change', async () => {
    const context = mock(stored); let finish!: (value: Response) => void
    const pending = new Promise<Response>(resolve => { finish = resolve })
    const view = render(<SavedProxiesDialog server={context.server} locale="en" t={translator('en')} onClose={vi.fn()} />)
    await screen.findByRole('button', { name: startupText('en', 'reveal') })
    context.fetch.mockReturnValueOnce(pending)
    await userEvent.click(screen.getByRole('button', { name: startupText('en', 'reveal') }))
    await userEvent.click(screen.getByRole('button', { name: startupText('en', 'hide') }))
    finish(new Response(JSON.stringify({ ok: true, result: { username: 'fictional-late-user', password: 'fictional-late-password' } })))
    await act(async () => { await pending })
    expect(screen.queryByDisplayValue('fictional-late-password')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: startupText('en', 'reveal') }))
    const password = await screen.findByDisplayValue('fictional-password') as HTMLInputElement
    view.rerender(<SavedProxiesDialog server={{ ...context.server, state: { ...state, savedProxies: { ...stored, entries: [{ ...entry, revision: 'replacement' }] } } }} locale="en" t={translator('en')} onClose={vi.fn()} />)
    expect(password.value).toBe(''); expect(screen.queryByDisplayValue('fictional-password')).not.toBeInTheDocument()
  })
  it('reviews exact saved start scope and does not start on cancel', async () => {
    const context = mock(stored)
    render(<SavedProxiesDialog server={context.server} locale="en" t={translator('en')} onClose={vi.fn()} />)
    await userEvent.click(await screen.findByRole('button', { name: startupText('en', 'reviewStart') }))
    const button = await screen.findByRole('button', { name: startupText('en', 'startConfirm') }); await waitFor(() => expect(button).toBeEnabled())
    expect(screen.getByRole('region')).toHaveTextContent('100.64.0.2'); expect(screen.getByRole('region')).toHaveTextContent('120')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(context.calls.some(call => call.name === 'proxy.saved.start')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: startupText('en', 'reviewStart') })); await waitFor(() => expect(screen.getByRole('button', { name: startupText('en', 'startConfirm') })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: startupText('en', 'startConfirm') }))
    expect(await screen.findByRole('status')).toHaveTextContent(startupText('en', 'startedProxy'))
    expect(context.calls.find(call => call.name === 'proxy.saved.start')?.payload).toEqual({ name: entry.name, expectedRevision: entry.revision })
  })
  it.each(['disable', 'delete'] as const)('reviews %s impact and leaves failed persistence unconfirmed', async kind => {
    const context = mock(stored, `proxy.saved.${kind}`)
    render(<SavedProxiesDialog server={context.server} locale="en" t={translator('en')} onClose={vi.fn()} />)
    await userEvent.click(await screen.findByRole('button', { name: startupText('en', kind === 'disable' ? 'disableProxy' : 'deleteProxy') }))
    expect(screen.getByRole('region')).toHaveTextContent(startupText('en', kind === 'disable' ? 'disableProxyHint' : 'deleteProxyHint'))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(context.calls.some(call => call.name === `proxy.saved.${kind}`)).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: startupText('en', kind === 'disable' ? 'disableProxy' : 'deleteProxy') }))
    await userEvent.click(screen.getByRole('button', { name: startupText('en', kind === 'disable' ? 'disableProxyConfirm' : 'deleteProxyConfirm') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(startupText('en', 'private_settings_unavailable'))
    expect(screen.queryByText(startupText('en', kind === 'disable' ? 'proxyDisabled' : 'deleted'))).not.toBeInTheDocument()
    expect(context.calls.some(call => call.name === 'proxy.stop')).toBe(false)
  })
})

it.each(['disable', 'delete'] as const)('confirms only the exact reviewed saved %s result', async kind => {
  const context = mock(stored)
  render(<SavedProxiesDialog server={context.server} locale="en" t={translator('en')} onClose={vi.fn()} />)
  await userEvent.click(await screen.findByRole('button', { name: startupText('en', kind === 'disable' ? 'disableProxy' : 'deleteProxy') }))
  await userEvent.click(screen.getByRole('button', { name: startupText('en', kind === 'disable' ? 'disableProxyConfirm' : 'deleteProxyConfirm') }))
  expect(await screen.findByRole('status')).toHaveTextContent(startupText('en', kind === 'disable' ? 'proxyDisabled' : 'deleted'))
  expect(context.calls.find(call => call.name === `proxy.saved.${kind}`)?.payload).toEqual({ name: entry.name, expectedRevision: entry.revision })
  expect(context.calls.some(call => ['proxy.start', 'proxy.saved.start', 'proxy.reveal', 'proxy.stop'].includes(call.name))).toBe(false)
  if (kind === 'delete') expect(await screen.findByText(startupText('en', 'savedEmpty'))).toBeVisible()
})
