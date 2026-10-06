import { messageByteLength } from './api'

export type DeviceCardMode = 'lan' | 'direct-lan'
export interface DeviceCard {
  version: 1
  mode: DeviceCardMode
  publicKey: string
  name: string
  endpoint?: string
  relay?: { address: string; certificateSHA256: string }
}
export interface DeviceCardView extends DeviceCard { verification: 'unverified'; freshness: 'unknown' }
export interface DeviceCardInspection extends DeviceCardView { contentDigest: string }
export interface DeviceCardExport extends DeviceCardView { card: string; qr?: boolean[][] }
export const MAX_CARD_INPUT_BYTES = 1040
export const MAX_CARD_BYTES = 1035
const prefix = 'soba-card1.'
const hex = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value) && !/^0+$/.test(value)
const object = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === 'object' && !Array.isArray(value))
const only = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).every(key => keys.includes(key))
export function validCardName(value: unknown): value is string {
  return typeof value === 'string' && !/\p{Surrogate}/u.test(value) && messageByteLength(value) >= 1 && messageByteLength(value) <= 80 && value.trim() === value && !/[\p{Cc}\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/u.test(value)
}
function endpoint(value: unknown, direct: boolean): value is string {
  if (typeof value !== 'string' || value.length > 80) return false
  const match = /^(\[[a-f0-9:]+\]|\d+\.\d+\.\d+\.\d+):([1-9]\d{0,4})$/.exec(value)
  if (!match) return false
  const port = Number(match[2]), host = match[1]
  if (port > 65535 || (direct && (port < 1024 || [54543, 54544, 54545].includes(port)))) return false
  if (host.startsWith('[')) {
    try { if (new URL(`http://${value}`).hostname !== host) return false } catch { return false }
    if (host === '[::]' || host.startsWith('[ff') || host.startsWith('[::ffff:')) return false
    return !direct || host === '[::1]' || /^\[f[cd][a-f0-9]{2}:/.test(host)
  }
  const parts = host.split('.')
  if (!parts.every(part => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255)) return false
  const [a, b] = parts.map(Number)
  if (host === '0.0.0.0' || (a >= 224 && a <= 239)) return false
  return !direct || a === 10 || a === 127 || a === 172 && b >= 16 && b <= 31 || a === 192 && b === 168
}
function publicFields(value: Record<string, unknown>, mode: DeviceCardMode): DeviceCard | undefined {
  if (value.version !== 1 || value.mode !== mode || !hex(value.publicKey) || !validCardName(value.name)) return
  const card: DeviceCard = { version: 1, mode, publicKey: value.publicKey, name: value.name }
  if ('endpoint' in value) {
    if (mode !== 'direct-lan' || !endpoint(value.endpoint, true)) return
    card.endpoint = value.endpoint
  }
  if ('relay' in value) {
    if (mode !== 'lan' || !object(value.relay) || !only(value.relay, ['address', 'certificateSHA256']) || !endpoint(value.relay.address, false) || !hex(value.relay.certificateSHA256)) return
    card.relay = { address: value.relay.address, certificateSHA256: value.relay.certificateSHA256 }
  }
  return card
}
function canonicalJSON(card: DeviceCard) {
  // encoding/json's HTML escapes are part of Core's canonical v1 wire format.
  return JSON.stringify(card).replace(/[<>&]/g, char => `\\u${char.charCodeAt(0).toString(16).padStart(4, '0')}`)
}
export type CardInputError = 'tooLarge' | 'invalid' | 'wrongMode'
export function readCardInput(input: string, mode: DeviceCardMode): { text: string; card: DeviceCard } | CardInputError {
  if (input.length > MAX_CARD_INPUT_BYTES || messageByteLength(input) > MAX_CARD_INPUT_BYTES) return 'tooLarge'
  const text = input.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, '')
  if (text.length > MAX_CARD_BYTES) return 'tooLarge'
  if (!text.startsWith(prefix) || !/^[A-Za-z0-9_-]+$/.test(text.slice(prefix.length))) return 'invalid'
  try {
    const encoded = text.slice(prefix.length), raw = atob(encoded.replace(/-/g, '+').replace(/_/g, '/'))
    if (raw.length > 768) return 'tooLarge'
    if (btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') !== encoded) return 'invalid'
    const decoded = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Uint8Array.from(raw, char => char.charCodeAt(0)))
    const value: unknown = JSON.parse(decoded)
    if (!object(value) || !only(value, ['version', 'mode', 'publicKey', 'name', 'endpoint', 'relay'])) return 'invalid'
    if ((value.mode === 'lan' || value.mode === 'direct-lan') && value.mode !== mode) return 'wrongMode'
    const card = publicFields(value, mode)
    if (!card || canonicalJSON(card) !== decoded) return 'invalid'
    return { text, card }
  } catch { return 'invalid' }
}
export async function cardDigest(text: string) {
  const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('')
}
export function validCardQR(value: unknown): value is boolean[][] {
  if (!Array.isArray(value) || value.length < 29 || value.length > 185 || (value.length - 29) % 4 !== 0) return false
  const size = value.length
  return Array.from(value).every((row, y) => Array.isArray(row) && row.length === size && Array.from(row).every((pixel, x) => typeof pixel === 'boolean' && (!(x < 4 || y < 4 || x >= size - 4 || y >= size - 4) || pixel === false))) && value.some(row => row.some(Boolean))
}
export function readCardInspection(value: unknown, expected: DeviceCard, digest: string): DeviceCardInspection | undefined {
  if (!object(value) || !only(value, ['version', 'mode', 'publicKey', 'name', 'endpoint', 'relay', 'verification', 'freshness', 'contentDigest']) || value.verification !== 'unverified' || value.freshness !== 'unknown' || value.contentDigest !== digest) return
  const card = publicFields(value, expected.mode)
  if (!card || canonicalJSON(card) !== canonicalJSON(expected)) return
  return { ...card, verification: 'unverified', freshness: 'unknown', contentDigest: digest }
}
export function readCardExport(value: unknown, mode: DeviceCardMode, name: string, publicKey: string, wantQR: boolean): DeviceCardExport | undefined {
  if (!object(value) || !only(value, ['version', 'mode', 'publicKey', 'name', 'endpoint', 'relay', 'verification', 'freshness', 'card', 'qr']) || value.verification !== 'unverified' || value.freshness !== 'unknown' || typeof value.card !== 'string') return
  const parsed = readCardInput(value.card, mode), card = publicFields(value, mode)
  if (typeof parsed === 'string' || parsed.text !== value.card || !card || canonicalJSON(parsed.card) !== canonicalJSON(card) || card.name !== name || card.publicKey !== publicKey || card.endpoint !== undefined || card.relay !== undefined) return
  if (wantQR ? !validCardQR(value.qr) : 'qr' in value) return
  return { ...card, verification: 'unverified', freshness: 'unknown', card: parsed.text, ...(value.qr ? { qr: value.qr as boolean[][] } : {}) }
}
export async function readCardFile(file: File): Promise<string> {
  if (file.size > MAX_CARD_INPUT_BYTES) throw new Error('tooLarge')
  // Plain UTF-8 text only. File names and MIME types do not bypass validation.
  const bytes = await file.arrayBuffer()
  if (bytes.byteLength > MAX_CARD_INPUT_BYTES) throw new Error('tooLarge')
  return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
}
