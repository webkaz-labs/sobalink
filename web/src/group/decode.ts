import { readOutcome, sameSettings } from '../resource/decode'
import type { ManagementPreview, OperationEvidence, RemoteSelector } from '../resource/types'
import {
  groupArray, groupBoolean, groupBounded, groupClosed, groupDigest, groupEnum, groupID, groupInteger,
  groupLiteral, groupObject, groupReviewRevision, invalidGroup, MAX_GROUP_EVIDENCE_BYTES,
  MAX_GROUP_MEMBERS, MAX_GROUP_OBSERVATION_TIME, MAX_GROUP_RESPONSE_BYTES, MAX_GROUP_REVIEW_BYTES,
  readExecutionPeers, readGroupSelector, readGroupSettings, readPreviewInputShape, readSelectionShape,
  resolveSelectionShape, reviewCanonicalJSON, verifyTemplate,
} from './canonical'
import type {
  CancelView, CurrentReviewView, GroupApplyRequest, GroupEvidence, GroupLocalDurability, GroupMemberEvidence,
  GroupPreviewReply, GroupResolvedMember, GroupResolvedSelection, GroupReview, GroupReviewRow,
  GroupSelection, GroupStatusObservation, GroupSummary, PreparedView, PreviewInput, RunView,
} from './types'

export { GroupResponseError } from './canonical'

