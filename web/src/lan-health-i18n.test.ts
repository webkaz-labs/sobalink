import { describe, expect, it } from 'vitest'
import { en, ja, errorText, translator } from './i18n'

describe('LAN relay startup diagnosis', () => {
  it.each(['en', 'ja'] as const)('localizes safe typed startup codes in %s', locale => {
    for (const code of ['lan_listener_conflict', 'lan_listener_permission_denied', 'lan_listener_capacity', 'lan_listener_address_unavailable', 'lan_start_failed'] as const) {
      const expected = (locale === 'ja' ? ja : en)[code]
      expect(errorText({ code }, translator(locale))).toBe(expected)
      expect(expected).toMatch(locale === 'ja' ? /再試行/ : /retry/)
    }
    expect((locale === 'ja' ? ja : en).lan_start_failed).toMatch(locale === 'ja' ? /未分類/ : /unclassified/)
  })
})
