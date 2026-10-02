import { describe, expect, it } from 'vitest'
import { parsePorts, formatPorts, previewPorts } from './ports'

describe('scoped port previews', () => {
  it('normalizes overlapping ranges without double-counting', () => {
    expect(formatPorts(parsePorts('8080-8083, 8082-8085, 8000, 8000'))).toBe('8000, 8080-8085')
  })
  it('subtracts reserved and individually excluded ports', () => {
    expect(previewPorts('1-65535', '22,54543-54545', '', 'share')).toMatchObject({ ports: '1-21, 23-54542, 54546-65535', count: 65531 })
  })
  it('caps local listeners while keeping shares compact', () => {
    expect(() => previewPorts('8000-8064', '', '', 'connect')).toThrow('too_many_ports')
    expect(previewPorts('8000-8063', '', '', 'connect').count).toBe(64)
    expect(previewPorts('1-65535', '', '', 'share').count).toBe(65532)
  })
  it('maps one contiguous range and rejects overflow or disjoint remapping', () => {
    expect(previewPorts('8000-8002', '', '9000', 'connect').localPorts).toBe('9000-9002')
    expect(() => previewPorts('8000-8002', '', '65534', 'connect')).toThrow('invalid_mapping')
    expect(() => previewPorts('8000,9000', '', '7000', 'connect')).toThrow('invalid_mapping')
  })
  it.each(['0', '65536', '9000-8000', '1,,2', '1-2-3', '80/tcp', '-3', '1.5'])('rejects malformed scope %s', value => {
    expect(() => parsePorts(value)).toThrow('invalid_ports')
  })
  it('rejects a fully excluded scope', () => { expect(() => previewPorts('80', '80', '', 'connect')).toThrow('empty_ports') })
  it('excludes dynamic backend ports from sharing previews', () => { expect(previewPorts('8000-8004', '', '', 'share', [8002]).ports).toBe('8000-8001, 8003-8004') })
})
