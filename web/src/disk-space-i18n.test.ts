import { describe, expect, it } from 'vitest'
import { en, ja, errorText, transferFailureText, translator } from './i18n'
import { policyLabel } from './policy-i18n'

describe('disk-space recovery guidance', () => {
  for (const locale of ['en', 'ja'] as const) {
    it(`localizes command and transfer-file failures in ${locale}`, () => {
      const t = translator(locale)
      for (const code of ['peer_storage_unavailable', 'disk_space_low', 'disk_space_unknown', 'peer_disk_space_low', 'peer_disk_space_unknown'] as const) {
        const expected = (locale === 'ja' ? ja : en)[code]
        expect(errorText({ code }, t)).toBe(expected)
        expect(transferFailureText(code, t)).toBe(expected)
        expect(expected).toMatch(locale === 'ja' ? /再試行/ : /retry/)
      }
      expect(transferFailureText('unrelated file error', t)).toBe('unrelated file error')
      expect(policyLabel(locale, 'diskReserveBytes')).not.toBe('diskReserveBytes')
    })
  }
})