function sameSelector(a: RemoteSelector<2>, b: RemoteSelector<2>): boolean {
  return a.protocolVersion === b.protocolVersion && a.target.schemaVersion === b.target.schemaVersion
    && a.target.resourceId === b.target.resourceId && a.grantId === b.grantId && a.grantRevision === b.grantRevision
}
function samePeers(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every(peer => b.includes(peer))
}
export function sameGroupSelection(a: GroupSelection | GroupResolvedSelection, b: GroupSelection | GroupResolvedSelection): boolean {
  if (a.schemaVersion !== b.schemaVersion || a.template.revision !== b.template.revision || !sameSettings(a.template.settings, b.template.settings) || a.members.length !== b.members.length) return false
  return a.members.every(member => {
    const other = b.members.find(candidate => candidate.peerKey === member.peerKey)
    if (!other || !sameSelector(member.selector, other.selector) || Boolean(member.override) !== Boolean(other.override)) return false
    for (const key of ['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const) {
      const left = member.override?.[key], right = other.override?.[key]
      if (Boolean(left) !== Boolean(right) || left && right && (left.mode !== right.mode || left.mode === 'limited' && (right.mode !== 'limited' || left.value !== right.value))) return false
    }
    return true
  })
}
export function sameGroupReview(a: GroupReview, b: GroupReview): boolean {
  return a.revision === b.revision && reviewCanonicalJSON(a) === reviewCanonicalJSON(b)
}
function readResolvedSelection(value: unknown): GroupResolvedSelection {
  const object = groupClosed(value, ['schemaVersion', 'template', 'members'])
  const supplied = groupArray(object.members, 1).map(raw => {
    const member = groupClosed(raw, ['peerKey', 'selector', 'requested'], ['override'])
    return { member: { peerKey: member.peerKey, selector: member.selector, ...(Object.hasOwn(member, 'override') ? { override: member.override } : {}) }, requested: readGroupSettings(member.requested) }
  })
  const selection = readSelectionShape({ schemaVersion: object.schemaVersion, template: object.template, members: supplied.map(row => row.member) })
  const resolved = resolveSelectionShape(selection)
  for (const raw of supplied) {
    const member = resolved.members.find(candidate => candidate.peerKey === raw.member.peerKey)
    if (!member || !sameSettings(member.requested, raw.requested)) return invalidGroup()
  }
  return resolved
}
function readPreviewReply(value: unknown, member: GroupResolvedMember): GroupPreviewReply {
  const object = groupClosed(value, ['protocolVersion', 'target', 'grantId', 'grantRevision', 'action', 'preview'])
  const selector = readGroupSelector({ protocolVersion: object.protocolVersion, target: object.target, grantId: object.grantId, grantRevision: object.grantRevision })
  if (!sameSelector(selector, member.selector)) return invalidGroup()
  const preview = groupClosed(object.preview, ['operationId', 'baseRevision', 'reviewRevision', 'requested', 'effective'])
  const effective = groupClosed(preview.effective, ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  const requested = readGroupSettings(preview.requested)
  if (!sameSettings(requested, member.requested)) return invalidGroup()
  return Object.freeze({ ...selector, action: groupLiteral(object.action, 'preview'), preview: Object.freeze({
    operationId: groupDigest(preview.operationId), baseRevision: groupDigest(preview.baseRevision), reviewRevision: groupDigest(preview.reviewRevision), requested,
    effective: Object.freeze({ transferConcurrentFiles: groupInteger(effective.transferConcurrentFiles, 1), transferConcurrentPerPeer: groupInteger(effective.transferConcurrentPerPeer, 1) }),
  }) })
}
function readReviewShape(value: unknown): GroupReview {
  const object = groupClosed(value, ['schemaVersion', 'selection', 'rows', 'executionPeers', 'revision'])
  const selection = readResolvedSelection(object.selection), members = new Map(selection.members.map(member => [member.peerKey, member]))
  const rows = groupArray(object.rows, 1).map((raw): GroupReviewRow => {
    const row = groupObject(raw)
    groupClosed(row, ['schemaVersion', 'peerKey', 'state'], row.state === 'ready' ? ['reply'] : [])
    const peerKey = groupDigest(row.peerKey), member = members.get(peerKey)
    if (!member) return invalidGroup()
    const base = { schemaVersion: groupLiteral(row.schemaVersion, 1), peerKey }
    if (row.state === 'ready') return Object.freeze({ ...base, state: 'ready', reply: readPreviewReply(row.reply, member) })
    return Object.freeze({ ...base, state: groupEnum(row.state, ['unavailable', 'unsupported', 'invalid_reply', 'canceled_before_preview'] as const) })
  }).sort((a, b) => a.peerKey < b.peerKey ? -1 : a.peerKey > b.peerKey ? 1 : 0)
  if (rows.length !== members.size || rows.some((row, i) => i > 0 && row.peerKey === rows[i - 1].peerKey)) return invalidGroup()
  const executionPeers = readExecutionPeers(object.executionPeers)
  if (executionPeers.some(peer => !rows.some(row => row.peerKey === peer && row.state === 'ready'))) return invalidGroup()
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), selection, rows: Object.freeze(rows), executionPeers, revision: groupDigest(object.revision) }), MAX_GROUP_REVIEW_BYTES)
}
async function verifyReview(review: GroupReview): Promise<void> {
  await verifyTemplate(review.selection.template)
  if (await groupReviewRevision(review) !== review.revision) invalidGroup()
}
function readPreparedShape(value: unknown): PreparedView {
  const object = groupClosed(value, ['schemaVersion', 'reviewId', 'review', 'admissionState', 'initializesLocalEvidence'])
  const review = readReviewShape(object.review)
  const admissionState = groupEnum(object.admissionState, ['prepared', 'canceled', 'unavailable'] as const)
  if (admissionState === 'prepared' && review.executionPeers.length === 0) return invalidGroup()
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), reviewId: groupID(object.reviewId), review, admissionState, initializesLocalEvidence: groupBoolean(object.initializesLocalEvidence) }), MAX_GROUP_RESPONSE_BYTES)
}
export async function readPreviewView(value: unknown, input: PreviewInput): Promise<PreparedView> {
  // Copy the full response and expectation before the first asynchronous step.
  const expected = readPreviewInputShape(input), view = readPreparedShape(value)
  if (!sameGroupSelection(view.review.selection, expected.selection)
    || !samePeers(view.review.executionPeers, view.review.rows.filter(row => row.state === 'ready').map(row => row.peerKey))) return invalidGroup()
  await verifyTemplate(expected.selection.template)
  await verifyReview(view.review)
  return view
}
export async function readSelectedView(value: unknown, previous: PreparedView, executionPeers: readonly string[]): Promise<PreparedView> {
  const old = readPreparedShape(previous), requested = readExecutionPeers(executionPeers), view = readPreparedShape(value)
  if (old.admissionState === 'canceled' || !samePeers(view.review.executionPeers, requested)
    || view.initializesLocalEvidence !== old.initializesLocalEvidence
    || !sameGroupReview({ ...view.review, executionPeers: old.review.executionPeers, revision: old.review.revision }, old.review)) return invalidGroup()
  const unchanged = samePeers(requested, old.review.executionPeers)
  if (unchanged ? view.reviewId !== old.reviewId || view.review.revision !== old.review.revision || view.admissionState !== old.admissionState
    : view.reviewId === old.reviewId || view.review.revision === old.review.revision || view.admissionState !== (requested.length ? 'prepared' : 'unavailable')) return invalidGroup()
  await verifyReview(old.review)
  await verifyReview(view.review)
  return view
}
// Current means this Core's one unused prepared object, not this browser's
// previous draft and not evidence about accepted runs or target nonexecution.
export async function readCurrentReviewView(value: unknown): Promise<CurrentReviewView> {
  const object = groupObject(value)
  groupLiteral(object.schemaVersion, 1)
  if (object.state === 'none') {
    groupClosed(object, ['schemaVersion', 'state'])
    return Object.freeze({ schemaVersion: 1, state: 'none' })
  }
  groupClosed(object, ['schemaVersion', 'state', 'prepared'])
  groupLiteral(object.state, 'current')
  const prepared = readPreparedShape(object.prepared)
  if (prepared.admissionState === 'canceled') return invalidGroup()
  const result = groupBounded(Object.freeze({ schemaVersion: 1 as const, state: 'current' as const, prepared }), MAX_GROUP_RESPONSE_BYTES)
  await verifyReview(prepared.review)
  return result
}
function samePreviewRequest(request: GroupApplyRequest, selector: RemoteSelector<2>, preview: ManagementPreview): boolean {
  return sameSelector(request, selector) && request.apply.operationId === preview.operationId
    && request.apply.baseRevision === preview.baseRevision && request.apply.reviewRevision === preview.reviewRevision
    && sameSettings(request.apply.settings, preview.requested)
}
function readOriginalRequest(value: unknown, row: Extract<GroupReviewRow, { state: 'ready' }>): GroupApplyRequest {
  const object = groupClosed(value, ['protocolVersion', 'target', 'grantId', 'grantRevision', 'action', 'apply'])
  const selector = readGroupSelector({ protocolVersion: object.protocolVersion, target: object.target, grantId: object.grantId, grantRevision: object.grantRevision })
  const apply = groupClosed(object.apply, ['operationId', 'baseRevision', 'reviewRevision', 'settings'])
  const request = Object.freeze({ ...selector, action: groupLiteral(object.action, 'apply'), apply: Object.freeze({ operationId: groupDigest(apply.operationId), baseRevision: groupDigest(apply.baseRevision), reviewRevision: groupDigest(apply.reviewRevision), settings: readGroupSettings(apply.settings) }) })
  if (!samePreviewRequest(request, row.reply, row.reply.preview)) return invalidGroup()
  return request
}
function readOperation(value: unknown, operationId: string): OperationEvidence {
  const object = groupClosed(value, ['operationId', 'outcome', 'evidenceDurable'])
  const outcome = groupClosed(object.outcome, ['status', 'configuration', 'accounting', 'transfer'])
  if (groupDigest(object.operationId) !== operationId) return invalidGroup()
  return Object.freeze({ operationId, outcome: readOutcome({ status: outcome.status, configuration: outcome.configuration, accounting: outcome.accounting, transfer: outcome.transfer }), evidenceDurable: groupBoolean(object.evidenceDurable) })
}
export function terminalDurable(operation: OperationEvidence | undefined): boolean {
  return operation !== undefined && operation.evidenceDurable && operation.outcome.status !== 'unknown'
}
function sameOutcome(a: OperationEvidence, b: OperationEvidence): boolean {
  return a.operationId === b.operationId && a.outcome.status === b.outcome.status && a.outcome.configuration === b.outcome.configuration
    && a.outcome.accounting === b.outcome.accounting && a.outcome.transfer === b.outcome.transfer
}
function sameOperation(a: OperationEvidence | undefined, b: OperationEvidence | undefined): boolean {
  return a === undefined || b === undefined ? a === b : sameOutcome(a, b) && a.evidenceDurable === b.evidenceDurable
}
function readStatus(value: unknown, request?: GroupApplyRequest): GroupStatusObservation {
  const object = groupObject(value)
  groupClosed(object, ['state', 'sequence', 'observedAt'], object.state === 'observed' ? ['operation'] : [])
  if (object.state === 'not_queried') return Object.freeze({ state: 'not_queried', sequence: groupLiteral(object.sequence, 0), observedAt: groupLiteral(object.observedAt, 0) })
  const sequence = groupInteger(object.sequence, 1), observedAt = groupInteger(object.observedAt, 1, MAX_GROUP_OBSERVATION_TIME)
  if (object.state === 'observed') {
    if (!request) return invalidGroup()
    return Object.freeze({ state: 'observed', sequence, observedAt, operation: readOperation(object.operation, request.apply.operationId) })
  }
  return Object.freeze({ state: groupEnum(object.state, ['unavailable', 'unsupported', 'query_failed'] as const), sequence, observedAt })
}
function localDurability(value: unknown): GroupLocalDurability { return groupEnum(value, ['durable', 'not_saved', 'uncertain'] as const) }
function readMemberEvidence(value: unknown, review: GroupReview, rows: ReadonlyMap<string, GroupReviewRow>): GroupMemberEvidence {
  const object = groupClosed(value, ['schemaVersion', 'groupRevision', 'peerKey', 'review', 'execution', 'dispatch', 'localDurability', 'status', 'admissionStop'], ['request', 'target'])
  const peerKey = groupDigest(object.peerKey), row = rows.get(peerKey)
  if (!row || groupDigest(object.groupRevision) !== review.revision || object.review !== row.state) return invalidGroup()
  const execution = groupLiteral(object.execution, review.executionPeers.includes(peerKey) ? 'selected' : 'excluded')
  let request: GroupApplyRequest | undefined
  if (row.state === 'ready') request = readOriginalRequest(object.request, row)
  else if (Object.hasOwn(object, 'request')) return invalidGroup()
  const dispatch = groupEnum(object.dispatch, ['not_attempted', 'dispatching', 'observed', 'unknown'] as const), durability = localDurability(object.localDurability)
  let target: OperationEvidence | undefined
  if (Object.hasOwn(object, 'target')) {
    if (!request) return invalidGroup()
    target = readOperation(object.target, request.apply.operationId)
  }
  const status = readStatus(object.status, request)
  if ((dispatch === 'not_attempted' || dispatch === 'dispatching') && (target || status.state !== 'not_queried')) return invalidGroup()
  if (dispatch === 'dispatching' && durability !== 'durable' || dispatch === 'observed' && !target) return invalidGroup()
  if (execution === 'excluded' && (dispatch !== 'not_attempted' || target || status.state !== 'not_queried')) return invalidGroup()
  if (dispatch === 'unknown' && target && status.state === 'not_queried') return invalidGroup()
  if (status.state !== 'not_queried' && (dispatch === 'not_attempted' || dispatch === 'dispatching')) return invalidGroup()
  if (status.state === 'observed') {
    if (!target || terminalDurable(status.operation) && (!terminalDurable(target) || !sameOutcome(target, status.operation))
      || !terminalDurable(target) && !sameOperation(target, status.operation)) return invalidGroup()
  }
  return Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), groupRevision: review.revision, peerKey, review: row.state, execution,
    ...(request ? { request } : {}), dispatch, localDurability: durability, ...(target ? { target } : {}), status,
    admissionStop: groupEnum(object.admissionStop, ['none', 'user_canceled', 'budget_exhausted', 'context_changed', 'persistence_uncertain', 'restarted'] as const),
  })
}
function readEvidence(value: unknown, review: GroupReview): GroupEvidence {
  const object = groupClosed(value, ['schemaVersion', 'members']), rows = new Map(review.rows.map(row => [row.peerKey, row]))
  const members = groupArray(object.members, 1).map(row => readMemberEvidence(row, review, rows)).sort((a, b) => a.peerKey < b.peerKey ? -1 : a.peerKey > b.peerKey ? 1 : 0)
  if (members.length !== rows.size || members.some((row, i) => i > 0 && row.peerKey === members[i - 1].peerKey)) return invalidGroup()
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), members: Object.freeze(members) }), MAX_GROUP_EVIDENCE_BYTES)
}

