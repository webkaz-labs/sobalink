import { describe, expect, it } from 'vitest'
import { cardDigest, MAX_CARD_INPUT_BYTES, readCardExport, readCardFile, readCardInput, readCardInspection, validCardName, validCardQR, type DeviceCard } from './device-cards'
const key = 'a1'.repeat(32), pin = 'c3'.repeat(32)
const base: DeviceCard = { version: 1, mode: 'lan', publicKey: key, name: 'Synthetic alias' }
const encode = (value: unknown) => 'soba-card1.' + btoa(String.fromCharCode(...new TextEncoder().encode(typeof value === 'string' ? value : JSON.stringify(value)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const qr = () => Array.from({ length: 29 }, (_, y) => Array.from({ length: 29 }, (_, x) => x === 4 && y === 4))
describe('bounded public device cards', () => {
  it.each(['lan', 'direct-lan'] as const)('reads canonical %s text and only the outer ASCII envelope', mode => {
    const card = { ...base, mode }, text = encode(card)
    expect(readCardInput(`\r\n${text}\t `, mode)).toEqual({ card, text })
    expect(readCardInput(`\u00a0${text}`, mode)).toBe('invalid')
    expect(readCardInput(text + ' '.repeat(MAX_CARD_INPUT_BYTES), mode)).toBe('tooLarge')
  })
  it('matches Go HTML escaping while preserving Japanese aliases', () => {
    const card = { ...base, name: '架空 <別名>&' }
    expect(readCardInput(encode(JSON.stringify(card).replace('<', '\\u003c').replace('>', '\\u003e').replace('&', '\\u0026')), 'lan')).toMatchObject({ card })
    expect(readCardInput(encode(card), 'lan')).toBe('invalid')
  })
  it.each(['soba-lan1.synthetic-private-invitation', 'soba-directlan1.synthetic-private-invitation', 'soba-card1.A', 'soba-card1.====', 'soba-card1.YQ==', 'soba-card1.eyJ9', '\ud800'])('rejects malformed/private input without carrying it into an error: %s', input => expect(readCardInput(input, 'lan')).toBe('invalid'))
  it('rejects noncanonical base64, invalid UTF-8, duplicates, unknown fields and trailing data', () => {
    const raw = JSON.stringify(base)
    for (const text of [encode({ ...base, secret: 'synthetic-private-value' }), encode({ ...base, publicKey: key.toUpperCase() }), encode(raw.replace('"name":', '"name":"duplicate","name":')), encode(raw + '\n'), encode({ name: base.name, version: 1, mode: 'lan', publicKey: key }), 'soba-card1._w', encode('\ufeff' + raw), encode(base) + '=', encode({ ...base, relay: null })]) expect(readCardInput(text, 'lan')).toBe('invalid')
  })
  it('rejects the other mode without interpreting its hints as a configuration', () => {
    expect(readCardInput(encode({ ...base, mode: 'direct-lan', endpoint: '192.168.50.2:48444' }), 'lan')).toBe('wrongMode')
    expect(readCardInput(encode({ ...base, endpoint: '192.168.50.2:48444' }), 'lan')).toBe('invalid')
  })
  it.each(['', ' alias', 'alias ', '\u0000', '\u061c', '\u2067', 'line\u2028break', '\ud800', 'あ'.repeat(27), 'a'.repeat(81)])('rejects invalid alias %j', name => expect(validCardName(name)).toBe(false))
  it.each(['Synthetic', 'あ'.repeat(26), '😀'.repeat(20)])('accepts bounded alias %j', name => expect(validCardName(name)).toBe(true))
  it.each(['https://example.test/', 'example.test:443', '0.0.0.0:1024', '224.0.0.1:1024', '192.168.050.2:48444', '[::ffff:7f00:1]:48444', '[2001:db8::2]:48444', '8.8.8.8:48444', '192.168.50.2:54543', '192.168.50.2:443'])('rejects unsafe/noncanonical direct hint %s', endpoint => expect(readCardInput(encode({ ...base, mode: 'direct-lan', endpoint }), 'direct-lan')).toBe('invalid'))
  it.each(['192.168.50.2:48444', '127.0.0.1:48444', '[::1]:48444', '[fd00::2]:48444'])('accepts a shape-valid direct hint %s only as unverified text', endpoint => expect(readCardInput(encode({ ...base, mode: 'direct-lan', endpoint }), 'direct-lan')).toMatchObject({ card: { endpoint } }))
  it('checks the complete content digest and exact public result allowlist', async () => {
    const digest = await cardDigest(encode(base)), view = { ...base, verification: 'unverified', freshness: 'unknown', contentDigest: digest }
    expect(digest).toMatch(/^[a-f0-9]{64}$/); expect(readCardInspection(view, base, digest)).toEqual(view)
    for (const changed of [{ contentDigest: 'f'.repeat(64) }, { verification: 'verified' }, { freshness: 'fresh' }, { name: 'Different' }, { privateKey: key }, { endpoint: '192.168.50.2:48444' }]) expect(readCardInspection({ ...view, ...changed }, base, digest)).toBeUndefined()
  })
  it('exports only the requested alias, mode, existing public identity and no address hints', () => {
    const view = { ...base, card: encode(base), verification: 'unverified', freshness: 'unknown' }
    expect(readCardExport(view, 'lan', base.name, key, false)).toEqual(view)
    for (const changed of [{ name: 'Other' }, { publicKey: pin }, { mode: 'direct-lan' }, { card: encode({ ...base, name: 'Other' }) }, { relay: { address: '192.0.2.1:443', certificateSHA256: pin } }, { invitation: 'synthetic-private-value' }]) expect(readCardExport({ ...view, ...changed }, 'lan', base.name, key, false)).toBeUndefined()
    expect(readCardExport(view, 'lan', base.name, key, true)).toBeUndefined()
    expect(readCardExport({ ...view, qr: qr() }, 'lan', base.name, key, false)).toBeUndefined()
    expect(readCardExport({ ...view, qr: qr() }, 'lan', base.name, key, true)?.qr).toEqual(qr())
  })
  it('requires a square bounded boolean bitmap including a four-module white quiet zone', () => {
    expect(validCardQR(qr())).toBe(true)
    const border = qr(); border[0][0] = true
    const ragged = qr(); ragged[10].pop()
    const nonBoolean = qr() as unknown[][]; nonBoolean[10][10] = 'false'
    const sparse = qr(); delete sparse[10][10]
    for (const invalid of [[], Array(186).fill([]), Array(30).fill(Array(30).fill(false)), Array(29).fill(Array(29).fill(false)), border, ragged, nonBoolean, sparse]) expect(validCardQR(invalid)).toBe(false)
  })
  it('bounds files before reading and rejects invalid UTF-8', async () => {
    let read = false
    await expect(readCardFile({ size: 1041, arrayBuffer: async () => { read = true; return new ArrayBuffer(1041) } } as File)).rejects.toThrow('tooLarge'); expect(read).toBe(false)
    await expect(readCardFile({ size: 1, arrayBuffer: async () => Uint8Array.of(255).buffer } as File)).rejects.toThrow()
    const bomBytes = new TextEncoder().encode('\ufeff' + encode(base))
    const bomText = await readCardFile({ size: bomBytes.byteLength, arrayBuffer: async () => bomBytes.buffer } as File)
    expect(readCardInput(bomText, 'lan')).toBe('invalid')
    const bytes = new TextEncoder().encode(encode(base))
    await expect(readCardFile({ size: bytes.byteLength, arrayBuffer: async () => bytes.buffer } as File)).resolves.toBe(encode(base))
  })
})
