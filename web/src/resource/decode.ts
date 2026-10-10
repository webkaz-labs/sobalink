import type {
  EffectiveSettings, LocalCatalog, LocalDescriptor, LocalOperation, LocalPreview, LocalStatus,
  ManagementPreview, ManagementReply, ManagementRequest, OperationEvidence, Outcome,
  RemoteInspection, RemoteSelector, ResourceSelection, ResourceTarget, SettingsValues,
  TransferChoice, TransferSettings,
} from './types'

export class ResourceResponseError extends Error {
  readonly code = 'resource_response_invalid'
  constructor() { super('The resource response could not be validated.'); this.name = 'ResourceResponseError' }
}
function invalid(): never { throw new ResourceResponseError() }
function object(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return invalid()
  const prototype = Object.getPrototypeOf(value)
  if (prototype !== Object.prototype && prototype !== null) return invalid()
  return value as Record<string, unknown>
}
function closed(value: unknown, keys: readonly string[]) {
  const v = object(value), actual = Object.keys(v)
  if (actual.length !== keys.length || actual.some(key => !keys.includes(key))) return invalid()
  return v
}
function literal<T extends string | number | boolean>(value: unknown, expected: T): T {
  return value === expected ? expected : invalid()
}
function positive(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : invalid()
}
function nonnegative(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : invalid()
}
function boolean(value: unknown): boolean { return typeof value === 'boolean' ? value : invalid() }
export function readID(value: unknown): string { return typeof value === 'string' && /^[0-9a-f]{32}$/.test(value) ? value : invalid() }
export function readDigest(value: unknown): string { return typeof value === 'string' && /^[0-9a-f]{64}$/.test(value) ? value : invalid() }
export function readLocalOperationID(value: unknown, target: ResourceTarget): string {
  if (typeof value !== 'string' || value.length > 86) return invalid()
  const parts = value.split(':')
  if (parts.length !== 3 || readID(parts[0]) !== target.resourceId || !/^[1-9][0-9]{0,19}$/.test(parts[2])) return invalid()
  readID(parts[1])
  if (BigInt(parts[2]) > 18446744073709551615n) return invalid()
  return value
}
export function readChoice(value: unknown): TransferChoice {
  const v = object(value)
  if (v.mode === 'default') { closed(v, ['mode']); return Object.freeze({ mode: 'default' }) }
  closed(v, ['mode', 'value'])
  return Object.freeze({ mode: literal(v.mode, 'limited'), value: positive(v.value) })
}
export function readSettings(value: unknown): TransferSettings {
  const v = closed(value, ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  return Object.freeze({ transferConcurrentFiles: readChoice(v.transferConcurrentFiles), transferConcurrentPerPeer: readChoice(v.transferConcurrentPerPeer) })
}
function readEffective(value: unknown): EffectiveSettings {
  const v = closed(value, ['transferConcurrentFiles', 'transferConcurrentPerPeer'])
  return Object.freeze({ transferConcurrentFiles: positive(v.transferConcurrentFiles), transferConcurrentPerPeer: positive(v.transferConcurrentPerPeer) })
}
export function readTarget(value: unknown): ResourceTarget {
  const v = closed(value, ['schemaVersion', 'resourceId'])
  return Object.freeze({ schemaVersion: literal(v.schemaVersion, 1), resourceId: readID(v.resourceId) })
}
function targetFields(v: Record<string, unknown>) { return readTarget({ schemaVersion: v.schemaVersion, resourceId: v.resourceId }) }
export function sameTarget(a: ResourceTarget, b: ResourceTarget) { return a.schemaVersion === b.schemaVersion && a.resourceId === b.resourceId }
function matchTarget(value: ResourceTarget, expected: ResourceTarget) { if (!sameTarget(value, expected)) invalid() }
function values(v: Record<string, unknown>): SettingsValues {
  return Object.freeze({ requested: readSettings(v.requested), effective: readEffective(v.effective) })
}
export function sameSettings(a: TransferSettings, b: TransferSettings) {
  const equal = (x: TransferChoice, y: TransferChoice) => x.mode === y.mode && (x.mode === 'default' || y.mode === 'limited' && x.value === y.value)
  return equal(a.transferConcurrentFiles, b.transferConcurrentFiles) && equal(a.transferConcurrentPerPeer, b.transferConcurrentPerPeer)
}
const operations = Object.freeze(['list', 'inspect', 'preview', 'apply', 'operation.status'] as const)
export function readLocalDescriptor(value: unknown, expected?: ResourceTarget): LocalDescriptor {
  const v = closed(value, ['schemaVersion', 'resourceId', 'type', 'authority', 'provider', 'operations', 'revision', 'requested', 'effective'])
  const target = targetFields(v)
  if (expected) matchTarget(target, expected)
  const actualOperations = v.operations
  if (!Array.isArray(actualOperations) || actualOperations.length !== operations.length || operations.some((op, i) => actualOperations[i] !== op)) return invalid()
  return Object.freeze({ ...target, ...values(v), type: literal(v.type, 'transfer-admission-settings'), authority: literal(v.authority, 'local'), provider: literal(v.provider, 'local'), operations, revision: readDigest(v.revision) })
}
// W1's existing local list contains one resource. A changed list shape requires
// a reviewed catalog integration, rather than silently hiding extra entries.
export function readLocalCatalog(value: unknown): LocalCatalog {
  const v = closed(value, ['schemaVersion', 'resources'])
  if (!Array.isArray(v.resources) || v.resources.length > 1) return invalid()
  return Object.freeze({ schemaVersion: literal(v.schemaVersion, 1), resources: Object.freeze(v.resources.map(item => readLocalDescriptor(item))) })
}
export function readLocalPreview(value: unknown, expected: ResourceTarget, settings: TransferSettings): LocalPreview {
  const v = closed(value, ['schemaVersion', 'resourceId', 'operationId', 'baseRevision', 'revision', 'requested', 'effective', 'destructive'])
  const target = targetFields(v), view = values(v)
  matchTarget(target, expected)
  if (!sameSettings(view.requested, settings)) return invalid()
  return Object.freeze({ ...target, ...view, operationId: readLocalOperationID(v.operationId, target), baseRevision: readDigest(v.baseRevision), revision: readDigest(v.revision), destructive: literal(v.destructive, false) })
}
export function readOutcome(value: unknown): Outcome {
  const v = closed(value, ['status', 'configuration', 'accounting', 'transfer'])
  const success = (s: unknown) => s === 'succeeded' || s === 'not_required'
  const pendingStages = v.accounting === 'not_attempted' && v.transfer === 'not_attempted'
  const partialStages = v.accounting === 'failed' && v.transfer === 'not_attempted' || success(v.accounting) && v.transfer === 'failed'
  const valid = v.status === 'applied' && v.configuration === 'durable' && success(v.accounting) && success(v.transfer)
    || v.status === 'failed' && (v.configuration === 'not_attempted' || v.configuration === 'not_published') && pendingStages
    || v.status === 'canceled' && v.configuration === 'not_attempted' && pendingStages
    || v.status === 'saved_not_applied' && v.configuration === 'durable' && partialStages
    || v.status === 'unknown' && (v.configuration === 'unobserved' && v.accounting === 'unobserved' && v.transfer === 'unobserved'
      || v.configuration === 'uncertain' && (partialStages || success(v.accounting) && success(v.transfer)))
  if (!valid) return invalid()
  // All four values have been checked together against the provider's closed
  // Outcome.Validate matrix above, not accepted by an unchecked response cast.
  return Object.freeze({ status: v.status, configuration: v.configuration, accounting: v.accounting, transfer: v.transfer }) as Outcome
}
function evidence(v: Record<string, unknown>, id: string): OperationEvidence {
  if (v.operationId !== id) return invalid()
  return Object.freeze({ operationId: id, outcome: readOutcome(v.outcome), evidenceDurable: boolean(v.evidenceDurable) })
}
export function readLocalOperation(value: unknown, expected: LocalStatus): LocalOperation {
  const v = closed(value, ['schemaVersion', 'resourceId', 'operationId', 'outcome', 'evidenceDurable', 'current', 'journal'])
  const target = targetFields(v)
  matchTarget(target, expected)
  const id = readLocalOperationID(v.operationId, target), j = closed(v.journal, ['records', 'bytes', 'maxRecords', 'maxBytes', 'writable'])
  if (id !== expected.operationId) return invalid()
  const journal = Object.freeze({ records: nonnegative(j.records), bytes: nonnegative(j.bytes), maxRecords: positive(j.maxRecords), maxBytes: positive(j.maxBytes), writable: boolean(j.writable) })
  if (journal.records > journal.maxRecords || journal.bytes > journal.maxBytes) return invalid()
  return Object.freeze({ ...target, ...evidence(v, id), current: readLocalDescriptor(v.current, target), journal })
}
export function readSelector(value: unknown): RemoteSelector {
  const v = closed(value, ['protocolVersion', 'target', 'grantId', 'grantRevision'])
  if (v.protocolVersion !== 1 && v.protocolVersion !== 2) return invalid()
  return Object.freeze({ protocolVersion: v.protocolVersion, target: readTarget(v.target), grantId: readID(v.grantId), grantRevision: positive(v.grantRevision) })
}
export function readSelection(value: unknown): ResourceSelection {
  const v = object(value)
  if (v.kind === 'local') { closed(v, ['kind', 'target']); return Object.freeze({ kind: 'local', target: readTarget(v.target) }) }
  closed(v, ['kind', 'peerKey', 'selector'])
  return Object.freeze({ kind: literal(v.kind, 'remote'), peerKey: readDigest(v.peerKey), selector: readSelector(v.selector) })
}
export function readRemoteInspection(value: unknown, expected: RemoteSelector<1>): RemoteInspection {
  const v = closed(value, ['protocolVersion', 'target', 'requested', 'effective']), target = readTarget(v.target)
  matchTarget(target, expected.target)
  return Object.freeze({ protocolVersion: literal(v.protocolVersion, 1), target, ...values(v) })
}
function readManagementPreview(value: unknown, settings: TransferSettings): ManagementPreview {
  const v = closed(value, ['operationId', 'baseRevision', 'reviewRevision', 'requested', 'effective']), view = values(v)
  if (!sameSettings(view.requested, settings)) return invalid()
  return Object.freeze({ ...view, operationId: readDigest(v.operationId), baseRevision: readDigest(v.baseRevision), reviewRevision: readDigest(v.reviewRevision) })
}
export function readManagementReply(value: unknown, expected: ManagementRequest): ManagementReply {
  const v = object(value), common = ['protocolVersion', 'target', 'grantId', 'grantRevision', 'action']
  const selector = readSelector({ protocolVersion: v.protocolVersion, target: v.target, grantId: v.grantId, grantRevision: v.grantRevision })
  if (selector.protocolVersion !== 2 || selector.grantId !== expected.grantId || selector.grantRevision !== expected.grantRevision || v.action !== expected.action) return invalid()
  matchTarget(selector.target, expected.target)
  const base = { ...selector, protocolVersion: 2 as const }
  switch (expected.action) {
    case 'inspect': {
      closed(v, [...common, 'inspection'])
      return Object.freeze({ ...base, action: 'inspect', inspection: values(closed(v.inspection, ['requested', 'effective'])) })
    }
    case 'preview': {
      closed(v, [...common, 'preview'])
      return Object.freeze({ ...base, action: 'preview', preview: readManagementPreview(v.preview, expected.preview.settings) })
    }
    case 'apply': {
      closed(v, [...common, 'operation'])
      const op = closed(v.operation, ['operationId', 'outcome', 'evidenceDurable'])
      readDigest(op.operationId)
      return Object.freeze({ ...base, action: 'apply', operation: evidence(op, expected.apply.operationId) })
    }
    case 'operation.status': {
      if (Object.hasOwn(v, 'unavailable')) {
        closed(v, [...common, 'unavailable'])
        const unavailable = closed(v.unavailable, ['operationId'])
        if (readDigest(unavailable.operationId) !== expected.status.operationId) return invalid()
        return Object.freeze({ ...base, action: 'operation.status', unavailable: Object.freeze({ operationId: expected.status.operationId }) })
      }
      closed(v, [...common, 'operation'])
      const op = closed(v.operation, ['operationId', 'outcome', 'evidenceDurable'])
      readDigest(op.operationId)
      return Object.freeze({ ...base, action: 'operation.status', operation: evidence(op, expected.status.operationId) })
    }
  }
}
