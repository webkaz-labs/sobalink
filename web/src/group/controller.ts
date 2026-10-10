import { ApiError } from '../api'
import { ResourceAttemptRegistry, type AttemptClaim } from '../resource/attempt-registry'
import { parseSettingsInput, settingsInput, choiceInput, type ChoiceInput, type SelectorInput } from '../resource/input'
import type { RemoteSelection } from '../resource/types'
import { createTemplate, groupID, readCurrentReviewInput, readApplyInput, readCancelInput, readExecutionPeers, readPreviewInput, readRefreshInput, readSelectInput, readStatusInput } from './canonical'
import { isRefreshEligible, readCancelView, readCurrentReviewView, readPreviewView, readRunView, readSelectedView } from './decode'
import type { GroupContext } from './context'
import { copyChoice, emptyGroupDraft, newMemberInput, parseMemberInput, selectorInput, type GroupDraft, type MemberInput, type OverrideInput, type SettingField } from './input'
import type { GroupReview, GroupTransport, PreparedView, PreviewInput, RunView, SelectInput } from './types'

export const MAX_GROUP_ATTEMPTS = 16
export type GroupNotice = 'invalid' | 'unavailable' | 'contextChanged' | 'limit' | 'blocked' | 'preparationUnknown' | 'statusUnavailable' | 'noUnusedReview' | null
export type GroupSnapshot = Readonly<{
  opened: boolean; context: GroupContext; draft: GroupDraft; prepared: PreparedView | null
  executionPeers: readonly string[]; confirmed: boolean; run: RunView | null; runIdInput: string
  retainedRunIds: readonly string[]; refreshPeers: readonly string[]
  busy: 'preview' | 'select' | 'apply' | 'status' | 'refresh' | 'cancel' | 'current' | null
  notice: GroupNotice; blocked: boolean; preparationUnknown: boolean; recoveryAvailable: boolean; subsetRecoveryAvailable: boolean; capacityReached: boolean
}>
type Pending = { controller: AbortController; generation: number; contextRevision: string }
type Attempt = { review: GroupReview; run?: RunView; contextRevision: string }
const emptyContext: GroupContext = Object.freeze({ localAvailable: false, remoteAvailable: false, revision: '', localPeerKey: null, peers: Object.freeze([]) })
const initial = (): GroupSnapshot => Object.freeze({ opened: false, context: emptyContext, draft: emptyGroupDraft(), prepared: null,
  executionPeers: Object.freeze([]), confirmed: false, run: null, runIdInput: '', retainedRunIds: Object.freeze([]), refreshPeers: Object.freeze([]),
  busy: null, notice: null, blocked: false, preparationUnknown: false, recoveryAvailable: false, subsetRecoveryAvailable: false, capacityReached: false })
