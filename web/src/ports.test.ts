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
  it('maps disjoint ranges sequentially with exact previews and rejects overflow', () => {
    expect(previewPorts('8000-8002', '', '9000', 'connect').localPorts).toBe('9000-9002')
    expect(() => previewPorts('8000-8002', '', '65534', 'connect')).toThrow('invalid_mapping')
    expect(previewPorts('8000,9000-9002', '', '7000', 'connect')).toMatchObject({ localPorts: '7000-7003', mappings: [{ local: '7000', remote: '8000' }, { local: '7001-7003', remote: '9000-9002' }] })
  })
  it.each(['22', '80', '1023'])('requires explicit high local mapping for remote %s', port => {
    expect(() => previewPorts(port, '', '', 'connect')).toThrow('invalid_mapping')
    expect(() => previewPorts(port, '', '1023', 'connect')).toThrow('invalid_mapping')
    expect(previewPorts(port, '', '1024', 'connect').localPort).toBe(1024)
  })
  it('accepts both local boundaries and exclusions before sequential mapping', () => {
    expect(previewPorts('1024', '', '', 'connect').localPorts).toBe('1024')
    expect(previewPorts('80', '', '65535', 'connect').localPorts).toBe('65535')
    expect(previewPorts('80,443,8080', '443', '1024', 'connect').mappings).toEqual([{ local: '1024', remote: '80' }, { local: '1025', remote: '8080' }])
    expect(() => previewPorts('80,443', '', '65535', 'connect')).toThrow('invalid_mapping')
  })
  it.each(['0', '65536', '9000-8000', '1,,2', '1-2-3', '80/tcp', '-3', '1.5'])('rejects malformed scope %s', value => {
    expect(() => parsePorts(value)).toThrow('invalid_ports')
  })
  it('rejects a fully excluded scope', () => { expect(() => previewPorts('80', '80', '', 'connect')).toThrow('empty_ports') })
  it('excludes dynamic backend ports from sharing previews', () => { expect(previewPorts('8000-8004', '', '', 'share', [8002]).ports).toBe('8000-8001, 8003-8004') })
})

describe('actual service capacity and inbound mappings', () => {
  it('uses the available runtime listener budget and counts UDP share ports', () => {
    expect(previewPorts('8000-8099', '', '', 'connect', [], { maxListeners: 100 }).count).toBe(100)
    expect(() => previewPorts('8000-8002', '', '', 'connect', [], { maxListeners: 2 })).toThrow('too_many_ports')
    expect(() => previewPorts('8000', '', '', 'share', [], { protocol: 'udp', maxListeners: 0 })).toThrow('too_many_ports')
    expect(previewPorts('8000-8999', '', '', 'share', [], { protocol: 'tcp', maxListeners: 0 }).count).toBe(1000)
  })
  it('previews a single inbound mapping including a low application port', () => {
    expect(previewPorts('8080', '', '80', 'share')).toMatchObject({ ports: '8080', localPorts: '80', count: 1 })
    expect(() => previewPorts('8080-8081', '', '9000', 'share')).toThrow('invalid_share_mapping')
    expect(() => previewPorts('8080', '', '54544', 'share')).toThrow('invalid_share_mapping')
    expect(() => previewPorts('8080', '', '9000', 'share', [9000])).toThrow('invalid_share_mapping')
  })
})
