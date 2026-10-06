import { describe, expect, it } from 'vitest'
import { parsePorts, previewPorts, subtractPorts, type PortRange } from './ports'

// Keep the previous implementation independent of the optimized helpers so
// grammar, error precedence, grouping, and sequential mappings have an oracle.
class ReferencePortError extends Error {
  constructor(public code: 'invalid_ports' | 'too_many_ports' | 'invalid_mapping' | 'empty_ports' | 'invalid_share_mapping') { super(code) }
}
function referenceParsePorts(value: string): PortRange[] {
  if (!value.trim()) throw new ReferencePortError('empty_ports')
  const ranges = value.split(',').map(raw => {
    const part = raw.trim()
    if (!/^\d+(?:\s*-\s*\d+)?$/.test(part)) throw new ReferencePortError('invalid_ports')
    const [start, end = start] = part.split('-').map(Number)
    if (start < 1 || end > 65535 || end < start) throw new ReferencePortError('invalid_ports')
    return { start, end }
  }).sort((a, b) => a.start - b.start)
  const merged: PortRange[] = []
  for (const range of ranges) {
    const previous = merged.at(-1)
    if (previous && range.start <= previous.end + 1) previous.end = Math.max(previous.end, range.end)
    else merged.push({ ...range })
  }
  return merged
}
function referenceSubtractPorts(ranges: PortRange[], excluded: PortRange[]): PortRange[] {
  let result = ranges
  for (const exclude of excluded) {
    result = result.flatMap(range => {
      if (exclude.end < range.start || exclude.start > range.end) return [range]
      return [...(exclude.start > range.start ? [{ start: range.start, end: exclude.start - 1 }] : []), ...(exclude.end < range.end ? [{ start: exclude.end + 1, end: range.end }] : [])]
    })
  }
  return result
}
function referenceFormatPorts(ranges: PortRange[]) { return ranges.map(({ start, end }) => start === end ? `${start}` : `${start}-${end}`).join(', ') }
function referencePreviewPorts(value: string, exclusions: string, local: string, mode: 'connect' | 'share', reservedPorts: number[] = [], options: { protocol?: 'tcp' | 'udp'; maxListeners?: number } = {}) {
  const excluded = exclusions.trim() ? referenceParsePorts(exclusions) : []
  const selected = referenceSubtractPorts(referenceParsePorts(value), excluded)
  if (mode === 'share' && local.trim() && selected.reduce((sum, range) => sum + range.end - range.start + 1, 0) !== 1) throw new ReferencePortError('invalid_share_mapping')
  if (mode === 'share') {
    excluded.push({ start: 54543, end: 54545 })
    for (const port of reservedPorts) if (Number.isInteger(port) && port > 0 && port <= 65535) excluded.push({ start: port, end: port })
  }
  const ranges = referenceSubtractPorts(referenceParsePorts(value), excluded)
  const count = ranges.reduce((sum, range) => sum + range.end - range.start + 1, 0)
  if (!count) throw new ReferencePortError('empty_ports')
  // Preserve the legacy bound only for snapshots without an effective budget.
  if ((mode === 'connect' || options.protocol === 'udp') && count > (options.maxListeners ?? 64)) throw new ReferencePortError('too_many_ports')
  const localPort = local.trim() ? Number(local) : undefined
  if (localPort !== undefined && (!/^\d+$/.test(local) || localPort < (mode === 'connect' ? 1024 : 1) || localPort + count - 1 > 65535)) throw new ReferencePortError('invalid_mapping')
  if (mode === 'share' && localPort !== undefined && ([54543, 54544, 54545, ...reservedPorts].includes(localPort))) throw new ReferencePortError('invalid_share_mapping')
  if (mode === 'connect' && localPort === undefined && ranges.some(range => range.start < 1024)) throw new ReferencePortError('invalid_mapping')
  let offset = 0
  const mappings = ranges.map(range => {
    const localRange = localPort === undefined ? range : { start: localPort + offset, end: localPort + offset + range.end - range.start }
    offset += range.end - range.start + 1
    return { remote: referenceFormatPorts([range]), local: referenceFormatPorts([localRange]) }
  })
  return { ranges, mappings, count, ports: referenceFormatPorts(ranges), localPorts: localPort ? referenceFormatPorts([{ start: localPort, end: localPort + count - 1 }]) : referenceFormatPorts(ranges), localPort }
}


type PreviewInput = Parameters<typeof previewPorts>
function outcome(run: () => unknown) {
  try { return { value: run() } } catch (error) {
    if (!(error instanceof Error) || !('code' in error)) throw error
    return { error: error.code }
  }
}
function referenceCase(input: PreviewInput) {
  expect(outcome(() => previewPorts(...input)), JSON.stringify(input)).toEqual(outcome(() => referencePreviewPorts(...input)))
}

