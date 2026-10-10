import type { RemoteSelector, TransferChoice, TransferSettings } from '../resource/types'
import type {
  ApplyInput, CancelInput, CurrentReviewInput, GroupMember, GroupOverride, GroupResolvedSelection, GroupReview,
  GroupSelection, GroupTemplate, PreviewInput, RefreshInput, SelectInput, StatusInput,
} from './types'

export const MAX_GROUP_MEMBERS = 16
export const MAX_GROUP_SELECTION_BYTES = 32 * 1024
export const MAX_GROUP_REVIEW_BYTES = 64 * 1024
export const MAX_GROUP_EVIDENCE_BYTES = 64 * 1024
export const MAX_GROUP_INPUT_BYTES = 40 * 1024
export const MAX_GROUP_RESPONSE_BYTES = 136 * 1024
export const MAX_GROUP_OBSERVATION_TIME = 253402300799

export class GroupResponseError extends Error {
  readonly code = 'resource_group_response_invalid'
  constructor() { super('The group data could not be validated.'); this.name = 'GroupResponseError' }
}
export function invalidGroup(): never { throw new GroupResponseError() }

// Only own enumerable data properties enter the fixed readers. No getter,
// toJSON, symbol, class instance or caller-owned array is executed or retained.
export function groupObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return invalidGroup()
  const prototype = Object.getPrototypeOf(value)
  if (prototype !== Object.prototype && prototype !== null) return invalidGroup()
  for (const key of Reflect.ownKeys(value)) {
    const property = Object.getOwnPropertyDescriptor(value, key)
    if (typeof key !== 'string' || !property || !Object.hasOwn(property, 'value') || !property.enumerable) return invalidGroup()
  }
  return value as Record<string, unknown>
}
export function groupClosed(value: unknown, required: readonly string[], optional: readonly string[] = []): Record<string, unknown> {
  const object = groupObject(value), keys = Object.keys(object)
  if (required.some(key => !Object.hasOwn(object, key)) || keys.some(key => !required.includes(key) && !optional.includes(key))) return invalidGroup()
  return object
}
export function groupLiteral<T extends string | number | boolean>(value: unknown, expected: T): T {
  return value === expected ? expected : invalidGroup()
}
export function groupEnum<T extends string>(value: unknown, allowed: readonly T[]): T {
  return typeof value === 'string' && allowed.includes(value as T) ? value as T : invalidGroup()
}
export function groupInteger(value: unknown, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum && value <= maximum ? value : invalidGroup()
}
export function groupBoolean(value: unknown): boolean { return typeof value === 'boolean' ? value : invalidGroup() }
export function groupID(value: unknown): string {
  return typeof value === 'string' && value.length === 32 && /^[0-9a-f]{32}$/.test(value) ? value : invalidGroup()
}
export function groupDigest(value: unknown): string {
  return typeof value === 'string' && value.length === 64 && /^[0-9a-f]{64}$/.test(value) ? value : invalidGroup()
}
export function groupArray(value: unknown, minimum = 0): readonly unknown[] {
  if (!Array.isArray(value) || Object.getPrototypeOf(value) !== Array.prototype || value.length < minimum || value.length > MAX_GROUP_MEMBERS) return invalidGroup()
  const keys = Reflect.ownKeys(value)
  if (keys.length !== value.length + 1) return invalidGroup()
  for (let i = 0; i < value.length; i++) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(i))
    if (!descriptor || !Object.hasOwn(descriptor, 'value') || !descriptor.enumerable) return invalidGroup()
  }
  return value
}
export function requireGroupByteLimit(text: string, limit: number): void {
  if (new TextEncoder().encode(text).byteLength > limit) invalidGroup()
}
// Internal callers pass only independently copied, fixed-field data.
export function groupBounded<T>(value: T, limit: number): T {
  requireGroupByteLimit(JSON.stringify(value), limit)
  return value
}
export function readGroupChoice(value: unknown): TransferChoice {
  const object = groupObject(value)
  if (object.mode === 'default') {
    groupClosed(object, ['mode'])
    return Object.freeze({ mode: 'default' })
  }
  groupClosed(object, ['mode', 'value'])
  return Object.freeze({ mode: groupLiteral(object.mode, 'limited'), value: groupInteger(object.value, 1) })
}
export function readGroupSettings(value: unknown): TransferSettings {
  const object = groupClosed(value, ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  return Object.freeze({ transferConcurrentFiles: readGroupChoice(object.transferConcurrentFiles), transferConcurrentPerPeer: readGroupChoice(object.transferConcurrentPerPeer) })
}
export function readGroupSelector(value: unknown): RemoteSelector<2> {
  const object = groupClosed(value, ['protocolVersion', 'target', 'grantId', 'grantRevision'])
  const target = groupClosed(object.target, ['schemaVersion', 'resourceId'])
  return Object.freeze({ protocolVersion: groupLiteral(object.protocolVersion, 2), target: Object.freeze({ schemaVersion: groupLiteral(target.schemaVersion, 1), resourceId: groupID(target.resourceId) }), grantId: groupID(object.grantId), grantRevision: groupInteger(object.grantRevision, 1) })
}
export function readGroupOverride(value: unknown): GroupOverride {
  const object = groupClosed(value, [], ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  if (Object.keys(object).length === 0) return invalidGroup()
  return Object.freeze({
    ...(Object.hasOwn(object, 'transferConcurrentFiles') ? { transferConcurrentFiles: readGroupChoice(object.transferConcurrentFiles) } : {}),
    ...(Object.hasOwn(object, 'transferConcurrentPerPeer') ? { transferConcurrentPerPeer: readGroupChoice(object.transferConcurrentPerPeer) } : {}),
  })
}
function readTemplateShape(value: unknown): GroupTemplate {
  const object = groupClosed(value, ['schemaVersion', 'settings', 'revision'])
  return Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), settings: readGroupSettings(object.settings), revision: groupDigest(object.revision) })
}
export function readSelectionShape(value: unknown, localPeerKey?: string): GroupSelection {
  const object = groupClosed(value, ['schemaVersion', 'template', 'members'])
  const members = groupArray(object.members, 1).map(raw => {
    const member = groupClosed(raw, ['peerKey', 'selector'], ['override'])
    const peerKey = groupDigest(member.peerKey)
    if (localPeerKey !== undefined && peerKey === groupDigest(localPeerKey)) return invalidGroup()
    return Object.freeze({ peerKey, selector: readGroupSelector(member.selector), ...(Object.hasOwn(member, 'override') ? { override: readGroupOverride(member.override) } : {}) })
  }).sort((a, b) => a.peerKey < b.peerKey ? -1 : a.peerKey > b.peerKey ? 1 : 0)
  if (members.some((member, i) => i > 0 && member.peerKey === members[i - 1].peerKey)) return invalidGroup()
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), template: readTemplateShape(object.template), members: Object.freeze(members) }), MAX_GROUP_SELECTION_BYTES)
}
export function resolvedSettings(template: TransferSettings, override?: GroupOverride): TransferSettings {
  return readGroupSettings({ transferConcurrentFiles: override?.transferConcurrentFiles ?? template.transferConcurrentFiles, transferConcurrentPerPeer: override?.transferConcurrentPerPeer ?? template.transferConcurrentPerPeer })
}
export function resolveSelectionShape(selection: GroupSelection): GroupResolvedSelection {
  return Object.freeze({ schemaVersion: 1, template: selection.template, members: Object.freeze(selection.members.map(member => Object.freeze({ ...member, requested: resolvedSettings(selection.template.settings, member.override) }))) })
}

