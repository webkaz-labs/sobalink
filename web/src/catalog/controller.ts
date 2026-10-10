import { ApiError } from '../api'
import { readLocalCatalog } from '../resource/decode'
import type { LocalDescriptor } from '../resource/types'
import type { CatalogContext } from './context'
import { CatalogResponseError, readCatalogRequest, readCatalogResponse } from './decode'
import type { CatalogRequest, CatalogResponse, CatalogTransport, Workflow } from './types'
import { navigation, type Navigation } from './workflow'
export type CatalogNotice = 'invalid' | 'unavailable' | 'limited' | 'contextChanged' | 'selectionRequired' | null
export type CatalogView = Readonly<{ opened: boolean; context: CatalogContext; request: CatalogRequest | null; localResources: readonly LocalDescriptor[] | null; candidate: CatalogResponse | null; observation: 'unread' | 'current' | 'stale'; busy: 'list' | 'snapshot' | null; notice: CatalogNotice }>
const emptyContext: CatalogContext = Object.freeze({ available: false, revision: '', processId: null, managedDirectLAN: false, peers: Object.freeze([]), managedKeys: Object.freeze([]), budget: Object.freeze({ rows: 128, bytes: 1024 * 1024 }) })
export class ResourceCatalogController {
  private view: CatalogView = Object.freeze({ opened: false, context: emptyContext, request: null, localResources: null, candidate: null, observation: 'unread', busy: null, notice: null })
  private listeners = new Set<() => void>()
  private pending: { abort: AbortController; generation: number; context: string } | null = null
  private generation = 0
  constructor(private readonly transport: CatalogTransport, private readonly handleError: (error: unknown) => void) {}
  getSnapshot = () => this.view
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  private publish(update: Partial<CatalogView>) { this.view = Object.freeze({ ...this.view, ...update }); this.listeners.forEach(listener => listener()) }
  private interrupt() { ++this.generation; const pending = this.pending; this.pending = null; pending?.abort.abort() }
  updateContext(context: CatalogContext) {
    if (context.revision === this.view.context.revision) { if (JSON.stringify(context.peers) !== JSON.stringify(this.view.context.peers)) this.publish({ context }); return }
    this.interrupt(); this.publish({ context, request: null, localResources: null, candidate: null, observation: 'unread', busy: null, notice: context.available ? 'contextChanged' : null })
  }
  open() { this.publish({ opened: true, notice: null }) }
  close() { this.interrupt(); this.publish({ opened: false, request: null, localResources: null, candidate: null, observation: 'unread', busy: null, notice: null }) }
  select(value: unknown): boolean {
    this.interrupt(); const generation = this.generation, contextRevision = this.view.context.revision
    this.publish({ request: null, candidate: null, observation: 'unread', busy: null, notice: null })
    if (generation !== this.generation || contextRevision !== this.view.context.revision || !this.view.opened || !this.view.context.available) return false
    try {
      const request = readCatalogRequest(value), context = this.view.context
      for (const source of request.sources) {
        if (source.kind === 'local_settings' && !this.view.localResources?.some(item => item.resourceId === source.target.resourceId)) throw new CatalogResponseError()
        if ((source.kind === 'remote_settings_v1' || source.kind === 'remote_settings_v2') && (!context.managedDirectLAN || !context.managedKeys.includes(source.peerKey))) throw new CatalogResponseError()
        if (source.kind === 'remote_service' && !context.peers.some(peer => peer.id === source.peerId)) throw new CatalogResponseError()
      }
      if (request.sources.some(s => s.kind === 'remote_service') && request.sources.some(s => s.kind === 'remote_settings_v1' || s.kind === 'remote_settings_v2') && !context.managedDirectLAN) throw new CatalogResponseError()
      this.publish({ request }); return true
    } catch { this.publish({ notice: 'selectionRequired' }); return false }
  }
  invalidateSelection() { this.interrupt(); this.publish({ request: null, candidate: null, observation: 'unread', busy: null, notice: null }) }
  stopWaiting() { this.interrupt(); this.publish({ busy: null, observation: this.view.candidate ? 'stale' : 'unread' }) }
  unavailable() { this.publish({ notice: 'unavailable' }) }
  private begin(busy: 'list' | 'snapshot') {
    if (!this.view.opened || !this.view.context.available || this.pending) return null
    const pending = { abort: new AbortController(), generation: ++this.generation, context: this.view.context.revision }
    this.pending = pending; this.publish({ busy, notice: null, observation: this.view.candidate ? 'stale' : 'unread' }); return pending
  }
  private owns(pending: NonNullable<ResourceCatalogController['pending']>) { return this.pending === pending && pending.generation === this.generation && pending.context === this.view.context.revision && this.view.opened && this.view.context.available }
  private report(error: unknown) {
    if (error instanceof ApiError && error.code === 'unauthenticated') { this.close(); this.handleError(error); return }
    this.publish({ notice: error instanceof CatalogResponseError ? 'invalid' : error instanceof ApiError && error.code === 'resource_catalog_capacity' ? 'limited' : 'unavailable' })
  }
  async loadLocal() {
    if (this.pending) return
    const expectedGeneration = this.generation + 1, context = this.view.context.revision
    this.invalidateSelection()
    if (this.generation !== expectedGeneration || this.view.context.revision !== context) return
    const pending = this.begin('list'); if (!pending || !this.owns(pending)) return
    try { const result = readLocalCatalog(await this.transport.list(pending.abort.signal)); if (this.owns(pending)) this.publish({ localResources: result.resources }) }
    catch (error) { if (this.owns(pending)) { this.publish({ localResources: null }); if (this.owns(pending)) this.report(error) } }
    finally { if (this.owns(pending)) { this.pending = null; this.publish({ busy: null }) } }
  }
  async refresh() {
    const request = this.view.request, context = this.view.context
    if (!request || !context.processId) { this.publish({ notice: 'selectionRequired' }); return }
    const pending = this.begin('snapshot'); if (!pending || !this.owns(pending)) return
    try {
      const raw = await this.transport.snapshot(request, pending.abort.signal)
      if (!this.owns(pending)) return
      const candidate = readCatalogResponse(raw, request, context.processId, context.budget)
      if (this.owns(pending)) this.publish({ candidate, observation: 'current' })
    } catch (error) { if (this.owns(pending)) this.report(error) }
    finally { if (this.owns(pending)) { this.pending = null; this.publish({ busy: null }) } }
  }
  resolve(workflow: Workflow): Navigation | null {
    if (!this.view.opened || !this.view.context.available || this.view.busy || this.view.observation !== 'current' || !this.view.candidate) return null
    return navigation(this.view.candidate.snapshot, workflow)
  }
}
