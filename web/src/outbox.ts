import { outboxRequestTime } from './outbox-id'
import { ApiError, type TransferEntry, type UploadSelection } from './api'
import { samePeers } from './dispatch'

export const outboxStatuses = ['prepared', 'queued', 'waiting_for_peer', 'sending', 'awaiting_receipt', 'completed', 'paused', 'blocked_permission', 'blocked_recovery', 'cancel_requested', 'cancelled', 'expired', 'discarded'] as const
export type OutboxStatus = typeof outboxStatuses[number]
export type DeliveryOutcome = 'not_attempted' | 'unknown' | 'partial' | 'confirmed'
export interface OutboxFile { batchId: string; fileId: string; size: number; sha256: string }
export interface OutboxChild { peerId: string; deliveryId: string; status: OutboxStatus; deliveryOutcome: DeliveryOutcome; reasonCode?: string; files?: OutboxFile[] }
export interface OutboxRecord {
  version: 1; kind: 'outbox'; outboxId: string; requestId: string; reviewRevision: string
  admission: 'prepared' | 'enqueued'; status: OutboxStatus; deliveryOutcome: DeliveryOutcome; reasonCode?: string; processId?: string
  peerIds: string[]; entries: (TransferEntry & { id: string; sha256?: string })[]; totalBytes: number
  createdAt: string; expiresAt: string; updatedAt: string; children: OutboxChild[]
}
export interface OutboxExpected { requestId?: string; processId?: string; peerIds?: string[]; selection?: UploadSelection; expiresAt?: string; review?: OutboxRecord }
export interface PendingOutbox { requestId: string; processId: string; expiresAt: string; phase: 'preparing' | 'prepared' | 'committing' | 'unknown'; review?: OutboxRecord }
const fail = (): never => { throw new ApiError('outbox_invalid_response', '') }
const object = (v: unknown): Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : fail()
const id = (v: unknown): string => typeof v === 'string' && /^[A-Za-z0-9][A-Za-z0-9_:-]{0,127}$/.test(v) ? v : fail()
const digest = (v: unknown): string => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v) ? v : fail()
const time = (v: unknown): string => typeof v === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(v) && Number.isFinite(Date.parse(v)) ? v : fail()
const reason = (v: unknown): string | undefined => v === undefined || v === '' ? undefined : typeof v === 'string' && /^outbox_[a-z0-9_]+$/.test(v) ? v : fail()
const status = (v: unknown): OutboxStatus => outboxStatuses.includes(v as OutboxStatus) ? v as OutboxStatus : fail()
const outcome = (v: unknown): DeliveryOutcome => ['not_attempted', 'unknown', 'partial', 'confirmed'].includes(v as string) ? v as DeliveryOutcome : fail()
const size = (v: unknown): number => Number.isSafeInteger(v) && (v as number) >= 0 ? v as number : fail()

