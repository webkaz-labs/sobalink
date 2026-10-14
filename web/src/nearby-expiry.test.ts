import { describe, expect, it } from 'vitest'
import { nearbyReasonKey, nearbyText } from './nearby-i18n'

describe('nearby expiry guidance', () => {
  it('keeps expired discovery separate from malformed copied input', () => {
    expect(nearbyReasonKey('setup_candidate_expired')).toBe('candidateExpired')
    expect(nearbyReasonKey('setup_contact_invalid')).toBe('invalidPeer')
    expect(nearbyText('en', 'candidateExpired')).toContain('cancel its temporary session')
    expect(nearbyText('ja', 'candidateExpired')).toContain('一時的なセッションを取り消し')
    for (const locale of ['en', 'ja'] as const) {
      expect(nearbyText(locale, 'candidateExpired')).not.toContain('TTL')
      expect(nearbyText(locale, 'expired')).not.toContain('TTL')
    }
  })
  it('tells both devices to start a fresh expired session', () => {
    expect(nearbyReasonKey('setup_session_expired')).toBe('expired')
    expect(nearbyText('en', 'expired')).toContain('both devices')
    expect(nearbyText('ja', 'expired')).toContain('両端末で新しく開始')
  })
})