function reviewClaims(review: GroupReview): readonly AttemptClaim[] {
  return Object.freeze(review.rows.filter(row => review.executionPeers.includes(row.peerKey)).map(row => {
    if (row.state !== 'ready') throw new Error('Executable row required')
    return Object.freeze({ peerKey: row.peerKey, resourceId: row.reply.target.resourceId, operationId: row.reply.preview.operationId })
  }))
}
function draftFromReview(review: GroupReview): GroupDraft {
  return Object.freeze({ template: settingsInput(review.selection.template.settings), members: Object.freeze(review.selection.members.map(member => Object.freeze({
    ...newMemberInput(member.peerKey), selector: selectorInput({ kind: 'remote', peerKey: member.peerKey, selector: member.selector }),
    transferConcurrentFiles: member.override?.transferConcurrentFiles ? choiceInput(member.override.transferConcurrentFiles) : Object.freeze({ mode: 'inherit' as const, value: '' }),
    transferConcurrentPerPeer: member.override?.transferConcurrentPerPeer ? choiceInput(member.override.transferConcurrentPerPeer) : Object.freeze({ mode: 'inherit' as const, value: '' }),
  }))) })
}
// One app-lifetime owner. Observation, subscriptions and modal lifecycle never
// dispatch transport. All writes are explicit, single-shot local commands.
export class ResourceGroupController {
  private snapshot = initial()
  private readonly listeners = new Set<() => void>()
  private readonly attempts = new Map<string, Attempt>()
  private pending: Pending | null = null
  private generation = 0
  private knownPreparedId: string | null = null
  private recoveryInput: PreviewInput | null = null
  private selectRecovery: Readonly<{ previous: PreparedView; input: SelectInput }> | null = null
  constructor(private readonly transport: GroupTransport, private readonly handleError: (error: unknown) => void, private readonly registry: ResourceAttemptRegistry) {
    registry.subscribe(() => {
      if (this.draftBlocked() && (this.snapshot.prepared || this.snapshot.busy === 'preview' || this.snapshot.busy === 'select')) {
        this.interrupt(); this.publish({ prepared: null, executionPeers: Object.freeze([]), confirmed: false, busy: null, notice: 'blocked' })
      } else this.publish()
    })
  }
  getSnapshot = () => this.snapshot
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  private draftBlocked() {
    return this.snapshot.draft.members.some(member => { const parsed = parseMemberInput(member); return parsed && this.registry.blocked({ peerKey: parsed.peerKey, resourceId: parsed.selector.target.resourceId }) })
  }
  private publish(update: Partial<GroupSnapshot> = {}) {
    this.snapshot = Object.freeze({ ...this.snapshot, ...update })
    const visible = this.snapshot.opened && this.snapshot.context.localAvailable
    this.snapshot = Object.freeze({ ...this.snapshot, blocked: this.draftBlocked(), capacityReached: this.attempts.size >= MAX_GROUP_ATTEMPTS || this.registry.full,
      recoveryAvailable: visible && this.recoveryInput !== null, subsetRecoveryAvailable: visible && this.selectRecovery !== null,
      retainedRunIds: Object.freeze(visible ? [...this.attempts].filter(([, attempt]) => attempt.contextRevision === this.snapshot.context.revision).map(([id]) => id) : []),
    })
    this.listeners.forEach(listener => listener())
  }
  private interrupt() { ++this.generation; const pending = this.pending; this.pending = null; pending?.controller.abort() }
  private ready(remote = false) { return this.snapshot.opened && this.snapshot.context.localAvailable && (!remote || this.snapshot.context.remoteAvailable) }
  private owns(pending: Pending) { return this.pending === pending && pending.generation === this.generation && pending.contextRevision === this.snapshot.context.revision && this.ready() }
  private begin(busy: NonNullable<GroupSnapshot['busy']>, update: Partial<GroupSnapshot> = {}): Pending | null {
    if (!this.ready() || this.pending) return null
    const pending = { controller: new AbortController(), generation: ++this.generation, contextRevision: this.snapshot.context.revision }
    this.pending = pending; this.publish({ ...update, busy, notice: null, confirmed: false }); return pending
  }
  private finish(pending: Pending) { if (this.owns(pending)) { this.pending = null; this.publish({ busy: null }) } }
  private report(pending: Pending, error: unknown, notice: GroupNotice) {
    if (!this.owns(pending)) return
    if (error instanceof ApiError && error.code === 'unauthenticated') this.handleError(error)
    if (this.owns(pending)) this.publish({ notice })
  }
  updateContext(context: GroupContext) {
    // Fixed internal projection supplies these data; never accept external
    // callbacks, inferred grants, labels as keys, or an observer transport.
    if (context.revision === this.snapshot.context.revision) { if (JSON.stringify(context.peers) !== JSON.stringify(this.snapshot.context.peers)) this.publish({ context }); return }
    this.interrupt(); this.knownPreparedId = null; this.recoveryInput = null; this.selectRecovery = null
    this.publish({ ...initial(), opened: this.snapshot.opened, context, notice: context.localAvailable ? 'contextChanged' : null })
  }
  open() { this.publish({ opened: true, notice: null }) }
  close() {
    const preparationUnknown = this.snapshot.preparationUnknown || this.snapshot.busy === 'preview' || this.snapshot.busy === 'select'
    this.interrupt()
    // Private attempts and registry claims survive modal/auth/navigation loss.
    this.publish({ preparationUnknown, opened: false, prepared: null, confirmed: false, executionPeers: Object.freeze([]), run: null, runIdInput: '', refreshPeers: Object.freeze([]), busy: null, notice: null })
  }
  stopWaiting() {
    if (!this.pending) return
    const preparationUnknown = this.snapshot.preparationUnknown || this.snapshot.busy === 'preview' || this.snapshot.busy === 'select'
    this.interrupt(); this.publish({ busy: null, prepared: null, confirmed: false, preparationUnknown, notice: preparationUnknown ? 'preparationUnknown' : 'statusUnavailable' })
  }
  private edit(draft: GroupDraft) {
    if (!this.ready(true) || this.pending || this.snapshot.preparationUnknown) return false
    this.interrupt(); this.recoveryInput = null; this.selectRecovery = null
    this.publish({ draft: Object.freeze(draft), prepared: null, confirmed: false, executionPeers: Object.freeze([]), notice: null }); return true
  }
  addPeer(peerKey: string): boolean {
    if (!this.snapshot.context.peers.some(peer => peer.key === peerKey) || peerKey === this.snapshot.context.localPeerKey || this.snapshot.draft.members.length >= 16 || this.snapshot.draft.members.some(member => member.peerKey === peerKey)) return false
    return this.edit({ ...this.snapshot.draft, members: Object.freeze([...this.snapshot.draft.members, newMemberInput(peerKey)].sort((a, b) => a.peerKey.localeCompare(b.peerKey))) })
  }
  addSelection(selection: RemoteSelection): boolean {
    if (selection.selector.protocolVersion !== 2 || !this.snapshot.context.peers.some(peer => peer.key === selection.peerKey)) return false
    const existing = this.snapshot.draft.members.find(member => member.peerKey === selection.peerKey)
    if (!existing && this.snapshot.draft.members.length >= 16) return false
    const member = Object.freeze({ ...(existing ?? newMemberInput(selection.peerKey)), selector: selectorInput(selection) })
    if (!parseMemberInput(member)) return false
    return this.edit({ ...this.snapshot.draft, members: Object.freeze([...this.snapshot.draft.members.filter(item => item.peerKey !== selection.peerKey), member].sort((a, b) => a.peerKey.localeCompare(b.peerKey))) })
  }
  removePeer(peerKey: string) { return this.edit({ ...this.snapshot.draft, members: Object.freeze(this.snapshot.draft.members.filter(member => member.peerKey !== peerKey)) }) }
  setTemplate(field: SettingField, value: ChoiceInput) { return this.edit({ ...this.snapshot.draft, template: Object.freeze({ ...this.snapshot.draft.template, [field]: copyChoice(value) }) }) }
  private editMember(peerKey: string, update: (member: MemberInput) => MemberInput) { return this.edit({ ...this.snapshot.draft, members: Object.freeze(this.snapshot.draft.members.map(member => member.peerKey === peerKey ? Object.freeze(update(member)) : member)) }) }
  setSelector(peerKey: string, value: SelectorInput) { return this.editMember(peerKey, member => ({ ...member, selector: Object.freeze({ ...value, peerKey, protocol: '2' }) })) }
  setOverride(peerKey: string, field: SettingField, value: OverrideInput) { return this.editMember(peerKey, member => ({ ...member, [field]: Object.freeze({ mode: value.mode, value: value.value }) })) }
  async preview() {
    if (!this.ready(true) || this.pending || this.snapshot.preparationUnknown || this.draftBlocked() || this.snapshot.capacityReached) return
    const draft = this.snapshot.draft, settings = parseSettingsInput(draft.template), members = draft.members.map(parseMemberInput)
    if (!settings || !members.length || members.some(member => !member) || members.some(member => !this.snapshot.context.peers.some(peer => peer.key === member!.peerKey))) { this.publish({ notice: 'invalid' }); return }
    const replaceReviewId = this.knownPreparedId, localPeer = this.snapshot.context.localPeerKey
    const pending = this.begin('preview', { prepared: null, executionPeers: Object.freeze([]) }); if (!pending || !this.owns(pending)) return
    try {
      const template = await createTemplate(settings); if (!this.owns(pending)) return
      const input = await readPreviewInput({ schemaVersion: 1, selection: { schemaVersion: 1, template, members }, ...(replaceReviewId ? { replaceReviewId } : {}) }, localPeer ?? undefined)
      if (!this.owns(pending) || this.draftBlocked()) return
      this.recoveryInput = input
      await this.previewRequest(pending, input)
    } catch (error) { this.report(pending, error, this.recoveryInput ? 'preparationUnknown' : 'invalid'); if (this.owns(pending) && this.recoveryInput) this.publish({ preparationUnknown: true }) }
    finally { this.finish(pending) }
  }
  private async previewRequest(pending: Pending, input: PreviewInput) {
    if (!this.owns(pending) || this.draftBlocked()) return
    const raw = await this.transport('resource.group.preview', input, pending.controller.signal); if (!this.owns(pending)) return
    const prepared = await readPreviewView(raw, input); if (!this.owns(pending) || this.draftBlocked()) return
    this.knownPreparedId = prepared.reviewId; this.recoveryInput = null; this.selectRecovery = null
    this.publish({ prepared, preparationUnknown: false, executionPeers: prepared.review.executionPeers, notice: null })
  }
  async recoverPreview() {
    const input = this.recoveryInput
    if (!input || !this.ready(true) || this.pending || this.draftBlocked()) return
    const pending = this.begin('preview'); if (!pending || !this.owns(pending)) return
    try { await this.previewRequest(pending, input) } catch (error) { this.report(pending, error, 'preparationUnknown'); if (this.owns(pending)) this.publish({ preparationUnknown: true }) } finally { this.finish(pending) }
  }
  setExecutionPeers(value: readonly string[]) {
    if (!this.ready(true) || this.pending || !this.snapshot.prepared || this.snapshot.preparationUnknown) return
    try { const peers = readExecutionPeers(value); if (peers.some(peer => !this.snapshot.prepared!.review.rows.some(row => row.peerKey === peer && row.state === 'ready'))) return; this.publish({ executionPeers: peers, confirmed: false }) } catch { this.publish({ notice: 'invalid' }) }
  }
  async selectExecution() {
    const previous = this.snapshot.prepared
    if (!previous || !this.ready(true) || this.pending || this.snapshot.preparationUnknown || this.draftBlocked()) return
    const peers = this.snapshot.executionPeers, input = readSelectInput({ schemaVersion: 1, reviewId: previous.reviewId, reviewRevision: previous.review.revision, executionPeers: peers })
    this.selectRecovery = Object.freeze({ previous, input })
    const pending = this.begin('select', { prepared: null }); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.review.select', input, pending.controller.signal); if (!this.owns(pending)) return
      const prepared = await readSelectedView(raw, previous, peers); if (!this.owns(pending) || this.draftBlocked()) return
      this.knownPreparedId = prepared.reviewId; this.recoveryInput = null; this.selectRecovery = null
      this.publish({ prepared, executionPeers: prepared.review.executionPeers, preparationUnknown: false })
    } catch (error) { this.report(pending, error, 'preparationUnknown'); if (this.owns(pending)) this.publish({ preparationUnknown: true }) } finally { this.finish(pending) }
  }
  async recoverSubset() {
    const recovery = this.selectRecovery
    if (!recovery || !this.ready(true) || this.pending || this.draftBlocked()) return
    const pending = this.begin('select', { prepared: null }); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.review.select', recovery.input, pending.controller.signal); if (!this.owns(pending)) return
      const prepared = await readSelectedView(raw, recovery.previous, recovery.input.executionPeers); if (!this.owns(pending) || this.draftBlocked()) return
      this.knownPreparedId = prepared.reviewId; this.selectRecovery = null; this.recoveryInput = null
      this.publish({ prepared, executionPeers: prepared.review.executionPeers, preparationUnknown: false })
    } catch (error) { this.report(pending, error, 'preparationUnknown') } finally { this.finish(pending) }
  }
  async currentReview() {
    if (!this.ready() || this.pending) return
    const pending = this.begin('current', { prepared: null }); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.review.current', readCurrentReviewInput({ schemaVersion: 1 }), pending.controller.signal); if (!this.owns(pending)) return
      const result = await readCurrentReviewView(raw); if (!this.owns(pending)) return
      this.recoveryInput = null; this.selectRecovery = null
      if (result.state === 'none') {
        this.knownPreparedId = null
        this.publish({ prepared: null, preparationUnknown: false, notice: 'noUnusedReview', executionPeers: Object.freeze([]) }); return
      }
      const prepared = result.prepared
      // A current-read cannot resurrect an already consumed browser attempt.
      if (this.attempts.has(prepared.reviewId)) { this.publish({ notice: 'blocked' }); return }
      this.knownPreparedId = prepared.reviewId
      const draft = draftFromReview(prepared.review)
      this.publish({ draft, prepared, preparationUnknown: false, executionPeers: prepared.review.executionPeers, confirmed: false })
    } catch (error) { this.report(pending, error, 'unavailable') } finally { this.finish(pending) }
  }
  canConfirm() {
    const { prepared, executionPeers, preparationUnknown } = this.snapshot
    return this.ready(true) && !this.pending && !preparationUnknown && !this.draftBlocked() && !this.snapshot.capacityReached
      && prepared?.admissionState === 'prepared' && !this.attempts.has(prepared.reviewId)
      && prepared.review.selection.members.every(member => member.peerKey !== this.snapshot.context.localPeerKey && this.snapshot.context.peers.some(peer => peer.key === member.peerKey)) && prepared.review.executionPeers.length > 0 && JSON.stringify(executionPeers) === JSON.stringify(prepared.review.executionPeers)
  }
  setConfirmed(value: boolean) { this.publish({ confirmed: value === true && this.canConfirm() }) }
  async apply() {
    const prepared = this.snapshot.prepared
    if (!prepared || !this.snapshot.confirmed || !this.canConfirm()) return
    const input = readApplyInput({ schemaVersion: 1, reviewId: prepared.reviewId, reviewRevision: prepared.review.revision, executionPeers: prepared.review.executionPeers, confirm: true })
    const owner = `group:${prepared.reviewId}`
    if (this.attempts.has(prepared.reviewId) || !this.registry.reserveNew(owner, reviewClaims(prepared.review))) { this.publish({ confirmed: false, notice: 'blocked' }); return }
    // All scopes are reserved before any subscriber can try another dispatch.
    this.attempts.set(prepared.reviewId, { review: prepared.review, contextRevision: this.snapshot.context.revision })
    this.knownPreparedId = null; this.recoveryInput = null; this.selectRecovery = null
    const pending = this.begin('apply', { prepared: null, executionPeers: Object.freeze([]), run: null, runIdInput: prepared.reviewId })
    this.registry.emit()
    if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.apply', input, pending.controller.signal); if (!this.owns(pending)) return
      const run = await readRunView(raw, { runId: prepared.reviewId, review: prepared.review }); if (!this.owns(pending)) return
      this.acceptRun(pending, run)
    } catch (error) { this.report(pending, error, 'statusUnavailable') } finally { this.finish(pending) }
  }
  setRunIdInput(value: string) { if (!this.pending) this.publish({ runIdInput: value.slice(0, 32), run: null, refreshPeers: Object.freeze([]), confirmed: false, notice: null }) }
  private canRetain(id: string) { return this.attempts.has(id) || this.attempts.size < MAX_GROUP_ATTEMPTS && !this.registry.full }
  private acceptRun(pending: Pending, run: RunView) {
    if (!this.owns(pending) || !this.canRetain(run.runId)) return false
    const claims = reviewClaims(run.review), owner = `group:${run.runId}`
    if (!this.registry.retainHistory(owner, claims)) { this.publish({ notice: 'limit' }); return false }
    this.attempts.set(run.runId, { review: run.review, run, contextRevision: this.snapshot.context.revision })
    for (const claim of claims) {
      const row = run.evidence.members.find(member => member.peerKey === claim.peerKey)!
      if (row.target) this.registry.settleTarget(owner, claim, row.target)
      this.registry.settleGroupNonDispatch(owner, claim, { runId: run.runId, activity: run.activity, admissionFinished: run.summary.admissionFinished, dispatch: row.dispatch, admissionStop: row.admissionStop, localDurability: run.localDurability, rowLocalDurability: row.localDurability })
    }
    this.publish({ run, runIdInput: run.runId, refreshPeers: Object.freeze([]), notice: null })
    this.registry.emit(); return true
  }
  async status(id = this.snapshot.runIdInput) {
    if (!this.ready() || this.pending) return
    let runId: string; try { runId = groupID(id) } catch { this.publish({ notice: 'invalid' }); return }
    if (!this.canRetain(runId)) { this.publish({ notice: 'limit' }); return }
    const attempt = this.attempts.get(runId), pending = this.begin('status'); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.status', readStatusInput({ schemaVersion: 1, runId }), pending.controller.signal); if (!this.owns(pending)) return
      const run = await readRunView(raw, { runId, review: attempt?.review, previous: attempt?.run }); if (!this.owns(pending)) return
      this.acceptRun(pending, run)
    } catch (error) { this.report(pending, error, 'statusUnavailable') } finally { this.finish(pending) }
  }
  setRefreshPeers(value: readonly string[]) {
    if (this.pending || !this.snapshot.run) return
    try { const peers = readExecutionPeers(value); if (peers.some(peer => !this.snapshot.run!.evidence.members.some(row => row.peerKey === peer && isRefreshEligible(row)))) return; this.publish({ refreshPeers: peers }) } catch { this.publish({ notice: 'invalid' }) }
  }
  async refreshStatus() {
    const previous = this.snapshot.run
    if (!previous || !this.ready() || this.pending || !this.snapshot.refreshPeers.length) return
    const input = readRefreshInput({ schemaVersion: 1, runId: previous.runId, peers: this.snapshot.refreshPeers })
    if (input.peers.some(peer => !previous.evidence.members.some(row => row.peerKey === peer && isRefreshEligible(row)))) return
    const pending = this.begin('refresh'); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.status.refresh', input, pending.controller.signal); if (!this.owns(pending)) return
      const run = await readRunView(raw, { runId: previous.runId, review: previous.review, previous }); if (!this.owns(pending)) return
      this.acceptRun(pending, run)
    } catch (error) { this.report(pending, error, 'statusUnavailable') } finally { this.finish(pending) }
  }
  async cancel() {
    const prepared = this.snapshot.prepared, runId = this.snapshot.run?.runId || this.snapshot.runIdInput
    const id = prepared?.reviewId || (this.attempts.has(runId) ? runId : this.knownPreparedId)
    if (!id || !this.ready()) return
    const attempt = this.attempts.get(id)
    this.interrupt()
    const pending = this.begin('cancel', { prepared: null, confirmed: false }); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport('resource.group.cancel', readCancelInput({ schemaVersion: 1, reviewId: id }), pending.controller.signal); if (!this.owns(pending)) return
      const result = await readCancelView(raw, { reviewId: id, review: prepared?.review ?? attempt?.review, previous: attempt?.run }); if (!this.owns(pending)) return
      if (result.run) this.acceptRun(pending, result.run)
      else { this.knownPreparedId = null; this.recoveryInput = null; this.selectRecovery = null; this.publish({ prepared: result.prepared, preparationUnknown: false }) }
    } catch (error) { this.report(pending, error, attempt ? 'statusUnavailable' : 'preparationUnknown'); if (this.owns(pending) && !attempt) this.publish({ preparationUnknown: true }) } finally { this.finish(pending) }
  }
}