export function readExecutionPeers(value: unknown, allowEmpty = true): readonly string[] {
  const peers = groupArray(value, allowEmpty ? 0 : 1).map(groupDigest).sort()
  if (peers.some((peer, i) => i > 0 && peer === peers[i - 1])) return invalidGroup()
  return Object.freeze(peers)
}

// All canonical objects below are constructed field by field. They are never
// generated by sorting arbitrary caller keys or serializing imported JSON.
function choiceBody(choice: TransferChoice) {
  return choice.mode === 'default' ? { mode: 'default' } : { mode: 'limited', value: choice.value }
}
function settingsBody(settings: TransferSettings) {
  return { transferConcurrentFiles: choiceBody(settings.transferConcurrentFiles), transferConcurrentPerPeer: choiceBody(settings.transferConcurrentPerPeer) }
}
function selectorBody(selector: RemoteSelector<2>) {
  return { protocolVersion: 2, target: { schemaVersion: 1, resourceId: selector.target.resourceId }, grantId: selector.grantId, grantRevision: selector.grantRevision }
}
function overrideBody(override: GroupOverride) {
  return { ...(override.transferConcurrentFiles ? { transferConcurrentFiles: choiceBody(override.transferConcurrentFiles) } : {}), ...(override.transferConcurrentPerPeer ? { transferConcurrentPerPeer: choiceBody(override.transferConcurrentPerPeer) } : {}) }
}
function memberBody(member: GroupMember) {
  return { peerKey: member.peerKey, selector: selectorBody(member.selector), ...(member.override ? { override: overrideBody(member.override) } : {}) }
}
export function templateCanonicalJSON(settings: TransferSettings): string {
  return JSON.stringify({ schemaVersion: 1, settings: settingsBody(readGroupSettings(settings)) })
}
async function sha256(domain: string, json: string): Promise<string> {
  const data = new TextEncoder().encode(`${domain}\u0000${json}`)
  if (!globalThis.crypto?.subtle) return invalidGroup()
  const hash = await globalThis.crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, '0')).join('')
}
export async function createTemplate(settings: TransferSettings): Promise<GroupTemplate> {
  const copied = readGroupSettings(settings)
  const revision = await sha256('sobalink.resourcegroup.template.v1', templateCanonicalJSON(copied))
  return Object.freeze({ schemaVersion: 1, settings: copied, revision })
}
export async function verifyTemplate(template: GroupTemplate): Promise<void> {
  if ((await createTemplate(template.settings)).revision !== template.revision) invalidGroup()
}
export async function readGroupSelection(value: unknown, localPeerKey?: string): Promise<GroupSelection> {
  const selection = readSelectionShape(value, localPeerKey)
  await verifyTemplate(selection.template)
  return selection
}
export async function resolveSelection(selection: GroupSelection, localPeerKey?: string): Promise<GroupResolvedSelection> {
  return resolveSelectionShape(await readGroupSelection(selection, localPeerKey))
}
export function reviewCanonicalJSON(review: GroupReview): string {
  const selection = review.selection
  return JSON.stringify({
    schemaVersion: 1,
    selection: { schemaVersion: 1, template: { schemaVersion: 1, settings: settingsBody(selection.template.settings), revision: selection.template.revision }, members: [...selection.members].sort((a, b) => a.peerKey < b.peerKey ? -1 : 1).map(member => ({ ...memberBody(member), requested: settingsBody(member.requested) })) },
    rows: [...review.rows].sort((a, b) => a.peerKey < b.peerKey ? -1 : 1).map(row => ({ schemaVersion: 1, peerKey: row.peerKey, state: row.state, ...(row.state === 'ready' ? { reply: { ...selectorBody(row.reply), action: 'preview', preview: { operationId: row.reply.preview.operationId, baseRevision: row.reply.preview.baseRevision, reviewRevision: row.reply.preview.reviewRevision, requested: settingsBody(row.reply.preview.requested), effective: { transferConcurrentFiles: row.reply.preview.effective.transferConcurrentFiles, transferConcurrentPerPeer: row.reply.preview.effective.transferConcurrentPerPeer } } } } : {}) })),
    executionPeers: [...review.executionPeers].sort(),
  })
}
export async function groupReviewRevision(review: GroupReview): Promise<string> {
  return sha256('sobalink.resourcegroup.review.v1', reviewCanonicalJSON(review))
}

