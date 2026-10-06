export interface PortRange { start: number; end: number }
export class PortError extends Error {
  constructor(public code: 'invalid_ports' | 'too_many_ports' | 'invalid_mapping' | 'empty_ports' | 'invalid_share_mapping') { super(code) }
}
export function parsePorts(value: string): PortRange[] {
  if (!value.trim()) throw new PortError('empty_ports')
  const ranges = value.split(',').map(raw => {
    const part = raw.trim()
    if (!/^\d+(?:\s*-\s*\d+)?$/.test(part)) throw new PortError('invalid_ports')
    const [start, end = start] = part.split('-').map(Number)
    if (start < 1 || end > 65535 || end < start) throw new PortError('invalid_ports')
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
export function subtractPorts(ranges: PortRange[], excluded: PortRange[]): PortRange[] {
  if (!excluded.length) return ranges
  // Merge once instead of rescanning every surviving interval per exclusion:
  // O((E + R) log(E + 1) + K) time and O(E + K) space for K result intervals.
  // In a parsed 1..65535 scope, at most 32768 disjoint intervals can survive.
  const merged: PortRange[] = []
  for (const range of [...excluded].sort((a, b) => a.start - b.start)) {
    const previous = merged.at(-1)
    if (previous && range.start <= previous.end + 1) previous.end = Math.max(previous.end, range.end)
    else merged.push({ ...range })
  }
  const result: PortRange[] = []
  for (const range of ranges) {
    // A binary search also preserves the order of unsorted/overlapping inputs.
    let low = 0, high = merged.length
    while (low < high) {
      const middle = Math.floor((low + high) / 2)
      if (merged[middle].end < range.start) low = middle + 1
      else high = middle
    }
    let start = range.start
    for (let index = low; index < merged.length && merged[index].start <= range.end; index++) {
      const exclude = merged[index]
      if (exclude.start > start) result.push({ start, end: exclude.start - 1 })
      start = Math.max(start, exclude.end + 1)
      if (start > range.end) break
    }
    if (start <= range.end) result.push(start === range.start ? range : { start, end: range.end })
  }
  return result
}
export function formatPorts(ranges: PortRange[]) { return ranges.map(({ start, end }) => start === end ? `${start}` : `${start}-${end}`).join(', ') }
export function previewPorts(value: string, exclusions: string, local: string, mode: 'connect' | 'share', reservedPorts: number[] = [], options: { protocol?: 'tcp' | 'udp'; maxListeners?: number } = {}) {
  const excluded = exclusions.trim() ? parsePorts(exclusions) : []
  const selected = subtractPorts(parsePorts(value), excluded)
  if (mode === 'share' && local.trim() && selected.reduce((sum, range) => sum + range.end - range.start + 1, 0) !== 1) throw new PortError('invalid_share_mapping')
  let ranges = selected
  if (mode === 'share') {
    const reserved = [{ start: 54543, end: 54545 }]
    for (const port of reservedPorts) if (Number.isInteger(port) && port > 0 && port <= 65535) reserved.push({ start: port, end: port })
    ranges = subtractPorts(selected, reserved)
  }
  const count = ranges.reduce((sum, range) => sum + range.end - range.start + 1, 0)
  if (!count) throw new PortError('empty_ports')
  // Preserve the legacy bound only for snapshots without an effective budget.
  if ((mode === 'connect' || options.protocol === 'udp') && count > (options.maxListeners ?? 64)) throw new PortError('too_many_ports')
  const localPort = local.trim() ? Number(local) : undefined
  if (localPort !== undefined && (!/^\d+$/.test(local) || localPort < (mode === 'connect' ? 1024 : 1) || localPort + count - 1 > 65535)) throw new PortError('invalid_mapping')
  if (mode === 'share' && localPort !== undefined && ([54543, 54544, 54545, ...reservedPorts].includes(localPort))) throw new PortError('invalid_share_mapping')
  if (mode === 'connect' && localPort === undefined && ranges.some(range => range.start < 1024)) throw new PortError('invalid_mapping')
  let offset = 0
  const mappings = ranges.map(range => {
    const localRange = localPort === undefined ? range : { start: localPort + offset, end: localPort + offset + range.end - range.start }
    offset += range.end - range.start + 1
    return { remote: formatPorts([range]), local: formatPorts([localRange]) }
  })
  return { ranges, mappings, count, ports: formatPorts(ranges), localPorts: localPort ? formatPorts([{ start: localPort, end: localPort + count - 1 }]) : formatPorts(ranges), localPort }
}