// This is the complete G1 reduction over already validated exact review rows.
// It does not infer reachability, remote cancellation or present compliance.
export function reduceGroupEvidence(evidence: GroupEvidence): GroupSummary {
  const summary = { schemaVersion: 1 as const, selected: evidence.members.length, executable: 0, excluded: 0, reviewFailures: 0,
    notAttempted: 0, dispatching: 0, dispatchObserved: 0, dispatchUnknown: 0, applied: 0, failed: 0, canceled: 0,
    savedNotApplied: 0, targetUnknown: 0, targetUnobserved: 0, targetNonDurable: 0, localNonDurable: 0, statusFailures: 0,
    admissionFinished: true, reconciliationRequired: false, allApplied: true }
  for (const row of evidence.members) {
    if (row.review !== 'ready') summary.reviewFailures++
    if (row.localDurability !== 'durable') summary.localNonDurable++
    if (row.execution === 'excluded') { summary.excluded++; continue }
    summary.executable++
    switch (row.dispatch) {
      case 'not_attempted': summary.notAttempted++; if (row.admissionStop === 'none') summary.admissionFinished = false; break
      case 'dispatching': summary.dispatching++; summary.admissionFinished = false; break
      case 'observed': summary.dispatchObserved++; break
      case 'unknown': summary.dispatchUnknown++; break
    }
    if (row.status.state === 'unavailable' || row.status.state === 'unsupported' || row.status.state === 'query_failed') summary.statusFailures++
    if (!row.target) summary.targetUnobserved++
    else {
      if (!row.target.evidenceDurable) summary.targetNonDurable++
      switch (row.target.outcome.status) {
        case 'applied': summary.applied++; break
        case 'failed': summary.failed++; break
        case 'canceled': summary.canceled++; break
        case 'saved_not_applied': summary.savedNotApplied++; break
        case 'unknown': summary.targetUnknown++; break
      }
    }
    if (!terminalDurable(row.target) || row.target?.outcome.status !== 'applied') summary.allApplied = false
    if (row.dispatch !== 'not_attempted' && (!terminalDurable(row.target) || row.localDurability !== 'durable')) summary.reconciliationRequired = true
  }
  if (summary.executable === 0) summary.allApplied = false
  return Object.freeze(summary)
}
export const GROUP_SUMMARY_FIELDS = Object.freeze([
  'schemaVersion', 'selected', 'executable', 'excluded', 'reviewFailures', 'notAttempted', 'dispatching', 'dispatchObserved',
  'dispatchUnknown', 'applied', 'failed', 'canceled', 'savedNotApplied', 'targetUnknown', 'targetUnobserved', 'targetNonDurable',
  'localNonDurable', 'statusFailures', 'admissionFinished', 'reconciliationRequired', 'allApplied',
] as const)
function readSummary(value: unknown, evidence: GroupEvidence): GroupSummary {
  const object = groupClosed(value, GROUP_SUMMARY_FIELDS), expected = reduceGroupEvidence(evidence)
  for (const field of GROUP_SUMMARY_FIELDS) {
    const actual = typeof expected[field] === 'boolean' ? groupBoolean(object[field]) : groupInteger(object[field], 0, MAX_GROUP_MEMBERS)
    if (actual !== expected[field]) return invalidGroup()
  }
  return expected
}
function aggregateDurability(evidence: GroupEvidence): GroupLocalDurability {
  return evidence.members.some(row => row.localDurability === 'uncertain') ? 'uncertain'
    : evidence.members.some(row => row.localDurability === 'not_saved') ? 'not_saved' : 'durable'
}
function readRunShape(value: unknown): RunView {
  const object = groupClosed(value, ['schemaVersion', 'runId', 'acceptedAt', 'review', 'evidence', 'summary', 'localDurability', 'activity'])
  const review = readReviewShape(object.review), evidence = readEvidence(object.evidence, review), durability = localDurability(object.localDurability)
  if (durability !== aggregateDurability(evidence)) return invalidGroup()
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), runId: groupID(object.runId), acceptedAt: groupInteger(object.acceptedAt, 1, MAX_GROUP_OBSERVATION_TIME), review, evidence,
    summary: readSummary(object.summary, evidence), localDurability: durability, activity: groupEnum(object.activity, ['idle', 'applying', 'refreshing'] as const),
  }), MAX_GROUP_RESPONSE_BYTES)
}
function verifyPreviousRun(view: RunView, previous: RunView): void {
  if (view.runId !== previous.runId || view.acceptedAt !== previous.acceptedAt || !sameGroupReview(view.review, previous.review)) return invalidGroup()
  const oldRows = new Map(previous.evidence.members.map(row => [row.peerKey, row]))
  for (const row of view.evidence.members) {
    const old = oldRows.get(row.peerKey)
    if (!old || row.status.sequence < old.status.sequence) return invalidGroup()
    if (row.status.sequence === old.status.sequence && (row.status.state !== old.status.state || row.status.observedAt !== old.status.observedAt || !sameOperation(row.status.operation, old.status.operation))) return invalidGroup()
    if (terminalDurable(old.target) && !sameOperation(row.target, old.target)) return invalidGroup()
    if (old.dispatch !== 'not_attempted' && row.dispatch === 'not_attempted') return invalidGroup()
    if ((old.dispatch === 'observed' || old.dispatch === 'unknown') && row.dispatch === 'dispatching') return invalidGroup()
    if (old.dispatch === 'observed' && row.dispatch !== 'observed') return invalidGroup()
    if (old.target && !row.target) return invalidGroup()
    if (old.admissionStop !== 'none' && row.admissionStop !== old.admissionStop) return invalidGroup()
  }
}
export type RunExpectation = Readonly<{ runId: string; review?: GroupReview; previous?: RunView }>
export async function readRunView(value: unknown, expectation: RunExpectation): Promise<RunView> {
  const runId = groupID(expectation.runId), expectedReview = expectation.review ? readReviewShape(expectation.review) : undefined
  const previous = expectation.previous ? readRunShape(expectation.previous) : undefined, view = readRunShape(value)
  if (view.runId !== runId || expectedReview && !sameGroupReview(view.review, expectedReview)) return invalidGroup()
  if (previous) verifyPreviousRun(view, previous)
  await verifyReview(view.review)
  return view
}
export type CancelExpectation = Readonly<{ reviewId: string; review?: GroupReview; previous?: RunView }>
export async function readCancelView(value: unknown, expectation: CancelExpectation): Promise<CancelView> {
  const object = groupObject(value), reviewId = groupID(expectation.reviewId)
  groupLiteral(object.schemaVersion, 1)
  if (Object.hasOwn(object, 'prepared')) {
    groupClosed(object, ['schemaVersion', 'prepared'])
    const prepared = readPreparedShape(object.prepared), expected = expectation.review ? readReviewShape(expectation.review) : undefined
    if (expectation.previous || prepared.reviewId !== reviewId || prepared.admissionState !== 'canceled' || expected && !sameGroupReview(prepared.review, expected)) return invalidGroup()
    const result = groupBounded(Object.freeze({ schemaVersion: 1 as const, prepared }), MAX_GROUP_RESPONSE_BYTES)
    await verifyReview(prepared.review)
    return result
  }
  groupClosed(object, ['schemaVersion', 'run'])
  const run = await readRunView(object.run, { runId: reviewId, review: expectation.review, previous: expectation.previous })
  return groupBounded(Object.freeze({ schemaVersion: 1 as const, run }), MAX_GROUP_RESPONSE_BYTES)
}
export function isRefreshEligible(row: GroupMemberEvidence): boolean {
  return row.execution === 'selected' && row.request !== undefined && row.dispatch !== 'not_attempted' && row.dispatch !== 'dispatching'
}