export function readPreviewInputShape(value: unknown, localPeerKey?: string): PreviewInput {
  const object = groupClosed(value, ['schemaVersion', 'selection'], ['replaceReviewId'])
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), selection: readSelectionShape(object.selection, localPeerKey), ...(Object.hasOwn(object, 'replaceReviewId') ? { replaceReviewId: groupID(object.replaceReviewId) } : {}) }), MAX_GROUP_INPUT_BYTES)
}
export async function readPreviewInput(value: unknown, localPeerKey?: string): Promise<PreviewInput> {
  const input = readPreviewInputShape(value, localPeerKey)
  await verifyTemplate(input.selection.template)
  return input
}
function selectFields(object: Record<string, unknown>, allowEmpty: boolean): SelectInput {
  return Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), reviewId: groupID(object.reviewId), reviewRevision: groupDigest(object.reviewRevision), executionPeers: readExecutionPeers(object.executionPeers, allowEmpty) })
}
export function readSelectInput(value: unknown): SelectInput {
  return groupBounded(selectFields(groupClosed(value, ['schemaVersion', 'reviewId', 'reviewRevision', 'executionPeers']), true), MAX_GROUP_INPUT_BYTES)
}
export function readApplyInput(value: unknown): ApplyInput {
  const object = groupClosed(value, ['schemaVersion', 'reviewId', 'reviewRevision', 'executionPeers', 'confirm'])
  return groupBounded(Object.freeze({ ...selectFields(object, false), confirm: groupLiteral(object.confirm, true) }), MAX_GROUP_INPUT_BYTES)
}
export function readStatusInput(value: unknown): StatusInput {
  const object = groupClosed(value, ['schemaVersion', 'runId'])
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), runId: groupID(object.runId) }), MAX_GROUP_INPUT_BYTES)
}
export function readRefreshInput(value: unknown): RefreshInput {
  const object = groupClosed(value, ['schemaVersion', 'runId', 'peers'])
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), runId: groupID(object.runId), peers: readExecutionPeers(object.peers, false) }), MAX_GROUP_INPUT_BYTES)
}
export function readCancelInput(value: unknown): CancelInput {
  const object = groupClosed(value, ['schemaVersion', 'reviewId'])
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1), reviewId: groupID(object.reviewId) }), MAX_GROUP_INPUT_BYTES)
}
export function readCurrentReviewInput(value: unknown): CurrentReviewInput {
  const object = groupClosed(value, ['schemaVersion'])
  return groupBounded(Object.freeze({ schemaVersion: groupLiteral(object.schemaVersion, 1) }), MAX_GROUP_INPUT_BYTES)
}
