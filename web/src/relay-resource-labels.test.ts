import { describe, expect, it } from 'vitest'
import { policyEnglish, policyJapanese, policyLabel, policyText } from './policy-i18n'
import { routeEnglish, routeJapanese } from './route-i18n'

describe('relay resource language parity', () => {
  it('labels all adjustable budgets and next-start semantics in both languages', () => {
    for (const key of ['relayPresenceConnections', 'relayCandidateAttempts', 'relayTLSConnections', 'relayAdmissionConnections']) {
      expect(policyLabel('en', key)).not.toBe(key)
      expect(policyLabel('ja', key)).toMatch(/[ぁ-んァ-ヶ一-龠]/)
    }
    expect(policyText('en', 'relayRestart')).toContain('next network start')
    expect(policyText('ja', 'relayRestart')).toContain('次回')
    expect(Object.keys(policyEnglish).sort()).toEqual(Object.keys(policyJapanese).sort())
    expect(Object.keys(routeEnglish).sort()).toEqual(Object.keys(routeJapanese).sort())
    expect(routeEnglish.maximum).not.toContain('Up to four')
    expect(routeJapanese.maximum).not.toContain('4つまで')
  })
})