// Stable durable identities are independent of process lifetimes. Only live
// prepared reviews require a process token; no status is proof of delivery.
export function readOutbox(value: unknown, expected: OutboxExpected = {}): OutboxRecord {
  const v = object(value)
  if (v.version !== 1 || v.kind !== 'outbox' || !['prepared', 'enqueued'].includes(v.admission as string)) fail()
  const outboxId = id(v.outboxId), requestId = id(v.requestId), requestCreated = outboxRequestTime(v.requestId)
  if (requestCreated === undefined) fail()
  if (outboxId !== requestId || expected.requestId !== undefined && requestId !== expected.requestId) fail()
  if (typeof v.reasonCode !== 'string') fail()
  const processId = v.processId === undefined || v.processId === '' ? undefined : digest(v.processId)
  if (v.admission === 'enqueued' && v.processId !== undefined) fail()
  if (v.admission === 'prepared' && (!processId || expected.processId !== undefined && expected.processId !== processId)) fail()
  if (!Array.isArray(v.peerIds) || !v.peerIds.length || !Array.isArray(v.entries) || !v.entries.length || !Array.isArray(v.children)) fail()
  const peerIds = (v.peerIds as unknown[]).map(id)
  if (new Set(peerIds).size !== peerIds.length || expected.peerIds && !samePeers(peerIds, expected.peerIds)) fail()
  const entries = (v.entries as unknown[]).map(raw => {
    const e = object(raw), entrySize = size(e.size)
    if (typeof e.path !== 'string' || !e.path || /[\\\u0000-\u001f\u007f]/.test(e.path) || e.path.split('/').some(part => !part || part === '.' || part === '..') || e.kind !== 'file' && e.kind !== 'directory') fail()
    if (e.kind === 'directory' && (entrySize !== 0 || e.sha256 !== undefined && e.sha256 !== '')) fail()
    return { id: id(e.id), path: e.path as string, kind: e.kind as 'file' | 'directory', size: entrySize, ...(e.kind === 'file' ? { sha256: digest(e.sha256) } : {}) }
  })
  const totalBytes = size(v.totalBytes)
  if (new Set(entries.map(e => e.id)).size !== entries.length || new Set(entries.map(e => e.path)).size !== entries.length || entries.reduce((sum, e) => sum + e.size, 0) !== totalBytes) fail()
  if (expected.selection && (entries.length !== expected.selection.entries.length || entries.some((e, i) => { const s = expected.selection!.entries[i]; return e.path !== s.path || e.kind !== s.kind || e.size !== s.size }))) fail()
  const children = (v.children as unknown[]).map((raw, index) => {
    const c = object(raw), peerId = id(c.peerId), deliveryId = id(c.deliveryId)
    if (typeof c.reasonCode !== 'string' || !Array.isArray(c.files)) fail()
    if (peerId !== peerIds[index] || c.files !== undefined && !Array.isArray(c.files)) fail()
    const files = c.files === undefined ? undefined : (c.files as unknown[]).map(rawFile => {
      const f = object(rawFile), fileId = id(f.fileId), entry = entries.find(e => e.id === fileId)
      if (id(f.batchId) !== deliveryId || !entry || size(f.size) !== entry.size || (entry.kind === 'file' ? digest(f.sha256) !== entry.sha256 : f.sha256 !== '')) fail()
      return { batchId: deliveryId, fileId, size: entry!.size, sha256: entry!.sha256 || '' }
    })
    if (files && new Set(files.map(f => f.fileId)).size !== files.length) fail()
    if (c.deliveryOutcome === 'not_attempted' && files?.length) fail()
    if (v.admission === 'enqueued' && ['prepared', 'discarded'].includes(c.status as string)) fail()
    if (c.deliveryOutcome === 'confirmed' && files?.length !== entries.length || c.status === 'completed' && c.deliveryOutcome !== 'confirmed') fail()
    return { peerId, deliveryId, status: status(c.status), deliveryOutcome: outcome(c.deliveryOutcome), reasonCode: reason(c.reasonCode), ...(files ? { files } : {}) }
  })
  if (children.length !== peerIds.length || new Set(children.map(c => c.deliveryId)).size !== children.length) fail()
  const record: OutboxRecord = { version: 1, kind: 'outbox', outboxId, requestId, reviewRevision: digest(v.reviewRevision), admission: v.admission as OutboxRecord['admission'], status: status(v.status), deliveryOutcome: outcome(v.deliveryOutcome), reasonCode: reason(v.reasonCode), ...(processId ? { processId } : {}), peerIds, entries, totalBytes, createdAt: time(v.createdAt), expiresAt: time(v.expiresAt), updatedAt: time(v.updatedAt), children }
  if (Date.parse(record.expiresAt) <= Date.parse(record.createdAt) || Date.parse(record.expiresAt) > requestCreated! + 7 * 86400000 || expected.expiresAt && Date.parse(expected.expiresAt) !== Date.parse(record.expiresAt)) fail()
  if (record.admission === 'prepared' && (!['prepared', 'discarded'].includes(record.status) || children.some(c => c.status !== record.status))) fail()
  if (record.admission === 'enqueued' && ['prepared', 'discarded'].includes(record.status)) fail()
  if (record.admission === 'prepared' && (record.deliveryOutcome !== 'not_attempted' || children.some(c => c.deliveryOutcome !== 'not_attempted'))) fail()
  if (record.status === 'completed' && (record.deliveryOutcome !== 'confirmed' || children.some(c => c.deliveryOutcome !== 'confirmed'))) fail()
  if (record.deliveryOutcome === 'not_attempted' && children.some(c => c.deliveryOutcome !== 'not_attempted')) fail()
  if (record.deliveryOutcome === 'confirmed' && children.some(c => c.deliveryOutcome !== 'confirmed')) fail()
  const review = expected.review
  if (review && (record.reviewRevision !== review.reviewRevision || !samePeers(record.peerIds, review.peerIds) || JSON.stringify(record.entries) !== JSON.stringify(review.entries) || record.totalBytes !== review.totalBytes || record.createdAt !== review.createdAt || Date.parse(record.expiresAt) !== Date.parse(review.expiresAt) || children.some((c, i) => c.deliveryId !== review.children[i]?.deliveryId))) fail()
  return record
}
export function readOutboxList(value: unknown): OutboxRecord[] {
  const v = object(value)
  if (v.version !== 1 || v.kind !== 'outbox' || !Array.isArray(v.items)) fail()
  const records = (v.items as unknown[]).map(item => readOutbox(item))
  if (new Set(records.map(r => r.outboxId)).size !== records.length) fail()
  return records
}
export function outboxTerminal(record: OutboxRecord) { return ['completed', 'cancelled', 'expired', 'discarded'].includes(record.status) }

const locatorKey = 'sobalink.outbox.pending.v1'
// Locators are recovery hints only. All outcomes always come from the server.
export function pendingOutboxLocators(): string[] {
  try { const value: unknown = JSON.parse(sessionStorage.getItem(locatorKey) || '[]'); return Array.isArray(value) ? value.filter(v => typeof v === 'string' && outboxRequestTime(v) !== undefined).slice(-32) : [] } catch { return [] }
}
export function retainOutboxLocator(requestId: string) {
  const ids = [...new Set([...pendingOutboxLocators(), requestId])].slice(-32)
  try { sessionStorage.setItem(locatorKey, JSON.stringify(ids)); window.dispatchEvent(new Event('sobalink-outbox-locators')) } catch { /* Server list remains authoritative when browser storage is unavailable. */ }
}
export function resolveOutboxLocator(requestId: string) {
  try { sessionStorage.setItem(locatorKey, JSON.stringify(pendingOutboxLocators().filter(id => id !== requestId))); window.dispatchEvent(new Event('sobalink-outbox-locators')) } catch { /* Locators never authorize sending. */ }
}

export function readOutboxSnapshot(value: unknown) {
  const v = object(value)
  if (typeof v.reasonCode !== 'string' || !['ready', 'blocked_recovery'].includes(v.status as string) || Object.keys(v).length !== 5) fail()
  return { items: readOutboxList(value), status: v.status as 'ready' | 'blocked_recovery', reasonCode: reason(v.reasonCode) }
}
