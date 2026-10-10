import { ResourceAttemptRegistry, type AttemptScope } from './attempt-registry'
import { ApiError } from '../api'
import {
  readDigest, readLocalCatalog, readLocalDescriptor, readLocalOperation, readLocalPreview,
  readManagementReply, readRemoteInspection, readSelection, readSettings, sameSettings,
} from './decode'
import type {
  LocalApply, LocalDescriptor, LocalPreview, ManagementApply, ManagementPreview, ManagementRequest,
  OperationEvidence, RemoteSelector, ResourceSelection, ResourceTransport, SavedManagementPeer,
  SettingsValues, TransferSettings,
} from './types'

export const MAX_RESOURCE_ATTEMPTS = 64
export type ResourceNotice = 'invalid' | 'unavailable' | 'unsupported' | 'conflict' | 'evidenceUnavailable' | 'limit' | 'contextChanged' | null
export type ResourceContext = Readonly<{
  authenticated: boolean
  // Current local process ID is only an invalidation hint, never authority.
  processId: number | null
  // Host-owned hint for selected mode/pair identity changes. Never derive this
  // from labels or routes, or treat it as a provider generation token.
  selectionRevision: string
  managedDirectLAN: boolean
  peers: readonly SavedManagementPeer[]
}>
export type CurrentObservation = Readonly<{
  state: 'unread' | 'observed' | 'unconfirmed'
  values?: SettingsValues
  checkedAt?: number
}>
export type SettingsReview = Readonly<{
  selection: ResourceSelection
  settings: TransferSettings
  previous?: CurrentObservation
  preview: LocalPreview | ManagementPreview
  contextEpoch: number
}>
export type SettingsAttempt = Readonly<{
  attemptId: string
  selection: ResourceSelection
  operationId: string
  // A rejected local revision conflict is definite pre-admission evidence,
  // not an invented provider outcome or a successful cancellation.
  dispatch: 'sent' | 'rejected'
  evidence?: OperationEvidence
  statusObservation: 'not_checked' | 'confirmed' | 'unavailable' | 'unconfirmed'
  checkedAt?: number
}>
export type ResourceSnapshot = Readonly<{
  opened: boolean; authenticated: boolean; available: boolean
  peers: readonly SavedManagementPeer[]; managedDirectLAN: boolean
  resources: readonly LocalDescriptor[] | null
  selection: ResourceSelection | null
  current: CurrentObservation
  draft: TransferSettings | null
  review: SettingsReview | null
  attempts: readonly SettingsAttempt[]
  busy: 'list' | 'inspect' | 'preview' | 'apply' | 'status' | null
  notice: ResourceNotice
  blocked: boolean
  evidenceLimitReached: boolean
}>
type Pending = { controller: AbortController; generation: number; epoch: number; attemptKey?: string }
type RetainedAttempt = SettingsAttempt & Readonly<{ review: SettingsReview }>
const unread: CurrentObservation = Object.freeze({ state: 'unread' })
const initial: ResourceSnapshot = Object.freeze({
  opened: false, authenticated: false, available: false, peers: Object.freeze([]), managedDirectLAN: false,
  resources: null, selection: null, current: unread, draft: null, review: null,
  attempts: Object.freeze([]), busy: null, notice: null, blocked: false, evidenceLimitReached: false,
})
export function sameScope(a: ResourceSelection, b: ResourceSelection): boolean {
  if (a.kind === 'local' && b.kind === 'local') return a.target.resourceId === b.target.resourceId
  return a.kind === 'remote' && b.kind === 'remote' && a.peerKey === b.peerKey
    && a.selector.protocolVersion === b.selector.protocolVersion
    && a.selector.target.resourceId === b.selector.target.resourceId && a.selector.grantId === b.selector.grantId
}
export function unresolved(attempt: SettingsAttempt): boolean {
  return attempt.dispatch === 'sent' && (!attempt.evidence || attempt.evidence.outcome.status === 'unknown' || !attempt.evidence.evidenceDurable)
}
function selectionKey(selection: ResourceSelection): string {
  return selection.kind === 'local' ? `local:${selection.target.resourceId}`
    : `remote:${selection.peerKey}:${selection.selector.protocolVersion}:${selection.selector.target.resourceId}:${selection.selector.grantId}`
}
function attemptKey(selection: ResourceSelection, preview: LocalPreview | ManagementPreview): string {
  return `${selectionKey(selection)}:${preview.operationId}:${'revision' in preview ? preview.revision : preview.reviewRevision}`
}
function managementSelector(selection: ResourceSelection): RemoteSelector<2> {
  if (selection.kind !== 'remote' || selection.selector.protocolVersion !== 2) throw new Error('Management selection required')
  return Object.freeze({ ...selection.selector, protocolVersion: 2 })
}
function currentValues(values: SettingsValues, now: number): CurrentObservation {
  return Object.freeze({ state: 'observed', values: Object.freeze({ requested: values.requested, effective: values.effective }), checkedAt: now })
}

