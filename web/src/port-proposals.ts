import type { ServiceConfigResult, ServicePortProposals } from './api'
import { previewPorts, formatPorts, parsePorts } from './ports'
import { readServiceConfig } from './service-form'

export interface PortProposalChoice { source: ServiceConfigResult; localPort: number; checkedAt: string }
export function samePortProposalSource(left: ServiceConfigResult, right: ServiceConfigResult) {
  const keys = ['id', 'name', 'direction', 'backend', 'network', 'peerId', 'peerIds', 'ports', 'excludePorts', 'localPort', 'loopbackHost', 'lifetime', 'ttlSeconds', 'purpose', 'discoverable', 'serviceId', 'serviceRevision'] as const
  return left.revision === right.revision && keys.every(key => JSON.stringify(left.configuration[key]) === JSON.stringify(right.configuration[key]))
}
export function readPortProposals(value: unknown, source: ServiceConfigResult): ServicePortProposals {
  const result = value as ServicePortProposals
  const fail = () => { throw new Error('invalid_response') }
  if (source.active || !result || result.reservation !== false || !['listener_conflict', 'listener_proposals_exhausted'].includes(result.code) || !samePortProposalSource(readServiceConfig({ configuration: result.configuration, revision: result.revision, active: false }, source.configuration.id, 'connect'), source)) fail()
  const c = source.configuration
  const original = previewPorts(c.ports, c.excludePorts || '', c.localPort ? String(c.localPort) : '', 'connect', [], { maxListeners: 65535 })
  const integer = (n: number, min: number, max: number) => Number.isSafeInteger(n) && n >= min && n <= max
  if (typeof result.effectivePorts !== 'string' || formatPorts(parsePorts(result.effectivePorts)) !== original.ports || !integer(result.conflictPort, 1024, 65535) || !parsePorts(original.localPorts).some(range => result.conflictPort >= range.start && result.conflictPort <= range.end) || typeof result.checkedAt !== 'string' || !Number.isFinite(Date.parse(result.checkedAt)) || !integer(result.fromPort, 1024, 65535) || !integer(result.requestedCount, 1, 64512) || !integer(result.attemptBudget, 1, 64512) || !integer(result.attempts, 0, result.attemptBudget) || !integer(result.bindChecks, 1, Number.MAX_SAFE_INTEGER) || !['requested_count', 'attempt_budget', 'bind_budget', 'port_range'].includes(result.stopReason) || typeof result.nextSteps?.en !== 'string' || typeof result.nextSteps?.ja !== 'string' || !Array.isArray(result.proposals) || (result.code === 'listener_proposals_exhausted' ? result.proposals.length !== 0 : result.proposals.length < 1) || result.proposals.length > result.requestedCount || result.proposals.length > result.attempts) fail()
  const starts = new Set<number>()
  for (const proposal of result.proposals) {
    if (!proposal || !integer(proposal.localPort, result.fromPort, 65535) || !integer(proposal.localEnd, proposal.localPort, 65535) || proposal.localEnd - proposal.localPort + 1 !== original.count || starts.has(proposal.localPort)) fail()
    starts.add(proposal.localPort)
  }
  return result
}
