import { describe, expect, it } from 'vitest'
import { ResourceAttemptRegistry, MAX_SHARED_ATTEMPTS } from '../resource/attempt-registry'
import { ResourceGroupController, MAX_GROUP_ATTEMPTS } from './controller'
import type { GroupContext } from './context'
import type { GroupCommandName, GroupCommandPayloads, GroupReview, GroupTransport, PreparedView, RunView } from './types'
import { groupEvidence, groupOperation, groupOtherRunId, groupPrepared, groupRun, groupRunId, groupSelection, mutableGroup, reviseGroupReview } from './fixtures.test-support'

type Call = Readonly<{ name: GroupCommandName; payload: GroupCommandPayloads[GroupCommandName]; signal: AbortSignal }>
function deferred<T = unknown>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function completeRun(review: GroupReview): RunView {
  const evidence = mutableGroup(groupEvidence(review))
  for (const row of evidence.members) if (row.execution === 'selected') { row.dispatch = 'observed'; row.target = mutableGroup(groupOperation(row)) }
  return { ...groupRun(review, evidence), activity: 'idle' }
}
function unknownRun(review: GroupReview): RunView {
  const evidence = mutableGroup(groupEvidence(review))
  for (const row of evidence.members) if (row.execution === 'selected') row.dispatch = 'unknown'
  return { ...groupRun(review, evidence), activity: 'idle' }
}
function claimFor(review: GroupReview, index = 0) {
  const row = review.rows[index]
  if (row.state !== 'ready') throw new Error('Synthetic claim requires ready row')
  return { peerKey: row.peerKey, resourceId: row.reply.target.resourceId, operationId: row.reply.preview.operationId }
}
async function harness(options: Readonly<{ count?: number; registry?: ResourceAttemptRegistry; handler?: (call: Call, prepared: PreparedView) => unknown | Promise<unknown> }> = {}) {
  let prepared = await groupPrepared(options.count ?? 2)
  const selection = groupSelection(options.count ?? 2), registry = options.registry ?? new ResourceAttemptRegistry()
  const calls: Call[] = [], errors: unknown[] = []
  const context: GroupContext = { localAvailable: true, remoteAvailable: true, revision: 'synthetic-group-context-1', localPeerKey: 'f'.repeat(64), peers: selection.members.map((member, i) => ({ key: member.peerKey, name: `Synthetic device ${i + 1}` })) }
  const defaultResponse = async (call: Call): Promise<unknown> => {
    switch (call.name) {
      case 'resource.group.preview': return prepared
      case 'resource.group.review.select': {
        const input = call.payload as GroupCommandPayloads['resource.group.review.select']
        const unchanged = JSON.stringify(input.executionPeers) === JSON.stringify(prepared.review.executionPeers)
        if (!unchanged) prepared = { ...prepared, reviewId: groupOtherRunId, review: await reviseGroupReview({ ...prepared.review, executionPeers: input.executionPeers }), admissionState: input.executionPeers.length ? 'prepared' : 'unavailable' }
        return prepared
      }
      case 'resource.group.apply': case 'resource.group.status': case 'resource.group.status.refresh': return { ...completeRun(prepared.review), runId: prepared.reviewId }
      case 'resource.group.cancel': return { schemaVersion: 1, prepared: { ...prepared, admissionState: 'canceled' } }
      case 'resource.group.review.current': return { schemaVersion: 1, state: 'current', prepared }
    }
  }
  const transport: GroupTransport = (name, payload, signal) => {
    const call: Call = { name, payload, signal }; calls.push(call)
    return Promise.resolve(options.handler ? options.handler(call, prepared) : defaultResponse(call))
  }
  const controller = new ResourceGroupController(transport, error => errors.push(error), registry)
  controller.updateContext(context); controller.open()
  controller.setTemplate('transferConcurrentPerPeer', { mode: 'limited', value: '3' })
  for (const member of selection.members) controller.addSelection({ kind: 'remote', peerKey: member.peerKey, selector: member.selector })
  return { controller, calls, errors, registry, context, initialPrepared: prepared }
}

