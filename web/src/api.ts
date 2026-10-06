export type TransportBackend = 'lan' | 'tailnet' | 'direct-lan'
export type Network = TransportBackend | 'mixed'
export type Locale = 'en' | 'ja'
export type Theme = 'system' | 'light' | 'dark'
export interface Peer {
  id: string
  name: string
  networks: Network[]
  online: boolean
  verified: boolean
  trusted: boolean
  bridge: boolean
  discovery?: { state: 'pending' | 'confirmed' | 'unconfirmed' | 'unsupported' | 'limited' | 'stale'; checkedAt?: string; code?: string; services: number }
  path: 'direct' | 'relay' | 'unknown'
  route?: LanRouteObservation
  address?: string
  fingerprint?: string
  autosave?: { enabled: boolean; paused: boolean; directory?: string }
}
export interface Message {
  id: string
  peerId: string
  direction: 'incoming' | 'outgoing'
  text: string
  createdAt: string
  status: 'sent' | 'received' | 'failed' | 'pending'
  error?: string
}
export interface TransferEntry {
  id?: string
  path: string
  size: number
  kind: 'file' | 'directory'
  status?: string
  error?: string
  storedPath?: string
}
export interface Transfer {
  id: string
  peerId: string
  direction: 'incoming' | 'outgoing'
  name: string
  entries: TransferEntry[]
  totalBytes: number
  completedBytes: number
  status: 'offered' | 'awaiting-acceptance' | 'queued' | 'transferring' | 'saving' | 'completed' | 'cancelled' | 'failed' | 'declined'
  createdAt: string
  error?: string
}
export type ServiceLifetime = 'finite' | 'until-stopped' | 'until-revoked'
export interface ServicePreset { id: string; purpose: string; network: 'tcp' | 'udp'; port: number; localPort: number; label: Record<Locale, string> }
export interface CapacityChoice { mode: 'default' | 'limited' | 'unlimited'; value?: number }
export interface CapacityPolicy { version: 1; logical: Record<string, CapacityChoice>; resources: Record<string, CapacityChoice> }
export interface PolicyConfig {
 restartRequiredResources?: string[]
 relayResourceEditable?: boolean
  version: 1; requested: CapacityPolicy; effective: CapacityPolicy; revision: string
  catalog: Record<'logical' | 'resources', Record<string, { default: number; unit: string }>>
  adjustable: Record<'logical' | 'resources', Record<string, boolean>>
  usage: Record<string, number>
}
export interface PolicyPreview { restartRequired?: boolean; version: 1; requested: CapacityPolicy; effective: CapacityPolicy; revision: string; destructive: false; usage: Record<string, number> }
export interface HistoryPreview { version: 1; revision: string; messageIds: string[]; retained: number; remove: number; destructive: true }
export interface ServiceLimits { effective: { logical?: Record<string, CapacityChoice>; resources: Record<string, CapacityChoice> }; usage: { materializedListeners: number; [key: string]: number } }
export interface DiagnosticGuidance {
  code: string; category: string; action: 'review_network' | 'review_capacity' | 'refresh_state' | 'wait'; summary: Record<Locale, string>; nextSteps: Record<Locale, string>
}
export interface ServiceDiagnostic {
  serviceId: string; port: number; checkedAt: string; code: string
  transport: 'reachable' | 'unreachable'; application: 'unverified'; nextSteps: Record<Locale, string>
}
export interface ServiceFailure { code: string; at: string; nextSteps: Record<Locale, string> }
export interface ProxyTarget { peerId: string; port: number }
export interface ProxyScope {
  name: string; backend: Network; loopbackHost: '127.0.0.1' | '::1'; localPort: number
  lifetime: 'finite' | 'until-stopped'; ttlSeconds: number; targets: ProxyTarget[]
}
export interface ProxyReview {
  scope: ProxyScope; revision: string; endpoint: string; targets: (ProxyTarget & { host: string })[]
  authentication: 'username-password-required'; application: 'unverified'
}
export interface SavedProxyView { name: string; scope: ProxyScope; revision: string; startOnLaunch: boolean; credentialsSaved: true; valid: boolean; state: string }
export interface SavedProxyList { entries: SavedProxyView[]; revision: string; suppressed: boolean }
export interface StartupEntry { name: string; ids?: string[]; group?: string; services: ServiceConfiguration[]; enabled: boolean; valid: boolean; revision: string; state: string }
export interface StartupList { entries: StartupEntry[]; revision: string; suppressed: boolean }
export interface StartupReview { name: string; ids?: string[]; group?: string; services: ServiceConfiguration[]; enabled: false; selectionRevision: string; revision: string; storeRevision: string; network: string; hostname: string }
export interface ProxyView {
  id: string; name: string; backend: Network; endpoint: string; targets: ProxyTarget[]
  expiresAt: string | null; lifetime: 'finite' | 'until-stopped'; ttlSeconds: number
  status: 'active' | 'failed' | 'stopped' | 'expired'; protocol: 'socks5-tcp-connect'; authentication: 'required'; application: 'unverified'
}
export interface Service {
  id: string
  peerId: string
  peerIds?: string[]
  name: string
  network: 'tcp' | 'udp'
  ports?: string
  localPort?: number
  remotePort?: number
  purpose?: string
  checkedAt?: string
  revision?: string
  endpoint?: string
  expiresAt?: string | null
  loopbackHost?: '127.0.0.1' | '::1'
  lifetime?: ServiceLifetime
  ttlSeconds?: number
  status: 'active' | 'reconnecting' | 'stopped' | 'failed' | 'saved' | 'expired'
  application?: 'unverified'
  owner?: string
  leaseSeconds?: number
  leaseExpiresAt?: string | null
  diagnostic?: ServiceDiagnostic
  lastFailure?: ServiceFailure
  error?: string
}
export type LanRouteLifetime = 'finite' | 'until-revoked'
export interface LanRouteObservation { state: 'idle' | 'connecting' | 'reconnecting' | 'ready' | 'unavailable' | 'closed' | 'expired' | 'unknown'; path: 'unknown' | 'direct' | 'relay'; candidateId?: string; scope?: 'local' | 'external'; observedAt?: string; expires?: string }
export interface LanRouteCandidate { candidateId: string; address: string; certificateSHA256: string; scope: 'local' | 'external' }
export interface LanOwnRoutes { candidates: LanRouteCandidate[]; primaryCandidateId?: string; editable?: boolean }
export interface LanRouteReview { digest: string; issuer: string; recipient: string; sequence: number; issued: string; lifetime: LanRouteLifetime; expires: string | null; candidates: LanRouteCandidate[] }
export interface LanPeerRoutes { lifetime?: LanRouteLifetime; observation?: LanRouteObservation; legacy: boolean; issuedSequence: number; receivedSequence: number; candidates: LanRouteCandidate[]; approvals: { candidateId: string; lifetime: LanRouteLifetime; expires: string | null }[]; permittedIds: string[]; expires: string | null; nextExpiry: string | null; recoveryRequired: boolean }
export interface LanAddress { interface: string; address: string; prefix?: string }
export interface LanPolicy { mode: 'trusted-relay' | 'allowed-lan-destinations'; prefixes: string[]; editable: boolean; restartRequired: boolean }
export interface LanInvitationPreview { recipientPublicKey: string; recipientMatches: true; hostPublicKey: string; hostName: string; expires: string; relay: { kind: 'relay'; address: string; certificateSHA256: string } }
export interface LanRelay { kind: 'relay' | 'host'; address: string; certificateSHA256?: string }
export interface ReceiveRecovery { state: string; code: string; reservedBytes: number | null; applied: boolean; review: string[] }
export const RECEIVE_RECOVERY_REVIEW = ['previous_default_destinations', 'previous_peer_destinations', 'previous_manual_destinations', 'unfinished_staging', 'previously_saved_output', 'untracked_partials_resolved'] as const
export function readReceiveRecovery(value: unknown): ReceiveRecovery | null {
  if (!value || typeof value !== 'object') return null
  const view = value as ReceiveRecovery
  if (!['ready', 'blocked'].includes(view.state) || typeof view.code !== 'string' || typeof view.applied !== 'boolean' || !Array.isArray(view.review) || !view.review.every(item => typeof item === 'string')) return null
  if (view.reservedBytes !== null && (!Number.isSafeInteger(view.reservedBytes) || view.reservedBytes < 0)) return null
  if (view.state === 'ready' ? view.code !== '' || view.reservedBytes === null : !view.code || view.applied) return null
  return view
}
export function receivingBlocked(state: Pick<State, 'receiveRecovery'>) {
  // Older Core versions omit this field. A present but invalid view must never
  // look ready or turn an unknown retained byte count into an empty inventory.
  return state.receiveRecovery !== undefined && readReceiveRecovery(state.receiveRecovery)?.state !== 'ready'
}
export interface DirectLanStatus { configured: boolean; listenerReady: boolean; publicKey?: string; endpoint?: string; prefixes?: string[]; recoveryRequired?: boolean; resourceRestartRequired?: boolean }
export interface DirectLanInvitationPreview { hostPublicKey: string; hostName: string; endpoint: string; recipientPublicKey: string; recipientMatches: true; expires: string }
export interface MixedStatus { configured: boolean; error?: string; active?: boolean; backendStatusAvailable?: boolean; workerResources?: { frameBytes: number; requests: number; handles: number }; resourceRestartRequired?: boolean; backends?: TransportBackend[]; identity?: string; publicKey?: string; bindings?: { peerId: string; publicKey: string; identities: { backend: TransportBackend; id: string }[] }[]; routes?: { peerId: string; backend: TransportBackend; transportId: string; name: string; backendReady: boolean; expired: boolean }[]; backendStates?: { backend: TransportBackend; state: string; running: boolean; selfId?: string; availability?: "ready" | "confirmed-unavailable" | "authorization-required" | "readiness-unconfirmed"; restartRequired?: boolean }[] }
export interface State {
  csrfToken: string
  self: { name: string; status: string; error?: string; errorCode?: string; guidance?: DiagnosticGuidance | null; receiveDirectory?: string; networks?: Network[] }
  peers: Peer[]
  messages: Message[]
  transfers: Transfer[]
  receiveRecovery?: ReceiveRecovery
  services: Service[]
  shares: Service[]
  proxies?: ProxyView[]
  startup?: StartupList
  savedProxies?: SavedProxyList
  availableServices?: Service[]
  reservedPorts?: number[]
  servicePresets?: ServicePreset[]
  limits?: ServiceLimits
  lan?: { configured: boolean; readinessKnown?: boolean; publicKey?: string; relay?: LanRelay; pairingReady: boolean; listenerReady?: boolean; relayReady?: boolean; policy?: LanPolicy; certificate?: { state: 'valid' | 'expiring' | 'expired' | 'not-yet-valid'; notBefore: string; notAfter: string }; path: 'unknown' | 'direct' | 'relay' }
  mixed?: MixedStatus
  directLAN?: DirectLanStatus
  settings?: { network?: 'none' | Network; locale?: 'auto' | Locale; theme?: Theme; hostname?: string; receiveDirectory?: string; maxFiles?: number; maxBatchBytes?: number }
}
export interface CommandResult { ok: boolean; result?: { authUrl?: string; [key: string]: unknown } }
export interface ServiceConfiguration extends ServicePayload {
  id: string
  direction: 'forward' | 'share'
}
export interface ServicePortProposals {
  configuration: ServiceConfiguration; revision: string; code: 'listener_conflict' | 'listener_proposals_exhausted'; conflictPort: number
  effectivePorts: string; checkedAt: string; fromPort: number; attempts: number; requestedCount: number
  attemptBudget: number; bindChecks: number; stopReason: 'requested_count' | 'attempt_budget' | 'bind_budget' | 'port_range'
  proposals: { localPort: number; localEnd: number }[]; reservation: false; nextSteps: Record<Locale, string>
}
export interface ServiceConfigResult { configuration: ServiceConfiguration; revision: string; active: boolean }
export interface ServicePayload {
  backend?: Network | ''
  replaceId?: string
  expectedRevision?: string
  name: string
  peerId?: string
  serviceId?: string
  serviceRevision?: string
  peerIds?: string[]
  network: 'tcp' | 'udp'
  ports: string
  excludePorts?: string
  localPort?: number
  loopbackHost?: '127.0.0.1' | '::1'
  lifetime?: ServiceLifetime
  ttlSeconds: number
  purpose: string
  discoverable: boolean
}
export interface RustDeskMetadata { publicKey: string; idServiceId: string; heartbeatServiceId: string; natServiceId: string; relayServiceId: string }
export interface ServiceGroup { name: string; serviceIds: string[]; rustdesk?: RustDeskMetadata }
export interface DiscoveryRefresh { services: Service[]; observations: ({ peerId: string } & NonNullable<Peer['discovery']>)[]; partial: boolean }
export interface ClientNotice { code: string; message: string; messageJa: string }
export interface RustDeskSetup { name: string; backend: Network; idPeerId: string; relayPeerId: string; publicKey: string; idPort: number; relayPort: number; localIdPort: number; localRelayPort: number; loopbackHost: '127.0.0.1' | '::1'; lifetime: 'finite' | 'until-stopped'; ttlSeconds: number }
export interface RustDeskRoleSettings { role: string; serviceId: string; network: 'tcp' | 'udp'; peerId: string; remotePort: number; localEndpoint: string; lifetime: ServiceLifetime; ttlSeconds: number; status: Service['status'] | 'planned'; listenerReady: boolean }
export interface RustDeskClientSettings { group: string; idServer: string; relayServer: string; publicKey: string; proxy: string; udpEnabled: boolean; remoteIdSuffix: string; application: 'unverified'; roles: RustDeskRoleSettings[]; notices: ClientNotice[] }
export interface RustDeskSetupReview { configuration: RustDeskSetup; group: ServiceGroup; services: ServiceConfiguration[]; revision: string; saved: boolean; applied: boolean; clientSettings: RustDeskClientSettings }
export interface ClientServiceSettings { id: string; name: string; backend: Network; direction: 'forward' | 'share'; purpose: string; network: 'tcp' | 'udp'; peerId?: string; allowedPeerIds?: string[]; localHost: string; localEndpoint?: string; remoteHosts?: string[]; remoteEndpoints: string[]; mappings: { localFirst: number; localLast: number; remoteFirst: number; remoteLast: number }[]; lifetime: ServiceLifetime; ttlSeconds: number; status: Service['status']; listenerReady: boolean; application: 'unverified'; ssh?: { hostKeyAlias: string; args: string[]; command: string }; httpCandidate?: string; notices: ClientNotice[] }
export interface ClientSettingsView { services: ClientServiceSettings[]; rustdesk: RustDeskClientSettings[]; application: 'unverified'; notices: ClientNotice[] }
export interface DefinitionBundle { version: 1; services: ServiceConfiguration[]; groups: ServiceGroup[] | null }
export interface DefinitionExport { profile: DefinitionBundle; revision: string; disabled: true }
export interface DefinitionImport extends DefinitionExport { replacesServices: number; preservesIdentity: true; removesRustDeskMetadata?: string[]; applied?: boolean }
export interface ServiceSelection { services: ServiceConfiguration[]; revision: string; group: string; ready: boolean; states: { id: string; status: Service['status']; lifetime?: ServiceLifetime; ttlSeconds?: number; expiresAt?: string | null; owner?: string; leaseSeconds?: number; leaseExpiresAt?: string | null }[]; application: 'unverified' }
export interface GroupList { groups: ServiceGroup[] | null; revision: string }
export type FavoriteReference = { kind: 'service'; serviceId: string } | { kind: 'group'; groupName: string }
export type FavoriteEntry = FavoriteReference & { available: boolean }
export interface FavoritesView { version: 1; revision: string; entries: FavoriteEntry[]; durabilityUncertain: boolean }
export interface CommandPayloads {
  'device-card.export': { mode: 'lan' | 'direct-lan'; name: string; includeEndpointHint?: boolean; qr?: boolean }
  'device-card.inspect': { card: string; expectedMode: 'lan' | 'direct-lan' }
  'favorites.list': Record<string, never>
  'favorites.add': { reference: FavoriteReference; expectedRevision: string }
  'favorites.remove': { reference: FavoriteReference; expectedRevision: string }
  'rustdesk.preview': { configuration: RustDeskSetup }
  'rustdesk.save': { configuration: RustDeskSetup; expectedRevision: string }
  'rustdesk.settings': { group: string }
  'client.settings': { ids?: string[]; group?: string }
  'diagnostics.run': { serviceId: string; probeTCP: true; port: number }
  'startup.list': Record<string, never>
  'startup.preview': { name: string; ids?: string[]; group?: string }
  'startup.save': { name: string; ids?: string[]; group?: string; expectedRevision: string; expectedStoreRevision: string }
  'startup.disable': { name: string; expectedStoreRevision: string }
  'proxy.saved.list': Record<string, never>
  'proxy.save': { scope: ProxyScope; expectedRevision: string; expectedStoreRevision: string; username: string; password: string; startOnLaunch: boolean }
  'proxy.generate': { scope: ProxyScope; expectedRevision: string; expectedStoreRevision: string; startOnLaunch: boolean }
  'proxy.saved.start': { name: string; expectedRevision: string }
  'proxy.saved.disable': { name: string; expectedRevision: string }
  'proxy.saved.delete': { name: string; expectedRevision: string }
  'proxy.reveal': { name: string; expectedRevision: string }
  'proxy.preview': { scope: ProxyScope }
  'proxy.start': { scope: ProxyScope; expectedRevision: string; username: string; password: string }
  'proxy.list': Record<string, never>
  'proxy.stop': { id: string }
  'service.save': { configuration: Omit<ServiceConfiguration, 'id'> & { id?: string }; expectedRevision?: string }
  'service.delete': { id: string; expectedRevision: string; expectedProfileRevision: string; stopActive: boolean; removeFromGroups: boolean }
  'service.stop-shares': Record<string, never>
  'group.list': Record<string, never>
  'group.save': { group: ServiceGroup; expectedRevision?: string }
  'profile.export': Record<string, never>
  'profile.import.preview': { profile: DefinitionBundle }
  'profile.import': { profile: DefinitionBundle; expectedRevision: string }
  'service.selection': { ids?: string[]; group?: string }
  'services.start': { ids?: string[]; group?: string; expectedRevision: string; lifetime?: ServiceLifetime; ttlSeconds?: number }
  'services.stop': { ids?: string[]; group?: string; expectedRevision: string }
  'policy.config': Record<string, never>
  'policy.preview': { policy: CapacityPolicy }
  'policy.apply': { policy: CapacityPolicy; expectedRevision: string }
  'message.history.preview': Record<string, never>
  'message.history.cleanup': { expectedRevision: string }
  'message.list': { cursor?: string; revision?: string }
  'transfer.list': { cursor?: string; revision?: string }
  'service.list': { direction?: 'share' | 'forward'; cursor?: string; revision?: string }
  'message.send': { peerId: string; text: string }
  'transfer.accept': { transferId: string; destination?: string }
  'receive.recovery.confirm': { reviewed: boolean }
  'transfer.decline': { transferId: string }
  'transfer.cancel': { transferId: string }
  'transfer.retry': { transferId: string }
  'transfer.forget': { transferId: string }
  'peer.trust': { peerId: string; trusted: boolean }
  'discovery.refresh': { peerId?: string }
  'peer.reconnect': { peerId: string }
  'peer.autosave': { peerId: string; enabled?: boolean; paused?: boolean; directory?: string }
  'service.connect': ServicePayload
  'service.share': ServicePayload
  'service.stop': { id: string }
  'service.config': { id: string }
  'service.ports': { id: string; expectedRevision: string; fromPort?: number; count?: number; attempts?: number }
  'network.configure': { mode: 'none' | Network; mixed?: { backends: TransportBackend[] }; directLAN?: { listen: string; prefixes: string[] }; hostname?: string; rotateCertificate?: boolean; lanPolicy?: Pick<LanPolicy, 'mode' | 'prefixes'>; lan?: { kind: 'relay'; address: string; certificateSHA256: string } | { kind: 'host'; address: string } }
  'network.logout': Record<string, never>
  'network.login': { refresh?: boolean; qr?: boolean }
  'network.login.status': { qr?: boolean }
  'application.stop': Record<string, never>
  'mixed.status': Record<string, never>
  'mixed.bind': { peers: string[] }
  'mixed.unbind': { peerId: string }
  'wan.candidates.get': Record<string, never>
  'wan.candidates.set': { enabled: true; stunEndpoints: string[]; advertiseIPv6: boolean; probeBudget?: number } | { enabled: false }
  'direct-lan.status': Record<string, never>
  'direct-lan.identity': Record<string, never>
  'direct-lan.invite': { recipientPublicKey: string; name: string; ttlSeconds: number; qr?: boolean }
  'direct-lan.inspect': { invitation: string }
  'direct-lan.join': { invitation: string }
  'direct-lan.cancel': { invitation: string }
  'direct-lan.revoke': { peerId: string }
  'lan.routes.list': { peerId?: string }
  'lan.routes.add': { address: string; certificateSHA256: string; scope: 'local' | 'external' }
  'lan.routes.remove': { candidateId: string }
  'lan.routes.export': { peerId: string; lifetime: LanRouteLifetime; ttlSeconds?: number; withdraw?: boolean }
  'lan.routes.inspect': { peerId: string; update: string }
  'lan.routes.apply': { peerId: string; update: string; digest: string; candidateIds: string[]; lifetime: LanRouteLifetime; expires?: string }
  'lan.routes.review': { peerId: string }
  'lan.routes.approve': { peerId: string; digest: string; candidateIds: string[]; lifetime: LanRouteLifetime; expires?: string }
  'lan.routes.revoke': { peerId: string; candidateIds: string[] }
  'lan.addresses': Record<string, never>
  'lan.policy.get': Record<string, never>
  'lan.policy.set': Pick<LanPolicy, 'mode' | 'prefixes'>
  'lan.identity': Record<string, never>
  'lan.inspect': { invitation: string }
  'lan.invite': { recipientPublicKey: string; name: string; ttlSeconds: 300 }
  'lan.cancel': { invitation: string }
  'lan.join': { invitation: string }
  'lan.revoke': { peerId: string }
  'settings.update': { locale?: 'auto' | Locale; theme?: Theme; receiveDirectory?: string }
}
export type CommandName = keyof CommandPayloads

