// Fictional deterministic fixtures only. No server, filesystem or peer I/O.
import type { OperationEvidence, Outcome, TransferSettings } from '../resource/types'
import { groupReviewRevision } from './canonical'
import { reduceGroupEvidence } from './decode'
import type { GroupEvidence, GroupMemberEvidence, GroupReview, GroupReviewRow, GroupSelection, PreparedView, RunView } from './types'

export const groupSettings: TransferSettings = { transferConcurrentFiles: { mode: 'default' }, transferConcurrentPerPeer: { mode: 'limited', value: 3 } }
export const groupRunId = '1'.repeat(32)
export const groupOtherRunId = '2'.repeat(32)
export const groupTemplateRevision = 'dee87ed640530919edd7773915c5a15d06adf792c1a73d5fcb6ee92bdb81bd62'
export const groupApplied: Outcome = { status: 'applied', configuration: 'durable', accounting: 'succeeded', transfer: 'not_required' }
export const groupUnknown: Outcome = { status: 'unknown', configuration: 'unobserved', accounting: 'unobserved', transfer: 'unobserved' }
export type Mutable<T> = { -readonly [K in keyof T]: Mutable<T[K]> }
export function mutableGroup<T>(value: T): Mutable<T> { return JSON.parse(JSON.stringify(value)) as Mutable<T> }
export function groupSelection(count = 2): GroupSelection {
  return { schemaVersion: 1, template: { schemaVersion: 1, settings: mutableGroup(groupSettings), revision: groupTemplateRevision }, members: Array.from({ length: count }, (_, i) => ({
    peerKey: (i + 1).toString(16).padStart(64, '0'),
    selector: { protocolVersion: 2, target: { schemaVersion: 1, resourceId: (i + 100).toString(16).padStart(32, '0') }, grantId: (i + 1000).toString(16).padStart(32, '0'), grantRevision: 1 },
  })) }
}
export async function groupReview(count = 2): Promise<GroupReview> {
  const selected = groupSelection(count)
  const selection = { ...selected, members: selected.members.map(member => ({ ...member, requested: mutableGroup(groupSettings) })) }
  const rows: GroupReviewRow[] = selection.members.map(member => ({ schemaVersion: 1, peerKey: member.peerKey, state: 'ready', reply: {
    ...member.selector, action: 'preview', preview: { operationId: 'a'.repeat(64), baseRevision: 'b'.repeat(64), reviewRevision: 'c'.repeat(64), requested: mutableGroup(member.requested), effective: { transferConcurrentFiles: 8, transferConcurrentPerPeer: 3 } },
  } }))
  return reviseGroupReview({ schemaVersion: 1, selection, rows, executionPeers: rows.map(row => row.peerKey), revision: '0'.repeat(64) })
}
export async function reviseGroupReview(review: GroupReview): Promise<GroupReview> {
  return { ...review, revision: await groupReviewRevision(review) }
}
export async function groupPrepared(count = 2): Promise<PreparedView> {
  return { schemaVersion: 1, reviewId: groupRunId, review: await groupReview(count), admissionState: 'prepared', initializesLocalEvidence: false }
}
export function groupEvidence(review: GroupReview): GroupEvidence {
  return { schemaVersion: 1, members: review.rows.map((row): GroupMemberEvidence => ({
    schemaVersion: 1, groupRevision: review.revision, peerKey: row.peerKey, review: row.state,
    execution: review.executionPeers.includes(row.peerKey) ? 'selected' : 'excluded',
    ...(row.state === 'ready' ? { request: { protocolVersion: 2, target: mutableGroup(row.reply.target), grantId: row.reply.grantId, grantRevision: row.reply.grantRevision, action: 'apply', apply: { operationId: row.reply.preview.operationId, baseRevision: row.reply.preview.baseRevision, reviewRevision: row.reply.preview.reviewRevision, settings: mutableGroup(row.reply.preview.requested) } } } : {}),
    dispatch: 'not_attempted', localDurability: 'durable', status: { state: 'not_queried', sequence: 0, observedAt: 0 }, admissionStop: 'none',
  })) }
}
export function groupOperation(row: GroupMemberEvidence, outcome: Outcome = groupApplied, evidenceDurable = true): OperationEvidence {
  if (!row.request) throw new Error('The synthetic row requires a ready request.')
  return { operationId: row.request.apply.operationId, outcome: mutableGroup(outcome), evidenceDurable }
}
export function groupRun(review: GroupReview, evidence: GroupEvidence = groupEvidence(review)): RunView {
  return { schemaVersion: 1, runId: groupRunId, acceptedAt: 1, review, evidence, summary: reduceGroupEvidence(evidence),
    localDurability: evidence.members.some(row => row.localDurability === 'uncertain') ? 'uncertain' : evidence.members.some(row => row.localDurability === 'not_saved') ? 'not_saved' : 'durable', activity: 'applying' }
}
