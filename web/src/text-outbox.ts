import { ApiError, messageByteLength, type Peer, type State } from './api'

export const textOutboxStatuses = ['prepared', 'discarded', 'queued', 'waiting_peer', 'delivering', 'completed', 'cancel_requested', 'cancelled', 'expired', 'paused', 'blocked', 'needs_review'] as const
export type TextOutboxStatus = typeof textOutboxStatuses[number]
export interface TextReceipt {
  lease: { version: 1; deliveryId: string; digest: string; receiverEpoch: string; token: string; recipientId: string; expiresAt: string; receiptUntil: string }
  messageId: string; textBytes: number; textSHA256: string; savedAt: string
}
export interface TextOutboxRecord {
  version: 1; kind: 'text_outbox'; outboxId: string; requestId: string; messageId: string; peerId: string
  processId?: string; reviewRevision: string; admission: 'prepared' | 'enqueued'; status: TextOutboxStatus
  deliveryOutcome: 'not_attempted' | 'unknown' | 'confirmed'; reasonCode: string
  text?: string; textBytes: number; sha256: string; createdAt: string; expiresAt: string; updatedAt: string; receipt?: TextReceipt
}
export interface TextExpected { requestId?: string; peerId?: string; processId?: string; text?: string; sha256?: string; expiresAt?: string; review?: TextOutboxRecord; detail?: boolean }
export interface PendingText {
  requestId: string; processId: string; peerId: string; text: string; expiresAt: string
  phase: 'preparing' | 'prepared' | 'committing' | 'unknown' | 'absent'; review?: TextOutboxRecord
  failedAction?: 'prepare' | 'commit' | 'status' | 'discard'; reasonCode?: string
}
const invalid = (): never => { throw new ApiError('message_invalid_response', '') }
const object = (value: unknown): Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : invalid()
const closed = (v: Record<string, unknown>, required: string[], optional: string[] = []) => { if (required.some(k => !(k in v)) || Object.keys(v).some(k => !required.includes(k) && !optional.includes(k))) invalid() }
const hex = (v: unknown, length = 64): string => typeof v === 'string' && new RegExp(`^[a-f0-9]{${length}}$`).test(v) ? v : invalid()
const identifier = (v: unknown): string => typeof v === 'string' && /^[A-Za-z0-9][A-Za-z0-9_:-]{0,127}$/.test(v) ? v : invalid()
const time = (v: unknown): string => typeof v === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(v) && Number.isFinite(Date.parse(v)) ? v : invalid()
const reason = (v: unknown): string => typeof v === 'string' && (v === '' || /^[a-z][a-z0-9_]{0,95}$/.test(v)) ? v : invalid()
export function textRequestTime(value: unknown): number | undefined {
  if (typeof value !== 'string' || !/^mt1_\d{13}_[a-f0-9]{32}$/.test(value)) return undefined
  const ms = Number(value.slice(4, 17))
  return ms >= 1_000_000_000_000 && ms + 8 * 86400000 + 300000 <= 9_999_999_999_999 ? ms : undefined
}
export function newTextRequestID(now = Date.now()): string {
  const random = crypto.getRandomValues(new Uint8Array(16))
  const id = `mt1_${now}_${Array.from(random, byte => byte.toString(16).padStart(2, '0')).join('')}`
  if (textRequestTime(id) === undefined) throw new RangeError('Invalid text request creation time')
  return id
}
export async function textDigest(text: string) { return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))), byte => byte.toString(16).padStart(2, '0')).join('') }
export function canQueueText(peer: Peer, state: State) {
  return /^[a-f0-9]{64}$/.test(state.resourceCatalogProcessId || '') && peer.verified && peer.trusted && !peer.autosave?.paused && state.settings?.network !== 'mixed' && state.settings?.network !== 'none'
}
export function readTextOutbox(value: unknown, expected: TextExpected = {}, list = false): TextOutboxRecord {
  const v = object(value)
  closed(v, ['version', 'kind', 'outboxId', 'requestId', 'messageId', 'peerId', 'reviewRevision', 'admission', 'status', 'deliveryOutcome', 'reasonCode', 'textBytes', 'sha256', 'createdAt', 'expiresAt', 'updatedAt'], ['processId', 'text', 'receipt', ...(list ? ['id'] : [])])
  const created = textRequestTime(v.requestId)
  if (v.version !== 1 || v.kind !== 'text_outbox' || created === undefined || v.outboxId !== v.requestId || v.messageId !== v.requestId || list && (v.id !== v.requestId || v.text !== undefined)) invalid()
  if (!['prepared', 'enqueued'].includes(v.admission as string) || !textOutboxStatuses.includes(v.status as TextOutboxStatus) || !['not_attempted', 'unknown', 'confirmed'].includes(v.deliveryOutcome as string)) invalid()
  const record: TextOutboxRecord = { version: 1, kind: 'text_outbox', outboxId: v.outboxId as string, requestId: v.requestId as string, messageId: v.messageId as string, peerId: identifier(v.peerId), reviewRevision: hex(v.reviewRevision), admission: v.admission as TextOutboxRecord['admission'], status: v.status as TextOutboxStatus, deliveryOutcome: v.deliveryOutcome as TextOutboxRecord['deliveryOutcome'], reasonCode: reason(v.reasonCode), textBytes: v.textBytes as number, sha256: hex(v.sha256), createdAt: time(v.createdAt), expiresAt: time(v.expiresAt), updatedAt: time(v.updatedAt) }
  if (!Number.isSafeInteger(record.textBytes) || record.textBytes <= 0 || record.textBytes > Math.floor(((16 << 20) - 4096) / 6) || Date.parse(record.createdAt) !== created || Date.parse(record.expiresAt) <= created! || Date.parse(record.expiresAt) > created! + 7 * 86400000) invalid()
  if (v.text !== undefined) { if (typeof v.text !== 'string' || messageByteLength(v.text) !== record.textBytes) invalid(); record.text = v.text as string }
  if (record.admission === 'prepared') {
    record.processId = hex(v.processId)
    if (!['prepared', 'discarded'].includes(record.status) || record.deliveryOutcome !== 'not_attempted' || v.receipt !== undefined || expected.detail && record.status === 'prepared' && record.text === undefined) invalid()
  } else if (v.processId !== undefined || ['prepared', 'discarded'].includes(record.status)) invalid()
  if (record.status === 'completed' && record.deliveryOutcome !== 'confirmed' || record.deliveryOutcome === 'confirmed' && v.receipt === undefined) invalid()
  if (v.receipt !== undefined) {
    const r = object(v.receipt), l = object(r.lease)
    closed(r, ['lease', 'messageId', 'textBytes', 'textSHA256', 'savedAt']); closed(l, ['version', 'deliveryId', 'digest', 'receiverEpoch', 'token', 'recipientId', 'expiresAt', 'receiptUntil'])
    const savedAt = time(r.savedAt), expiresAt = time(l.expiresAt), receiptUntil = time(l.receiptUntil)
    if (record.deliveryOutcome !== 'confirmed' || l.version !== 1 || l.deliveryId !== record.messageId || l.recipientId !== record.peerId || r.messageId !== record.messageId || r.textBytes !== record.textBytes || r.textSHA256 !== record.sha256 || Date.parse(expiresAt) !== Date.parse(record.expiresAt) || Date.parse(receiptUntil) !== created! + 8 * 86400000 + 300000 || Date.parse(savedAt) < created! - 300000 || Date.parse(savedAt) > Date.parse(receiptUntil)) invalid()
    record.receipt = { lease: { version: 1, deliveryId: record.messageId, recipientId: record.peerId, digest: hex(l.digest), receiverEpoch: hex(l.receiverEpoch, 32), token: hex(l.token, 32), expiresAt, receiptUntil }, messageId: record.messageId, textBytes: record.textBytes, textSHA256: record.sha256, savedAt }
  }
  for (const field of ['requestId', 'peerId', 'sha256'] as const) if (expected[field] !== undefined && record[field] !== expected[field]) invalid()
  if (expected.processId && record.admission === 'prepared' && record.processId !== expected.processId || expected.text !== undefined && (messageByteLength(expected.text) !== record.textBytes || record.text !== undefined && record.text !== expected.text) || expected.expiresAt && Date.parse(record.expiresAt) !== Date.parse(expected.expiresAt)) invalid()
  const review = expected.review
  if (review?.receipt && (!record.receipt || (['messageId', 'textBytes', 'textSHA256', 'savedAt'] as const).some(key => record.receipt![key] !== review.receipt![key]) || (['version', 'deliveryId', 'digest', 'receiverEpoch', 'token', 'recipientId', 'expiresAt', 'receiptUntil'] as const).some(key => record.receipt!.lease[key] !== review.receipt!.lease[key]))) invalid()
  if (review && (record.requestId !== review.requestId || record.peerId !== review.peerId || record.reviewRevision !== review.reviewRevision || record.sha256 !== review.sha256 || record.textBytes !== review.textBytes || record.createdAt !== review.createdAt || Date.parse(record.expiresAt) !== Date.parse(review.expiresAt) || review.deliveryOutcome === 'confirmed' && record.deliveryOutcome !== 'confirmed')) invalid()
  return record
}
export function readTextPage(value: unknown) {
  const v = object(value); closed(v, ['version', 'kind', 'items', 'revision', 'total', 'status', 'reasonCode'], ['nextCursor'])
  if (v.version !== 1 || v.kind !== 'text_outbox' || !['ready', 'blocked'].includes(v.status as string) || !Array.isArray(v.items) || !Number.isSafeInteger(v.total) || (v.total as number) < 0 || (v.total as number) > 2048) invalid()
  const items = (v.items as unknown[]).map(item => readTextOutbox(item, {}, true)), revision = hex(v.revision), reasonCode = reason(v.reasonCode)
  if (items.length > (v.total as number) || new Set(items.map(r => r.requestId)).size !== items.length || items.some((r, i) => i > 0 && r.requestId <= items[i - 1].requestId) || v.status === 'ready' && reasonCode !== '' || v.status === 'blocked' && !reasonCode) invalid()
  const nextCursor = v.nextCursor === undefined ? undefined : identifier(v.nextCursor)
  if (nextCursor !== undefined && (!items.length || nextCursor !== items.at(-1)!.requestId || items.length >= (v.total as number))) invalid()
  return { items, revision, nextCursor, total: v.total as number, status: v.status as 'ready' | 'blocked', reasonCode }
}
export function textTerminal(record: TextOutboxRecord) { return ['completed', 'cancelled', 'expired', 'discarded'].includes(record.status) }
const storageKey = 'sobalink.text-outbox.pending.v1'
export const textLocatorEvent = 'sobalink-text-outbox-locators'
// Only body-free recovery locators are retained in browser storage. These are
// hints, never evidence of receipt or permission to send.
export function pendingTextLocators(): string[] {
  try { const values: unknown = JSON.parse(sessionStorage.getItem(storageKey) || '[]'); return Array.isArray(values) ? [...new Set(values.filter((v): v is string => textRequestTime(v) !== undefined))].slice(-32) : [] } catch { return [] }
}
export function retainTextLocator(id: string) { if (textRequestTime(id) === undefined) return; try { sessionStorage.setItem(storageKey, JSON.stringify([...new Set([...pendingTextLocators(), id])].slice(-32))); window.dispatchEvent(new Event(textLocatorEvent)) } catch { /* Live draft remains available. */ } }
export function resolveTextLocator(id: string) { try { sessionStorage.setItem(storageKey, JSON.stringify(pendingTextLocators().filter(value => value !== id))); window.dispatchEvent(new Event(textLocatorEvent)) } catch { /* A repeated status check remains safe. */ } }
export async function verifiedTextOutbox(value: unknown, expected: TextExpected = {}) {
  const record = readTextOutbox(value, expected)
  if (record.text !== undefined && await textDigest(record.text) !== record.sha256 || expected.text !== undefined && await textDigest(expected.text) !== record.sha256) invalid()
  return record
}
