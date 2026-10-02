export type Network = 'lan' | 'tailnet'
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
  path: 'direct' | 'relay' | 'unknown'
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
export interface Service {
  id: string
  peerId: string
  peerIds?: string[]
  name: string
  network: 'tcp' | 'udp'
  ports?: string
  localPort?: number
  remotePort?: number
  endpoint?: string
  expiresAt?: string
  status: 'active' | 'reconnecting' | 'stopped' | 'failed' | 'saved' | 'expired'
  application?: 'unverified'
  error?: string
}
export interface State {
  csrfToken: string
  self: { name: string; status: string; error?: string; errorCode?: string; receiveDirectory?: string; networks?: Network[] }
  peers: Peer[]
  messages: Message[]
  transfers: Transfer[]
  services: Service[]
  shares: Service[]
  availableServices?: Service[]
  reservedPorts?: number[]
  lan?: { configured: boolean; publicKey?: string; relay?: { kind: 'relay' | 'host'; address: string; certificateSHA256?: string }; pairingReady: boolean; path: 'unknown' | 'direct' | 'relay' }
  settings?: { network?: 'none' | Network; locale?: 'auto' | Locale; theme?: Theme; hostname?: string; receiveDirectory?: string; maxFiles?: number; maxBatchBytes?: number }
}
export interface CommandResult { ok: boolean; result?: { authUrl?: string; [key: string]: unknown } }
export interface ServicePayload {
  name: string
  peerId?: string
  serviceId?: string
  peerIds?: string[]
  network: 'tcp' | 'udp'
  ports: string
  excludePorts?: string
  localPort?: number
  ttlSeconds: number
  purpose: string
  discoverable: boolean
}
export interface CommandPayloads {
  'message.send': { peerId: string; text: string }
  'transfer.accept': { transferId: string; destination?: string }
  'transfer.decline': { transferId: string }
  'transfer.cancel': { transferId: string }
  'transfer.retry': { transferId: string }
  'transfer.forget': { transferId: string }
  'peer.trust': { peerId: string; trusted: boolean }
  'peer.reconnect': { peerId: string }
  'peer.autosave': { peerId: string; enabled: boolean; paused: boolean; directory: string }
  'service.connect': ServicePayload
  'service.share': ServicePayload
  'service.stop': { id: string }
  'network.configure': { mode: 'none' | Network; hostname?: string; lan?: { kind: 'relay'; address: string; certificateSHA256: string } }
  'network.login': Record<string, never>
  'lan.identity': Record<string, never>
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
  const timer = setTimeout(() => timeout.abort(), body === undefined ? 15000 : 30000)
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
export async function command<N extends CommandName>(name: N, payload: CommandPayloads[N], id: string = requestID()) {
  const result = await jsonRequest<CommandResult>('/api/command', { requestId: id, name, payload })
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
  if (peer.networks.includes('lan')) return state.settings?.network === 'lan' && Boolean(state.lan?.configured && state.lan?.pairingReady)
  return peer.networks.includes('tailnet')
}
