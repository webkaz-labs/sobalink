import { validCardName, validCardQR } from './device-cards'

export const SETUP_CONTACT_PREFIX = 'sobalink-setup-contact-v1:'
export const SETUP_VERIFY_PREFIX = 'sobalink-setup-verify-v1:'
export const MAX_SETUP_TEXT_BYTES = 1536
export interface SetupScope { scopeId: string; interfaceIndex: number; interface: string; localAddress: string; prefix: string }
export interface SetupContact { kind: 'sobalink-setup-contact'; version: 1 | 2; sessionId: string; alias: string; bootstrap: string; expiresAt: string }
export interface SetupCandidate { candidateId: string; contact: SetupContact; observedAt: string; expiresAt: string; verification: 'unverified' }
export type SetupMode = 'request' | 'receive' | 'outbound'
export type SetupVerification = 'tofu' | 'full-sha256'
export interface SetupTarget { revision: number; sessionId: string; peerKey: string; alias: string; address: string; expiresAt: string; verification: SetupVerification; keyStatus: 'new' | 'saved' | 'revoked'; relayAddress: string; relayCertificateSHA256: string; scopePrefix: string }
export interface SetupRequest { requestId: string; peerKey: string; alias: string; address: string; expiresAt: string; verification: SetupVerification; keyStatus: 'new' | 'saved' | 'revoked' }
export interface SetupParticipant {
  protocol?: 2; mode?: SetupMode; verification?: SetupVerification
  session_id: string; key: string; alias: string; bootstrap: string
  scope: { local_address: string; prefix: string; consent_revision: string }
  allow_host: boolean; existing_host: boolean; relay: { address: string; certificate_sha256: string } | null; expires: string
}
export interface SetupPlan {
  revision: number; fingerprint: string; verification: SetupVerification; verificationText: string; verificationQR?: boolean[][]
  backend: 'lan'; participants: [SetupParticipant, SetupParticipant]; hostKey: string
  relay: { kind: string; address: string; certificateSHA256: string }; expiresAt: string
}
export type SetupState = 'receiving' | 'requesting' | 'request_received' | 'rejected' | 'idle' | 'discovering' | 'selecting' | 'authenticating' | 'awaiting_approval' | 'preparing' | 'connecting' | 'connected' | 'recovery_required' | 'needs_restart' | 'cancelling' | 'cancelled' | 'expired' | 'stop_required' | 'failed'
export interface SetupSnapshot {
  kind: 'sobalink-setup'; version: 1 | 2; alias?: string; mode?: SetupMode; verification?: SetupVerification; requestId?: string; target?: SetupTarget; request?: SetupRequest; sessionId?: string; expiresAt?: string; state: SetupState; reasonCode?: string; nextAction?: string
  scope?: SetupScope; contact?: SetupContact; contactText?: string; contactQR?: boolean[][]; candidates: SetupCandidate[]; selected?: SetupContact; plan?: SetupPlan
  localApproved: boolean; remoteApproved: boolean; paired: boolean; connected: boolean; trusted: boolean
  process: { state: 'ready' | 'stop_required' | 'needs_restart'; profilePreserved: boolean; approvalsSurviveRestart: false }
  discovery: { state: 'active' | 'unavailable' | 'stopped'; reasonCode?: string }
}
export type SetupVerificationBasis = SetupVerification | 'unknown'
export interface SetupRevokedRecord { peerKey: string; generation: number; verification: SetupVerificationBasis }
export interface SetupReenrollResult extends SetupRevokedRecord { kind: 'sobalink-setup-reenroll'; version: 1; paired: false; trusted: false }
export interface SetupCommandPayloads {
  'setup.revoked': Record<string, never>
  'setup.reenroll': { peerKey: string; expectedGeneration: number; consent: true }
  'setup.scopes': Record<string, never>
  'setup.start': { mode?: SetupMode; verification?: SetupVerification; expiresInSeconds?: number; alias: string; scopeId: string; bootstrapPort: number; allowHost: boolean; relayPort: number; consent: true }
  'setup.status': { sessionId?: string }
  'setup.select': { sessionId: string; candidateId: string }
  'setup.manual': { sessionId: string; contactText: string }
  'setup.inspect-target': { sessionId: string; address?: string; candidateId?: string }
  'setup.request': { sessionId: string; targetRevision: number; targetSessionId: string; expectedPeerKey: string; tofuAcknowledged: boolean }
  'setup.reject': { sessionId: string; requestId: string }
  'setup.approve': { sessionId: string; revision: number; fingerprint: string; verified: boolean; requestId?: string; tofuAcknowledged?: boolean }
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
function endpoint(value: unknown, allowZero = false): value is string {
  if (typeof value !== 'string' || value.length > 54) return false
  const parsed = /^(?:\[([^\]]+)\]|([^:]+)):(0|[1-9]\d{0,4})$/.exec(value)
  return Boolean(parsed && numericAddress(parsed[1] || parsed[2]) && (validSetupPort(Number(parsed[3])) || allowZero && parsed[3] === '0'))
}
/** User-entered destinations are exact private numeric endpoints, never URLs or names. */
export function validSetupTargetAddress(value: string): boolean {
  if (!endpoint(value)) return false
  const address = value.startsWith('[') ? value.slice(1, value.indexOf(']')) : value.split(':')[0]
  return address !== '::1' && !address.startsWith('127.')
}
export function setupEndpoint(address: string, port: number): string { return `${address.includes(':') ? `[${address}]` : address}:${port}` }
function scope(value: unknown): value is SetupScope {
  return record(value) && only(value, ['scopeId', 'interfaceIndex', 'interface', 'localAddress', 'prefix']) && hex(value.scopeId) && positive(value.interfaceIndex) && typeof value.interface === 'string' && value.interface.length <= 128 && !/[\p{Cc}\p{Cf}]/u.test(value.interface) && numericAddress(value.localAddress) && prefix(value.prefix)
}
export function readSetupScopes(value: unknown): SetupScope[] | undefined {
  if (!record(value) || !only(value, ['kind', 'version', 'scopes']) || value.kind !== 'sobalink-setup-scopes' || value.version !== 1 || !Array.isArray(value.scopes) || value.scopes.length > 128 || !value.scopes.every(scope) || new Set(value.scopes.map(item => item.scopeId)).size !== value.scopes.length) return
  return value.scopes
}
function contact(value: unknown, version: 1 | 2 = 1): value is SetupContact {
  return record(value) && only(value, ['kind', 'version', 'sessionId', 'alias', 'bootstrap', 'expiresAt']) && value.kind === 'sobalink-setup-contact' && value.version === version && hex(value.sessionId) && validSetupAlias(value.alias) && endpoint(value.bootstrap, version === 2) && time(value.expiresAt)
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
function participant(value: unknown, version: 1 | 2): value is SetupParticipant {
  if (!record(value) || !only(value, ['protocol', 'mode', 'verification', 'session_id', 'key', 'alias', 'bootstrap', 'scope', 'allow_host', 'existing_host', 'relay', 'expires']) || !hex(value.session_id) || !hex(value.key) || !validSetupAlias(value.alias) || !endpoint(value.bootstrap, version === 2 && value.mode === 'outbound') || !time(value.expires) || typeof value.allow_host !== 'boolean' || typeof value.existing_host !== 'boolean' || !record(value.scope) || !only(value.scope, ['local_address', 'prefix', 'consent_revision']) || !numericAddress(value.scope.local_address) || !prefix(value.scope.prefix) || !hex(value.scope.consent_revision)) return false
  if (version === 1 ? 'protocol' in value || 'mode' in value || 'verification' in value : value.protocol !== 2 || !modes.includes(value.mode as SetupMode) || !verifications.includes(value.verification as SetupVerification)) return false
  if (version === 2 && value.mode !== 'receive' && value.allow_host) return false
  if (value.mode === 'outbound' && (!value.bootstrap.endsWith(':0') || value.allow_host || value.existing_host || value.relay !== null)) return false
  if (value.relay !== null && (!record(value.relay) || !only(value.relay, ['address', 'certificate_sha256']) || !endpoint(value.relay.address) || !hex(value.relay.certificate_sha256))) return false
  return value.bootstrap.startsWith(`${value.scope.local_address.includes(':') ? `[${value.scope.local_address}]` : value.scope.local_address}:`)
}
function plan(value: unknown, version: 1 | 2): value is SetupPlan {
  if (!record(value) || !only(value, ['revision', 'fingerprint', 'verification', 'verificationText', 'verificationQR', 'backend', 'participants', 'hostKey', 'relay', 'expiresAt']) || !positive(value.revision) || !hex(value.fingerprint) || (version === 1 ? value.verification !== 'full-sha256' : !verifications.includes(value.verification as SetupVerification)) || value.verificationText !== SETUP_VERIFY_PREFIX + value.fingerprint || ('verificationQR' in value && !validCardQR(value.verificationQR)) || value.backend !== 'lan' || !Array.isArray(value.participants) || value.participants.length !== 2 || !value.participants.every(item => participant(item, version)) || !hex(value.hostKey) || !record(value.relay) || !only(value.relay, ['kind', 'address', 'certificateSHA256']) || !code(value.relay.kind) || !endpoint(value.relay.address) || !hex(value.relay.certificateSHA256) || !time(value.expiresAt)) return false
  const [first, second] = value.participants
  const host = value.participants.find(item => item.key === value.hostKey)
  return (version === 1 || first.verification === value.verification && second.verification === value.verification && [first.mode, second.mode].filter(mode => mode === 'receive').length === 1) && first.key !== second.key && first.session_id !== second.session_id && Boolean(host?.allow_host && host.relay?.address === value.relay.address && host.relay?.certificate_sha256 === value.relay.certificateSHA256)
}
const modes: SetupMode[] = ['request', 'receive', 'outbound']
const verifications: SetupVerification[] = ['tofu', 'full-sha256']
function peerView(value: unknown, target: boolean): boolean {
  if (!record(value) || !only(value, [...(target ? ['revision', 'sessionId', 'relayAddress', 'relayCertificateSHA256', 'scopePrefix'] : ['requestId']), 'peerKey', 'alias', 'address', 'expiresAt', 'verification', 'keyStatus']) || !hex(value[target ? 'sessionId' : 'requestId']) || !hex(value.peerKey) || !validSetupAlias(value.alias) || !endpoint(value.address, !target) || !time(value.expiresAt) || !verifications.includes(value.verification as SetupVerification) || !['new', 'saved', 'revoked'].includes(value.keyStatus as string)) return false
  return !target || positive(value.revision) && endpoint(value.relayAddress) && hex(value.relayCertificateSHA256) && prefix(value.scopePrefix)
}
const states: SetupState[] = ['receiving', 'requesting', 'request_received', 'rejected', 'idle', 'discovering', 'selecting', 'authenticating', 'awaiting_approval', 'preparing', 'connecting', 'connected', 'recovery_required', 'needs_restart', 'cancelling', 'cancelled', 'expired', 'stop_required', 'failed']
export function readSetupSnapshot(value: unknown, expectedSession?: string): SetupSnapshot | undefined {
  if (!record(value) || !only(value, ['alias', 'mode', 'verification', 'requestId', 'target', 'request', 'kind', 'version', 'sessionId', 'expiresAt', 'state', 'reasonCode', 'nextAction', 'scope', 'contact', 'contactText', 'contactQR', 'candidates', 'selected', 'plan', 'localApproved', 'remoteApproved', 'paired', 'connected', 'trusted', 'process', 'discovery']) || value.kind !== 'sobalink-setup' || ![1, 2].includes(value.version as number) || !states.includes(value.state as SetupState)) return
  const version = value.version as 1 | 2
  if (version === 1 ? ['alias', 'mode', 'verification', 'requestId', 'target', 'request'].some(key => key in value) : !validSetupAlias(value.alias) || !modes.includes(value.mode as SetupMode) || !verifications.includes(value.verification as SetupVerification) || 'contactText' in value || 'contactQR' in value) return
  if (version === 1 && ['receiving', 'requesting', 'request_received', 'rejected'].includes(value.state as string)) return
  if (('requestId' in value && !hex(value.requestId)) || ('target' in value && !peerView(value.target, true)) || ('request' in value && !peerView(value.request, false))) return
  if (('sessionId' in value && !hex(value.sessionId)) || (expectedSession && value.sessionId !== expectedSession) || ('expiresAt' in value && !time(value.expiresAt)) || ('reasonCode' in value && !code(value.reasonCode)) || ('nextAction' in value && !code(value.nextAction))) return
  if (!['localApproved', 'remoteApproved', 'paired', 'connected', 'trusted'].every(key => typeof value[key] === 'boolean')) return
  if (!record(value.process) || !only(value.process, ['state', 'profilePreserved', 'approvalsSurviveRestart']) || !['ready', 'stop_required', 'needs_restart'].includes(value.process.state as string) || typeof value.process.profilePreserved !== 'boolean' || value.process.approvalsSurviveRestart !== false) return
  if (!record(value.discovery) || !only(value.discovery, ['state', 'reasonCode']) || !['active', 'unavailable', 'stopped'].includes(value.discovery.state as string) || ('reasonCode' in value.discovery && !code(value.discovery.reasonCode))) return
  if (('scope' in value && !scope(value.scope)) || ('contact' in value && !contact(value.contact, version)) || ('selected' in value && !contact(value.selected, version)) || ('plan' in value && !plan(value.plan, version)) || ('contactQR' in value && !validCardQR(value.contactQR))) return
  if ('contactText' in value && (typeof value.contactText !== 'string' || !readSetupContact(value.contactText) || !contact(value.contact, version) || canonicalContact(readSetupContact(value.contactText)!) !== canonicalContact(value.contact))) return
  if (!Array.isArray(value.candidates) || value.candidates.length > 64 || !value.candidates.every(item => record(item) && only(item, ['candidateId', 'contact', 'observedAt', 'expiresAt', 'verification']) && hex(item.candidateId) && contact(item.contact, version) && time(item.observedAt) && time(item.expiresAt) && item.verification === 'unverified') || new Set(value.candidates.map(item => item.candidateId)).size !== value.candidates.length) return
  const result = value as unknown as SetupSnapshot
  if (version === 2) {
    if (result.contact && result.contact.alias !== result.alias || result.candidates.some(item => item.contact.bootstrap.endsWith(':0'))) return
    if (result.mode === 'outbound' && (result.discovery.state === 'active' || result.candidates.length || result.contact !== undefined)) return
    if (result.mode !== 'outbound' && result.contact?.bootstrap.endsWith(':0')) return
    if (result.target && (result.mode === 'receive' || result.target.sessionId === result.sessionId || result.target.verification !== result.verification)) return
    if (result.request && (result.request.requestId !== result.requestId || result.request.verification !== result.verification || result.request.peerKey === result.plan?.participants.find(item => item.session_id === result.sessionId)?.key)) return
    if (result.plan && (result.plan.verification !== result.verification || !result.requestId || !result.request)) return
  }
  if (result.contact && (result.contact.sessionId !== result.sessionId || result.contact.expiresAt !== result.expiresAt || !result.scope || !result.contact.bootstrap.startsWith(`${result.scope.localAddress.includes(':') ? `[${result.scope.localAddress}]` : result.scope.localAddress}:`))) return
  if (result.plan) {
    const local = result.plan.participants.find(item => item.session_id === result.sessionId)
    const remote = result.plan.participants.find(item => item.session_id === result.selected?.sessionId)
    if (version === 2 && (local?.mode !== result.mode || remote?.key !== result.request?.peerKey || result.request?.address !== remote?.bootstrap || result.request?.alias !== remote?.alias || result.target && (result.target.peerKey !== remote?.key || result.target.sessionId !== remote?.session_id || result.target.address !== remote?.bootstrap || result.target.relayAddress !== result.plan.relay.address || result.target.relayCertificateSHA256 !== result.plan.relay.certificateSHA256 || result.target.scopePrefix !== remote.scope.prefix))) return
    if (!local || !remote || !result.scope || !(result.contact || version === 2 && result.mode === 'outbound') || local.alias !== (result.alias || result.contact?.alias) || local.bootstrap !== (result.contact?.bootstrap || setupEndpoint(result.scope.localAddress, 0)) || local.scope.local_address !== result.scope.localAddress || local.scope.prefix !== result.scope.prefix || remote.alias !== result.selected?.alias || remote.bootstrap !== result.selected?.bootstrap || Date.parse(result.plan.expiresAt) > Math.min(Date.parse(local.expires), Date.parse(remote.expires))) return
  }
  if ((result.localApproved || result.remoteApproved || result.paired || result.connected) && !result.plan) return
  if (result.connected && (!result.paired || !result.localApproved || !result.remoteApproved || result.state !== 'connected')) return
  if (result.state === 'connected' && !result.connected) return
  if (['cancelled', 'expired', 'failed'].includes(result.state) && result.sessionId && result.discovery.state !== 'stopped') return
  if (!['idle', 'stop_required', 'needs_restart', 'failed'].includes(result.state) && (!result.sessionId || !result.expiresAt || !result.scope || !(result.contact || version === 2 && result.mode === 'outbound'))) return
  return result
}
export function setupTerminal(snapshot: SetupSnapshot): boolean { return ['idle', 'cancelled', 'expired', 'connected', 'failed', 'stop_required', 'recovery_required'].includes(snapshot.state) || snapshot.process.state !== 'ready' }

function revokedRecord(value: unknown): value is SetupRevokedRecord {
  return record(value) && only(value, ['peerKey', 'generation', 'verification']) && hex(value.peerKey) && positive(value.generation) && ['tofu', 'full-sha256', 'unknown'].includes(value.verification as string)
}
/** Removed keys have no alias identity: only an exact key/generation can authorize reenrollment. */
export function readSetupRevoked(value: unknown): SetupRevokedRecord[] | undefined {
  if (!record(value) || !only(value, ['kind', 'version', 'records']) || value.kind !== 'sobalink-setup-revoked' || value.version !== 1 || !Array.isArray(value.records) || value.records.length > 10000 || !value.records.every(revokedRecord) || new Set(value.records.map(item => item.peerKey)).size !== value.records.length) return
  return value.records
}
export function readSetupReenroll(value: unknown, expected: SetupRevokedRecord): SetupReenrollResult | undefined {
  if (!record(value) || !only(value, ['kind', 'version', 'peerKey', 'generation', 'verification', 'paired', 'trusted']) || value.kind !== 'sobalink-setup-reenroll' || value.version !== 1 || value.peerKey !== expected.peerKey || !positive(value.generation) || value.generation !== expected.generation + 1 || value.verification !== expected.verification || value.paired !== false || value.trusted !== false) return
  return value as unknown as SetupReenrollResult
}