// Own one instance above the modal, for the authenticated app lifetime. No
// timers, effects, browser storage, URLs, queues, retries or automatic writes.
export class ResourceSettingsController {
  private snapshot: ResourceSnapshot = initial
  private readonly listeners = new Set<() => void>()
  private readonly retained = new Map<string, RetainedAttempt>()
  private pending: Pending | null = null
  private epoch = 0
  private generation = 0
  private contextKey = ''
  constructor(private readonly transport: ResourceTransport, private readonly handleError: (error: unknown) => void, private readonly now = Date.now, private readonly sharedAttempts = new ResourceAttemptRegistry()) {
    sharedAttempts.subscribe(() => {
      const selection = this.snapshot.selection
      const blocked = selection?.kind === 'remote' && sharedAttempts.blocked(this.remoteScope(selection))
      if (blocked && (this.snapshot.review || this.snapshot.busy === 'preview')) {
        this.interrupt(); this.publish({ review: null, busy: null })
      } else this.publish()
    })
  }
  private remoteScope(selection: Extract<ResourceSelection, { kind: 'remote' }>): AttemptScope { return { peerKey: selection.peerKey, resourceId: selection.selector.target.resourceId } }
  private sharedBlocked(selection: ResourceSelection | null): boolean { return selection?.kind === 'remote' && (this.sharedAttempts.full || this.sharedAttempts.blocked(this.remoteScope(selection))) || false }
  getSnapshot = (): ResourceSnapshot => this.snapshot
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }

  private publish(update: Partial<ResourceSnapshot> = {}) {
    const next = { ...this.snapshot, ...update }
    const attempts = next.authenticated && next.opened && next.selection
      ? [...this.retained.values()].filter(item => sameScope(item.selection, next.selection!)).map(({ review: _review, ...item }) => Object.freeze(item)) : []
    next.attempts = Object.freeze(attempts)
    next.evidenceLimitReached = this.retained.size >= MAX_RESOURCE_ATTEMPTS
    next.blocked = attempts.some(unresolved) || next.evidenceLimitReached || this.sharedBlocked(next.selection)
    this.snapshot = Object.freeze(next)
    this.listeners.forEach(listener => listener())
  }
  private interrupt() {
    ++this.generation
    const pending = this.pending
    this.pending = null
    pending?.controller.abort()
    if (pending?.attemptKey) {
      const attempt = this.retained.get(pending.attemptKey)
      if (attempt) this.retained.set(pending.attemptKey, Object.freeze({ ...attempt, statusObservation: 'unconfirmed' }))
    }
  }
  updateContext(context: ResourceContext) {
    const processId = Number.isSafeInteger(context.processId) && context.processId! > 0 ? context.processId : null
    // Copy only saved public keys and display names. Do not import incoming
    // grants, endpoints, trust, or service permission into the selector list.
    let peers: readonly SavedManagementPeer[] = []
    try {
      if (!context.authenticated || context.peers.length > 256) throw new Error('Authenticated bounded saved-peer selection required')
      const keys = new Set<string>()
      peers = Object.freeze(context.peers.map(peer => {
        const key = readDigest(peer.key)
        if (keys.has(key) || typeof peer.name !== 'string' || peer.name.length > 256) throw new Error('Invalid saved peer')
        keys.add(key)
        return Object.freeze({ key, name: peer.name })
      }))
    } catch { peers = Object.freeze([]) }
    const key = JSON.stringify([context.authenticated, processId, context.selectionRevision, context.managedDirectLAN, peers.map(peer => peer.key).sort()])
    if (key === this.contextKey) {
      if (JSON.stringify(peers) !== JSON.stringify(this.snapshot.peers)) this.publish({ peers })
      return
    }
    this.contextKey = key
    ++this.epoch
    this.interrupt()
    this.publish({ authenticated: context.authenticated, available: context.authenticated && processId !== null,
      managedDirectLAN: context.authenticated && context.managedDirectLAN, peers, resources: null, selection: null, current: unread,
      draft: null, review: null, busy: null, notice: context.authenticated ? 'contextChanged' : null })
  }
  open() { this.publish({ opened: true, notice: null }) }
  close() {
    this.interrupt()
    this.publish({ opened: false, resources: null, selection: null, current: unread, draft: null, review: null, busy: null, notice: null })
  }
  // Navigation/back/forward and every selector edit must call this, even if
  // an edited selector is invalid and cannot yet be selected.
  clearSelection() {
    this.interrupt()
    this.publish({ selection: null, current: unread, draft: null, review: null, busy: null, notice: null })
  }
  select(value: ResourceSelection): boolean {
    this.clearSelection()
    if (!this.ready()) return false
    try {
      const selection = readSelection(value)
      if (selection.kind === 'local') {
        if (!this.snapshot.resources?.some(item => item.resourceId === selection.target.resourceId)) throw new Error('List current local resources first')
      } else if (!this.snapshot.managedDirectLAN || !this.snapshot.peers.some(peer => peer.key === selection.peerKey)) throw new Error('Select an exact saved managed peer')
      this.publish({ selection })
      return true
    } catch { this.publish({ notice: 'invalid' }); return false }
  }
  setDraft(value: TransferSettings | null) {
    this.interrupt()
    let draft: TransferSettings | null = null
    try { if (value !== null) draft = readSettings(value) } catch { /* Invalid choices never reach the provider. */ }
    this.publish({ draft, review: null, busy: null, notice: value !== null && draft === null ? 'invalid' : null })
  }
  backToEdit() { this.interrupt(); this.publish({ review: null, busy: null, notice: null }) }
  stopWaiting() {
    this.interrupt()
    this.publish({ review: null, busy: null, current: this.snapshot.current.values ? Object.freeze({ ...this.snapshot.current, state: 'unconfirmed' }) : unread })
  }
  private ready() { return this.snapshot.opened && this.snapshot.authenticated && this.snapshot.available }
  private begin(busy: NonNullable<ResourceSnapshot['busy']>, attemptKey?: string): Pending | null {
    if (!this.ready() || this.pending) return null
    const pending = { controller: new AbortController(), generation: ++this.generation, epoch: this.epoch, attemptKey }
    this.pending = pending
    this.publish({ busy, notice: null, ...(busy === 'apply' ? { review: null } : {}) })
    return pending
  }
  private owns(pending: Pending) { return this.pending === pending && pending.epoch === this.epoch && pending.generation === this.generation && this.ready() }
  private finish(pending: Pending) { if (this.owns(pending)) { this.pending = null; this.publish({ busy: null }) } }
  private report(error: unknown, selection: ResourceSelection | null): ResourceNotice {
    if (error instanceof ApiError && error.code === 'unauthenticated') {
      ++this.epoch; this.interrupt()
      this.publish({ authenticated: false, available: false, peers: Object.freeze([]), managedDirectLAN: false, resources: null, selection: null, current: unread, draft: null, review: null, busy: null, notice: null })
      this.handleError(error)
      return null
    }
    if (error instanceof ApiError && selection?.kind === 'remote') {
      const unsupported = selection.selector.protocolVersion === 1 ? 'resource_remote_unsupported' : 'resource_management_remote_unsupported'
      if (error.code === unsupported) return 'unsupported'
    }
    if (error instanceof ApiError && selection?.kind === 'local' && error.code === 'resource_revision_conflict') return 'conflict'
    // Do not display provider text, or turn generic remote errors into an
    // invented cause such as offline, revoked, not found, or revision conflict.
    return 'unavailable'
  }
  async listLocal(): Promise<void> {
    if (!this.ready() || this.pending) return
    this.clearSelection()
    const pending = this.begin('list')
    if (!pending || !this.owns(pending)) return
    try {
      const catalog = readLocalCatalog(await this.transport('resource.list', {}, pending.controller.signal))
      if (this.owns(pending)) this.publish({ resources: catalog.resources })
    } catch (error) { if (this.owns(pending)) this.publish({ resources: null, notice: this.report(error, null) }) }
    finally { this.finish(pending) }
  }
  async inspect(): Promise<void> {
    const selection = this.snapshot.selection
    if (!selection) return
    const pending = this.begin('inspect')
    if (!pending || !this.owns(pending)) return
    this.publish({ review: null })
    if (!this.owns(pending)) return
    try {
      let values: SettingsValues
      if (selection.kind === 'local') {
        values = readLocalDescriptor(await this.transport('resource.inspect', selection.target, pending.controller.signal), selection.target)
      } else if (selection.selector.protocolVersion === 1) {
        const request = Object.freeze({ ...selection.selector, protocolVersion: 1 as const })
        values = readRemoteInspection(await this.transport('resource.remote.inspect', { peerKey: selection.peerKey, request }, pending.controller.signal), request)
      } else {
        const request = Object.freeze({ ...managementSelector(selection), action: 'inspect' as const })
        const reply = readManagementReply(await this.transport('resource.remote.management.inspect', { peerKey: selection.peerKey, request, confirm: false }, pending.controller.signal), request)
        if (reply.action !== 'inspect') throw new Error('Unexpected action')
        values = reply.inspection
      }
      if (this.owns(pending)) this.publish({ current: currentValues(values, this.now()), draft: values.requested })
    } catch (error) {
      if (this.owns(pending)) {
        const notice = this.report(error, selection)
        if (this.owns(pending)) this.publish({ current: Object.freeze({ ...this.snapshot.current, state: 'unconfirmed' }), notice })
      }
    } finally { this.finish(pending) }
  }
  async preview(): Promise<void> {
    const selection = this.snapshot.selection, settings = this.snapshot.draft
    if (!selection || !settings || this.sharedBlocked(selection) || this.snapshot.blocked || selection.kind === 'remote' && selection.selector.protocolVersion !== 2) return
    const pending = this.begin('preview')
    if (!pending || !this.owns(pending)) return
    this.publish({ review: null })
    if (!this.owns(pending)) return
    try {
      let preview: LocalPreview | ManagementPreview
      if (selection.kind === 'local') {
        preview = readLocalPreview(await this.transport('resource.preview', { ...selection.target, settings }, pending.controller.signal), selection.target, settings)
      } else {
        const request = Object.freeze({ ...managementSelector(selection), action: 'preview' as const, preview: Object.freeze({ settings }) })
        const reply = readManagementReply(await this.transport('resource.remote.management.preview', { peerKey: selection.peerKey, request, confirm: false }, pending.controller.signal), request)
        if (reply.action !== 'preview') throw new Error('Unexpected action')
        preview = reply.preview
      }
      if (this.owns(pending) && !this.sharedBlocked(selection)) {
        if (this.retained.has(attemptKey(selection, preview)) || [...this.retained.values()].some(attempt => sameScope(attempt.selection, selection) && attempt.operationId === preview.operationId && attempt.dispatch !== 'rejected')) { this.publish({ notice: 'unavailable' }); return }
        this.publish({ review: Object.freeze({ selection, settings, previous: this.snapshot.current, preview, contextEpoch: this.epoch }) })
      }
    } catch (error) { if (this.owns(pending)) this.publish({ notice: this.report(error, selection) }) }
    finally { this.finish(pending) }
  }
  async confirmApply(): Promise<void> {
    const review = this.snapshot.review, selection = this.snapshot.selection
    if (!review || !selection || !this.ready() || this.pending || this.snapshot.blocked || this.sharedBlocked(selection) || review.contextEpoch !== this.epoch
      || review.selection !== selection || !this.snapshot.draft || !sameSettings(review.settings, this.snapshot.draft)) return
    const key = attemptKey(selection, review.preview)
    if (this.retained.has(key) || this.retained.size >= MAX_RESOURCE_ATTEMPTS) { this.publish({ review: null, notice: 'limit' }); return }
    // Shared peer/resource inhibition ignores grant and auth generations.
    // Reservation performs no notification; pending/review state is installed
    // before either owner or registry subscribers can reenter this controller.
    if (selection.kind === 'remote' && !this.sharedAttempts.reserveNew(`single:${key}`, [{ ...this.remoteScope(selection), operationId: review.preview.operationId }])) { this.publish({ review: null, notice: 'limit' }); return }
    // Consume review, retain immutable request evidence, and reserve the busy
    // slot synchronously before calling even a synchronously throwing adapter.
    this.retained.set(key, Object.freeze({ attemptId: key, selection, review, operationId: review.preview.operationId, dispatch: 'sent', statusObservation: 'not_checked' }))
    const pending = this.begin('apply', key)
    if (selection.kind === 'remote') this.sharedAttempts.emit()
    if (!pending || !this.owns(pending)) return
    this.publish({ review: null, current: this.snapshot.current.values ? Object.freeze({ ...this.snapshot.current, state: 'unconfirmed' }) : unread })
    // Subscribers run synchronously during both notifications above. A close,
    // auth/selection change, or stop in either must prevent transport dispatch.
    // Keep the conservative retained identity; never turn this into a retry.
    if (!this.owns(pending)) return
    try {
      let evidence: OperationEvidence
      let current: CurrentObservation | undefined
      if (selection.kind === 'local') {
        if (!('revision' in review.preview)) throw new Error('Local review required')
        const request: LocalApply = Object.freeze({ ...selection.target, operationId: review.preview.operationId, baseRevision: review.preview.baseRevision, revision: review.preview.revision, settings: review.settings })
        const result = readLocalOperation(await this.transport('resource.apply', request, pending.controller.signal), request)
        evidence = Object.freeze({ operationId: result.operationId, outcome: result.outcome, evidenceDurable: result.evidenceDurable })
        current = currentValues(result.current, this.now())
      } else {
        if (!('reviewRevision' in review.preview)) throw new Error('Remote review required')
        const apply: ManagementApply = Object.freeze({ operationId: review.preview.operationId, baseRevision: review.preview.baseRevision, reviewRevision: review.preview.reviewRevision, settings: review.settings })
        const request = Object.freeze({ ...managementSelector(selection), action: 'apply' as const, apply })
        const reply = readManagementReply(await this.transport('resource.remote.management.apply', { peerKey: selection.peerKey, request, confirm: true }, pending.controller.signal), request)
        if (reply.action !== 'apply') throw new Error('Unexpected action')
        evidence = reply.operation
      }
      if (this.owns(pending)) {
        this.retained.set(key, Object.freeze({ ...this.retained.get(key)!, evidence, statusObservation: 'confirmed', checkedAt: this.now() }))
        if (selection.kind === 'remote') this.sharedAttempts.settleTarget(`single:${key}`, this.remoteScope(selection), evidence)
        this.publish(current ? { current } : {})
        if (selection.kind === 'remote') this.sharedAttempts.emit()
      }
    } catch (error) {
      if (this.owns(pending)) {
        const attempt = this.retained.get(key)!
        const rejected = selection.kind === 'local' && error instanceof ApiError && error.code === 'resource_revision_conflict'
        this.retained.set(key, Object.freeze({ ...attempt, dispatch: rejected ? 'rejected' : 'sent', statusObservation: 'unconfirmed' }))
        this.publish({ notice: this.report(error, selection) })
      }
    } finally { this.finish(pending) }
  }
  async checkStatus(attemptId: string): Promise<void> {
    const selection = this.snapshot.selection
    if (!selection) return
    const key = attemptId, attempt = this.retained.get(key)
    if (!attempt || attempt.dispatch === 'rejected' || !sameScope(attempt.selection, selection) || selection.kind === 'remote' && selection.selector.protocolVersion !== 2) return
    const operationId = attempt.operationId
    const pending = this.begin('status', key)
    if (!pending || !this.owns(pending)) return
    this.publish({ review: null })
    if (!this.owns(pending)) return
    try {
      let evidence: OperationEvidence | undefined
      let current: CurrentObservation | undefined
      if (selection.kind === 'local') {
        const request = Object.freeze({ ...selection.target, operationId })
        const reply = readLocalOperation(await this.transport('resource.operation.status', request, pending.controller.signal), request)
        evidence = Object.freeze({ operationId: reply.operationId, outcome: reply.outcome, evidenceDurable: reply.evidenceDurable })
        current = currentValues(reply.current, this.now())
      } else {
        // Explicit current selection may carry a newer revision for the same
        // grant identity. Never rewrite the original apply selector/review.
        const request: Extract<ManagementRequest, { action: 'operation.status' }> = Object.freeze({ ...managementSelector(selection), action: 'operation.status', status: Object.freeze({ operationId }) })
        const reply = readManagementReply(await this.transport('resource.remote.management.operation.status', { peerKey: selection.peerKey, request, confirm: false }, pending.controller.signal), request)
        if (reply.action !== 'operation.status') throw new Error('Unexpected action')
        evidence = reply.operation
      }
      if (this.owns(pending)) {
        // A failed, unavailable, or regressive later read cannot erase a
        // previously observed terminal result. Treat contradictory evidence as
        // an unconfirmed read, not as a different historical operation outcome.
        if (evidence && attempt.evidence?.evidenceDurable && attempt.evidence.outcome.status !== 'unknown'
          && (!evidence.evidenceDurable || JSON.stringify(evidence.outcome) !== JSON.stringify(attempt.evidence.outcome))) throw new Error('Contradictory operation evidence')
        this.retained.set(key, Object.freeze({ ...attempt, ...(evidence ? { evidence } : {}), statusObservation: evidence ? 'confirmed' : 'unavailable', checkedAt: this.now() }))
        if (evidence && selection.kind === 'remote') this.sharedAttempts.settleTarget(`single:${key}`, this.remoteScope(selection), evidence)
        this.publish({ ...(current ? { current } : {}), notice: evidence ? null : 'evidenceUnavailable' })
        if (selection.kind === 'remote') this.sharedAttempts.emit()
      }
    } catch (error) {
      if (this.owns(pending)) {
        this.retained.set(key, Object.freeze({ ...attempt, statusObservation: 'unconfirmed' }))
        this.publish({ notice: this.report(error, selection) })
      }
    } finally { this.finish(pending) }
  }
}