describe('app-lifetime fixed-group command owner', () => {
  it('keeps construction observation opening edits and closing transport-free until explicit preview', async () => {
    const h = await harness()
    h.controller.updateContext({ ...h.context }); h.controller.close(); h.controller.open()
    h.controller.setOverride(h.context.peers[0].key, 'transferConcurrentFiles', { mode: 'inherit', value: '' })
    expect(h.calls).toHaveLength(0)
    await h.controller.preview()
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.preview'])
    expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupRunId)
    expect(h.controller.getSnapshot().confirmed).toBe(false)
    expect(h.calls[0].payload).toEqual({ schemaVersion: 1, selection: groupSelection() })
  })
  it('keeps invalid or missing selectors visible and never shrinks selection to issue preview', async () => {
    const h = await harness({ count: 1 })
    h.controller.removePeer(h.context.peers[0].key); h.controller.addPeer(h.context.peers[0].key)
    await h.controller.preview()
    expect(h.controller.getSnapshot().draft.members).toHaveLength(1)
    expect(h.controller.getSnapshot().notice).toBe('invalid'); expect(h.calls).toHaveLength(0)
    expect(h.controller.addPeer(h.context.localPeerKey!)).toBe(false)
    expect(h.controller.addSelection({ kind: 'remote', peerKey: h.context.peers[0].key, selector: { ...groupSelection(1).members[0].selector, protocolVersion: 1 } })).toBe(false)
  })
  it('requires explicit subset update and a new confirmation while allowing unchanged and empty sets', async () => {
    const h = await harness(); await h.controller.preview(); h.controller.setConfirmed(true)
    const one = [h.context.peers[0].key]
    h.controller.setExecutionPeers(one)
    expect(h.controller.canConfirm()).toBe(false); expect(h.controller.getSnapshot().confirmed).toBe(false)
    await h.controller.apply(); expect(h.calls).toHaveLength(1)
    await h.controller.selectExecution()
    expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupOtherRunId)
    expect(h.controller.getSnapshot().prepared?.review.rows).toHaveLength(2)
    expect(h.controller.getSnapshot().confirmed).toBe(false)
    await h.controller.selectExecution()
    expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupOtherRunId)
    h.controller.setExecutionPeers([])
    // A separate response ID is required for a second changed subset.
    const empty = await harness(); await empty.controller.preview(); empty.controller.setExecutionPeers([]); await empty.controller.selectExecution()
    expect(empty.controller.getSnapshot().prepared?.admissionState).toBe('unavailable')
    expect(empty.controller.canConfirm()).toBe(false)
    expect(h.calls.every(call => !call.name.startsWith('resource.remote.'))).toBe(true)
  })
  it('recovers a lost preview only on an explicit identical frozen-input request', async () => {
    let previews = 0
    const h = await harness({ handler: (call, prepared) => { if (call.name === 'resource.group.preview' && ++previews === 1) throw new Error('Synthetic lost reply'); return prepared } })
    await h.controller.preview()
    expect(h.controller.getSnapshot()).toMatchObject({ preparationUnknown: true, recoveryAvailable: true, prepared: null, confirmed: false })
    const draft = h.controller.getSnapshot().draft
    h.controller.setTemplate('transferConcurrentFiles', { mode: 'limited', value: '9' }); await h.controller.preview()
    expect(h.controller.getSnapshot().draft).toBe(draft); expect(h.calls).toHaveLength(1)
    h.controller.updateContext({ ...h.context }); expect(h.calls).toHaveLength(1)
    await h.controller.recoverPreview()
    expect(h.calls).toHaveLength(2); expect(h.calls[1].payload).toEqual(h.calls[0].payload)
    expect(h.calls[1].payload).toBe(h.calls[0].payload)
    expect(h.controller.getSnapshot()).toMatchObject({ preparationUnknown: false, recoveryAvailable: false, confirmed: false })
  })
  it('recovers the same changed-subset input and replacement ID without applying', async () => {
    let selects = 0, replacement: PreparedView | undefined
    const h = await harness({ handler: async (call, prepared) => {
      if (call.name !== 'resource.group.review.select') return prepared
      const input = call.payload as GroupCommandPayloads['resource.group.review.select']
      replacement ??= { ...prepared, reviewId: groupOtherRunId, review: await reviseGroupReview({ ...prepared.review, executionPeers: input.executionPeers }) }
      if (++selects === 1) throw new Error('Synthetic lost subset reply')
      return replacement
    } })
    await h.controller.preview(); h.controller.setExecutionPeers([h.context.peers[0].key]); await h.controller.selectExecution()
    expect(h.controller.getSnapshot()).toMatchObject({ subsetRecoveryAvailable: true, preparationUnknown: true, prepared: null })
    await h.controller.apply(); expect(h.calls).toHaveLength(2)
    await h.controller.recoverSubset()
    expect(h.calls[2].payload).toBe(h.calls[1].payload)
    expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupOtherRunId)
    expect(h.controller.getSnapshot().confirmed).toBe(false)
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.preview', 'resource.group.review.select', 'resource.group.review.select'])
  })
  it('loads an explicit current review with confirmation cleared and never auto-reads on open', async () => {
    const h = await harness(); expect(h.calls).toHaveLength(0)
    await h.controller.currentReview()
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.review.current'])
    expect(h.calls[0].payload).toEqual({ schemaVersion: 1 })
    expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupRunId)
    expect(h.controller.getSnapshot().confirmed).toBe(false)
    h.controller.close(); h.controller.open(); expect(h.calls).toHaveLength(1)
  })
  it('synchronously consumes confirmed review and reserves all scopes before one apply call', async () => {
    const waiting = deferred(), h = await harness({ handler: (call, prepared) => call.name === 'resource.group.apply' ? waiting.promise : prepared })
    await h.controller.preview(); await h.controller.apply(); expect(h.calls).toHaveLength(1)
    h.controller.setConfirmed(true)
    const applying = h.controller.apply()
    expect(h.controller.getSnapshot()).toMatchObject({ prepared: null, confirmed: false, busy: 'apply', runIdInput: groupRunId })
    expect(h.controller.getSnapshot().retainedRunIds).toEqual([groupRunId])
    for (let i = 0; i < 2; i++) expect(h.registry.blocked(claimFor(h.initialPrepared.review, i))).toBe(true)
    await h.controller.apply(); expect(h.calls.filter(call => call.name === 'resource.group.apply')).toHaveLength(1)
    waiting.resolve(completeRun(h.initialPrepared.review)); await applying
    expect(h.controller.getSnapshot().run?.summary.allApplied).toBe(true)
    for (let i = 0; i < 2; i++) expect(h.registry.blocked(claimFor(h.initialPrepared.review, i))).toBe(false)
  })
  it('retains attempted identities after lost and malformed apply replies and uses only explicit local status', async () => {
    for (const failure of ['lost', 'malformed'] as const) {
      const h = await harness({ handler: (call, prepared) => {
        if (call.name === 'resource.group.apply') { if (failure === 'lost') throw new Error('Synthetic response loss'); return { schemaVersion: 1, runId: groupRunId } }
        if (call.name === 'resource.group.status') return completeRun(prepared.review)
        return prepared
      } })
      await h.controller.preview(); h.controller.setConfirmed(true); await h.controller.apply(); await h.controller.apply()
      expect(h.controller.getSnapshot()).toMatchObject({ run: null, notice: 'statusUnavailable', prepared: null })
      expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
      expect(h.calls.filter(call => call.name === 'resource.group.apply')).toHaveLength(1)
      await h.controller.status(groupRunId)
      expect(h.calls.at(-1)?.name).toBe('resource.group.status')
      expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(false)
    }
  })
  it('aborts local waiting without replay or accepting a late successful apply response', async () => {
    const waiting = deferred(), h = await harness({ handler: (call, prepared) => call.name === 'resource.group.apply' ? waiting.promise : prepared })
    await h.controller.preview(); h.controller.setConfirmed(true); const apply = h.controller.apply()
    h.controller.stopWaiting(); expect(h.calls.at(-1)?.signal.aborted).toBe(true)
    waiting.resolve(completeRun(h.initialPrepared.review)); await apply
    expect(h.controller.getSnapshot().run).toBeNull(); expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    await h.controller.apply(); expect(h.calls.filter(call => call.name === 'resource.group.apply')).toHaveLength(1)
  })
  it('rejects late preview results across context auth and close changes while keeping calls explicit', async () => {
    for (const change of ['context', 'auth', 'close'] as const) {
      const waiting = deferred(), started = deferred<void>(), h = await harness({ handler: call => { if (call.name === 'resource.group.preview') started.resolve(); return waiting.promise } })
      const preview = h.controller.preview(); await started.promise
      if (change === 'close') h.controller.close()
      else h.controller.updateContext({ ...h.context, revision: `synthetic-${change}`, ...(change === 'auth' ? { localAvailable: false, remoteAvailable: false, peers: [] } : {}) })
      expect(h.calls[0].signal.aborted).toBe(true)
      waiting.resolve(h.initialPrepared); await preview
      expect(h.controller.getSnapshot().prepared).toBeNull(); expect(h.calls).toHaveLength(1)
      if (change === 'close') {
        h.controller.open(); const draft = h.controller.getSnapshot().draft
        expect(h.controller.getSnapshot().preparationUnknown).toBe(true)
        expect(h.controller.getSnapshot().recoveryAvailable).toBe(true)
        h.controller.setTemplate('transferConcurrentFiles', { mode: 'limited', value: '7' })
        expect(h.controller.getSnapshot().draft).toBe(draft)
      }
    }
  })
  it('preserves unknown subset recovery across close and reopen', async () => {
    const waiting = deferred(), h = await harness({ handler: (call, prepared) => call.name === 'resource.group.review.select' ? waiting.promise : prepared })
    await h.controller.preview(); h.controller.setExecutionPeers([h.context.peers[0].key])
    const select = h.controller.selectExecution(); h.controller.close(); h.controller.open()
    expect(h.controller.getSnapshot()).toMatchObject({ preparationUnknown: true, subsetRecoveryAvailable: true, prepared: null, confirmed: false })
    await h.controller.preview(); expect(h.calls).toHaveLength(2)
    waiting.reject(new Error('Synthetic aborted reply')); await select
  })
  it('does not dispatch when a reentrant group or registry subscriber closes or changes authentication', async () => {
    for (const source of ['group', 'registry'] as const) {
      const h = await harness(); await h.controller.preview(); h.controller.setConfirmed(true)
      const invalidate = () => {
        if (!h.registry.blocked(claimFor(h.initialPrepared.review))) return
        if (source === 'group') h.controller.close()
        else h.controller.updateContext({ ...h.context, revision: 'synthetic-auth-lost', localAvailable: false, remoteAvailable: false, peers: [] })
      }
      let once = false
      const callback = () => { if (!once && h.registry.blocked(claimFor(h.initialPrepared.review))) { once = true; invalidate() } }
      if (source === 'group') h.controller.subscribe(callback); else h.registry.subscribe(callback)
      await h.controller.apply()
      expect(h.calls.some(call => call.name === 'resource.group.apply')).toBe(false)
      expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
      expect(h.controller.getSnapshot().retainedRunIds).toEqual([])
    }
  })
  it('blocks group admission from single-peer claims across grant and context changes while retaining unrelated scopes', async () => {
    const h = await harness(), claim = claimFor(h.initialPrepared.review)
    expect(h.registry.reserveNew('single:synthetic', [claim])).toBe(true); h.registry.emit()
    await h.controller.preview(); expect(h.calls).toHaveLength(0)
    h.controller.updateContext({ ...h.context, revision: 'synthetic-new-pair' }); h.controller.open()
    const member = groupSelection(1).members[0]
    h.controller.addSelection({ kind: 'remote', peerKey: member.peerKey, selector: { ...member.selector, grantId: 'e'.repeat(32), grantRevision: 9 } })
    await h.controller.preview(); expect(h.calls).toHaveLength(0)
    const other = groupSelection(2).members[1]
    expect(h.registry.blocked({ peerKey: other.peerKey, resourceId: other.selector.target.resourceId })).toBe(false)
  })
  it('invalidates an existing group review when another flow claims an overlapping scope', async () => {
    const h = await harness(); await h.controller.preview(); h.controller.setConfirmed(true)
    expect(h.registry.reserveNew('single:competing', [claimFor(h.initialPrepared.review)])).toBe(true); h.registry.emit()
    expect(h.controller.getSnapshot()).toMatchObject({ prepared: null, confirmed: false, blocked: true })
    await h.controller.apply(); expect(h.calls).toHaveLength(1)
  })
  it('never releases run claims from none current-review or unavailable local status', async () => {
    const h = await harness({ handler: (call, prepared) => {
      if (call.name === 'resource.group.apply') return unknownRun(prepared.review)
      if (call.name === 'resource.group.review.current') return { schemaVersion: 1, state: 'none' }
      if (call.name === 'resource.group.status') throw new Error('Synthetic unavailable local history')
      return prepared
    } })
    await h.controller.preview(); h.controller.setConfirmed(true); await h.controller.apply()
    await h.controller.currentReview()
    expect(h.controller.getSnapshot().notice).toBe('noUnusedReview')
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    await h.controller.status(groupRunId)
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    expect(h.controller.getSnapshot().retainedRunIds).toEqual([groupRunId])
  })
  it('supports explicit local status with remote backend offline and rejects altered known-run review', async () => {
    const h = await harness(); h.controller.updateContext({ ...h.context, revision: 'synthetic-local-only', remoteAvailable: false, peers: [] })
    await h.controller.preview(); expect(h.calls).toHaveLength(0)
    await h.controller.status(groupRunId)
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.status'])
    expect(h.controller.getSnapshot().run?.runId).toBe(groupRunId)
    const row = h.initialPrepared.review.rows[0]
    expect(row.state).toBe('ready')
  })
  it('retains known reviewed history instead of accepting changed selectors on later status', async () => {
    let changed: RunView | undefined
    const h = await harness({ handler: (call, prepared) => call.name === 'resource.group.status' ? changed! : call.name === 'resource.group.apply' ? unknownRun(prepared.review) : prepared })
    await h.controller.preview(); h.controller.setConfirmed(true); await h.controller.apply()
    const other = mutableGroup(h.initialPrepared.review); other.selection.members[0].selector.grantRevision++
    if (other.rows[0].state === 'ready') other.rows[0].reply.grantRevision++
    changed = completeRun(await reviseGroupReview(other))
    await h.controller.status(groupRunId)
    expect(h.controller.getSnapshot().notice).toBe('statusUnavailable')
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    expect(h.controller.getSnapshot().run?.review.revision).toBe(h.initialPrepared.review.revision)
  })
  it('refreshes only an explicit nonempty eligible original peer subset', async () => {
    const h = await harness({ handler: (call, prepared) => call.name === 'resource.group.status' || call.name === 'resource.group.status.refresh' ? unknownRun(prepared.review) : prepared })
    await h.controller.status(groupRunId); await h.controller.refreshStatus(); expect(h.calls).toHaveLength(1)
    h.controller.setRefreshPeers(['e'.repeat(64)]); await h.controller.refreshStatus(); expect(h.calls).toHaveLength(1)
    const one = [h.initialPrepared.review.executionPeers[0]]; h.controller.setRefreshPeers(one); await h.controller.refreshStatus()
    expect(h.calls.at(-1)).toMatchObject({ name: 'resource.group.status.refresh', payload: { schemaVersion: 1, runId: groupRunId, peers: one } })
    expect(Object.keys(h.calls.at(-1)!.payload)).toEqual(['schemaVersion', 'runId', 'peers'])
    expect(h.controller.getSnapshot().refreshPeers).toEqual([])
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
  })
  it('requests local admission stop by exact run ID while preserving possibly dispatched uncertainty', async () => {
    const waiting = deferred(), h = await harness({ handler: (call, prepared) => {
      if (call.name === 'resource.group.apply') return waiting.promise
      if (call.name === 'resource.group.cancel') return { schemaVersion: 1, run: unknownRun(prepared.review) }
      return prepared
    } })
    await h.controller.preview(); h.controller.setConfirmed(true); const apply = h.controller.apply()
    await h.controller.cancel()
    expect(h.calls.find(call => call.name === 'resource.group.apply')?.signal.aborted).toBe(true)
    expect(h.calls.at(-1)).toMatchObject({ name: 'resource.group.cancel', payload: { schemaVersion: 1, reviewId: groupRunId } })
    expect(h.controller.getSnapshot().run?.summary.dispatchUnknown).toBe(2)
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    waiting.resolve(completeRun(h.initialPrepared.review)); await apply
    expect(h.controller.getSnapshot().run?.summary.dispatchUnknown).toBe(2)
  })
  it('releases only validated durable idle stopped non-dispatch proof', async () => {
    const h = await harness({ handler: (call, prepared) => {
      if (call.name === 'resource.group.apply') throw new Error('Synthetic lost response')
      if (call.name === 'resource.group.status') {
        const evidence = mutableGroup(groupEvidence(prepared.review)); for (const row of evidence.members) row.admissionStop = 'user_canceled'
        return { ...groupRun(prepared.review, evidence), activity: 'idle' }
      }
      return prepared
    } })
    await h.controller.preview(); h.controller.setConfirmed(true); await h.controller.apply()
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(true)
    await h.controller.status(groupRunId)
    expect(h.controller.getSnapshot().run?.summary.notAttempted).toBe(2)
    expect(h.registry.blocked(claimFor(h.initialPrepared.review))).toBe(false)
  })
  it('refuses unseen history at sixteen retained attempts without evicting known identities', async () => {
    const h = await harness({ handler: (call, prepared) => ({ ...unknownRun(prepared.review), runId: (call.payload as GroupCommandPayloads['resource.group.status']).runId }) })
    for (let i = 1; i <= MAX_GROUP_ATTEMPTS; i++) await h.controller.status(i.toString(16).padStart(32, '0'))
    expect(h.controller.getSnapshot().retainedRunIds).toHaveLength(MAX_GROUP_ATTEMPTS)
    expect(h.controller.getSnapshot().capacityReached).toBe(true)
    const calls = h.calls.length
    await h.controller.status('e'.repeat(32)); expect(h.calls).toHaveLength(calls)
    expect(h.controller.getSnapshot().notice).toBe('limit')
    await h.controller.status('1'.padStart(32, '0')); expect(h.calls).toHaveLength(calls + 1)
  })
  it('refuses new group preview when shared bounded attempt retention is exhausted', async () => {
    const registry = new ResourceAttemptRegistry()
    for (let i = 0; i < MAX_SHARED_ATTEMPTS; i++) expect(registry.reserveNew(`single:synthetic-${i}`, [{ peerKey: (i + 100).toString(16).padStart(64, '0'), resourceId: '9'.repeat(32), operationId: '8'.repeat(64) }])).toBe(true)
    const h = await harness({ registry }); await h.controller.preview()
    expect(h.calls).toHaveLength(0); expect(h.controller.getSnapshot().capacityReached).toBe(true)
  })
})
