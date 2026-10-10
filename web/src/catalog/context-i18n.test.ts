import { describe, expect, it } from 'vitest'
import { catalogContextFromSession } from './context'
import { resourceContextFromSession } from '../resource/app-context'
import { deferred, harness, remotePreview, remoteSelection, response, selectRemote } from '../resource/fixtures.test-support'
import { appState } from './fixtures.test-support'
import { catalogEnglish, catalogJapanese, catalogText } from './i18n'
const observation = { state: appState, authenticated: true, stale: false, authEpoch: 1 }
describe('catalog context and bilingual boundaries', () => {
  it('requires authenticated fresh state with an opaque boot token rather than a PID alone', () => { expect(catalogContextFromSession(observation).available).toBe(true); expect(catalogContextFromSession({ ...observation, state: { ...appState, resourceCatalogProcessId: undefined } }).available).toBe(false); expect(catalogContextFromSession({ ...observation, authenticated: false }).available).toBe(false); expect(catalogContextFromSession({ ...observation, stale: true }).available).toBe(false) })
  it('changes revision for auth process backend local key and saved peer membership but not labels', () => {
    const original = catalogContextFromSession(observation).revision
    for (const next of [{ ...observation, authEpoch: 2 }, { ...observation, state: { ...appState, resourceCatalogProcessId: '9'.repeat(64) } }, { ...observation, state: { ...appState, settings: { network: 'tailnet' as const } } }, { ...observation, state: { ...appState, directLAN: { ...appState.directLAN!, publicKey: '9'.repeat(64) } } }, { ...observation, state: { ...appState, peers: [] } }]) expect(catalogContextFromSession(next).revision).not.toBe(original)
    expect(catalogContextFromSession({ ...observation, state: { ...appState, peers: appState.peers.map(peer => ({ ...peer, name: 'Changed synthetic label' })) } }).revision).toBe(original)
  })
  it('uses finite safe fallback budgets and explicit browser ceilings', () => { expect(catalogContextFromSession(observation).budget).toEqual({ rows: 128, bytes: 1024 * 1024 }); const state = { ...appState, limits: { effective: { resources: { pageEntries: { mode: 'limited' as const, value: Number.MAX_SAFE_INTEGER }, pageBytes: { mode: 'limited' as const, value: Infinity } } }, usage: { materializedListeners: 0 } } }; expect(catalogContextFromSession({ ...observation, state }).budget).toEqual({ rows: 8192, bytes: 1024 * 1024 }) })
  it('invalidates W1 review on same-PID boot token change while retaining ambiguous attempts', async () => {
    const pending = deferred(), h = harness(call => call.name === 'resource.remote.management.apply' ? pending.promise : response(call))
    h.controller.updateContext(resourceContextFromSession(observation)); h.controller.open(); await selectRemote(h.controller); await h.controller.preview()
    expect(h.controller.getSnapshot().review?.preview.operationId).toBe(remotePreview.operationId)
    const apply = h.controller.confirmApply()
    h.controller.updateContext(resourceContextFromSession({ ...observation, state: { ...appState, resourceCatalogProcessId: '9'.repeat(64) } }))
    expect(h.controller.getSnapshot().review).toBeNull(); expect(h.controller.getSnapshot().selection).toBeNull()
    h.controller.select(remoteSelection)
    expect(h.controller.getSnapshot().attempts).toHaveLength(1); expect(h.controller.getSnapshot().blocked).toBe(true)
    pending.resolve(response(h.calls[h.calls.length - 1])); await apply
    expect(h.controller.getSnapshot().blocked).toBe(true); expect(h.controller.getSnapshot().attempts[0].evidence).toBeUndefined()
  })
  it('represents absent or malformed boot tokens explicitly without substituting PID or nonce', () => {
    const current = resourceContextFromSession(observation).selectionRevision
    const missing = resourceContextFromSession({ ...observation, state: { ...appState, resourceCatalogProcessId: undefined } }).selectionRevision
    const invalid = resourceContextFromSession({ ...observation, state: { ...appState, resourceCatalogProcessId: 'not-a-boot-token' } }).selectionRevision
    expect(missing).not.toBe(current); expect(invalid).toBe(missing); expect(catalogContextFromSession({ ...observation, state: { ...appState, resourceCatalogProcessId: 'not-a-boot-token' } }).available).toBe(false)
  })
  it('keeps scoped EN and JA key sets aligned with English fallback', () => { expect(Object.keys(catalogJapanese).sort()).toEqual(Object.keys(catalogEnglish).sort()); expect(Object.values(catalogJapanese).every(value => value.length > 0)).toBe(true); expect(catalogText('ja', 'title')).toBe('リソース'); expect(catalogText('en', 'title')).toBe('Resources') })
})
