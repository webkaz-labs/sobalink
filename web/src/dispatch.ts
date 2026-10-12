import { ApiError, type TransferEntry, type UploadSelection } from './api'

export interface DispatchContext { processId: string; preparationId: string; peerIds: string[]; selection: UploadSelection }
export interface DispatchPrepared {
  version: 1; processId: string; preparationId: string; status: 'prepared'; reviewRevision: string
  peerIds: string[]; entries: (TransferEntry & { id: string; sha256?: string })[]; totalBytes: number
}
export interface DispatchCommitted {
  version: 1; processId: string; preparationId: string; dispatchId: string; status: 'committed'
  children: { peerId: string; transferId: string }[]
}
export interface DispatchDiscarded { version: 1; processId: string; preparationId: string; status: 'discarded' }
export type DispatchResult = DispatchPrepared | DispatchCommitted | DispatchDiscarded
export interface PendingDispatch {
  processId: string; preparationId: string; peerIds: string[]
  phase: 'preparing' | 'prepare-unknown' | 'prepared' | 'committing' | 'commit-unknown'
  review?: DispatchPrepared
}
const fail = (): never => { throw new ApiError('invalid_response', '') }
function object(value: unknown): Record<string, unknown> { return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : fail() }
function id(value: unknown): string { return typeof value === 'string' && value.length > 0 && value.length <= 128 && !/[\u0000-\u0020\u007f]/.test(value) ? value : fail() }
export function samePeers(left: readonly string[], right: readonly string[]) { return left.length === right.length && left.every((peer, index) => peer === right[index]) }

// A successful response is not sufficient: all caller-selected identities and
// file metadata must match before a preparation can authorize its one commit.
export function readDispatchResult(value: unknown, expected: DispatchContext): DispatchResult {
  const v = object(value)
  if (v.version !== 1 || v.processId !== expected.processId || v.preparationId !== expected.preparationId) fail()
  const base = { version: 1 as const, processId: expected.processId, preparationId: expected.preparationId }
  if (v.status === 'discarded') return { ...base, status: 'discarded' }
  if (v.status === 'committed') {
    if (v.dispatchId !== expected.preparationId || !Array.isArray(v.children) || v.children.length !== expected.peerIds.length) fail()
    const children = (v.children as unknown[]).map((raw, index) => {
      const child = object(raw)
      if (child.peerId !== expected.peerIds[index]) fail()
      return { peerId: expected.peerIds[index], transferId: id(child.transferId) }
    })
    if (new Set(children.map(child => child.transferId)).size !== children.length) fail()
    return { ...base, status: 'committed', dispatchId: expected.preparationId, children }
  }
  if (v.status !== 'prepared' || typeof v.reviewRevision !== 'string' || !/^[a-f0-9]{64}$/.test(v.reviewRevision)) fail()
  if (!Array.isArray(v.peerIds) || !samePeers(v.peerIds as string[], expected.peerIds) || new Set(v.peerIds).size !== expected.peerIds.length) fail()
  if (!Array.isArray(v.entries) || v.entries.length !== expected.selection.entries.length) fail()
  const entries = (v.entries as unknown[]).map((raw, index) => {
    const entry = object(raw), selected = expected.selection.entries[index]
    if (entry.path !== selected.path || entry.kind !== selected.kind || entry.size !== selected.size || !Number.isSafeInteger(entry.size) || (entry.size as number) < 0) fail()
    if (entry.kind === 'file' && (typeof entry.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(entry.sha256))) fail()
    if (entry.kind === 'directory' && (entry.size !== 0 || entry.sha256 !== undefined && entry.sha256 !== '')) fail()
    return { id: id(entry.id), path: selected.path, kind: selected.kind, size: selected.size, ...(entry.kind === 'file' ? { sha256: entry.sha256 as string } : {}) }
  })
  const totalBytes = entries.reduce((sum, entry) => sum + entry.size, 0)
  if (!Number.isSafeInteger(totalBytes) || v.totalBytes !== totalBytes || new Set(entries.map(entry => entry.id)).size !== entries.length) fail()
  return { ...base, status: 'prepared', reviewRevision: v.reviewRevision as string, peerIds: [...expected.peerIds], entries, totalBytes }
}
