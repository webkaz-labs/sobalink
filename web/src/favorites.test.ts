import { describe, expect, it } from 'vitest'
import { favoriteKey, favoriteReference, readFavorites } from './favorites'
import { favoritesEnglish, favoritesJapanese } from './favorites-i18n'

const view = { version: 1, revision: 'a'.repeat(64), entries: [{ kind: 'service', serviceId: 'sample:service_1', available: true }, { kind: 'group', groupName: '資料_Set', available: false }], durabilityUncertain: false }
describe('inert favorites reader', () => {
  it('reads only explicit reference and availability data without changing exact names', () => {
    const parsed = readFavorites(view)
    expect(parsed).toEqual(view)
    expect(parsed).not.toBe(view)
    expect(parsed.entries[1]).toEqual({ kind: 'group', groupName: '資料_Set', available: false })
    expect(favoriteReference(parsed.entries[0])).toEqual({ kind: 'service', serviceId: 'sample:service_1' })
    expect(favoriteReference(parsed.entries[1])).toEqual({ kind: 'group', groupName: '資料_Set' })
    expect(favoriteKey({ kind: 'group', groupName: 'Set' })).not.toBe(favoriteKey({ kind: 'group', groupName: 'set' }))
    expect(favoriteKey({ kind: 'service', serviceId: 'Set' })).not.toBe(favoriteKey({ kind: 'group', groupName: 'Set' }))
  })
  it.each([
    null, [], {}, { ...view, version: 2 }, { ...view, revision: 'a' }, { ...view, revision: 'A'.repeat(64) },
    { ...view, entries: null }, { ...view, entries: {} }, { ...view, entries: [null] }, { ...view, entries: [view.entries[0], view.entries[0]] },
    { ...view, durabilityUncertain: undefined }, { ...view, durabilityUncertain: 'false' }, { ...view, startup: true },
    ...[
      { kind: 'service', serviceId: '', available: true }, { kind: 'service', serviceId: ' x', available: true },
      { kind: 'service', serviceId: 'x'.repeat(129), available: true }, { kind: 'service', serviceId: 'x', available: 'true' },
      { kind: 'service', serviceId: 'x', groupName: 'Set', available: true }, { kind: 'service', serviceId: 'x', available: true, ports: '22' },
      { kind: 'group', groupName: ' Set', available: true }, { kind: 'group', groupName: 'Set ', available: true },
      { kind: 'group', groupName: 'x'.repeat(65), available: true }, { kind: 'group', groupName: 'Set', available: true, serviceIds: ['x'] },
      { kind: 'unknown', serviceId: 'x', available: true },
    ].map(entry => ({ ...view, entries: [entry] })),
  ])('rejects malformed or authority-bearing values %#', value => { expect(() => readFavorites(value)).toThrow('invalid_favorites') })
  it('keeps uncertain persistence and missing references visible, without auto-pruning', () => {
    expect(readFavorites({ ...view, durabilityUncertain: true }).durabilityUncertain).toBe(true)
    expect(readFavorites({ ...view, entries: [] }).entries).toEqual([])
    expect(readFavorites(view).entries).toHaveLength(2)
  })
  it('keeps Japanese and English interface keys aligned', () => {
    expect(Object.keys(favoritesJapanese).sort()).toEqual(Object.keys(favoritesEnglish).sort())
    expect(Object.values(favoritesJapanese).every(Boolean)).toBe(true)
  })
})
