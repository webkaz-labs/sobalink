import { describe, expect, it } from 'vitest'
import type { ServiceConfiguration, ServiceGroup } from './api'
import { filterSavedServices } from './saved-service-navigation'

const one: ServiceConfiguration = { id: 'fixture-one', name: 'Sample web', direction: 'forward', backend: 'direct-lan', network: 'tcp', ports: '8080', peerId: 'fixture-peer', localPort: 9080, lifetime: 'finite', ttlSeconds: 60, purpose: 'other', discoverable: false }
const two: ServiceConfiguration = { id: 'fixture-two', name: '資料', direction: 'share', backend: 'mixed', network: 'udp', ports: '9000', peerIds: ['fixture-two-peer'], lifetime: 'finite', ttlSeconds: 60, purpose: 'other', discoverable: false }
const services = [one, two]
const groups: ServiceGroup[] = [{ name: 'Sample_Set', serviceIds: [one.id] }, { name: '資料一式', serviceIds: [two.id] }]

describe('saved service search', () => {
  it.each(['', ' ', '\n\t'])('treats empty search %j as all saved services without changing order', query => {
    expect(filterSavedServices(services, groups, query)).toEqual(services)
  })
  it.each(['SAMPLE', 'Sample_Set', '9080', '8080', 'fixture-one', 'direct-lan tcp', 'fixture-peer', 'Friendly device'])('matches saved names, references, ports and peer labels: %s', query => {
    expect(filterSavedServices(services, groups, query, undefined, id => id === 'fixture-peer' ? 'Friendly device' : id)).toEqual([one])
  })
  it('supports Japanese and combines search with selection without mutating definitions or groups', () => {
    const before = structuredClone({ services, groups })
    expect(filterSavedServices(services, groups, '資料一式 udp')).toEqual([two])
    expect(filterSavedServices(services, groups, '資料', [one.id])).toEqual([])
    expect(filterSavedServices(services, groups, '', [])).toEqual([])
    expect(filterSavedServices(services, groups, '', [two.id, one.id, 'removed'])).toEqual(services)
    expect({ services, groups }).toEqual(before)
  })
  it('uses literal search text and requires every term without guessing fuzzy matches', () => {
    expect(filterSavedServices(services, groups, 'Sample 9000')).toEqual([])
    expect(filterSavedServices(services, groups, '.*')).toEqual([])
    expect(filterSavedServices(services, groups, 'Sample-Set')).toEqual([])
  })
})