export class ApiError extends Error {
  constructor(public code: string, message: string, public status = 0) { super(message); this.name = 'ApiError' }
}
let csrfToken = ''
export function setCSRFToken(value: string) { csrfToken = value }
export function requestID() { return crypto.randomUUID() }
export const MAX_MESSAGE_BYTES = 16_384
function choiceBudget(choice: CapacityChoice | undefined, fallback: number, allowUnlimited = true) {
  if (allowUnlimited && choice?.mode === 'unlimited') return Infinity
  return choice?.mode === 'limited' && Number.isSafeInteger(choice.value) && choice.value! > 0 ? choice.value! : fallback
}
export function exchangeBudgets(state: State) {
  const logical = state.limits?.effective.logical
  const resources = state.limits?.effective.resources
  const spoolBytes = choiceBudget(resources?.transferSpoolBytes, 4 * 1024 ** 3, false)
  const manifestBytes = choiceBudget(resources?.transferManifestBytes, 256 * 1024, false)
  const pathBytes = Math.min(choiceBudget(logical?.pathBytes, 4096), manifestBytes)
  return {
    pathBytes, pathDepth: Math.min(choiceBudget(logical?.pathDepth, 16), Math.max(1, Math.floor((pathBytes + 1) / 2))),
    messageBytes: Math.min(choiceBudget(logical?.messageBytes, MAX_MESSAGE_BYTES), choiceBudget(resources?.messageTextBytes, MAX_MESSAGE_BYTES, false)),
    batchEntries: Math.min(choiceBudget(logical?.batchEntries, state.settings?.maxFiles || 256), Math.max(1, Math.floor(manifestBytes / 512))),
    batchBytes: Math.min(choiceBudget(logical?.batchBytes, state.settings?.maxBatchBytes || 1024 ** 3), spoolBytes),
    fileBytes: Math.min(choiceBudget(logical?.fileBytes, 1024 ** 3), spoolBytes),
  }
}
export function messageByteLength(value: string) { return new TextEncoder().encode(value).byteLength }
export function safeAuthURL(value: string): string | null {
  // Match the CLI authorization-link boundary, including its bounded token form.
  if (value.length > 2048 || !/^https:\/\/login\.tailscale\.com\/a\/[A-Za-z0-9_-]+$/.test(value)) return null
  return value
}

