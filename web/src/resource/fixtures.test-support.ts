// Synthetic, private test fixtures only. No provider, network or file access.
import { ResourceSettingsController, type ResourceContext } from './controller'
import type { LocalDescriptor, LocalOperation, LocalPreview, ManagementPreview, Outcome, RemoteSelection, ResourceCommandName, ResourceCommandPayloads, ResourceSelection, ResourceTransport, TransferSettings } from './types'

export const target = { schemaVersion: 1 as const, resourceId: 'a'.repeat(32) }
export const settings: TransferSettings = { transferConcurrentFiles: { mode: 'default' }, transferConcurrentPerPeer: { mode: 'limited', value: 2 } }
export const changedSettings: TransferSettings = { transferConcurrentFiles: { mode: 'limited', value: 7 }, transferConcurrentPerPeer: { mode: 'default' } }
export const effective = { transferConcurrentFiles: 4, transferConcurrentPerPeer: 2 }
export const localSelection: ResourceSelection = { kind: 'local', target }
export const remoteSelection: RemoteSelection = { kind: 'remote', peerKey: 'b'.repeat(64), selector: { protocolVersion: 2, target, grantId: 'c'.repeat(32), grantRevision: 1 } }
export const descriptor: LocalDescriptor = { ...target, type: 'transfer-admission-settings', authority: 'local', provider: 'local', operations: ['list', 'inspect', 'preview', 'apply', 'operation.status'], revision: 'd'.repeat(64), requested: settings, effective }
export const localPreview: LocalPreview = { ...target, operationId: `${target.resourceId}:${'e'.repeat(32)}:1`, baseRevision: 'd'.repeat(64), revision: 'f'.repeat(64), requested: settings, effective, destructive: false }
export const remotePreview: ManagementPreview = { operationId: '1'.repeat(64), baseRevision: '2'.repeat(64), reviewRevision: '3'.repeat(64), requested: settings, effective }
export const applied: Outcome = { status: 'applied', configuration: 'durable', accounting: 'succeeded', transfer: 'not_required' }
export const unknown: Outcome = { status: 'unknown', configuration: 'unobserved', accounting: 'unobserved', transfer: 'unobserved' }
export const localOperation: LocalOperation = { ...target, operationId: localPreview.operationId, outcome: applied, evidenceDurable: true, current: descriptor, journal: { records: 1, bytes: 100, maxRecords: 16, maxBytes: 8192, writable: true } }
export const context: ResourceContext = { authenticated: true, processId: 123, selectionRevision: 'synthetic-pair-v1', managedDirectLAN: true, peers: [{ key: remoteSelection.peerKey, name: 'Synthetic device' }] }
export type Call = { name: ResourceCommandName; payload: ResourceCommandPayloads[ResourceCommandName]; signal: AbortSignal }
export function remoteReply(action: 'inspect' | 'preview' | 'apply' | 'operation.status', extra?: Record<string, unknown>) {
  const arm = action === 'inspect' ? { inspection: { requested: settings, effective } } : action === 'preview' ? { preview: remotePreview } : { operation: { operationId: remotePreview.operationId, outcome: applied, evidenceDurable: true } }
  return { ...remoteSelection.selector, action, ...arm, ...extra }
}
export function response(call: Call): unknown {
  switch (call.name) {
    case 'resource.list': return { schemaVersion: 1, resources: [descriptor] }
    case 'resource.inspect': return descriptor
    case 'resource.preview': return { ...localPreview, requested: (call.payload as ResourceCommandPayloads['resource.preview']).settings }
    case 'resource.apply': case 'resource.operation.status': return localOperation
    case 'resource.remote.inspect': return { protocolVersion: 1, target, requested: settings, effective }
    case 'resource.remote.management.inspect': return remoteReply('inspect')
    case 'resource.remote.management.preview': return remoteReply('preview', { preview: { ...remotePreview, requested: (call.payload as ResourceCommandPayloads['resource.remote.management.preview']).request.preview.settings } })
    case 'resource.remote.management.apply': return remoteReply('apply')
    case 'resource.remote.management.operation.status': return remoteReply('operation.status')
  }
}
export function harness(handler: (call: Call) => unknown | Promise<unknown> = response) {
  const calls: Call[] = [], errors: unknown[] = []
  const transport: ResourceTransport = (name, payload, signal) => {
    const call = { name, payload, signal }
    calls.push(call)
    return Promise.resolve(handler(call))
  }
  const controller = new ResourceSettingsController(transport, error => errors.push(error), () => 1000)
  controller.updateContext(context); controller.open()
  return { controller, calls, errors }
}
export async function selectLocal(controller: ResourceSettingsController) {
  await controller.listLocal(); controller.select(localSelection); await controller.inspect()
}
export async function selectRemote(controller: ResourceSettingsController, selection = remoteSelection) {
  controller.select(selection); await controller.inspect()
}
export function deferred<T = unknown>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
