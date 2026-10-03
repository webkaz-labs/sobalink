import { describe, expect, it } from 'vitest'
import type { ServiceConfigResult, State } from './api'
import { draftFromConfig, newServiceDraft, readServiceConfig, serviceDraftIssue, servicePayload, uniqueServiceName, validServiceName } from './service-form'
const state: State = { csrfToken: '', self: { name: 'Local', status: 'online' }, peers: [{ id: 'p', name: 'Target', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }], messages: [], transfers: [], services: [{ id: 'f', name: 'app', network: 'tcp', peerId: 'p', status: 'active' }], shares: [{ id: 's', name: 'app-2', network: 'udp', peerId: '', peerIds: ['p'], status: 'stopped' }], settings: { network: 'tailnet' } }
const record: ServiceConfigResult = { revision: 'a'.repeat(64), active: false, configuration: { id: 's', backend: 'tailnet', name: 'app-2', direction: 'share', network: 'udp', ports: '8000-8010', excludePorts: '8001,8003-8005', peerIds: ['p'], ttlSeconds: 120, purpose: 'custom', discoverable: true } }
describe('reviewed service configuration', () => {
  it('suggests bounded valid names unique across both connections and shares', () => {
    expect(uniqueServiceName('app', state)).toBe('app-3')
    expect(uniqueServiceName('app-2', state, 's')).toBe('app-2')
    expect(validServiceName(uniqueServiceName(' 🔗 / 日本語 '.repeat(20), state))).toBe(true)
    expect(newServiceDraft(state.peers[0], 'connect', state).name).toBe('connect-Target')
  })
  it('preserves complete copied scope, custom lifetime, purpose and discovery without overwrite fields', () => {
    const draft = draftFromConfig(record, { id: 's', intent: 'copy' }, state)
    expect(draft.name).toBe('app-2-2')
    expect(servicePayload(draft, 'share')).toEqual({ backend: 'tailnet', name: 'app-2-2', network: 'udp', ports: '8000-8010', excludePorts: '8001,8003-8005', peerIds: ['p'], ttlSeconds: 120, purpose: 'custom', discoverable: true })
  })
  it('adds exact replacement identity and revision only for an explicit edit', () => {
    const draft = draftFromConfig(record, { id: 's', intent: 'edit' }, state)
    expect(serviceDraftIssue(draft, state, 'share', record)).toBeNull()
    expect(servicePayload(draft, 'share')).toMatchObject({ replaceId: 's', expectedRevision: record.revision, backend: 'tailnet' })
    expect(servicePayload(draft, 'share')).not.toHaveProperty('id')
    expect(servicePayload(draft, 'share')).not.toHaveProperty('direction')
    expect(serviceDraftIssue({ ...draft, name: 'app' }, state, 'share', record)).toBe('service_name_conflict')
  })
  it('blocks active replacement, stale revisions and backend changes', () => {
    const draft = draftFromConfig(record, { id: 's', intent: 'edit' }, state)
    expect(serviceDraftIssue(draft, state, 'share', { ...record, active: true })).toBe('service_active')
    expect(serviceDraftIssue(draft, state, 'share', { ...record, revision: 'b'.repeat(64) })).toBe('service_revision_conflict')
    expect(serviceDraftIssue(draft, { ...state, settings: { network: 'lan' } }, 'share', record)).toBe('service_backend_mismatch')
    expect(serviceDraftIssue(draft, { ...state, peers: [] }, 'share', record)).toBe('missingPeers')
  })
  it('requires explicit legacy network review and never invents an original backend', () => {
    const legacy = { ...record, configuration: { ...record.configuration, backend: undefined } }
    const draft = draftFromConfig(legacy, { id: 's', intent: 'edit' }, state)
    expect(draft.source?.backend).toBe('')
    expect(serviceDraftIssue(draft, state, 'share', legacy)).toBe('legacyBackend')
    expect(serviceDraftIssue({ ...draft, legacyReviewed: true }, state, 'share', legacy)).toBeNull()
  })
  it('preserves the upstream discovered target separately from saved rule identity', () => {
    const forward: ServiceConfigResult = { ...record, configuration: { ...record.configuration, direction: 'forward', peerId: 'p', peerIds: undefined, serviceId: 'remote-rule', localPort: 1234 } }
    const draft = draftFromConfig(forward, { id: 's', intent: 'edit' }, state)
    expect(servicePayload(draft, 'connect')).toMatchObject({ serviceId: 'remote-rule', replaceId: 's', peerId: 'p', localPort: 1234 })
  })
  it('refuses partial status objects, wrong identities and invalid full-config responses', () => {
    expect(() => readServiceConfig(state.shares[0], 's', 'share')).toThrow('invalid_response')
    expect(() => readServiceConfig(record, 'other', 'share')).toThrow('invalid_response')
    expect(() => readServiceConfig(record, 's', 'connect')).toThrow('invalid_response')
    expect(() => readServiceConfig({ ...record, configuration: { ...record.configuration, ttlSeconds: undefined } }, 's', 'share')).toThrow('invalid_response')
    expect(readServiceConfig(record, 's', 'share')).toBe(record)
  })
})
