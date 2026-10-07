import { describe, expect, it } from 'vitest'
import { readerCardBytes } from './reader-result'
const valid = () => ({ isValid: true, format: 'QRCode', sequenceSize: -1, sequenceIndex: -1, sequenceId: '', bytes: Uint8Array.of(65, 66), text: 'AB' })
describe('exact single QR raw payload boundary', () => {
  it('rejects two results including identical payloads before choosing a card', () => {
    expect(readerCardBytes([])).toBe('noQRCode')
    expect(readerCardBytes([valid(), valid()])).toBe('ambiguousQRCode')
  })
  it.each([{ isValid: false }, { format: 'MicroQRCode' }, { sequenceSize: 2 }, { sequenceIndex: 0 }, { sequenceId: '1' }, { text: 'AC' }, { text: 'é', bytes: Uint8Array.of(233) }, { bytes: new Uint8Array(1036) }])('rejects invalid/foreign/structured/altered payload %j', patch => {
    expect(readerCardBytes([{ ...valid(), ...patch }])).toBe('invalidCard')
  })
  it('returns only a copied bounded buffer with a strict ASCII/text match', () => {
    const value = valid(); value.bytes = new Uint8Array(new ArrayBuffer(2000), 0, 2); value.bytes.set([65, 66])
    const result = readerCardBytes([value]) as Uint8Array
    expect(result).toEqual(Uint8Array.of(65, 66)); expect(result.buffer.byteLength).toBe(2)
  })
})
