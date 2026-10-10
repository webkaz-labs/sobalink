import { StrictMode } from 'react'
import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { ApiError, command, type State } from './api'
import { translator } from './i18n'
import { useServer } from './useServer'
import { ResourceAttemptRegistry } from './resource/attempt-registry'
import { ResourceSettingsController } from './resource/controller'
import { resourceEnglish as resourceText } from './resource/i18n'
import { deferred, harness as resourceHarness, remotePreview, remoteReply, remoteSelection, response as resourceResponse, type Call } from './resource/fixtures.test-support'
import { appState, fixtureTransport } from './catalog/fixtures.test-support'
import { ResourceCatalogController } from './catalog/controller'
import { ResourceGroupController } from './group/controller'
import { groupEnglish as en } from './group/i18n'
import { groupEvidence, groupOperation, groupPrepared, groupRun, groupRunId, mutableGroup, reviseGroupReview } from './group/fixtures.test-support'
import type { PreparedView } from './group/types'

type Request = Readonly<{ name: string; payload: unknown; requestId: string; signal: AbortSignal }>
// Uncalled compile-time assertions only. No command or assertion runs here.
function checkGroupCommandTypes() {
  void command('resource.group.review.current', { schemaVersion: 1 })
  void command('resource.group.status', { schemaVersion: 1, runId: groupRunId })
  void command('resource.group.apply', { schemaVersion: 1, reviewId: groupRunId, reviewRevision: 'a'.repeat(64), executionPeers: [remoteSelection.peerKey], confirm: true })
  // @ts-expect-error Current unused review never accepts a run/history selector.
  void command('resource.group.review.current', { schemaVersion: 1, runId: groupRunId })
  // @ts-expect-error The exact apply payload requires literal confirmation true.
  void command('resource.group.apply', { schemaVersion: 1, reviewId: groupRunId, reviewRevision: 'a'.repeat(64), executionPeers: [remoteSelection.peerKey], confirm: false })
  // @ts-expect-error A status payload cannot enter the apply command.
  void command('resource.group.apply', { schemaVersion: 1, runId: groupRunId })
  // @ts-expect-error Explicit target refresh never accepts a replacement selector.
  void command('resource.group.status.refresh', { schemaVersion: 1, runId: groupRunId, peers: [remoteSelection.peerKey], selector: remoteSelection.selector })
}
async function preparedForApp(): Promise<PreparedView> {
  const prepared = mutableGroup(await groupPrepared(1)), member = prepared.review.selection.members[0], row = prepared.review.rows[0]
  member.peerKey = remoteSelection.peerKey
  member.selector = { ...remoteSelection.selector, protocolVersion: 2 }
  row.peerKey = remoteSelection.peerKey
  if (row.state === 'ready') row.reply = { ...row.reply, ...member.selector }
  prepared.review.executionPeers = [remoteSelection.peerKey]
  return { ...prepared, review: await reviseGroupReview(prepared.review) }
}
function completedRun(prepared: PreparedView) {
  const evidence = mutableGroup(groupEvidence(prepared.review))
  evidence.members[0].dispatch = 'observed'; evidence.members[0].target = mutableGroup(groupOperation(evidence.members[0]))
  return { ...groupRun(prepared.review, evidence), activity: 'idle' as const }
}
async function setup(override?: (request: Request, prepared: PreparedView) => unknown | Promise<unknown>) {
  const prepared = await preparedForApp()
  let state: State = appState, readState = async () => new Response(JSON.stringify(state))
  const requests: Request[] = []
  localStorage.setItem('sobalink.locale', 'en')
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return readState()
    if (path === '/api/session') return new Response(JSON.stringify({ csrfToken: state.csrfToken }))
    if (path !== '/api/command') throw new Error('Unexpected synthetic group route')
    const request: Request = { ...JSON.parse(init!.body as string), signal: init!.signal as AbortSignal }; requests.push(request)
    let result = await override?.(request, prepared)
    if (result instanceof Response) return result
    if (result === undefined) {
      if (request.name === 'resource.group.review.current') result = { schemaVersion: 1, state: 'current', prepared }
      else if (request.name === 'resource.group.preview') result = prepared
      else if (request.name === 'resource.group.apply' || request.name === 'resource.group.status') result = completedRun(prepared)
      else if (request.name === 'resource.group.cancel') result = { schemaVersion: 1, prepared: { ...prepared, admissionState: 'canceled' } }
      else if (request.name.startsWith('resource.')) result = resourceResponse(request as Call)
      else throw new Error(`Unexpected synthetic command: ${request.name}`)
    }
    return new Response(JSON.stringify({ ok: true, result }))
  }))
  return { prepared, requests, setState: (value: State) => { state = value }, setStateRead: (read: () => Promise<Response>) => { readState = read }, restoreStateRead: () => { readState = async () => new Response(JSON.stringify(state)) } }
}
async function openGroup() {
  await userEvent.click(await screen.findByRole('button', { name: en.title }))
  await screen.findByRole('dialog', { name: en.title })
}
async function closeGroup() { await userEvent.click(within(screen.getByRole('dialog', { name: en.title })).getByRole('button', { name: translator('en')('close') })) }
async function currentReview() {
  await userEvent.click(screen.getByRole('button', { name: en.current }))
  return screen.findByRole('region', { name: en.review })
}
async function applyCurrent() {
  const review = await currentReview()
  await userEvent.click(within(review).getByRole('checkbox', { name: en.confirm }))
  await userEvent.click(within(review).getByRole('button', { name: en.apply }))
}
async function refreshHost() { await act(async () => { fireEvent(document, new Event('visibilitychange')) }) }
async function openSingle() {
  await userEvent.click(await screen.findByRole('button', { name: translator('en')('settings') }))
  await userEvent.click(screen.getByRole('button', { name: resourceText.title }))
  await screen.findByRole('dialog', { name: resourceText.title })
}
async function selectSingle() {
  await userEvent.click(screen.getByRole('button', { name: resourceText.remote }))
  await userEvent.selectOptions(screen.getByRole('combobox', { name: resourceText.peer }), remoteSelection.peerKey)
  await userEvent.selectOptions(screen.getByRole('combobox', { name: resourceText.protocol }), '2')
  await userEvent.type(screen.getByRole('textbox', { name: resourceText.resourceId }), remoteSelection.selector.target.resourceId)
  await userEvent.type(screen.getByRole('textbox', { name: resourceText.grantId }), remoteSelection.selector.grantId)
  await userEvent.type(screen.getByRole('textbox', { name: resourceText.grantRevision }), String(remoteSelection.selector.grantRevision))
  await userEvent.click(screen.getByRole('button', { name: resourceText.selectRemote }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.addSelected })).toBeEnabled())
}

