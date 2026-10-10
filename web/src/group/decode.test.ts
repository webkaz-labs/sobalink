import { describe, expect, it } from 'vitest'
import { MAX_GROUP_OBSERVATION_TIME } from './canonical'
import { GROUP_SUMMARY_FIELDS, isRefreshEligible, readCancelView, readCurrentReviewView, readPreviewView, readRunView, readSelectedView, sameGroupReview } from './decode'
import {
  groupApplied, groupEvidence, groupOperation, groupOtherRunId, groupPrepared, groupReview, groupRun,
  groupRunId, groupSelection, groupUnknown, mutableGroup, reviseGroupReview,
} from './fixtures.test-support'
import type { Mutable } from './fixtures.test-support'
import type { GroupMemberEvidence, GroupReview, PreparedView, RunView } from './types'
import type { Outcome } from '../resource/types'

function omit(value: object, key: string): Record<string, unknown> {
  const result: Record<string, unknown> = { ...value }; delete result[key]; return result
}
async function preparedFor(review: GroupReview, reviewId = groupRunId): Promise<PreparedView> {
  return { schemaVersion: 1, reviewId, review: await reviseGroupReview(review), admissionState: review.executionPeers.length ? 'prepared' : 'unavailable', initializesLocalEvidence: false }
}
function observedRun(review: GroupReview, outcome: Outcome = groupApplied, durable = true): RunView {
  const evidence = mutableGroup(groupEvidence(review))
  evidence.members[0].dispatch = 'observed'; evidence.members[0].target = mutableGroup(groupOperation(evidence.members[0], outcome, durable))
  return groupRun(review, evidence)
}

