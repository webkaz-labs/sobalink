import type { ManagementReply, ManagementRequest, OperationEvidence, RemoteSelector, TransferChoice, TransferSettings } from '../resource/types'

// Presentation data never restores Core-owned prepared admission or a grant.
export type GroupTemplate = Readonly<{ schemaVersion: 1; settings: TransferSettings; revision: string }>
export type GroupOverride = Readonly<{ transferConcurrentFiles?: TransferChoice; transferConcurrentPerPeer?: TransferChoice }>
export type GroupMember = Readonly<{ peerKey: string; selector: RemoteSelector<2>; override?: GroupOverride }>
export type GroupSelection = Readonly<{ schemaVersion: 1; template: GroupTemplate; members: readonly GroupMember[] }>
export type GroupResolvedMember = GroupMember & Readonly<{ requested: TransferSettings }>
export type GroupResolvedSelection = Readonly<{ schemaVersion: 1; template: GroupTemplate; members: readonly GroupResolvedMember[] }>
export type GroupReviewState = 'ready' | 'unavailable' | 'unsupported' | 'invalid_reply' | 'canceled_before_preview'
export type GroupPreviewReply = Extract<ManagementReply, { action: 'preview' }>
export type GroupApplyRequest = Extract<ManagementRequest, { action: 'apply' }>
export type GroupReviewRow = Readonly<{ schemaVersion: 1; peerKey: string }> & (
  | Readonly<{ state: 'ready'; reply: GroupPreviewReply }>
  | Readonly<{ state: Exclude<GroupReviewState, 'ready'>; reply?: never }>
)
export type GroupReview = Readonly<{
  schemaVersion: 1; selection: GroupResolvedSelection; rows: readonly GroupReviewRow[]
  executionPeers: readonly string[]; revision: string
}>
export type GroupLocalDurability = 'durable' | 'not_saved' | 'uncertain'
export type GroupDispatch = 'not_attempted' | 'dispatching' | 'observed' | 'unknown'
export type GroupAdmissionStop = 'none' | 'user_canceled' | 'budget_exhausted' | 'context_changed' | 'persistence_uncertain' | 'restarted'
export type GroupStatusObservation = (
  | Readonly<{ state: 'not_queried'; sequence: 0; observedAt: 0; operation?: never }>
  | Readonly<{ state: 'observed'; sequence: number; observedAt: number; operation: OperationEvidence }>
  | Readonly<{ state: 'unavailable' | 'unsupported' | 'query_failed'; sequence: number; observedAt: number; operation?: never }>
)
export type GroupMemberEvidence = Readonly<{
  schemaVersion: 1; groupRevision: string; peerKey: string; review: GroupReviewState
  execution: 'selected' | 'excluded'; request?: GroupApplyRequest; dispatch: GroupDispatch
  localDurability: GroupLocalDurability; target?: OperationEvidence
  status: GroupStatusObservation; admissionStop: GroupAdmissionStop
}>
export type GroupEvidence = Readonly<{ schemaVersion: 1; members: readonly GroupMemberEvidence[] }>
export type GroupSummary = Readonly<{
  schemaVersion: 1; selected: number; executable: number; excluded: number; reviewFailures: number
  notAttempted: number; dispatching: number; dispatchObserved: number; dispatchUnknown: number
  applied: number; failed: number; canceled: number; savedNotApplied: number; targetUnknown: number
  targetUnobserved: number; targetNonDurable: number; localNonDurable: number; statusFailures: number
  admissionFinished: boolean; reconciliationRequired: boolean; allApplied: boolean
}>
export type PreparedView = Readonly<{
  schemaVersion: 1; reviewId: string; review: GroupReview
  admissionState: 'prepared' | 'canceled' | 'unavailable'; initializesLocalEvidence: boolean
}>
export type RunView = Readonly<{
  schemaVersion: 1; runId: string; acceptedAt: number; review: GroupReview; evidence: GroupEvidence
  summary: GroupSummary; localDurability: GroupLocalDurability; activity: 'idle' | 'applying' | 'refreshing'
}>
export type CancelView = Readonly<{ schemaVersion: 1 }> & (
  | Readonly<{ prepared: PreparedView; run?: never }>
  | Readonly<{ run: RunView; prepared?: never }>
)
export type PreviewInput = Readonly<{ schemaVersion: 1; selection: GroupSelection; replaceReviewId?: string }>
export type SelectInput = Readonly<{ schemaVersion: 1; reviewId: string; reviewRevision: string; executionPeers: readonly string[] }>
export type ApplyInput = SelectInput & Readonly<{ confirm: true }>
export type StatusInput = Readonly<{ schemaVersion: 1; runId: string }>
export type RefreshInput = StatusInput & Readonly<{ peers: readonly string[] }>
export type CancelInput = Readonly<{ schemaVersion: 1; reviewId: string }>
// Separate reviewed local read: no accepted-run or active-operation listing.
export type CurrentReviewInput = Readonly<{ schemaVersion: 1 }>
export type CurrentReviewState = 'none' | 'current'
export type CurrentReviewView = Readonly<{ schemaVersion: 1 }> & (
  | Readonly<{ state: 'none'; prepared?: never }>
  | Readonly<{ state: 'current'; prepared: PreparedView }>
)
export interface GroupCommandPayloads {
  'resource.group.preview': PreviewInput
  'resource.group.review.select': SelectInput
  'resource.group.apply': ApplyInput
  'resource.group.status': StatusInput
  'resource.group.status.refresh': RefreshInput
  'resource.group.cancel': CancelInput
  'resource.group.review.current': CurrentReviewInput
}
export type GroupCommandName = keyof GroupCommandPayloads
// Adapter returns authenticated api.command(...).result, never useServer.run.
export type GroupTransport = <N extends GroupCommandName>(name: N, payload: GroupCommandPayloads[N], signal: AbortSignal) => Promise<unknown>