describe('port preview reference equivalence', () => {
  it.each([
    '', ' ', '\t\n', '00080', '1 - 2', ' 1,\n 3 \t- 4 ', '1\u00a0-\u00a03',
    '65535, 1-65534', '8080,8080,8079-8081', '0', '65536', '65535-65536',
    '2-1', '1,,2', ',1', '1,', '1-2-3', '80/tcp', '-3', '+3', '1.5',
    '1e2', '0x50', '１２', '1 2', 'Infinity', '9'.repeat(400),
  ])('preserves parsing grammar for %j', value => {
    expect(outcome(() => parsePorts(value))).toEqual(outcome(() => referenceParsePorts(value)))
  })

  it.each<[string, PreviewInput]>([
    ['invalid_ports', ['', 'bad', '', 'share']],
    ['empty_ports', ['', '', 'bad', 'share']],
    ['invalid_ports', ['bad', '', 'bad', 'share']],
    ['invalid_share_mapping', ['8080-8081', '', 'bad', 'share']],
    ['invalid_share_mapping', ['8080', '8080', '80', 'share']],
    ['invalid_share_mapping', ['8080-8081', '', '80', 'share', [8081]]],
    ['empty_ports', ['54543', '', '80', 'share']],
    ['empty_ports', ['8080', '', '80', 'share', [8080]]],
    ['empty_ports', ['8080', '8080', 'bad', 'connect']],
    ['too_many_ports', ['8000-8064', '', 'bad', 'connect']],
    ['too_many_ports', ['8080', '', 'bad', 'share', [], { protocol: 'udp', maxListeners: 0 }]],
    ['invalid_mapping', ['8080', '', 'bad', 'share', [], { protocol: 'tcp', maxListeners: 0 }]],
    ['invalid_mapping', ['8080', '', ' 9000 ', 'connect']],
    ['invalid_mapping', ['8080', '', '1e4', 'connect']],
    ['invalid_mapping', ['8080', '', '0', 'share']],
    ['invalid_mapping', ['8080', '', '1023', 'connect']],
    ['invalid_mapping', ['8080-8081', '', '65535', 'connect']],
    ['invalid_mapping', ['1023', '', '', 'connect']],
    ['invalid_share_mapping', ['8080', '', '54544', 'share']],
    ['invalid_share_mapping', ['8080', '', '9000', 'share', [9000]]],
  ])('preserves %s and validation precedence', (code, input) => {
    referenceCase(input)
    expect(outcome(() => previewPorts(...input))).toEqual({ error: code })
  })

  it.each<PreviewInput>([
    ['8080-8082,80,8080,443', '8081', '1024', 'connect'],
    ['8080-8082,80,8080,443', '8081', '0001024', 'connect'],
    ['1024,65535', '', ' ', 'connect'],
    ['8080,8081', '8081', '80', 'share'],
    ['54542-54546', '', '', 'share'],
    ['54542-54546', '', '', 'connect'],
    ['8080-8084', '8083', '', 'share', [8084, 8081, 8081, 0, -1, 65536, 8080.5, NaN, Infinity]],
    ['8080-8084', '', '8081', 'connect', [8081]],
    ['8000-8999', '', '', 'share', [], { protocol: 'tcp', maxListeners: 0 }],
    ['8000-8099', '', '', 'share', [], { protocol: 'udp', maxListeners: 100 }],
    ['1-65535', '', '', 'share'],
    ['1024-65535', '', '', 'connect', [], { maxListeners: 64512 }],
  ])('preserves the complete preview for %j', (...input) => referenceCase(input))

  it('matches a deterministic mix of ranges, exclusions, reservations, modes, budgets, and mappings', () => {
    let seed = 0x12345678
    const random = (max: number) => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed % max }
    const bases = [1, 64, 1010, 1024, 8000, 54530, 65510]
    const scope = () => Array.from({ length: 1 + random(8) }, () => {
      const start = bases[random(bases.length)] + random(16)
      const end = Math.min(65535, start + random(12))
      return random(2) ? `${start}-${end}` : String(start)
    }).join(', ')
    const locals = ['', ' ', '1', '80', '00080', '1023', '1024', '8000', '54544', '65535', ' 1024', 'bad']
    const options: NonNullable<PreviewInput[5]>[] = [{}, { protocol: 'udp', maxListeners: 0 }, { protocol: 'tcp', maxListeners: 1 }, { protocol: 'udp', maxListeners: 64 }, { protocol: 'tcp', maxListeners: 65535 }]
    for (let index = 0; index < 300; index++) {
      const value = scope(), excluded = random(3) ? scope() : '', local = locals[random(locals.length)]
      const reserved = [bases[random(bases.length)] + random(16), 0, -1, 65536, 8000.5]
      for (const mode of ['connect', 'share'] as const) {
        for (const option of options) referenceCase([value, excluded, local, mode, reserved, option])
      }
    }
  })

  it('preserves arbitrary input order, duplicate ranges, and input objects while subtracting', () => {
    const ranges = [{ start: 20, end: 30 }, { start: 1, end: 10 }, { start: 8, end: 24 }, { start: 1, end: 10 }]
    const excluded = [{ start: 22, end: 26 }, { start: 5, end: 6 }, { start: 3, end: 5 }, { start: 7, end: 7 }, { start: 22, end: 26 }]
    const before = structuredClone({ ranges, excluded })
    expect(subtractPorts(ranges, excluded)).toEqual(referenceSubtractPorts(ranges, excluded))
    expect({ ranges, excluded }).toEqual(before)
    expect(subtractPorts(ranges, [])).toBe(ranges)
    const untouched = subtractPorts(ranges, [{ start: 40, end: 50 }])
    expect(untouched).toEqual(ranges)
    untouched.forEach((range, index) => expect(range).toBe(ranges[index]))
    expect(subtractPorts([], excluded)).toEqual([])
  })

  it('matches subtraction for every pair of subsets of a small domain', () => {
    const subsets = Array.from({ length: 64 }, (_, bits) => Array.from({ length: 6 }, (_, index) => index + 1).filter(port => bits & (1 << (port - 1))))
    for (const selected of subsets) {
      for (const omitted of subsets) {
        const ranges = selected.length ? parsePorts(selected.join(',')) : []
        const excluded = omitted.length ? parsePorts(omitted.join(',')) : []
        expect(subtractPorts(ranges, excluded)).toEqual(referenceSubtractPorts(ranges, excluded))
      }
    }
  })
})

