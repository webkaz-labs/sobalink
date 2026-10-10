import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { Locale, State } from './api'
import { translator } from './i18n'
import { resourceEnglish as en, resourceJapanese as ja } from './resource/i18n'
import { deferred, localOperation, localPreview, remotePreview, remoteReply, remoteSelection, response, settings, target, type Call } from './resource/fixtures.test-support'

const state: State = {
  csrfToken: 'synthetic-app-resource-csrf', processId: 123,
  self: { name: 'Synthetic local', status: 'online' }, peers: [], services: [], shares: [], messages: [], transfers: [],
  settings: { network: 'direct-lan' },
  directLAN: { configured: true, listenerReady: true, publicKey: 'a'.repeat(64), peers: [{ key: remoteSelection.peerKey, name: 'Synthetic remote', endpoint: '192.0.2.20:40000' }] },
}
type Request = Call & { requestId: string }
function setup(handler: (call: Call) => unknown | Promise<unknown> = response, initial = state, locale: Locale = 'en') {
  let current = initial
  let stateRead = () => Promise.resolve(new Response(JSON.stringify(current)))
  let stateReads = 0
  const commands: Request[] = []
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') { ++stateReads; return stateRead() }
    if (path === '/api/session') return new Response(JSON.stringify({ csrfToken: current.csrfToken }))
    if (path !== '/api/command') throw new Error('Unexpected synthetic API route')
    const command = JSON.parse(init!.body as string)
    const call: Request = { ...command, signal: init!.signal as AbortSignal }
    commands.push(call)
    const value = await handler(call)
    return value instanceof Response ? value : new Response(JSON.stringify({ ok: true, result: value }))
  })
  vi.stubGlobal('fetch', fetch)
  localStorage.setItem('sobalink.locale', locale)
  return { commands, fetch, stateReads: () => stateReads, setState: (value: State) => { current = value }, setStateRead: (read: () => Promise<Response>) => { stateRead = read }, restoreStateRead: () => { stateRead = () => Promise.resolve(new Response(JSON.stringify(current))) } }
}
async function refreshHost() { await act(async () => { fireEvent(document, new Event('visibilitychange')) }) }
async function openResources(locale: Locale = 'en') {
  const text = locale === 'ja' ? ja : en, t = translator(locale)
  await userEvent.click(await screen.findByRole('button', { name: t('settings') }))
  await userEvent.click(screen.getByRole('button', { name: text.title }))
  await screen.findByRole('dialog', { name: text.title })
}
async function selectLocal(locale: Locale = 'en') {
  const text = locale === 'ja' ? ja : en
  await userEvent.click(screen.getByRole('button', { name: text.loadLocal }))
  await userEvent.click(await screen.findByRole('button', { name: text.selectLocal }))
  await waitFor(() => expect(screen.getByRole('button', { name: text.preview })).toBeEnabled())
}
async function selectRemote(expectedReview: 'enabled' | 'blocked' = 'enabled') {
  await userEvent.click(screen.getByRole('button', { name: en.remote }))
  await userEvent.selectOptions(screen.getByRole('combobox', { name: en.peer }), remoteSelection.peerKey)
  await userEvent.selectOptions(screen.getByRole('combobox', { name: en.protocol }), '2')
  await userEvent.type(screen.getByRole('textbox', { name: en.resourceId }), target.resourceId)
  await userEvent.type(screen.getByRole('textbox', { name: en.grantId }), remoteSelection.selector.grantId)
  await userEvent.type(screen.getByRole('textbox', { name: en.grantRevision }), '1')
  await userEvent.click(screen.getByRole('button', { name: en.selectRemote }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.refresh })).toBeEnabled())
  if (expectedReview === 'blocked') expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
  else expect(screen.getByRole('button', { name: en.preview })).toBeEnabled()
}
async function reviewAndApply() {
  await userEvent.click(screen.getByRole('button', { name: en.preview }))
  await userEvent.click(await screen.findByRole('checkbox', { name: en.confirmation }))
  await userEvent.click(screen.getByRole('button', { name: en.apply }))
}
async function closeResources() { await userEvent.click(within(screen.getByRole('dialog', { name: en.title })).getByRole('button', { name: 'Close' })) }