describe('mounted App fixed-group boundary', () => {
  it('opens in StrictMode without group commands and uses explicit current/status with fresh request IDs', async () => {
    const h = await setup(); render(<StrictMode><App /></StrictMode>); await openGroup(); await refreshHost()
    expect(h.requests).toHaveLength(0)
    const review = await currentReview()
    expect(within(review).getByRole('textbox', { name: en.runId })).toHaveValue(groupRunId)
    const history = screen.getByRole('region', { name: en.history })
    await userEvent.type(within(history).getByRole('textbox', { name: en.runId }), groupRunId)
    await userEvent.click(within(history).getByRole('button', { name: en.status }))
    await waitFor(() => expect(within(history).getAllByRole('listitem')).toHaveLength(1))
    expect(h.requests.map(request => request.name)).toEqual(['resource.group.review.current', 'resource.group.status'])
    expect(new Set(h.requests.map(request => request.requestId)).size).toBe(2)
    expect(h.requests.every(request => request.signal instanceof AbortSignal)).toBe(true)
  })
  it('picks a saved peer without creating a selector and navigates inertly to its single-device selector', async () => {
    const h = await setup(); render(<App />); await openGroup()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: en.choosePeer }), remoteSelection.peerKey)
    await userEvent.click(screen.getByRole('button', { name: en.addPeer }))
    expect(screen.getByText(en.missing)).toBeVisible(); expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: en.single }))
    await screen.findByRole('dialog', { name: resourceText.title })
    expect(screen.getByRole('combobox', { name: resourceText.peer })).toHaveValue(remoteSelection.peerKey)
    expect(screen.getByRole('textbox', { name: resourceText.grantId })).toHaveValue('')
    expect(h.requests).toHaveLength(0)
  })
  it('adds the exact existing v2 single selector to group draft without preview apply or grant discovery', async () => {
    const h = await setup(); render(<App />); await openSingle(); await selectSingle()
    expect(h.requests.map(request => request.name)).toEqual(['resource.remote.management.inspect'])
    await userEvent.click(screen.getByRole('button', { name: en.addSelected }))
    await screen.findByRole('dialog', { name: en.title })
    const members = within(screen.getByRole('region', { name: en.peers })).getAllByRole('listitem')
    expect(members).toHaveLength(1)
    expect(within(members[0]).getByLabelText(resourceText.resourceId)).toHaveValue(remoteSelection.selector.target.resourceId)
    expect(within(members[0]).getByLabelText(resourceText.grantId)).toHaveValue(remoteSelection.selector.grantId)
    expect(within(members[0]).getByLabelText(resourceText.grantRevision)).toHaveValue('1')
    expect(h.requests.map(request => request.name)).toEqual(['resource.remote.management.inspect'])
  })
  it('defensively clears an injected retained W1 selection A before inert peer B navigation while preserving A uncertainty', async () => {
    let resourceOwner: ResourceSettingsController | undefined
    const originalOpen = ResourceSettingsController.prototype.open
    vi.spyOn(ResourceSettingsController.prototype, 'open').mockImplementation(function (this: ResourceSettingsController) { resourceOwner = this; originalOpen.call(this) })
    const peerB = '8'.repeat(64), pending = deferred()
    const h = await setup(request => request.name === 'resource.remote.management.apply' ? pending.promise : undefined)
    h.setState({ ...appState,
      peers: [...appState.peers, { ...appState.peers[0], id: peerB, name: 'Synthetic second peer' }],
      directLAN: { ...appState.directLAN!, peers: [...appState.directLAN!.peers!, { key: peerB, name: 'Synthetic second peer', endpoint: '192.0.2.26:40000' }] },
    })
    render(<App />); await openSingle(); await selectSingle()
    await userEvent.click(screen.getByRole('button', { name: resourceText.preview }))
    await userEvent.click(await screen.findByRole('checkbox', { name: resourceText.confirmation }))
    await userEvent.click(screen.getByRole('button', { name: resourceText.apply }))
    const beforeNavigation = h.requests.length
    expect(h.requests.map(request => request.name)).toEqual(['resource.remote.management.inspect', 'resource.remote.management.preview', 'resource.remote.management.apply'])
    // Close A's dialog explicitly; closing waiting does not settle its attempt.
    await userEvent.click(within(screen.getByRole('dialog', { name: resourceText.title })).getByRole('button', { name: translator('en')('close') }))
    expect(h.requests.at(-1)?.signal.aborted).toBe(true)
    await openGroup()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: en.choosePeer }), peerB)
    await userEvent.click(screen.getByRole('button', { name: en.addPeer }))
    // Ordinary closes already clear A. Explicitly inject retained/reentrant
    // visible A state on the real owner without transport, so this test covers
    // defensive boundary hardening rather than an ordinary navigation failure.
    act(() => { resourceOwner!.open(); expect(resourceOwner!.select(remoteSelection)).toBe(true) })
    expect(resourceOwner!.getSnapshot().selection).toEqual(remoteSelection)
    expect(resourceOwner!.getSnapshot().blocked).toBe(true)
    expect(resourceOwner!.getSnapshot().attempts[0].operationId).toBe(remotePreview.operationId)
    expect(h.requests).toHaveLength(beforeNavigation)
    await userEvent.click(screen.getByRole('button', { name: en.single }))
    await screen.findByRole('dialog', { name: resourceText.title })
    expect(screen.getByRole('combobox', { name: resourceText.peer })).toHaveValue(peerB)
    expect(screen.getByRole('combobox', { name: resourceText.protocol })).toHaveValue('2')
    expect(screen.getByRole('textbox', { name: resourceText.resourceId })).toHaveValue('')
    expect(screen.getByRole('textbox', { name: resourceText.grantId })).toHaveValue('')
    expect(screen.getByRole('textbox', { name: resourceText.grantRevision })).toHaveValue('')
    expect(screen.queryByRole('checkbox', { name: resourceText.confirmation })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: resourceText.selectRemote })).toBeDisabled()
    expect(h.requests).toHaveLength(beforeNavigation)
    await act(async () => { pending.resolve(remoteReply('apply')); await pending.promise })
    // Returning explicitly to A reveals its retained unresolved attempt; the
    // clear-selection navigation did not clear either no-replay owner.
    await selectSingle()
    expect(screen.getByText(resourceText.blocked)).toBeVisible()
    expect(screen.getByText(remotePreview.operationId)).toBeVisible()
    expect(screen.getByRole('button', { name: resourceText.preview })).toBeDisabled()
    expect(h.requests.filter(request => request.name === 'resource.remote.management.apply')).toHaveLength(1)
    expect(h.requests.filter(request => request.name.startsWith('resource.group.'))).toHaveLength(0)
    expect(h.requests.some(request => request.name === 'resource.remote.management.operation.status')).toBe(false)
  })
  it('keeps group attempt inhibition across Back and reopening while ignoring late apply success', async () => {
    const pending = deferred(), h = await setup(request => request.name === 'resource.group.apply' ? pending.promise : undefined)
    render(<StrictMode><App /></StrictMode>); await openGroup(); await applyCurrent()
    const apply = h.requests.find(request => request.name === 'resource.group.apply')!
    act(() => window.dispatchEvent(new PopStateEvent('popstate')))
    expect(screen.queryByRole('dialog', { name: en.title })).not.toBeInTheDocument(); expect(apply.signal.aborted).toBe(true)
    await openGroup(); expect(screen.getByText(en.blocked)).toBeVisible()
    await act(async () => { pending.resolve(completedRun(h.prepared)); await pending.promise })
    expect(screen.getByText(en.blocked)).toBeVisible()
    expect(h.requests.filter(request => request.name === 'resource.group.apply')).toHaveLength(1)
    expect(h.requests.some(request => request.name === 'resource.group.status')).toBe(false)
    const history = screen.getByRole('region', { name: en.history })
    await userEvent.type(within(history).getByRole('textbox', { name: en.runId }), groupRunId)
    await userEvent.click(within(history).getAllByRole('button', { name: en.status })[0])
    await waitFor(() => expect(screen.queryByText(en.blocked)).not.toBeInTheDocument())
  })
  it('hides attempted data on authentication loss and restores only explicitly read known-run status', async () => {
    const h = await setup(request => request.name === 'resource.group.apply' ? new Response('{"code":"unauthenticated"}', { status: 401 }) : undefined)
    render(<App />); await openGroup(); await applyCurrent()
    const code = await screen.findByLabelText(translator('en')('accessCode'))
    expect(screen.queryByDisplayValue(groupRunId)).not.toBeInTheDocument()
    await userEvent.type(code, 'synthetic-group-access-code')
    await userEvent.click(screen.getByRole('button', { name: translator('en')('unlock') }))
    await openGroup()
    expect(screen.queryByDisplayValue(groupRunId)).not.toBeInTheDocument()
    const history = screen.getByRole('region', { name: en.history })
    await userEvent.type(within(history).getByRole('textbox', { name: en.runId }), groupRunId)
    await userEvent.click(within(history).getByRole('button', { name: en.status }))
    await waitFor(() => expect(within(history).getAllByRole('listitem')).toHaveLength(1))
    expect(h.requests.filter(request => request.name === 'resource.group.apply')).toHaveLength(1)
    expect(h.requests.filter(request => request.name === 'resource.group.status')).toHaveLength(1)
  })
  it('invalidates stale pending apply synchronously and rejects late results before explicit local recovery', async () => {
    const pending = deferred(), h = await setup(request => request.name === 'resource.group.apply' ? pending.promise : undefined)
    render(<App />); await openGroup(); await applyCurrent()
    h.setStateRead(async () => { throw new TypeError('Synthetic stale host') }); await refreshHost()
    await screen.findByText(en.localUnavailable)
    expect(h.requests.find(request => request.name === 'resource.group.apply')?.signal.aborted).toBe(true)
    await act(async () => { pending.resolve(completedRun(h.prepared)); await pending.promise })
    expect(screen.queryByRole('checkbox', { name: en.confirm })).not.toBeInTheDocument()
    h.restoreStateRead(); await refreshHost(); await waitFor(() => expect(screen.queryByText(en.localUnavailable)).not.toBeInTheDocument())
    const history = screen.getByRole('region', { name: en.history })
    await userEvent.type(within(history).getByRole('textbox', { name: en.runId }), groupRunId)
    const directStatus = within(history).getAllByRole('button', { name: en.status }).filter(button => button.parentElement === history)
    expect(directStatus).toHaveLength(1)
    await userEvent.click(directStatus[0])
    await waitFor(() => expect(within(history).getAllByRole('listitem')).toHaveLength(1))
    expect(h.requests.filter(request => request.name === 'resource.group.apply')).toHaveLength(1)
    const statusRequests = h.requests.filter(request => request.name === 'resource.group.status')
    expect(statusRequests).toHaveLength(1)
    expect(statusRequests[0].payload).toEqual({ schemaVersion: 1, runId: groupRunId })
  })
  it('never exposes a current review after source-close reentrant navigation invalidates a single-to-group transition', async () => {
    let group: ResourceGroupController | undefined
    const original = ResourceGroupController.prototype.open
    vi.spyOn(ResourceGroupController.prototype, 'open').mockImplementation(function (this: ResourceGroupController) { group = this; original.call(this) })
    const h = await setup(); render(<App />); await openGroup(); await closeGroup(); await openSingle(); await selectSingle()
    let navigated = false
    const unsubscribe = group!.subscribe(() => { if (!navigated && group!.getSnapshot().opened) { navigated = true; window.dispatchEvent(new PopStateEvent('popstate')) } })
    await userEvent.click(screen.getByRole('button', { name: en.addSelected })); unsubscribe()
    expect(navigated).toBe(true)
    expect(screen.queryByRole('dialog', { name: en.title })).not.toBeInTheDocument()
    expect(h.requests.map(request => request.name)).toEqual(['resource.remote.management.inspect'])
  })
  it('does not deliver older group-ready context after resource or catalog observation recursively loses auth', async () => {
    for (const source of ['resource', 'catalog'] as const) {
      const resource = resourceHarness().controller, catalogFixture = fixtureTransport(), catalog = new ResourceCatalogController(catalogFixture.transport, () => {})
      const commands: string[] = [], group = new ResourceGroupController(async name => { commands.push(name); throw new Error('Unexpected synthetic group dispatch') }, () => {}, new ResourceAttemptRegistry())
      const fetch = vi.fn(async () => new Response(JSON.stringify(appState))); vi.stubGlobal('fetch', fetch)
      const hook = renderHook(() => useServer(resource, catalog, group)); await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
      let invalidated = false
      const observer = source === 'resource' ? resource : catalog
      const unsubscribe = observer.subscribe(() => { if (!invalidated) { invalidated = true; hook.result.current.handleError(new ApiError('unauthenticated', '')) } })
      fetch.mockImplementation(async () => new Response(JSON.stringify({ ...appState, processId: 200 })))
      await act(async () => { expect(await hook.result.current.refresh()).toBeNull() }); unsubscribe()
      expect(invalidated).toBe(true); expect(hook.result.current.auth).toBe('locked')
      expect(group.getSnapshot().context.localAvailable).toBe(false); expect(commands).toEqual([])
      hook.unmount()
    }
  })
  it('does not publish successful host state after a group subscriber recursively loses auth', async () => {
    const resource = resourceHarness().controller, catalogFixture = fixtureTransport(), catalog = new ResourceCatalogController(catalogFixture.transport, () => {})
    const group = new ResourceGroupController(async () => { throw new Error('Unexpected synthetic group dispatch') }, () => {}, new ResourceAttemptRegistry())
    const fetch = vi.fn(async () => new Response(JSON.stringify(appState))); vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(resource, catalog, group)); await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    let invalidated = false
    const unsubscribe = group.subscribe(() => { if (!invalidated && group.getSnapshot().context.localAvailable) { invalidated = true; hook.result.current.handleError(new ApiError('unauthenticated', '')) } })
    fetch.mockImplementation(async () => new Response(JSON.stringify({ ...appState, resourceCatalogProcessId: '9'.repeat(64) })))
    await act(async () => { expect(await hook.result.current.refresh()).toBeNull() }); unsubscribe()
    expect(invalidated).toBe(true); expect(hook.result.current.state).toBeNull()
    expect(group.getSnapshot().context.localAvailable).toBe(false)
  })
})
