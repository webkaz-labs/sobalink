import { describe, expect, it } from 'vitest'
import { ApiError } from '../api'
import { MAX_RESOURCE_ATTEMPTS } from './controller'
import { changedSettings, context, deferred, descriptor, harness, localOperation, localPreview, localSelection, remotePreview, remoteReply, remoteSelection, response, selectLocal, selectRemote, settings, target, unknown } from './fixtures.test-support'
import type { ResourceCommandPayloads } from './types'

describe('one-shot resource settings controller', () => {
  it('does no work until explicit actions and requires a listed local target', async () => {
    const { controller, calls } = harness()
    expect(calls).toHaveLength(0)
    expect(controller.select(localSelection)).toBe(false)
    await controller.preview(); await controller.confirmApply()
    expect(calls).toHaveLength(0)
    await selectLocal(controller)
    expect(calls.map(call => call.name)).toEqual(['resource.list', 'resource.inspect'])
    expect(controller.getSnapshot().current.values?.requested).toEqual(settings)
  })
  it('never derives a selector from pairing/trust and keeps inspection v1 read-only', async () => {
    const { controller, calls } = harness()
    await controller.inspect(); await controller.preview(); await controller.confirmApply()
    expect(calls).toHaveLength(0)
    expect(controller.select({ ...remoteSelection, peerKey: '0'.repeat(64) })).toBe(false)
    controller.updateContext({ ...context, managedDirectLAN: false })
    expect(controller.select(remoteSelection)).toBe(false)
    controller.updateContext(context)
    await selectRemote(controller, { ...remoteSelection, selector: { ...remoteSelection.selector, protocolVersion: 1 } })
    await controller.preview(); await controller.confirmApply()
    expect(calls.map(call => call.name)).toEqual(['resource.remote.inspect'])
    expect(Object.keys(calls[0].payload)).toEqual(['peerKey', 'request'])
  })
  it('freezes requested settings and exact local review tokens, dispatching one repeated click only', async () => {
    const wait = deferred(), { controller, calls } = harness(call => call.name === 'resource.apply' ? wait.promise : response(call))
    await selectLocal(controller); controller.setDraft(changedSettings); await controller.preview()
    const review = controller.getSnapshot().review!
    expect(Object.isFrozen(review)).toBe(true)
    expect(Object.isFrozen(review.settings.transferConcurrentFiles)).toBe(true)
    expect(calls.filter(call => call.name === 'resource.apply')).toHaveLength(0)
    const pending = controller.confirmApply()
    await controller.confirmApply(); await controller.preview()
    const applies = calls.filter(call => call.name === 'resource.apply')
    expect(applies).toHaveLength(1)
    expect(applies[0].payload).toEqual({ ...target, operationId: localPreview.operationId, baseRevision: localPreview.baseRevision, revision: localPreview.revision, settings: changedSettings })
    expect(controller.getSnapshot().review).toBeNull()
    expect(controller.getSnapshot().attempts[0].operationId).toBe(localPreview.operationId)
    wait.resolve({ ...localOperation, current: { ...descriptor, requested: changedSettings } }); await pending
    expect(controller.getSnapshot().attempts[0].evidence?.outcome.status).toBe('applied')
    expect(controller.getSnapshot().current.values?.requested).toEqual(changedSettings)
  })
  it('sends the v2 apply arm with confirm:true and no local revision or new preview', async () => {
    const { controller, calls } = harness()
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    const apply = calls.find(call => call.name === 'resource.remote.management.apply')!
    expect(apply.payload).toEqual({ peerKey: remoteSelection.peerKey, confirm: true, request: { ...remoteSelection.selector, action: 'apply', apply: { operationId: remotePreview.operationId, baseRevision: remotePreview.baseRevision, reviewRevision: remotePreview.reviewRevision, settings } } })
    expect(calls.filter(call => call.name.includes('preview'))).toHaveLength(1)
    expect(controller.getSnapshot().current.state).toBe('unconfirmed')
    expect(controller.getSnapshot().attempts[0].evidence?.evidenceDurable).toBe(true)
  })
  it.each([
    ['close', 'begin'], ['close', 'consumed'],
    ['authentication', 'begin'], ['authentication', 'consumed'],
    ['selection', 'begin'], ['selection', 'consumed'],
  ] as const)('does not dispatch after reentrant %s at the %s notification', async (action, phase) => {
    const { controller, calls } = harness()
    await selectRemote(controller); await controller.preview()
    let invalidated = false
    const unsubscribe = controller.subscribe(() => {
      const snapshot = controller.getSnapshot()
      if (invalidated || snapshot.busy !== 'apply' || phase === 'consumed' && snapshot.review !== null) return
      invalidated = true
      if (action === 'close') controller.close()
      if (action === 'authentication') controller.updateContext({ ...context, authenticated: false })
      if (action === 'selection') controller.clearSelection()
    })
    await controller.confirmApply(); unsubscribe()
    expect(invalidated).toBe(true)
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
    expect(controller.getSnapshot().selection).toBeNull()
    expect(controller.getSnapshot().review).toBeNull()
    if (action === 'authentication') controller.updateContext(context)
    controller.open(); controller.select(remoteSelection)
    expect(controller.getSnapshot().attempts[0].evidence).toBeUndefined()
    expect(controller.getSnapshot().blocked).toBe(true)
  })
  it.each(['lost', 'malformed', 'synchronous', 'unknown', 'nondurable'] as const)('keeps status-only recovery after %s apply evidence', async failure => {
    const { controller, calls } = harness(call => {
      if (call.name !== 'resource.remote.management.apply') return response(call)
      if (failure === 'lost') return Promise.reject(new ApiError('network_error', ''))
      if (failure === 'synchronous') throw new Error('synthetic transport failure')
      if (failure === 'malformed') return remoteReply('apply', { operation: { operationId: remotePreview.operationId, outcome: unknown } })
      return remoteReply('apply', { operation: { operationId: remotePreview.operationId, outcome: failure === 'unknown' ? unknown : localOperation.outcome, evidenceDurable: failure === 'unknown' } })
    })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    expect(controller.getSnapshot().blocked).toBe(true)
    await controller.inspect(); await controller.preview(); await controller.confirmApply()
    expect(controller.getSnapshot().blocked).toBe(true)
    expect(calls.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
    expect(calls.filter(call => call.name === 'resource.remote.management.preview')).toHaveLength(1)
    await controller.checkStatus(controller.getSnapshot().attempts[0].attemptId)
    expect(controller.getSnapshot().blocked).toBe(false)
    expect(calls.at(-1)?.name).toBe('resource.remote.management.operation.status')
  })
  it('allows a fresh manual local review after exact pre-admission conflict, retaining the rejected review', async () => {
    let previews = 0
    const { controller, calls } = harness(call => {
      if (call.name === 'resource.preview') return { ...localPreview, revision: (++previews === 1 ? 'f' : '0').repeat(64) }
      if (call.name === 'resource.apply') throw new ApiError('resource_revision_conflict', '')
      return response(call)
    })
    await selectLocal(controller); await controller.preview(); await controller.confirmApply()
    expect(controller.getSnapshot().notice).toBe('conflict')
    expect(controller.getSnapshot().blocked).toBe(false)
    expect(controller.getSnapshot().attempts[0].dispatch).toBe('rejected')
    expect(calls.filter(call => call.name === 'resource.preview')).toHaveLength(1)
    await controller.inspect(); await controller.preview()
    expect(controller.getSnapshot().review?.preview.operationId).toBe(localPreview.operationId)
    expect(calls.filter(call => call.name === 'resource.apply')).toHaveLength(1)
    await controller.confirmApply()
    expect(controller.getSnapshot().attempts).toHaveLength(2)
  })
  it.each([new ApiError('resource_management_remote_unavailable', 'resource_revision_conflict'), new ApiError('resource_revision_conflict', ''), { code: 'resource_revision_conflict' }])('does not reinterpret remote error or arbitrary text as a conflict', async error => {
    const { controller } = harness(call => { if (call.name === 'resource.remote.management.apply') throw error; return response(call) })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    expect(controller.getSnapshot().notice).toBe('unavailable')
    expect(controller.getSnapshot().blocked).toBe(true)
  })
  it('invalidates a preview after field edit, selector edit, or cancel without mutation', async () => {
    const { controller, calls } = harness()
    await selectRemote(controller); await controller.preview()
    controller.setDraft(changedSettings); await controller.confirmApply()
    expect(controller.getSnapshot().review).toBeNull()
    await controller.preview(); controller.backToEdit(); await controller.confirmApply()
    await controller.preview(); controller.clearSelection(); await controller.confirmApply()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it.each(['close', 'selection', 'draft', 'context', 'authentication'] as const)('ignores delayed preview after %s invalidation', async action => {
    const wait = deferred(), { controller } = harness(call => call.name === 'resource.remote.management.preview' ? wait.promise : response(call))
    await selectRemote(controller)
    const pending = controller.preview()
    if (action === 'close') controller.close()
    if (action === 'selection') controller.clearSelection()
    if (action === 'draft') controller.setDraft(changedSettings)
    if (action === 'context') controller.updateContext({ ...context, processId: 456 })
    if (action === 'authentication') controller.updateContext({ ...context, authenticated: false })
    wait.resolve(remoteReply('preview')); await pending
    expect(controller.getSnapshot().review).toBeNull()
  })
  it('ignores a late previous-target read after selecting another scope', async () => {
    const wait = deferred(), { controller } = harness(call => call.name === 'resource.remote.management.inspect' ? wait.promise : response(call))
    controller.select(remoteSelection)
    const pending = controller.inspect()
    controller.select({ ...remoteSelection, selector: { ...remoteSelection.selector, grantId: '9'.repeat(32) } })
    wait.resolve(remoteReply('inspect')); await pending
    expect(controller.getSnapshot().current.state).toBe('unread')
    expect(controller.getSnapshot().selection?.kind).toBe('remote')
  })
  it.each(['stop', 'close', 'process', 'authentication'] as const)('retains uncertain identity across %s after dispatch, without accepting a late success', async action => {
    const wait = deferred(), { controller, calls } = harness(call => call.name === 'resource.remote.management.apply' ? wait.promise : response(call))
    await selectRemote(controller); await controller.preview()
    const pending = controller.confirmApply(), apply = calls.at(-1)!
    if (action === 'stop') controller.stopWaiting()
    if (action === 'close') controller.close()
    if (action === 'process') controller.updateContext({ ...context, processId: 456 })
    if (action === 'authentication') controller.updateContext({ ...context, authenticated: false })
    expect(apply.signal.aborted).toBe(true)
    if (action !== 'stop') expect(controller.getSnapshot().attempts).toHaveLength(0)
    wait.resolve(remoteReply('apply')); await pending
    if (action === 'authentication') controller.updateContext(context)
    controller.open(); controller.select(remoteSelection)
    const attempt = controller.getSnapshot().attempts[0]
    expect(attempt.operationId).toBe(remotePreview.operationId)
    expect(attempt.evidence).toBeUndefined()
    expect(controller.getSnapshot().blocked).toBe(true)
    await controller.preview(); await controller.confirmApply()
    expect(calls.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
    await controller.checkStatus(attempt.attemptId)
    expect(controller.getSnapshot().attempts[0].evidence?.outcome.status).toBe('applied')
  })
  it('clears visible data on auth error and routes to the existing authentication handler', async () => {
    const { controller, errors } = harness(call => { if (call.name === 'resource.remote.management.apply') throw new ApiError('unauthenticated', ''); return response(call) })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    expect(controller.getSnapshot()).toMatchObject({ authenticated: false, peers: [], managedDirectLAN: false, selection: null, current: { state: 'unread' }, review: null, attempts: [] })
    expect(errors).toHaveLength(1)
    controller.updateContext({ ...context, authenticated: false }); controller.updateContext(context); controller.select(remoteSelection)
    expect(controller.getSnapshot().blocked).toBe(true)
  })
  it('uses explicitly reselected current grant revision for status while retaining original apply evidence', async () => {
    const { controller, calls } = harness(call => {
      if (call.name === 'resource.remote.management.apply') throw new ApiError('network_error', '')
      if (call.name === 'resource.remote.management.operation.status') return remoteReply('operation.status', { grantRevision: 2 })
      return response(call)
    })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    controller.clearSelection(); controller.select({ ...remoteSelection, selector: { ...remoteSelection.selector, grantRevision: 2 } })
    const attempt = controller.getSnapshot().attempts[0]
    await controller.checkStatus(attempt.attemptId)
    expect((calls.at(-1)!.payload as ResourceCommandPayloads['resource.remote.management.operation.status']).request.grantRevision).toBe(2)
    expect(attempt.selection).toEqual(remoteSelection)
    expect(controller.getSnapshot().attempts[0].selection).toEqual(remoteSelection)
  })
  it.each(['unavailable', 'malformed', 'unknown', 'contradictory'] as const)('never erases terminal historical attribution after a later %s status/read', async kind => {
    const { controller } = harness(call => {
      if (call.name === 'resource.remote.management.operation.status') {
        if (kind === 'unavailable') return { ...remoteSelection.selector, action: 'operation.status', unavailable: { operationId: remotePreview.operationId } }
        if (kind === 'malformed') return {}
        return remoteReply('operation.status', { operation: { operationId: remotePreview.operationId, outcome: kind === 'unknown' ? unknown : { status: 'failed', configuration: 'not_attempted', accounting: 'not_attempted', transfer: 'not_attempted' }, evidenceDurable: true } })
      }
      return response(call)
    })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    await controller.checkStatus(controller.getSnapshot().attempts[0].attemptId)
    expect(controller.getSnapshot().attempts[0].evidence?.outcome.status).toBe('applied')
    expect(controller.getSnapshot().attempts[0].statusObservation).toBe(kind === 'unavailable' ? 'unavailable' : 'unconfirmed')
  })
  it('keeps unknown barrier after unavailable status and matching current settings', async () => {
    const { controller, calls } = harness(call => {
      if (call.name === 'resource.remote.management.apply') throw new ApiError('resource_management_remote_unavailable', '')
      if (call.name === 'resource.remote.management.operation.status') return { ...remoteSelection.selector, action: 'operation.status', unavailable: { operationId: remotePreview.operationId } }
      return response(call)
    })
    await selectRemote(controller); await controller.preview(); await controller.confirmApply()
    await controller.checkStatus(controller.getSnapshot().attempts[0].attemptId); await controller.inspect(); await controller.preview()
    expect(controller.getSnapshot().blocked).toBe(true)
    expect(controller.getSnapshot().attempts[0].statusObservation).toBe('unavailable')
    expect(controller.getSnapshot().current.values?.requested).toEqual(settings)
    expect(calls.filter(call => call.name === 'resource.remote.management.preview')).toHaveLength(1)
  })
  it('labels unsupported only for the exact authoritative protocol error code', async () => {
    let code = 'resource_remote_unsupported'
    const { controller } = harness(call => { if (call.name === 'resource.remote.management.inspect') throw new ApiError(code, ''); return response(call) })
    await selectRemote(controller)
    expect(controller.getSnapshot().notice).toBe('unavailable')
    code = 'resource_management_remote_unsupported'; await controller.inspect()
    expect(controller.getSnapshot().notice).toBe('unsupported')
  })
  it('blocks new applies instead of evicting retained operation identities at its finite budget', async () => {
    let sequence = 0
    const { controller, calls } = harness(call => {
      if (call.name === 'resource.preview') return { ...localPreview, operationId: `${descriptor.resourceId}:${'e'.repeat(32)}:${++sequence}`, revision: sequence.toString(16).padStart(64, '0') }
      if (call.name === 'resource.apply') return { ...localOperation, operationId: (call.payload as ResourceCommandPayloads['resource.apply']).operationId }
      return response(call)
    })
    await selectLocal(controller)
    for (let i = 0; i < MAX_RESOURCE_ATTEMPTS; i++) { await controller.preview(); await controller.confirmApply() }
    expect(controller.getSnapshot().attempts).toHaveLength(MAX_RESOURCE_ATTEMPTS)
    expect(controller.getSnapshot().evidenceLimitReached).toBe(true)
    expect(controller.getSnapshot().blocked).toBe(true)
    await controller.preview(); await controller.confirmApply()
    expect(calls.filter(call => call.name === 'resource.apply')).toHaveLength(MAX_RESOURCE_ATTEMPTS)
  })
})
