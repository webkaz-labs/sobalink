import { MAX_CARD_BYTES } from '../device-cards'
export type ReaderFailure = 'noQRCode' | 'ambiguousQRCode' | 'invalidCard'
/** No filtering or first-result choice: even duplicate payloads are ambiguous. */
export function readerCardBytes(results: unknown): Uint8Array | ReaderFailure {
  if (!Array.isArray(results)) return 'invalidCard'
  if (results.length === 0) return 'noQRCode'
  if (results.length !== 1) return 'ambiguousQRCode'
  const value = results[0]
  if (!value || value.isValid !== true || value.format !== 'QRCode' || value.sequenceSize !== -1 || value.sequenceIndex !== -1 || value.sequenceId !== ''
    || !(value.bytes instanceof Uint8Array) || !(value.bytes.buffer instanceof ArrayBuffer) || value.bytes.length === 0 || value.bytes.length > MAX_CARD_BYTES
    || typeof value.text !== 'string' || value.text.length !== value.bytes.length) return 'invalidCard'
  for (let i = 0; i < value.bytes.length; i++) if (value.bytes[i] > 127 || value.text.charCodeAt(i) !== value.bytes[i]) return 'invalidCard'
  // Do not retain a potentially oversized decoder backing buffer or metadata.
  return Uint8Array.from(value.bytes)
}