describe('strict fixed-group prepared and historical response readers', () => {
  it('accepts complete ready preview correspondence and independently reordered peer arrays', async () => {
    const prepared = await groupPrepared(3), raw = mutableGroup(prepared)
    raw.review.rows.reverse(); raw.review.selection.members.reverse(); raw.review.executionPeers.reverse()
    const view = await readPreviewView(raw, { schemaVersion: 1, selection: groupSelection(3) })
    expect(view).toEqual(prepared)
    expect(view.review.rows[0].state === 'ready' && view.review.rows[0].reply.preview.effective.transferConcurrentFiles).toBe(8)
    expect(view.review.selection.members[0].requested.transferConcurrentFiles).toEqual({ mode: 'default' })
    expect(Object.isFrozen(view.review.rows)).toBe(true)
    expect(Object.isFrozen(view.review.selection.members[0].selector.target)).toBe(true)
  })
  it('retains failure rows and excludes them only from the explicit execution subset', async () => {
    const original = await groupReview(5), rows = [...original.rows]
    const failures = ['unavailable', 'unsupported', 'invalid_reply', 'canceled_before_preview'] as const
    for (let i = 1; i < rows.length; i++) rows[i] = { schemaVersion: 1, peerKey: rows[i].peerKey, state: failures[i - 1] }
    const prepared = await preparedFor({ ...original, rows, executionPeers: [rows[0].peerKey] })
    const result = await readPreviewView(prepared, { schemaVersion: 1, selection: groupSelection(5) })
    expect(result.review.rows).toHaveLength(5)
    const run = await readRunView(groupRun(result.review), { runId: groupRunId, review: result.review })
    expect(run.summary.reviewFailures).toBe(4); expect(run.summary.excluded).toBe(4)
    for (const row of run.evidence.members.slice(1)) { expect(row.request).toBeUndefined(); expect(row.dispatch).toBe('not_attempted') }
    const allFailed = await preparedFor({ ...original, rows: original.rows.map(row => ({ schemaVersion: 1, peerKey: row.peerKey, state: 'unavailable' })), executionPeers: [] })
    expect((await readPreviewView(allFailed, { schemaVersion: 1, selection: groupSelection(5) })).admissionState).toBe('unavailable')
    await expect(readPreviewView({ ...allFailed, admissionState: 'prepared' }, { schemaVersion: 1, selection: groupSelection(5) })).rejects.toThrow()
  })
  it('rejects omitted, duplicate, expanded and unrelated review membership', async () => {
    const prepared = await groupPrepared(), input = { schemaVersion: 1 as const, selection: groupSelection() }
    const changes: ((value: Mutable<PreparedView>) => void)[] = [
      value => { value.review.rows.pop() },
      value => { value.review.rows[1] = value.review.rows[0] },
      value => { value.review.rows[0].peerKey = 'f'.repeat(64) },
      value => { value.review.selection.members.pop() },
      value => { value.review.selection.members[0].selector.grantRevision++ },
      value => { value.review.selection.members[0].requested.transferConcurrentFiles = { mode: 'limited', value: 4 } },
      value => { value.review.executionPeers.pop() },
      value => { value.review.executionPeers.push(value.review.executionPeers[0]) },
      value => { value.review.executionPeers[0] = 'f'.repeat(64) },
      value => { value.review.revision = 'f'.repeat(64) },
    ]
    for (const change of changes) { const bad = mutableGroup(prepared); change(bad); await expect(readPreviewView(bad, input)).rejects.toThrow() }
    for (const field of ['schemaVersion', 'reviewId', 'review', 'admissionState', 'initializesLocalEvidence']) await expect(readPreviewView(omit(prepared, field), input)).rejects.toThrow()
    await expect(readPreviewView({ ...prepared, initializesLocalEvidence: null }, input)).rejects.toThrow()
    await expect(readPreviewView({ ...prepared, origin: 'invented' }, input)).rejects.toThrow()
  })
  it('checks selectors, requested choices, opaque preview tokens and closed reply unions', async () => {
    const prepared = await groupPrepared(1), row = prepared.review.rows[0]
    if (row.state !== 'ready') throw new Error('Expected synthetic ready row')
    const input = { schemaVersion: 1 as const, selection: groupSelection(1) }
    const replies: unknown[] = [
      { ...row.reply, protocolVersion: 1 }, { ...row.reply, grantId: 'f'.repeat(32) }, { ...row.reply, grantRevision: 2 },
      { ...row.reply, target: { ...row.reply.target, resourceId: 'f'.repeat(32) } }, { ...row.reply, action: 'apply' },
      { ...row.reply, unavailable: { operationId: row.reply.preview.operationId } },
      { ...row.reply, preview: { ...row.reply.preview, operationId: 'invalid' } },
      { ...row.reply, preview: { ...row.reply.preview, baseRevision: 'invalid' } },
      { ...row.reply, preview: { ...row.reply.preview, reviewRevision: 'invalid' } },
      { ...row.reply, preview: { ...row.reply.preview, requested: { ...row.reply.preview.requested, transferConcurrentFiles: { mode: 'limited', value: 4 } } } },
      { ...row.reply, preview: { ...row.reply.preview, effective: { ...row.reply.preview.effective, transferConcurrentFiles: 0 } } },
      null,
    ]
    for (const reply of replies) await expect(readPreviewView({ ...prepared, review: { ...prepared.review, rows: [{ ...row, reply }] } }, input)).rejects.toThrow()
    await expect(readPreviewView({ ...prepared, review: { ...prepared.review, rows: [{ ...row, state: 'unavailable' }] } }, input)).rejects.toThrow()
    const altered = mutableGroup(prepared)
    if (altered.review.rows[0].state === 'ready') altered.review.rows[0].reply.preview.operationId = 'f'.repeat(64)
    await expect(readPreviewView(altered, input)).rejects.toThrow()
  })
  it('accepts unchanged subset as a no-op and binds changed and empty subsets to replacement IDs', async () => {
    const previous = await groupPrepared(3)
    expect(await readSelectedView(previous, previous, [...previous.review.executionPeers].reverse())).toEqual(previous)
    const peers = previous.review.executionPeers.slice(0, 1)
    const selected = await preparedFor({ ...previous.review, executionPeers: peers }, groupOtherRunId)
    expect((await readSelectedView(selected, previous, peers)).review.rows).toHaveLength(3)
    const empty = await preparedFor({ ...selected.review, executionPeers: [] }, '3'.repeat(32))
    expect((await readSelectedView(empty, selected, [])).admissionState).toBe('unavailable')
    await expect(readSelectedView({ ...selected, reviewId: previous.reviewId }, previous, peers)).rejects.toThrow()
    await expect(readSelectedView({ ...previous, reviewId: groupOtherRunId }, previous, previous.review.executionPeers)).rejects.toThrow()
    await expect(readSelectedView(selected, previous, [])).rejects.toThrow()
    await expect(readSelectedView({ ...selected, initializesLocalEvidence: true }, previous, peers)).rejects.toThrow()
    const changed = mutableGroup(selected)
    if (changed.review.rows[0].state === 'ready') changed.review.rows[0].reply.preview.baseRevision = 'f'.repeat(64)
    changed.review = mutableGroup(await reviseGroupReview(changed.review))
    await expect(readSelectedView(changed, previous, peers)).rejects.toThrow()
    const evidence = await readRunView(groupRun(selected.review), { runId: selected.reviewId, review: selected.review }).catch(() => undefined)
    expect(evidence).toBeUndefined() // The run's synthetic ID must also match.
    const run = await readRunView({ ...groupRun(selected.review), runId: selected.reviewId }, { runId: selected.reviewId })
    expect(run.evidence.members[1].request).toBeDefined()
    expect(run.evidence.members[1].execution).toBe('excluded')
  })
  it('requires all 21 summary fields independently, including false and zero', async () => {
    const run = groupRun(await groupReview())
    expect(GROUP_SUMMARY_FIELDS).toHaveLength(21)
    for (const field of GROUP_SUMMARY_FIELDS) {
      await expect(readRunView({ ...run, summary: omit(run.summary, field) }, { runId: groupRunId })).rejects.toThrow()
      const actual = run.summary[field]
      const changed = { ...run.summary, [field]: typeof actual === 'boolean' ? !actual : actual + 1 }
      await expect(readRunView({ ...run, summary: changed }, { runId: groupRunId })).rejects.toThrow()
      await expect(readCancelView({ schemaVersion: 1, run: { ...run, summary: omit(run.summary, field) } }, { reviewId: groupRunId })).rejects.toThrow()
    }
    await expect(readRunView({ ...run, summary: { ...run.summary, extra: 0 } }, { runId: groupRunId })).rejects.toThrow()
  })
  it('retains original request selectors, all three tokens, settings and exact evidence coverage', async () => {
    const run = groupRun(await groupReview())
    const changes: ((value: Mutable<RunView>) => void)[] = [
      value => { value.evidence.members.pop() }, value => { value.evidence.members[1] = value.evidence.members[0] },
      value => { value.evidence.members[0].peerKey = 'f'.repeat(64) },
      value => { value.evidence.members[0].groupRevision = 'f'.repeat(64) },
      value => { value.evidence.members[0].execution = 'excluded' },
      value => { value.evidence.members[0].request!.grantRevision++ },
      value => { value.evidence.members[0].request!.grantId = 'f'.repeat(32) },
      value => { value.evidence.members[0].request!.target.resourceId = 'f'.repeat(32) },
      value => { value.evidence.members[0].request!.apply.operationId = 'f'.repeat(64) },
      value => { value.evidence.members[0].request!.apply.baseRevision = 'f'.repeat(64) },
      value => { value.evidence.members[0].request!.apply.reviewRevision = value.review.revision },
      value => { value.evidence.members[0].request!.apply.settings.transferConcurrentFiles = { mode: 'limited', value: 8 } },
    ]
    for (const change of changes) { const bad = mutableGroup(run); change(bad); await expect(readRunView(bad, { runId: groupRunId })).rejects.toThrow() }
    const reordered = mutableGroup(run); reordered.evidence.members.reverse(); reordered.review.rows.reverse(); reordered.review.selection.members.reverse()
    expect(await readRunView(reordered, { runId: groupRunId })).toEqual(run)
    await expect(readRunView({ ...run, evidence: { ...run.evidence, members: [{ ...run.evidence.members[0], request: { ...run.evidence.members[0].request, confirm: true } }, run.evidence.members[1]] } }, { runId: groupRunId })).rejects.toThrow()
  })
  it('separately reduces target outcomes, local durability, dispatch and admission stops', async () => {
    const review = await groupReview(4), evidence = mutableGroup(groupEvidence(review))
    evidence.members[0].dispatch = 'observed'; evidence.members[0].target = mutableGroup(groupOperation(evidence.members[0]))
    evidence.members[1].dispatch = 'observed'; evidence.members[1].target = mutableGroup(groupOperation(evidence.members[1], { status: 'saved_not_applied', configuration: 'durable', accounting: 'succeeded', transfer: 'failed' }))
    evidence.members[2].dispatch = 'unknown'
    evidence.members[3].admissionStop = 'user_canceled'
    const run = await readRunView(groupRun(review, evidence), { runId: groupRunId })
    expect(run.summary).toEqual({ schemaVersion: 1, selected: 4, executable: 4, excluded: 0, reviewFailures: 0, notAttempted: 1, dispatching: 0, dispatchObserved: 2, dispatchUnknown: 1, applied: 1, failed: 0, canceled: 0, savedNotApplied: 1, targetUnknown: 0, targetUnobserved: 2, targetNonDurable: 0, localNonDurable: 0, statusFailures: 0, admissionFinished: true, reconciliationRequired: true, allApplied: false })
    expect(run.evidence.members[3].target).toBeUndefined()
    const single = await groupReview(1), uncertain = mutableGroup(observedRun(single))
    uncertain.evidence.members[0].localDurability = 'uncertain'
    const result = await readRunView(groupRun(single, uncertain.evidence), { runId: groupRunId })
    expect(result.summary.allApplied).toBe(true); expect(result.summary.reconciliationRequired).toBe(true)
    expect(result.summary.targetNonDurable).toBe(0); expect(result.summary.localNonDurable).toBe(1)
    await expect(readRunView({ ...result, localDurability: 'durable' }, { runId: groupRunId })).rejects.toThrow()
  })
  it('checks all target outcomes and required false durability without promoting local evidence', async () => {
    const review = await groupReview(1)
    const outcomes: Outcome[] = [groupApplied, groupUnknown,
      { status: 'failed', configuration: 'not_published', accounting: 'not_attempted', transfer: 'not_attempted' },
      { status: 'canceled', configuration: 'not_attempted', accounting: 'not_attempted', transfer: 'not_attempted' },
      { status: 'saved_not_applied', configuration: 'durable', accounting: 'failed', transfer: 'not_attempted' },
    ]
    for (const outcome of outcomes) for (const durable of [false, true]) {
      const result = await readRunView(observedRun(review, outcome, durable), { runId: groupRunId })
      expect(result.evidence.members[0].target?.evidenceDurable).toBe(durable)
      expect(result.summary.allApplied).toBe(durable && outcome.status === 'applied')
      expect(result.summary.reconciliationRequired).toBe(!durable || outcome.status === 'unknown')
      const row = result.evidence.members[0]
      await expect(readRunView({ ...result, evidence: { schemaVersion: 1, members: [{ ...row, target: omit(row.target!, 'evidenceDurable') }] } }, { runId: groupRunId })).rejects.toThrow()
    }
    const raw = mutableGroup(observedRun(review))
    raw.evidence.members[0].target!.outcome.configuration = 'uncertain'
    await expect(readRunView(raw, { runId: groupRunId })).rejects.toThrow()
  })
  it('preserves stronger historical target evidence beside weaker or failed latest observations', async () => {
    const review = await groupReview(1), previous = observedRun(review)
    for (const state of ['unavailable', 'unsupported', 'query_failed'] as const) {
      const evidence = mutableGroup(previous.evidence)
      evidence.members[0].status = { state, sequence: 1, observedAt: 2 }
      const result = await readRunView(groupRun(review, evidence), { runId: groupRunId, previous })
      expect(result.summary.allApplied).toBe(true); expect(result.summary.statusFailures).toBe(1)
      expect(result.evidence.members[0].target).toEqual(previous.evidence.members[0].target)
    }
    const evidence = mutableGroup(previous.evidence)
    evidence.members[0].status = { state: 'observed', sequence: 1, observedAt: 2, operation: mutableGroup(groupOperation(evidence.members[0], groupUnknown, false)) }
    const result = await readRunView(groupRun(review, evidence), { runId: groupRunId, previous })
    expect(result.summary.allApplied).toBe(true)
    const conflicting = mutableGroup(evidence)
    conflicting.members[0].status = { state: 'observed', sequence: 2, observedAt: 3, operation: mutableGroup(groupOperation(conflicting.members[0], { status: 'failed', configuration: 'not_attempted', accounting: 'not_attempted', transfer: 'not_attempted' })) }
    await expect(readRunView(groupRun(review, conflicting), { runId: groupRunId })).rejects.toThrow()
  })
  it('enforces dispatch/status evidence unions and refresh eligibility', async () => {
    const review = await groupReview(1), fresh = groupRun(review), row = fresh.evidence.members[0]
    expect(isRefreshEligible(row)).toBe(false)
    for (const update of [
      { dispatch: 'observed' }, { dispatch: 'not_attempted', target: groupOperation(row) },
      { dispatch: 'dispatching', localDurability: 'not_saved' }, { dispatch: 'dispatching', target: groupOperation(row) },
      { dispatch: 'unknown', target: groupOperation(row) },
      { status: { state: 'unavailable', sequence: 1, observedAt: 1 } },
      { status: { state: 'not_queried', sequence: 1, observedAt: 0 } },
      { status: { state: 'not_queried', sequence: 0, observedAt: 0, operation: groupOperation(row) } },
    ]) await expect(readRunView({ ...fresh, evidence: { schemaVersion: 1, members: [{ ...row, ...update }] } }, { runId: groupRunId })).rejects.toThrow()
    const dispatching: GroupMemberEvidence = { ...row, dispatch: 'dispatching' }
    expect((await readRunView(groupRun(review, { schemaVersion: 1, members: [dispatching] }), { runId: groupRunId })).summary.dispatching).toBe(1)
    expect(isRefreshEligible(dispatching)).toBe(false)
    expect(isRefreshEligible({ ...row, dispatch: 'unknown' })).toBe(true)
    expect(isRefreshEligible({ ...row, dispatch: 'observed', target: groupOperation(row) })).toBe(true)
  })
  it('bounds positive seconds timestamps and safe status sequences, including clock movement', async () => {
    const review = await groupReview(1), base = observedRun(review), row = base.evidence.members[0]
    for (const acceptedAt of [0, -1, 1.5, MAX_GROUP_OBSERVATION_TIME + 1, NaN, Infinity]) await expect(readRunView({ ...base, acceptedAt }, { runId: groupRunId })).rejects.toThrow()
    expect((await readRunView({ ...base, acceptedAt: MAX_GROUP_OBSERVATION_TIME }, { runId: groupRunId })).acceptedAt).toBe(MAX_GROUP_OBSERVATION_TIME)
    for (const [sequence, observedAt] of [[0, 1], [1.5, 1], [Number.MAX_SAFE_INTEGER + 1, 1], [1, 0], [1, MAX_GROUP_OBSERVATION_TIME + 1], [NaN, 1]]) {
      const evidence = { schemaVersion: 1 as const, members: [{ ...row, status: { state: 'unavailable' as const, sequence, observedAt } }] }
      await expect(readRunView(groupRun(review, evidence), { runId: groupRunId })).rejects.toThrow()
    }
    const earlier = groupRun(review, { schemaVersion: 1, members: [{ ...row, status: { state: 'unavailable', sequence: 1, observedAt: 10 } }] })
    const later = groupRun(review, { schemaVersion: 1, members: [{ ...row, status: { state: 'unavailable', sequence: 2, observedAt: 9 } }] })
    expect((await readRunView(later, { runId: groupRunId, previous: earlier })).evidence.members[0].status.observedAt).toBe(9)
    await expect(readRunView(earlier, { runId: groupRunId, previous: later })).rejects.toThrow()
    const sameSequenceChanged = groupRun(review, { schemaVersion: 1, members: [{ ...row, status: { state: 'query_failed', sequence: 2, observedAt: 9 } }] })
    await expect(readRunView(sameSequenceChanged, { runId: groupRunId, previous: later })).rejects.toThrow()
  })
  it('binds retained history while supporting explicit runId-only local reload reads', async () => {
    const review = await groupReview(1), previous = observedRun(review)
    expect(await readRunView(previous, { runId: groupRunId })).toEqual(previous)
    expect(await readRunView(previous, { runId: groupRunId, review, previous })).toEqual(previous)
    await expect(readRunView(previous, { runId: groupOtherRunId })).rejects.toThrow()
    await expect(readRunView({ ...previous, acceptedAt: 2 }, { runId: groupRunId, previous })).rejects.toThrow()
    await expect(readRunView(observedRun(review, groupUnknown, false), { runId: groupRunId, previous })).rejects.toThrow()
    await expect(readRunView(groupRun(review), { runId: groupRunId, previous })).rejects.toThrow()
    const modified = mutableGroup(review)
    modified.selection.members[0].selector.grantRevision++
    if (modified.rows[0].state === 'ready') modified.rows[0].reply.grantRevision++
    const other = await reviseGroupReview(modified)
    expect(sameGroupReview(review, other)).toBe(false)
    await expect(readRunView(groupRun(other), { runId: groupRunId, review })).rejects.toThrow()
  })
  it('accepts exactly one canceled prepared/run arm with the same known ID', async () => {
    const prepared = await groupPrepared(1), canceled = { ...prepared, admissionState: 'canceled' as const }, run = groupRun(prepared.review)
    expect((await readCancelView({ schemaVersion: 1, prepared: canceled }, { reviewId: groupRunId, review: prepared.review })).prepared?.admissionState).toBe('canceled')
    expect((await readCancelView({ schemaVersion: 1, run }, { reviewId: groupRunId })).run).toEqual(run)
    for (const value of [{ schemaVersion: 1 }, { schemaVersion: 1, prepared }, { schemaVersion: 1, prepared: canceled, run }, { schemaVersion: 1, run: null }, { schemaVersion: 1, run, unknown: false }]) await expect(readCancelView(value, { reviewId: groupRunId })).rejects.toThrow()
    await expect(readCancelView({ schemaVersion: 1, prepared: canceled }, { reviewId: groupOtherRunId })).rejects.toThrow()
    await expect(readCancelView({ schemaVersion: 1, prepared: canceled }, { reviewId: groupRunId, previous: run })).rejects.toThrow()
  })
  it('freezes every retained response before hashing and never aliases mutable caller data', async () => {
    const raw = mutableGroup(observedRun(await groupReview(1))), promise = readRunView(raw, { runId: groupRunId })
    raw.evidence.members[0].target!.evidenceDurable = false
    raw.review.selection.members[0].selector.grantRevision = 7
    const result = await promise
    expect(result.evidence.members[0].target?.evidenceDurable).toBe(true)
    expect(result.review.selection.members[0].selector.grantRevision).toBe(1)
    for (const value of [result, result.evidence, result.evidence.members, result.evidence.members[0], result.evidence.members[0].target, result.evidence.members[0].status, result.summary]) expect(Object.isFrozen(value)).toBe(true)
  })
  it('reads the separately reviewed none/current union without inventing history or admission', async () => {
    expect(await readCurrentReviewView({ schemaVersion: 1, state: 'none' })).toEqual({ schemaVersion: 1, state: 'none' })
    const original = await groupPrepared(3), subset = original.review.executionPeers.slice(0, 1)
    const selected = await preparedFor({ ...original.review, executionPeers: subset }, groupOtherRunId)
    expect((await readCurrentReviewView({ schemaVersion: 1, state: 'current', prepared: selected })).state).toBe('current')
    const empty = await preparedFor({ ...selected.review, executionPeers: [] })
    expect(await readCurrentReviewView({ schemaVersion: 1, state: 'current', prepared: empty })).toEqual({ schemaVersion: 1, state: 'current', prepared: empty })
    for (const value of [null, {}, { schemaVersion: 1 }, { schemaVersion: 2, state: 'none' },
      { schemaVersion: 1, state: 'none', prepared: null }, { schemaVersion: 1, state: 'none', prepared: selected },
      { schemaVersion: 1, state: 'current' }, { schemaVersion: 1, state: 'current', prepared: null },
      { schemaVersion: 1, state: 'current', prepared: { ...selected, admissionState: 'canceled' } },
      { schemaVersion: 1, state: 'current', prepared: { ...empty, admissionState: 'prepared' } },
      { schemaVersion: 1, state: 'current', prepared: selected, run: groupRun(selected.review) },
      { schemaVersion: 1, state: 'none', runId: groupRunId }, { schemaVersion: 1, state: 'future' },
    ]) await expect(readCurrentReviewView(value)).rejects.toThrow()
    const changed = mutableGroup(selected); changed.review.revision = 'f'.repeat(64)
    await expect(readCurrentReviewView({ schemaVersion: 1, state: 'current', prepared: changed })).rejects.toThrow()
    const raw = mutableGroup(selected), promise = readCurrentReviewView({ schemaVersion: 1, state: 'current', prepared: raw })
    raw.reviewId = groupRunId
    const result = await promise
    expect(result.state === 'current' && result.prepared.reviewId).toBe(groupOtherRunId)
    expect(Object.isFrozen(result)).toBe(true)
  })
  it('decodes exact changed-subset recovery as the same replacement view with no extra authority fields', async () => {
    const previous = await groupPrepared(3), subset = previous.review.executionPeers.slice(0, 2)
    const selected = await preparedFor({ ...previous.review, executionPeers: subset }, groupOtherRunId)
    const first = await readSelectedView(selected, previous, subset)
    const recovered = await readSelectedView(mutableGroup(selected), previous, [...subset].reverse())
    expect(recovered).toEqual(first)
    expect(recovered.reviewId).not.toBe(previous.reviewId)
    const empty = await preparedFor({ ...previous.review, executionPeers: [] }, groupOtherRunId)
    expect((await readSelectedView(empty, previous, [])).admissionState).toBe('unavailable')
    await expect(readSelectedView({ ...selected, recoveryToken: 'invented' }, previous, subset)).rejects.toThrow()
    await expect(readSelectedView({ ...selected, admissionState: 'canceled' }, previous, subset)).rejects.toThrow()
    await expect(readSelectedView(selected, previous, previous.review.executionPeers)).rejects.toThrow()
  })
})
