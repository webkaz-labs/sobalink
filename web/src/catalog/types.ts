import type { LocalDescriptor, ResourceTarget, SettingsValues } from '../resource/types'

export type SourceRequest = Readonly<
  | { kind: 'local_settings'; target: ResourceTarget }
  | { kind: 'local_service' | 'transfer_activity' }
  | { kind: 'remote_service'; peerId: string }
  | { kind: 'remote_settings_v1' | 'remote_settings_v2'; peerKey: string; target: ResourceTarget; grantId: string; grantRevision: number }
>
export type SourceKind = SourceRequest['kind']
export type CatalogRequest = Readonly<{ schemaVersion: 1; sources: readonly SourceRequest[] }>
export interface ResourceCatalogCommandPayloads { 'resource.catalog.snapshot': CatalogRequest }
export type CatalogTransport = {
  list(signal: AbortSignal): Promise<unknown>
  snapshot(request: CatalogRequest, signal: AbortSignal): Promise<unknown>
}
export type CatalogLimits = Readonly<{ maxSources: number; maxRemoteTargets: number; maxRows: number; maxPageRows: number; maxPages: number; maxBytes: number; maxPageBytes: number; maxStringBytes: number }>
export type DecodeBudget = Readonly<{ rows: number; bytes: number }>
export type Selection = Readonly<{
  sourceId: SourceKind; kind: SourceKind; epoch: string; peerKey: string
  target: ResourceTarget | Readonly<{ schemaVersion: 0; resourceId: '' }>
  grantId: string; grantRevision: number; processId: string
}>
export type Identity = Readonly<{ id: string; lifetime: 'persistent' | 'activation' | 'process'; direction: '' | 'incoming' | 'outgoing' }>
export type SavedService = Readonly<{ name: string; direction: 'share' | 'forward'; network: 'tcp' | 'udp'; ports: string; lifetime: '' | 'finite' | 'until-stopped' | 'until-revoked'; state: 'saved' | 'starting' | 'active' | 'reconnecting' | 'failed' | 'expired' | 'stopped'; application: 'unverified' }>
export type SharedService = Readonly<{ purpose: '' | 'generic' | 'custom' | 'web' | 'ssh' | 'db' | 'postgres' | 'ai' | 'local-ai' | 'desktop' | 'rustdesk'; network: 'tcp' | 'udp'; ports: string; lifetime: '' | 'finite' | 'until-revoked'; expiresAt: number; application: 'unverified'; reviewRevision: string }>
export type TransferActivity = Readonly<{ peerId: string; totalBytes: number; completedBytes: number; state: 'awaiting-acceptance' | 'queued' | 'transferring' | 'saving' | 'failed' | 'completed' | 'cancelled' | 'declined' }>
export type CatalogRow = Readonly<{ identity: Identity } & (
  | { localSettings: LocalDescriptor } | { remoteSettingsV1: SettingsValues } | { remoteSettingsV2: SettingsValues }
  | { localService: SavedService } | { remoteService: SharedService } | { transferActivity: TransferActivity }
)>
export type SourceState = 'unconfirmed' | 'current' | 'stale' | 'unavailable' | 'unsupported' | 'invalid' | 'limited'
export type SourceView = Readonly<{ selection: Selection; state: SourceState; checkedAt: number; revision: string; complete: boolean; total?: number; rows: readonly CatalogRow[] }>
export type CatalogSnapshot = Readonly<{ schemaVersion: 1; id: string; scopeId: string; revision: string; complete: boolean; sources: readonly SourceView[] }>
export type CatalogResponse = Readonly<{ schemaVersion: 1; limits: CatalogLimits; snapshot: CatalogSnapshot }>
export type WorkflowKind = 'inspect_settings' | 'review_settings' | 'review_saved_service' | 'review_service_start' | 'review_service_stop' | 'open_transfer'
export type Workflow = Readonly<{ kind: WorkflowKind; sourceId: SourceKind; identity: Identity }>
export type TransferFocus = Readonly<{ processId: string; peerId: string; id: string; direction: 'incoming' | 'outgoing' }>
