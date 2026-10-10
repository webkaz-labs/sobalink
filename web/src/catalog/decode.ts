import { readDigest, readID, readLocalDescriptor, readSettings, readTarget, sameTarget } from '../resource/decode'
import type { SettingsValues } from '../resource/types'
import type { CatalogLimits, CatalogRequest, CatalogResponse, CatalogRow, DecodeBudget, Identity, Selection, SourceKind, SourceRequest, SourceView } from './types'

export class CatalogResponseError extends Error { constructor() { super('Invalid local catalog response'); this.name = 'CatalogResponseError' } }
function fail(): never { throw new CatalogResponseError() }
const encoder = new TextEncoder()
const kinds = ['local_settings', 'local_service', 'remote_service', 'remote_settings_v1', 'remote_settings_v2', 'transfer_activity'] as const
export function closed(value: unknown, required: readonly string[], optional: readonly string[] = []): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value) || ![Object.prototype, null].includes(Object.getPrototypeOf(value))) return fail()
  const v = value as Record<string, unknown>, keys = Object.keys(v)
  if (required.some(key => !Object.hasOwn(v, key)) || keys.some(key => !required.includes(key) && !optional.includes(key))) return fail()
  return v
}
export function integer(v: unknown, min = 0, max = Number.MAX_SAFE_INTEGER): number { return typeof v === 'number' && Number.isSafeInteger(v) && v >= min && v <= max ? v : fail() }
function bool(v: unknown): boolean { return typeof v === 'boolean' ? v : fail() }
function equal<T extends string | number>(v: unknown, expected: T): T { return v === expected ? expected : fail() }
function choice<T extends string>(v: unknown, values: readonly T[]): T { return typeof v === 'string' && values.includes(v as T) ? v as T : fail() }
function text(v: unknown, max: number, empty = false): string {
  if (typeof v !== 'string' || !empty && !v || v.length > max || /[\u0000-\u001f\u007f]/.test(v) || /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(v) || encoder.encode(v).length > max) return fail()
  return v
}
export function peerID(v: unknown): string { const s = text(v, 128); return /^[A-Za-z0-9][A-Za-z0-9:_-]*$/.test(s) ? s : fail() }
const timestamp = (v: unknown) => integer(v, 1, 253402300799000)
function array(v: unknown, max: number): unknown[] { return Array.isArray(v) && v.length <= max ? v : fail() }
// Match Go's string escaping for byte bounds/order only, not digest verification.
function json(v: unknown): string { return JSON.stringify(v).replace(/[<>&\u2028\u2029]/g, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`) }
export function identityKey(v: Identity): string { return json({ id: v.id, lifetime: v.lifetime, direction: v.direction }) }
export function readCatalogRequest(value: unknown): CatalogRequest {
  const v = closed(value, ['schemaVersion', 'sources']); equal(v.schemaVersion, 1)
  const seen = new Set<string>(), peers = new Set<string>()
  const sources = array(v.sources, 5).map(raw => {
    const base = closed(raw, ['kind'], ['target', 'peerId', 'peerKey', 'grantId', 'grantRevision'])
    const kind = choice(base.kind, kinds)
    const key = kind.startsWith('remote_settings') ? 'remote_settings' : kind
    if (seen.has(key)) fail(); seen.add(key)
    if (kind === 'local_service' || kind === 'transfer_activity') { closed(raw, ['kind']); return Object.freeze({ kind }) }
    if (kind === 'local_settings') { closed(raw, ['kind', 'target']); return Object.freeze({ kind, target: readTarget(base.target) }) }
    if (kind === 'remote_service') { closed(raw, ['kind', 'peerId']); const peerId = peerID(base.peerId); peers.add(peerId); return Object.freeze({ kind, peerId }) }
    closed(raw, ['kind', 'peerKey', 'target', 'grantId', 'grantRevision'])
    const peerKey = readDigest(base.peerKey); peers.add(peerKey)
    return Object.freeze({ kind, peerKey, target: readTarget(base.target), grantId: readID(base.grantId), grantRevision: integer(base.grantRevision, 1) })
  }).sort((a, b) => a.kind < b.kind ? -1 : a.kind > b.kind ? 1 : 0)
  const request = Object.freeze({ schemaVersion: 1 as const, sources: Object.freeze(sources) })
  if (!sources.length || peers.size > 1 || encoder.encode(json(request)).length > 4096) fail()
  return request
}
function limits(raw: unknown, budget: DecodeBudget): CatalogLimits {
  const keys = ['maxSources', 'maxRemoteTargets', 'maxRows', 'maxPageRows', 'maxPages', 'maxBytes', 'maxPageBytes', 'maxStringBytes'] as const
  const v = closed(raw, keys)
  const l = Object.freeze({ maxSources: integer(v.maxSources, 1, 5), maxRemoteTargets: equal(v.maxRemoteTargets, 1), maxRows: integer(v.maxRows, 1, budget.rows), maxPageRows: integer(v.maxPageRows, 1, budget.rows), maxPages: equal(v.maxPages, 1), maxBytes: integer(v.maxBytes, 1, budget.bytes), maxPageBytes: integer(v.maxPageBytes, 1, budget.bytes), maxStringBytes: integer(v.maxStringBytes, 1, budget.bytes) })
  if (l.maxPageRows > l.maxRows || l.maxPageBytes > l.maxBytes || l.maxStringBytes > l.maxPageBytes) fail()
  return l
}
function selection(raw: unknown, request: SourceRequest, epoch: string, process: string): Selection {
  const v = closed(raw, ['sourceId', 'kind', 'epoch', 'peerKey', 'target', 'grantId', 'grantRevision', 'processId'])
  const kind = equal(v.kind, request.kind), sourceId = equal(v.sourceId, kind)
  equal(v.epoch, epoch)
  const remote = request.kind === 'remote_settings_v1' || request.kind === 'remote_settings_v2'
  const target = 'target' in request ? readTarget(v.target) : (() => { const z = closed(v.target, ['schemaVersion', 'resourceId']); return Object.freeze({ schemaVersion: equal(z.schemaVersion, 0), resourceId: equal(z.resourceId, '') }) })()
  if ('target' in request && (target.schemaVersion !== 1 || !sameTarget(target, request.target))) fail()
  return Object.freeze({ sourceId, kind, epoch, target, peerKey: equal(v.peerKey, remote ? request.peerKey : request.kind === 'remote_service' ? request.peerId : ''), grantId: equal(v.grantId, remote ? request.grantId : ''), grantRevision: equal(v.grantRevision, remote ? request.grantRevision : 0), processId: equal(v.processId, kind === 'transfer_activity' ? process : '') })
}
function values(raw: unknown): SettingsValues {
  const v = closed(raw, ['requested', 'effective']), e = closed(v.effective, ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  return Object.freeze({ requested: readSettings(v.requested), effective: Object.freeze({ transferConcurrentFiles: integer(e.transferConcurrentFiles, 1), transferConcurrentPerPeer: integer(e.transferConcurrentPerPeer, 1) }) })
}
function ports(v: unknown, max: number): string {
  const s = text(v, max), chunks = s.split(','); if (chunks.length > 65535) fail()
  for (const chunk of chunks) { const ends = chunk.trim().split('-'); if (ends.length > 2 || ends.some(end => !/^\d+$/.test(end.trim()))) fail(); const numbers = ends.map(end => integer(Number(end.trim()), 1, 65535)); if (numbers.length === 2 && numbers[1] < numbers[0]) fail() }
  return s
}
function row(raw: unknown, s: Selection, l: CatalogLimits): CatalogRow {
  const arm = { local_settings: 'localSettings', remote_settings_v1: 'remoteSettingsV1', remote_settings_v2: 'remoteSettingsV2', local_service: 'localService', remote_service: 'remoteService', transfer_activity: 'transferActivity' }[s.kind]
  const v = closed(raw, ['identity', arm]), ident = closed(v.identity, ['id', 'lifetime', 'direction'])
  const identity = Object.freeze({ id: text(ident.id, l.maxStringBytes), lifetime: equal(ident.lifetime, s.kind === 'remote_service' ? 'activation' : s.kind === 'transfer_activity' ? 'process' : 'persistent'), direction: s.kind === 'transfer_activity' ? choice(ident.direction, ['incoming', 'outgoing'] as const) : equal(ident.direction, '') })
  if (s.kind === 'local_settings') { if (s.target.schemaVersion !== 1 || identity.id !== s.target.resourceId) fail(); return Object.freeze({ identity, localSettings: readLocalDescriptor(v[arm], s.target) }) }
  if (s.kind === 'remote_settings_v1' || s.kind === 'remote_settings_v2') { if (identity.id !== s.target.resourceId) fail(); return s.kind === 'remote_settings_v1' ? Object.freeze({ identity, remoteSettingsV1: values(v[arm]) }) : Object.freeze({ identity, remoteSettingsV2: values(v[arm]) }) }
  if (s.kind === 'local_service') {
    peerID(identity.id); const x = closed(v[arm], ['name', 'direction', 'network', 'ports', 'lifetime', 'state', 'application'])
    const direction = choice(x.direction, ['share', 'forward'] as const), lifetime = choice(x.lifetime, ['', 'finite', 'until-stopped', 'until-revoked'] as const)
    if (lifetime === 'until-stopped' && direction !== 'forward' || lifetime === 'until-revoked' && direction !== 'share') fail()
    return Object.freeze({ identity, localService: Object.freeze({ name: text(x.name, l.maxStringBytes), direction, network: choice(x.network, ['tcp', 'udp'] as const), ports: ports(x.ports, l.maxStringBytes), lifetime, state: choice(x.state, ['saved', 'starting', 'active', 'reconnecting', 'failed', 'expired', 'stopped'] as const), application: equal(x.application, 'unverified') }) })
  }
  if (s.kind === 'remote_service') {
    peerID(identity.id); const x = closed(v[arm], ['purpose', 'network', 'ports', 'lifetime', 'expiresAt', 'application', 'reviewRevision']), lifetime = choice(x.lifetime, ['', 'finite', 'until-revoked'] as const)
    return Object.freeze({ identity, remoteService: Object.freeze({ purpose: choice(x.purpose, ['', 'generic', 'custom', 'web', 'ssh', 'db', 'postgres', 'ai', 'local-ai', 'desktop', 'rustdesk'] as const), network: choice(x.network, ['tcp', 'udp'] as const), ports: ports(x.ports, l.maxStringBytes), lifetime, expiresAt: lifetime === 'until-revoked' ? equal(x.expiresAt, 0) : timestamp(x.expiresAt), application: equal(x.application, 'unverified'), reviewRevision: text(x.reviewRevision, l.maxStringBytes) }) })
  }
  if (!/^[A-Za-z0-9_-]{1,128}$/.test(identity.id)) fail()
  const x = closed(v[arm], ['peerId', 'totalBytes', 'completedBytes', 'state']), peerId = text(x.peerId, Math.min(256, l.maxStringBytes)), totalBytes = integer(x.totalBytes)
  if (/[\s\p{Cc}]/u.test(peerId)) fail()
  return Object.freeze({ identity, transferActivity: Object.freeze({ peerId, totalBytes, completedBytes: integer(x.completedBytes, 0, totalBytes), state: choice(x.state, ['awaiting-acceptance', 'queued', 'transferring', 'saving', 'failed', 'completed', 'cancelled', 'declined'] as const) }) })
}
export function readCatalogResponse(raw: unknown, request: CatalogRequest, process: string, budget: DecodeBudget): CatalogResponse {
  const expected = readCatalogRequest(request); readDigest(process); integer(budget.rows, 1); integer(budget.bytes, 1)
  const envelope = closed(raw, ['schemaVersion', 'limits', 'snapshot']); equal(envelope.schemaVersion, 1)
  const l = limits(envelope.limits, budget), v = closed(envelope.snapshot, ['schemaVersion', 'id', 'scopeId', 'revision', 'complete', 'sources']); equal(v.schemaVersion, 1)
  const id = readDigest(v.id), scopeId = readDigest(v.scopeId), revision = readDigest(v.revision), complete = bool(v.complete)
  const rawSources = array(v.sources, l.maxSources); if (rawSources.length !== expected.sources.length) fail()
  let remaining = l.maxRows
  const sources: SourceView[] = rawSources.map((rawSource, index) => {
    const x = closed(rawSource, ['selection', 'state', 'checkedAt', 'revision', 'complete', 'rows'], ['total'])
    const selected = selection(x.selection, expected.sources[index], id, process)
    const state = choice(x.state, ['unconfirmed', 'current', 'stale', 'unavailable', 'unsupported', 'invalid', 'limited'] as const)
    // Cached discovery has no current publication-origin proof in this slice.
    if (selected.kind === 'remote_service' && state === 'current') fail()
    const checkedAt = state === 'unconfirmed' || state === 'invalid' && x.checkedAt === 0 ? equal(x.checkedAt, 0) : timestamp(x.checkedAt)
    const sourceRevision = text(x.revision, l.maxStringBytes, true), sourceComplete = bool(x.complete)
    const rawRows = array(x.rows, Math.min(remaining, l.maxPageRows)); remaining -= rawRows.length
    const rows = Object.freeze(rawRows.map(r => row(r, selected, l)))
    const total = Object.hasOwn(x, 'total') ? integer(x.total, rows.length) : undefined
    if (['unconfirmed', 'unavailable', 'unsupported', 'invalid'].includes(state) && (sourceRevision !== '' || sourceComplete || total !== undefined || rows.length) || state === 'limited' && sourceComplete || (state === 'current' || state === 'stale' || rows.length > 0) && !sourceRevision || sourceComplete && total !== undefined && total !== rows.length || sourceComplete && selected.kind.includes('settings') && rows.length !== 1) fail()
    for (let i = 1; i < rows.length; ++i) if (identityKey(rows[i - 1].identity) >= identityKey(rows[i].identity)) fail()
    return Object.freeze({ selection: selected, state, checkedAt, revision: sourceRevision, complete: sourceComplete, ...(total === undefined ? {} : { total }), rows })
  })
  if (complete !== sources.every(source => source.complete)) fail()
  const snapshot = Object.freeze({ schemaVersion: 1 as const, id, scopeId, revision, complete, sources: Object.freeze(sources) })
  const checkStrings = (value: unknown, depth = 0): void => {
    if (depth > 12) fail()
    if (typeof value === 'string') { text(value, l.maxStringBytes, true); return }
    if (Array.isArray(value)) { for (const item of value) checkStrings(item, depth + 1) }
    else if (value && typeof value === 'object') for (const item of Object.values(value)) checkStrings(item, depth + 1)
  }
  checkStrings(snapshot)
  if (encoder.encode(json(snapshot)).length > l.maxBytes) fail()
  return Object.freeze({ schemaVersion: 1, limits: l, snapshot })
}
