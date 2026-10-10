// Exact existing command DTOs only. These values select an existing scope;
// they neither establish management authority nor create or upgrade a grant.
export type TransferChoice = Readonly<{ mode: 'default' } | { mode: 'limited'; value: number }>
export type TransferSettings = Readonly<{
  transferConcurrentFiles: TransferChoice
  transferConcurrentPerPeer: TransferChoice
}>
export type EffectiveSettings = Readonly<{ transferConcurrentFiles: number; transferConcurrentPerPeer: number }>
export type ResourceTarget = Readonly<{ schemaVersion: 1; resourceId: string }>
export type SettingsValues = Readonly<{ requested: TransferSettings; effective: EffectiveSettings }>
export type LocalDescriptor = ResourceTarget & SettingsValues & Readonly<{
  type: 'transfer-admission-settings'; authority: 'local'; provider: 'local'
  operations: readonly ['list', 'inspect', 'preview', 'apply', 'operation.status']
  revision: string
}>
export type LocalCatalog = Readonly<{ schemaVersion: 1; resources: readonly LocalDescriptor[] }>
export type LocalPreview = ResourceTarget & SettingsValues & Readonly<{
  operationId: string; baseRevision: string; revision: string; destructive: false
}>
export type LocalApply = ResourceTarget & Readonly<{
  operationId: string; baseRevision: string; revision: string; settings: TransferSettings
}>
export type LocalStatus = ResourceTarget & Readonly<{ operationId: string }>
export type Outcome = Readonly<{
  status: 'applied' | 'failed' | 'canceled' | 'saved_not_applied' | 'unknown'
  configuration: 'durable' | 'not_attempted' | 'not_published' | 'unobserved' | 'uncertain'
  accounting: 'succeeded' | 'not_required' | 'failed' | 'not_attempted' | 'unobserved'
  transfer: 'succeeded' | 'not_required' | 'failed' | 'not_attempted' | 'unobserved'
}>
export type OperationEvidence = Readonly<{ operationId: string; outcome: Outcome; evidenceDurable: boolean }>
export type LocalOperation = ResourceTarget & OperationEvidence & Readonly<{
  current: LocalDescriptor
  journal: Readonly<{ records: number; bytes: number; maxRecords: number; maxBytes: number; writable: boolean }>
}>
export type RemoteSelector<V extends 1 | 2 = 1 | 2> = Readonly<{
  protocolVersion: V; target: ResourceTarget; grantId: string; grantRevision: number
}>
export type RemoteInspection = SettingsValues & Readonly<{ protocolVersion: 1; target: ResourceTarget }>
export type ManagementPreview = SettingsValues & Readonly<{
  operationId: string; baseRevision: string; reviewRevision: string
}>
export type ManagementApply = Readonly<{
  operationId: string; baseRevision: string; reviewRevision: string; settings: TransferSettings
}>
export type ManagementRequest = RemoteSelector<2> & (
  | Readonly<{ action: 'inspect' }>
  | Readonly<{ action: 'preview'; preview: Readonly<{ settings: TransferSettings }> }>
  | Readonly<{ action: 'apply'; apply: ManagementApply }>
  | Readonly<{ action: 'operation.status'; status: Readonly<{ operationId: string }> }>
)
export type ManagementReply = RemoteSelector<2> & (
  | Readonly<{ action: 'inspect'; inspection: SettingsValues }>
  | Readonly<{ action: 'preview'; preview: ManagementPreview }>
  | Readonly<{ action: 'apply'; operation: OperationEvidence }>
  | Readonly<{ action: 'operation.status'; operation: OperationEvidence; unavailable?: never }>
  | Readonly<{ action: 'operation.status'; unavailable: Readonly<{ operationId: string }>; operation?: never }>
)
type RemoteInput<A extends ManagementRequest['action'], C extends boolean> = Readonly<{
  peerKey: string; request: Extract<ManagementRequest, { action: A }>; confirm: C
}>
export interface ResourceCommandPayloads {
  'resource.list': Record<string, never>
  'resource.inspect': ResourceTarget
  'resource.preview': ResourceTarget & Readonly<{ settings: TransferSettings }>
  'resource.apply': LocalApply
  'resource.operation.status': LocalStatus
  'resource.remote.inspect': Readonly<{ peerKey: string; request: RemoteSelector<1> }>
  'resource.remote.management.inspect': RemoteInput<'inspect', false>
  'resource.remote.management.preview': RemoteInput<'preview', false>
  'resource.remote.management.apply': RemoteInput<'apply', true>
  'resource.remote.management.operation.status': RemoteInput<'operation.status', false>
}
export type ResourceCommandName = keyof ResourceCommandPayloads
// The integrator supplies api.command(...).result. Never use useServer.run:
// its undefined result/retry semantics cannot represent this operation flow.
export type ResourceTransport = <N extends ResourceCommandName>(
  name: N, payload: ResourceCommandPayloads[N], signal: AbortSignal,
) => Promise<unknown>
export type LocalSelection = Readonly<{ kind: 'local'; target: ResourceTarget }>
export type RemoteSelection = Readonly<{ kind: 'remote'; peerKey: string; selector: RemoteSelector }>
export type ResourceSelection = LocalSelection | RemoteSelection
// A saved key is a selection candidate, never proof of remote permission.
export type SavedManagementPeer = Readonly<{ key: string; name: string }>
