import { validCardName, validCardQR } from './device-cards'

export const SETUP_CONTACT_PREFIX = 'sobalink-setup-contact-v1:'
export const SETUP_VERIFY_PREFIX = 'sobalink-setup-verify-v1:'
export const MAX_SETUP_TEXT_BYTES = 1536
export interface SetupScope { scopeId: string; interfaceIndex: number; interface: string; localAddress: string; prefix: string }
export interface SetupContact { kind: 'sobalink-setup-contact'; version: 1; sessionId: string; alias: string; bootstrap: string; expiresAt: string }
export interface SetupCandidate { candidateId: string; contact: SetupContact; observedAt: string; expiresAt: string; verification: 'unverified' }
export interface SetupParticipant {
  session_id: string; key: string; alias: string; bootstrap: string
  scope: { local_address: string; prefix: string; consent_revision: string }
  allow_host: boolean; existing_host: boolean; relay: { address: string; certificate_sha256: string } | null; expires: string
}
export interface SetupPlan {
  revision: number; fingerprint: string; verification: 'full-sha256'; verificationText: string; verificationQR?: boolean[][]
  backend: 'lan'; participants: [SetupParticipant, SetupParticipant]; hostKey: string
  relay: { kind: string; address: string; certificateSHA256: string }; expiresAt: string
}
export type SetupState = 'idle' | 'discovering' | 'selecting' | 'authenticating' | 'awaiting_approval' | 'preparing' | 'connecting' | 'connected' | 'recovery_required' | 'needs_restart' | 'cancelling' | 'cancelled' | 'expired' | 'stop_required' | 'failed'
export interface SetupSnapshot {
  kind: 'sobalink-setup'; version: 1; sessionId?: string; expiresAt?: string; state: SetupState; reasonCode?: string; nextAction?: string
  scope?: SetupScope; contact?: SetupContact; contactText?: string; contactQR?: boolean[][]; candidates: SetupCandidate[]; selected?: SetupContact; plan?: SetupPlan
  localApproved: boolean; remoteApproved: boolean; paired: boolean; connected: boolean; trusted: boolean
  process: { state: 'ready' | 'stop_required' | 'needs_restart'; profilePreserved: boolean; approvalsSurviveRestart: false }
  discovery: { state: 'active' | 'unavailable' | 'stopped'; reasonCode?: string }
}
export interface SetupCommandPayloads {
  'setup.scopes': Record<string, never>
  'setup.start': { alias: string; scopeId: string; bootstrapPort: number; allowHost: boolean; relayPort: number; consent: true }
  'setup.status': { sessionId?: string }
  'setup.select': { sessionId: string; candidateId: string }
  'setup.manual': { sessionId: string; contactText: string }
  'setup.approve': { sessionId: string; revision: number; fingerprint: string; verified: true }
  'setup.cancel': { sessionId: string }
}
const record = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === 'object' && !Array.isArray(value))
const only = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).every(key => keys.includes(key))
const hex = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value) && !/^0+$/.test(value)
const code = (value: unknown) => typeof value === 'string' && value.length <= 100 && /^[a-z0-9_-]*$/.test(value)
const time = (value: unknown): value is string => typeof value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value))
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) > 0
export function validSetupAlias(value: unknown): value is string { return validCardName(value) && !/\p{Cf}/u.test(value) }
export function validSetupPort(value: number): boolean { return Number.isSafeInteger(value) && value >= 1024 && value <= 65535 && ![54543, 54544, 54545].includes(value) }
function numericAddress(value: unknown): value is string {
  if (typeof value !== 'string' || value.length > 45 || !/^[a-f0-9:.]+$/.test(value)) return false
  if (value.includes(':')) {
    try { return new URL(`http://[${value}]`).hostname === `[${value}]` && (value === '::1' || /^f[cd][a-f0-9]{2}:/.test(value)) } catch { return false }
  }
  const pieces = value.split('.')
  return pieces.length === 4 && pieces.every(part => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255) && (Number(pieces[0]) === 10 || Number(pieces[0]) === 127 || Number(pieces[0]) === 172 && Number(pieces[1]) >= 16 && Number(pieces[1]) <= 31 || Number(pieces[0]) === 192 && Number(pieces[1]) === 168)
}
function prefix(value: unknown): value is string {
  if (typeof value !== 'string') return false
  const [address, bits, extra] = value.split('/')
  return extra === undefined && numericAddress(address) && /^[1-9]\d{0,2}$/.test(bits || '') && Number(bits) <= (address.includes(':') ? 128 : 32)
}
function endpoint(value: unknown): value is string {
  if (typeof value !== 'string' || value.length > 54) return false
  const parsed = /^(?:\[([^\]]+)\]|([^:]+)):([1-9]\d{0,4})$/.exec(value)
  return Boolean(parsed && numericAddress(parsed[1] || parsed[2]) && validSetupPort(Number(parsed[3])))
}
export function setupEndpoint(address: string, port: number): string { return `${address.includes(':') ? `[${address}]` : address}:${port}` }
function scope(value: unknown): value is SetupScope {
  return record(value) && only(value, ['scopeId', 'interfaceIndex', 'interface', 'localAddress', 'prefix']) && hex(value.scopeId) && positive(value.interfaceIndex) && typeof value.interface === 'string' && value.interface.length <= 128 && !/[\p{Cc}\p{Cf}]/u.test(value.interface) && numericAddress(value.localAddress) && prefix(value.prefix)
}
export function readSetupScopes(value: unknown): SetupScope[] | undefined {
  if (!record(value) || !only(value, ['kind', 'version', 'scopes']) || value.kind !== 'sobalink-setup-scopes' || value.version !== 1 || !Array.isArray(value.scopes) || value.scopes.length > 128 || !value.scopes.every(scope) || new Set(value.scopes.map(item => item.scopeId)).size !== value.scopes.length) return
  return value.scopes
}
function contact(value: unknown): value is SetupContact {
  return record(value) && only(value, ['kind', 'version', 'sessionId', 'alias', 'bootstrap', 'expiresAt']) && value.kind === 'sobalink-setup-contact' && value.version === 1 && hex(value.sessionId) && validSetupAlias(value.alias) && endpoint(value.bootstrap) && time(value.expiresAt)
}
function canonicalContact(value: SetupContact): string {
  return JSON.stringify({ kind: value.kind, version: value.version, sessionId: value.sessionId, alias: value.alias, bootstrap: value.bootstrap, expiresAt: value.expiresAt }).replace(/[<>&]/g, char => `\\u${char.charCodeAt(0).toString(16).padStart(4, '0')}`)
}
/** Exact, bounded typed wire input. Decoding is never approval or a command. */
export function readSetupContact(text: string): SetupContact | undefined {
  if (text.length > MAX_SETUP_TEXT_BYTES || !text.startsWith(SETUP_CONTACT_PREFIX)) return
  const encoded = text.slice(SETUP_CONTACT_PREFIX.length)
  if (!/^[A-Za-z0-9_-]+$/.test(encoded)) return
  try {
    const raw = atob(encoded.replace(/-/g, '+').replace(/_/g, '/'))
    if (btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') !== encoded) return
    const json = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Uint8Array.from(raw, char => char.charCodeAt(0)))
    const value: unknown = JSON.parse(json)
    if (!contact(value) || canonicalContact(value) !== json) return
    return value
  } catch { return }
}
export function readSetupFingerprint(text: string): string | undefined {
  const value = text.startsWith(SETUP_VERIFY_PREFIX) ? text.slice(SETUP_VERIFY_PREFIX.length) : text
  return hex(value) ? value : undefined
}
function participant(value: unknown): value is SetupParticipant {
  if (!record(value) || !only(value, ['session_id', 'key', 'alias', 'bootstrap', 'scope', 'allow_host', 'existing_host', 'relay', 'expires']) || !hex(value.session_id) || !hex(value.key) || !validSetupAlias(value.alias) || !endpoint(value.bootstrap) || !time(value.expires) || typeof value.allow_host !== 'boolean' || typeof value.existing_host !== 'boolean' || !record(value.scope) || !only(value.scope, ['local_address', 'prefix', 'consent_revision']) || !numericAddress(value.scope.local_address) || !prefix(value.scope.prefix) || !hex(value.scope.consent_revision)) return false
  if (value.relay !== null && (!record(value.relay) || !only(value.relay, ['address', 'certificate_sha256']) || !endpoint(value.relay.address) || !hex(value.relay.certificate_sha256))) return false
  return value.bootstrap.startsWith(`${value.scope.local_address.includes(':') ? `[${value.scope.local_address}]` : value.scope.local_address}:`)
}
function plan(value: unknown): value is SetupPlan {
  if (!record(value) || !only(value, ['revision', 'fingerprint', 'verification', 'verificationText', 'verificationQR', 'backend', 'participants', 'hostKey', 'relay', 'expiresAt']) || !positive(value.revision) || !hex(value.fingerprint) || value.verification !== 'full-sha256' || value.verificationText !== SETUP_VERIFY_PREFIX + value.fingerprint || ('verificationQR' in value && !validCardQR(value.verificationQR)) || value.backend !== 'lan' || !Array.isArray(value.participants) || value.participants.length !== 2 || !value.participants.every(participant) || !hex(value.hostKey) || !record(value.relay) || !only(value.relay, ['kind', 'address', 'certificateSHA256']) || !code(value.relay.kind) || !endpoint(value.relay.address) || !hex(value.relay.certificateSHA256) || !time(value.expiresAt)) return false
  const [first, second] = value.participants
  const host = value.participants.find(item => item.key === value.hostKey)
  return first.key !== second.key && first.session_id !== second.session_id && Boolean(host?.allow_host && host.relay?.address === value.relay.address && host.relay?.certificate_sha256 === value.relay.certificateSHA256)
}
const states: SetupState[] = ['idle', 'discovering', 'selecting', 'authenticating', 'awaiting_approval', 'preparing', 'connecting', 'connected', 'recovery_required', 'needs_restart', 'cancelling', 'cancelled', 'expired', 'stop_required', 'failed']
export function readSetupSnapshot(value: unknown, expectedSession?: string): SetupSnapshot | undefined {
  if (!record(value) || !only(value, ['kind', 'version', 'sessionId', 'expiresAt', 'state', 'reasonCode', 'nextAction', 'scope', 'contact', 'contactText', 'contactQR', 'candidates', 'selected', 'plan', 'localApproved', 'remoteApproved', 'paired', 'connected', 'trusted', 'process', 'discovery']) || value.kind !== 'sobalink-setup' || value.version !== 1 || !states.includes(value.state as SetupState)) return
  if (('sessionId' in value && !hex(value.sessionId)) || (expectedSession && value.sessionId !== expectedSession) || ('expiresAt' in value && !time(value.expiresAt)) || ('reasonCode' in value && !code(value.reasonCode)) || ('nextAction' in value && !code(value.nextAction))) return
  if (!['localApproved', 'remoteApproved', 'paired', 'connected', 'trusted'].every(key => typeof value[key] === 'boolean')) return
  if (!record(value.process) || !only(value.process, ['state', 'profilePreserved', 'approvalsSurviveRestart']) || !['ready', 'stop_required', 'needs_restart'].includes(value.process.state as string) || typeof value.process.profilePreserved !== 'boolean' || value.process.approvalsSurviveRestart !== false) return
  if (!record(value.discovery) || !only(value.discovery, ['state', 'reasonCode']) || !['active', 'unavailable', 'stopped'].includes(value.discovery.state as string) || ('reasonCode' in value.discovery && !code(value.discovery.reasonCode))) return
  if (('scope' in value && !scope(value.scope)) || ('contact' in value && !contact(value.contact)) || ('selected' in value && !contact(value.selected)) || ('plan' in value && !plan(value.plan)) || ('contactQR' in value && !validCardQR(value.contactQR))) return
  if ('contactText' in value && (typeof value.contactText !== 'string' || !readSetupContact(value.contactText) || !contact(value.contact) || canonicalContact(readSetupContact(value.contactText)!) !== canonicalContact(value.contact))) return
  if (!Array.isArray(value.candidates) || value.candidates.length > 64 || !value.candidates.every(item => record(item) && only(item, ['candidateId', 'contact', 'observedAt', 'expiresAt', 'verification']) && hex(item.candidateId) && contact(item.contact) && time(item.observedAt) && time(item.expiresAt) && item.verification === 'unverified') || new Set(value.candidates.map(item => item.candidateId)).size !== value.candidates.length) return
  const result = value as unknown as SetupSnapshot
  if (result.contact && (result.contact.sessionId !== result.sessionId || result.contact.expiresAt !== result.expiresAt || !result.scope || !result.contact.bootstrap.startsWith(`${result.scope.localAddress.includes(':') ? `[${result.scope.localAddress}]` : result.scope.localAddress}:`))) return
  if (result.plan) {
    const local = result.plan.participants.find(item => item.session_id === result.sessionId)
    const remote = result.plan.participants.find(item => item.session_id === result.selected?.sessionId)
    if (!local || !remote || !result.scope || !result.contact || local.alias !== result.contact.alias || local.bootstrap !== result.contact.bootstrap || local.scope.local_address !== result.scope.localAddress || local.scope.prefix !== result.scope.prefix || remote.alias !== result.selected?.alias || remote.bootstrap !== result.selected?.bootstrap || Date.parse(result.plan.expiresAt) > Math.min(Date.parse(local.expires), Date.parse(remote.expires))) return
  }
  if ((result.localApproved || result.remoteApproved || result.paired || result.connected) && !result.plan) return
  if (result.connected && (!result.paired || !result.localApproved || !result.remoteApproved || result.state !== 'connected')) return
  if (result.state === 'connected' && !result.connected) return
  if (['cancelled', 'expired', 'failed'].includes(result.state) && result.sessionId && result.discovery.state !== 'stopped') return
  if (!['idle', 'stop_required', 'needs_restart', 'failed'].includes(result.state) && (!result.sessionId || !result.expiresAt || !result.scope || !result.contact)) return
  return result
}
export function setupTerminal(snapshot: SetupSnapshot): boolean { return ['idle', 'cancelled', 'expired', 'connected', 'failed', 'stop_required', 'recovery_required'].includes(snapshot.state) || snapshot.process.state !== 'ready' }
