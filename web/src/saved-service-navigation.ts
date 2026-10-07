import type { ServiceConfiguration, ServiceGroup } from './api'

// Search changes presentation only. Never normalize the stored names, IDs or group membership.
export function filterSavedServices(services: ServiceConfiguration[], groups: ServiceGroup[], query: string, selectedIds?: string[], peerName: (id: string) => string = id => id): ServiceConfiguration[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  const selected = selectedIds === undefined ? undefined : new Set(selectedIds)
  const memberships = new Map<string, string[]>()
  for (const group of groups) for (const id of group.serviceIds) {
    const names = memberships.get(id) || []
    names.push(group.name)
    memberships.set(id, names)
  }
  return services.filter(service => {
    if (selected && !selected.has(service.id)) return false
    if (!terms.length) return true
    const peers = service.peerIds || (service.peerId ? [service.peerId] : [])
    const text = [service.name, service.id, service.ports, service.localPort, service.network, service.backend, ...peers, ...peers.map(peerName), ...(memberships.get(service.id) || [])].join(' ').toLowerCase()
    return terms.every(term => text.includes(term))
  })
}