describe('mounted App resource settings boundary', () => {
  it.each(['en', 'ja'] as const)('opens the %s entry without resource polling and applies only the exact explicitly confirmed local review', async locale => {
    const text = locale === 'ja' ? ja : en, fixture = setup(response, state, locale)
    render(<App />); await openResources(locale)
    expect(fixture.commands).toHaveLength(0)
    const reads = fixture.stateReads(); await refreshHost()
    await waitFor(() => expect(fixture.stateReads()).toBeGreaterThan(reads))
    expect(fixture.commands).toHaveLength(0)
    await selectLocal(locale)
    await userEvent.click(screen.getByRole('button', { name: text.preview }))
    expect(await screen.findByRole('button', { name: text.apply })).toBeDisabled()
    expect(fixture.commands.some(call => call.name === 'resource.apply')).toBe(false)
    await userEvent.click(screen.getByRole('checkbox', { name: text.confirmation }))
    await userEvent.click(screen.getByRole('button', { name: text.apply }))
    expect(await screen.findByText(text.applied)).toBeVisible()
    const applies = fixture.commands.filter(call => call.name === 'resource.apply')
    expect(applies).toHaveLength(1)
    expect(applies[0].payload).toEqual({ ...target, operationId: localPreview.operationId, baseRevision: localPreview.baseRevision, revision: localPreview.revision, settings })
    expect(new Set(fixture.commands.map(call => call.requestId)).size).toBe(fixture.commands.length)
    expect(applies[0].signal).toBeInstanceOf(AbortSignal)
  })
  it('does not treat a trusted ordinary peer as a supplied remote management selector', async () => {
    const fixture = setup(response, { ...state, directLAN: { ...state.directLAN!, peers: [] }, peers: [{ id: remoteSelection.peerKey, name: 'Trusted synthetic peer', networks: ['direct-lan'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }] })
    render(<App />); await openResources()
    await userEvent.click(screen.getByRole('button', { name: en.remote }))
    expect(screen.getByText(en.noPeers)).toBeVisible()
    expect(screen.queryByRole('button', { name: en.selectRemote })).not.toBeInTheDocument()
    expect(fixture.commands).toHaveLength(0)
  })
  it('preserves an exact review across unchanged host polling and display-only peer changes', async () => {
    const fixture = setup()
    render(<App />); await openResources(); await selectRemote()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await screen.findByRole('button', { name: en.apply })
    fixture.setState({ ...state, directLAN: { ...state.directLAN!, peers: [{ ...state.directLAN!.peers![0], name: 'Renamed synthetic remote', endpoint: '192.0.2.21:40000' }] } })
    await refreshHost()
    expect(screen.getByRole('button', { name: en.apply })).toBeDisabled()
    expect(screen.getByText(remotePreview.reviewRevision)).toBeVisible()
    expect(fixture.commands.map(call => call.name)).toEqual(['resource.remote.management.inspect', 'resource.remote.management.preview'])
  })
  it('retains uncertain identity across modal reopening and ignores a late apply success until manual status', async () => {
    const pendingApply = deferred(), fixture = setup(call => call.name === 'resource.remote.management.apply' ? pendingApply.promise : response(call))
    render(<App />); await openResources(); await selectRemote(); await reviewAndApply()
    const apply = fixture.commands.find(call => call.name === 'resource.remote.management.apply')!
    await closeResources()
    expect(apply.signal.aborted).toBe(true)
    await openResources(); await selectRemote('blocked')
    expect(screen.getByText(en.unknown)).toBeVisible()
    await act(async () => { pendingApply.resolve(remoteReply('apply')); await pendingApply.promise })
    expect(screen.queryByText(en.applied)).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: en.checkStatus }))
    expect(await screen.findByText(en.applied)).toBeVisible()
    expect(fixture.commands.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
    expect(fixture.commands.filter(call => call.name === 'resource.remote.management.operation.status')).toHaveLength(1)
  })
  it('retains the app-lifetime barrier through authentication loss and explicit login/reselection', async () => {
    const fixture = setup(call => call.name === 'resource.remote.management.apply' ? new Response('{"code":"unauthenticated"}', { status: 401 }) : response(call))
    render(<App />); await openResources(); await selectRemote(); await reviewAndApply()
    const t = translator('en')
    const input = await screen.findByLabelText(t('accessCode'))
    expect(screen.queryByText(remotePreview.operationId)).not.toBeInTheDocument()
    await userEvent.type(input, 'synthetic-access-code')
    await userEvent.click(screen.getByRole('button', { name: t('unlock') }))
    await openResources(); await selectRemote('blocked')
    expect(screen.getByText(remotePreview.operationId)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: en.checkStatus }))
    expect(await screen.findByText(en.applied)).toBeVisible()
    expect(fixture.commands.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
  })
  it('does not accept late successful host state after a resource response causes authentication loss', async () => {
    const oldState = deferred<Response>(), fixture = setup(call => call.name === 'resource.remote.management.preview' ? new Response('{"code":"unauthenticated"}', { status: 401 }) : response(call))
    render(<App />); await openResources(); await selectRemote()
    fixture.setStateRead(() => oldState.promise)
    const reads = fixture.stateReads(); await refreshHost()
    await waitFor(() => expect(fixture.stateReads()).toBeGreaterThan(reads))
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    const accessCode = await screen.findByLabelText(translator('en')('accessCode'))
    await act(async () => { oldState.resolve(new Response(JSON.stringify(state))); await oldState.promise })
    expect(accessCode).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: en.title })).not.toBeInTheDocument()
    expect(fixture.commands.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it('aborts pending apply after a failed host refresh and requires status after explicit reselection', async () => {
    const pendingApply = deferred(), fixture = setup(call => call.name === 'resource.remote.management.apply' ? pendingApply.promise : response(call))
    render(<App />); await openResources(); await selectRemote(); await reviewAndApply()
    fixture.setStateRead(() => Promise.reject(new TypeError('Synthetic state failure')))
    await refreshHost()
    expect(await screen.findByText(en.contextUnavailable)).toBeVisible()
    expect(fixture.commands.find(call => call.name === 'resource.remote.management.apply')!.signal.aborted).toBe(true)
    await act(async () => { pendingApply.resolve(remoteReply('apply')); await pendingApply.promise })
    fixture.restoreStateRead(); await refreshHost()
    await screen.findByRole('button', { name: en.remote })
    await selectRemote('blocked')
    expect(screen.queryByText(en.applied)).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: en.checkStatus }))
    expect(await screen.findByText(en.applied)).toBeVisible()
    expect(fixture.commands.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
  })
  it.each(['process', 'local-key', 'peer-key'] as const)('invalidates a pending preview after an observed %s change and rejects its late response', async change => {
    const pendingPreview = deferred(), fixture = setup(call => call.name === 'resource.remote.management.preview' ? pendingPreview.promise : response(call))
    render(<App />); await openResources(); await selectRemote()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    const next = change === 'process' ? { ...state, processId: 456 }
      : { ...state, directLAN: { ...state.directLAN!, ...(change === 'local-key' ? { publicKey: 'c'.repeat(64) } : { peers: [{ ...state.directLAN!.peers![0], key: 'd'.repeat(64) }] }) } }
    fixture.setState(next); await refreshHost()
    expect(await screen.findByText(en.contextChanged)).toBeVisible()
    const preview = fixture.commands.find(call => call.name === 'resource.remote.management.preview')!
    expect(preview.signal.aborted).toBe(true)
    await act(async () => { pendingPreview.resolve(remoteReply('preview')); await pendingPreview.promise })
    expect(screen.queryByRole('button', { name: en.apply })).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: en.grantId })).toHaveValue('')
    expect(fixture.commands.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it('closes and invalidates a pending preview synchronously on browser navigation', async () => {
    const pendingPreview = deferred(), fixture = setup(call => call.name === 'resource.preview' ? pendingPreview.promise : response(call))
    render(<App />); await openResources(); await selectLocal()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    act(() => { fireEvent(window, new PopStateEvent('popstate')) })
    expect(fixture.commands.find(call => call.name === 'resource.preview')!.signal.aborted).toBe(true)
    expect(screen.queryByRole('dialog', { name: en.title })).not.toBeInTheDocument()
    await act(async () => { pendingPreview.resolve(localPreview); await pendingPreview.promise })
    await openResources()
    expect(screen.queryByRole('button', { name: en.apply })).not.toBeInTheDocument()
    expect(fixture.commands.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it('keeps StrictMode lifecycle observation read-only and one explicit apply single-shot', async () => {
    const pendingApply = deferred(), fixture = setup(call => call.name === 'resource.apply' ? pendingApply.promise : response(call))
    const view = render(<StrictMode><App /></StrictMode>)
    await openResources(); expect(fixture.commands).toHaveLength(0)
    await selectLocal(); await reviewAndApply()
    expect(fixture.commands.filter(call => call.name === 'resource.apply')).toHaveLength(1)
    view.unmount()
    expect(fixture.commands.find(call => call.name === 'resource.apply')!.signal.aborted).toBe(true)
    await act(async () => { pendingApply.resolve(localOperation); await pendingApply.promise })
    expect(fixture.commands.filter(call => call.name === 'resource.apply')).toHaveLength(1)
  })
})
