import { describe, expect, it } from 'vitest'
import { parseSelectorInput, parseSettingsInput, positiveIntegerInput, settingsInput } from './input'
import { resourceEnglish, resourceJapanese, resourceLocale, resourceText, type ResourceTextKey } from './i18n'
import { remoteSelection, settings } from './fixtures.test-support'

describe('resource inputs and first-class EN/JA labels', () => {
  it('parses only canonical positive safe integers without truncation or coercion', () => {
    expect(positiveIntegerInput('9007199254740991')).toBe(Number.MAX_SAFE_INTEGER)
    for (const value of ['', '0', '-1', '01', '1.1', '1e3', '+2', ' 2', '2 ', 'Infinity', '9007199254740992', '２']) expect(positiveIntegerInput(value)).toBeNull()
    expect(parseSettingsInput(settingsInput(settings))).toEqual(settings)
    expect(parseSettingsInput({ ...settingsInput(settings), transferConcurrentPerPeer: { mode: 'limited', value: '0' } })).toBeNull()
  })
  it('requires explicit protocol and exact remote selector fields', () => {
    const input = { peerKey: remoteSelection.peerKey, protocol: '2' as const, resourceId: remoteSelection.selector.target.resourceId, grantId: remoteSelection.selector.grantId, grantRevision: '1' }
    expect(parseSelectorInput(input)).toEqual(remoteSelection)
    expect(parseSelectorInput({ ...input, protocol: '' })).toBeNull()
    expect(parseSelectorInput({ ...input, grantRevision: '1.2' })).toBeNull()
    expect(parseSelectorInput({ ...input, grantId: input.grantId.toUpperCase() })).toBeNull()
  })
  it('aligns all bilingual keys and uses existing automatic/override/fallback policy', () => {
    expect(Object.keys(resourceJapanese)).toEqual(Object.keys(resourceEnglish))
    for (const key of Object.keys(resourceEnglish) as ResourceTextKey[]) {
      expect(resourceText('en', key)).toBe(resourceEnglish[key])
      expect(resourceText('ja', key)).toBe(resourceJapanese[key])
      expect(resourceJapanese[key].length).toBeGreaterThan(0)
    }
    expect(resourceLocale('auto', ['ja-JP'])).toBe('ja')
    expect(resourceLocale('auto', ['en-US'])).toBe('en')
    expect(resourceLocale('auto', ['unknown'])).toBe('en')
    expect(resourceLocale('en', ['ja-JP'])).toBe('en')
    expect(resourceLocale('ja', ['en-US'])).toBe('ja')
    expect(resourceJapanese.managementScope).toContain('operation.status')
  })
})