async function readResponse<T>(response: Response): Promise<T> {
  const data = await response.json().catch(() => null)
  if (!response.ok) {
    const error = data?.error
    throw new ApiError(data?.code || error?.code || (response.status === 401 ? 'unauthenticated' : 'request_failed'),
      typeof error === 'string' ? error : error?.message || data?.message || '', response.status)
  }
  if (data === null) throw new ApiError('invalid_response', '')
  return data as T
}
async function jsonRequest<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const timeout = new AbortController()
  // Mutating operation deadlines belong to the selected Core policy.
  const timer = path === '/api/command' ? undefined : setTimeout(() => timeout.abort(), body === undefined ? 15000 : 30000)
  const combined = signal ? AbortSignal.any([signal, timeout.signal]) : timeout.signal
  try {
    return await readResponse<T>(await fetch(path, {
      method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store',
      redirect: 'error', signal: combined,
      headers: body === undefined ? { Accept: 'application/json' } : {
        Accept: 'application/json', 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    }))
  } catch (error) {
    if (timeout.signal.aborted && !signal?.aborted) throw new ApiError('network_error', '')
    if (error instanceof ApiError || (error instanceof DOMException && error.name === 'AbortError')) throw error
    throw new ApiError('network_error', '')
  } finally { clearTimeout(timer) }
}
export async function getState(signal?: AbortSignal): Promise<State> {
  const data = await jsonRequest<State>('/api/state', undefined, signal)
  if (!data.self || !Array.isArray(data.peers) || typeof data.csrfToken !== 'string') throw new ApiError('invalid_response', '')
  return { ...data, messages: data.messages || [], transfers: data.transfers || [], services: data.services || [], shares: data.shares || [] }
}
export async function login(code: string, signal?: AbortSignal) {
  const result = await jsonRequest<{ csrfToken?: string }>('/api/session', { code }, signal)
  return result
}
export async function command<N extends CommandName>(name: N, payload: CommandPayloads[N], id: string = requestID(), signal?: AbortSignal) {
  const result = await jsonRequest<CommandResult>('/api/command', { requestId: id, name, payload }, signal)
  if (!result.ok) throw new ApiError('request_failed', '')
  return result
}
export interface UploadSelection { entries: TransferEntry[]; files: { path: string; file: File }[] }
export function upload(peerId: string, selection: UploadSelection, id: string, onProgress: (loaded: number, total: number) => void, signal: AbortSignal): Promise<CommandResult> {
  // XHR reports staging progress only. Peer delivery progress always comes from /api/state.
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const form = new FormData()
    form.append('peerId', peerId)
    form.append('requestId', id)
    form.append('manifest', JSON.stringify(selection.entries))
    for (const { file, path } of selection.files) form.append('files', file, path)
    xhr.open('POST', '/api/upload')
    xhr.setRequestHeader('X-CSRF-Token', csrfToken)
    xhr.setRequestHeader('Accept', 'application/json')
    xhr.withCredentials = true
    const abort = () => xhr.abort()
    const cleanup = () => signal.removeEventListener('abort', abort)
    xhr.upload.onprogress = event => onProgress(event.loaded, event.lengthComputable ? event.total : 0)
    xhr.onload = () => {
      cleanup()
      let data: CommandResult & { code?: string; error?: string | { message?: string; code?: string } }
      try { data = JSON.parse(xhr.responseText) } catch { reject(new ApiError('invalid_response', '')); return }
      if (xhr.status >= 200 && xhr.status < 300 && data.ok) resolve(data)
      else reject(new ApiError(data.code || (typeof data.error === 'object' ? data.error.code : '') || (xhr.status === 401 ? 'unauthenticated' : 'upload_failed'), typeof data.error === 'string' ? data.error : data.error?.message || '', xhr.status))
    }
    xhr.onerror = () => { cleanup(); reject(new ApiError('network_error', '')) }
    xhr.onabort = () => { cleanup(); reject(new DOMException('Aborted', 'AbortError')) }
    if (signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
    signal.addEventListener('abort', abort, { once: true })
    xhr.send(form)
  })
}

export function canExchange(peer: Peer) { return peer.online && peer.verified && peer.trusted && peer.bridge && !peer.autosave?.paused }
export function canUseServices(peer: Peer, state: State) {
  if (peer.networks.includes('mixed')) return state.settings?.network === 'mixed' && Boolean(state.mixed?.configured)
  if (peer.networks.includes('direct-lan')) return state.settings?.network === 'direct-lan' && Boolean(state.directLAN?.configured && state.directLAN?.listenerReady && !state.directLAN?.recoveryRequired)
  if (peer.networks.includes('lan')) return state.settings?.network === 'lan' && Boolean(state.lan?.configured && state.lan?.pairingReady)
  return peer.networks.includes('tailnet')
}
