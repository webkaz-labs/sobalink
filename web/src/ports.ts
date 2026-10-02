export interface PortRange { start: number; end: number }
export class PortError extends Error {
  constructor(public code: 'invalid_ports' | 'too_many_ports' | 'invalid_mapping' | 'empty_ports') { super(code) }
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
  let result = ranges
  for (const exclude of excluded) {
    result = result.flatMap(range => {
      if (exclude.end < range.start || exclude.start > range.end) return [range]
      return [...(exclude.start > range.start ? [{ start: range.start, end: exclude.start - 1 }] : []), ...(exclude.end < range.end ? [{ start: exclude.end + 1, end: range.end }] : [])]
    })
  }
  return result
}
export function formatPorts(ranges: PortRange[]) { return ranges.map(({ start, end }) => start === end ? `${start}` : `${start}-${end}`).join(', ') }
export function previewPorts(value: string, exclusions: string, local: string, mode: 'connect' | 'share', reservedPorts: number[] = []) {
  const excluded = exclusions.trim() ? parsePorts(exclusions) : []
  if (mode === 'share') {
    excluded.push({ start: 54543, end: 54545 })
    for (const port of reservedPorts) if (Number.isInteger(port) && port > 0 && port <= 65535) excluded.push({ start: port, end: port })
  }
  const ranges = subtractPorts(parsePorts(value), excluded)
  const count = ranges.reduce((sum, range) => sum + range.end - range.start + 1, 0)
  if (!count) throw new PortError('empty_ports')
  if (mode === 'connect' && count > 64) throw new PortError('too_many_ports')
  const localPort = local.trim() ? Number(local) : undefined
  if (localPort !== undefined && (!/^\d+$/.test(local) || localPort < 1 || localPort + count - 1 > 65535 || ranges.length !== 1)) throw new PortError('invalid_mapping')
  return { ranges, count, ports: formatPorts(ranges), localPorts: localPort ? formatPorts([{ start: localPort, end: localPort + count - 1 }]) : formatPorts(ranges), localPort }
}