describe('physical port-domain preview bounds', () => {
  const allPorts = Array.from({ length: 65535 }, (_, index) => index + 1)
  const odd = allPorts.filter(port => port % 2 === 1)
  const even = allPorts.filter(port => port % 2 === 0)
  const singletons = (ports: number[]) => ports.map(port => ({ start: port, end: port }))

  it('maps the full alternating domain sequentially with an explicit listener budget', () => {
    const exclusions = odd.join(',')
    const result = previewPorts('1-65535', exclusions, '1024', 'connect', [], { protocol: 'tcp', maxListeners: even.length })
    expect(result.ranges).toEqual(singletons(even))
    expect(result.count).toBe(32767)
    expect(result.ports).toBe(even.join(', '))
    expect(result.localPorts).toBe('1024-33790')
    expect(result.mappings).toEqual(even.map((port, index) => ({ remote: String(port), local: String(1024 + index) })))
    expect(previewPorts('1-65535', exclusions, '1024', 'connect', [], { protocol: 'udp', maxListeners: even.length })).toEqual(result)
    expect(() => previewPorts('1-65535', exclusions, '1024', 'connect', [], { maxListeners: even.length - 1 })).toThrow('too_many_ports')
  })

  it('keeps a maximally fragmented scope and exclusions distinct without imposing an interval cap', () => {
    const expected = odd.filter(port => port < 54543 || port > 54545)
    const result = previewPorts(odd.join(','), even.join(','), '', 'share', [], { protocol: 'tcp', maxListeners: 0 })
    expect(result.ranges).toEqual(singletons(expected))
    expect(result.count).toBe(32766)
    expect(result.ports).toBe(expected.join(', '))
    expect(result.localPorts).toBe(result.ports)
    expect(result.mappings).toEqual(expected.map(port => ({ remote: String(port), local: String(port) })))
  })

  it('handles reverse-ordered, duplicate dynamic reservations across the full domain', () => {
    const reserved = [...even].reverse().flatMap(port => [port, port])
    const expected = odd.filter(port => port < 54543 || port > 54545)
    const result = previewPorts('1-65535', '', '', 'share', reserved, { protocol: 'udp', maxListeners: expected.length })
    expect(result.ranges).toEqual(singletons(expected))
    expect(result.count).toBe(expected.length)
    expect(result.ports).toBe(expected.join(', '))
    expect(() => previewPorts('1-65535', '', '', 'share', reserved, { protocol: 'udp', maxListeners: expected.length - 1 })).toThrow('too_many_ports')
    expect(() => previewPorts('1-65535', '', '', 'share', allPorts)).toThrow('empty_ports')
  })

  it('rejects full-domain mapping overflow and an entirely excluded scope', () => {
    expect(() => previewPorts('1-65535', '', '1024', 'connect', [], { maxListeners: 65535 })).toThrow('invalid_mapping')
    expect(() => previewPorts('1-65535', '1-65535', '', 'connect', [], { maxListeners: 65535 })).toThrow('empty_ports')
    expect(() => previewPorts('1-65535', '1-65535', '80', 'share')).toThrow('invalid_share_mapping')
  })
})
