import { describe, expect, it } from 'vitest'
import { portProposalEnglish, portProposalJapanese } from './port-proposals-i18n'
import { policyLabel, policyText } from './policy-i18n'
import { readPortProposals } from './port-proposals'
import { source, proposal } from './port-proposals-fixture'
describe('complete port proposal observation decoding', () => {
  it('preserves the stable machine result including exclusion mapping', () => { expect(readPortProposals(proposal, source)).toEqual(proposal) })
  it.each([
    { reservation: true }, { revision: 'b'.repeat(64) }, { configuration: { ...source.configuration, network: 'udp' } },
    { effectivePorts: '8000-8002' }, { conflictPort: 18082 }, { checkedAt: 'invalid' }, { requestedCount: 0 },
    { attempts: 33 }, { bindChecks: -1 }, { stopReason: 'unknown' }, { proposals: [] },
    { proposals: [{ localPort: 49152, localEnd: 49152 }] }, { proposals: [{ localPort: 65535, localEnd: 65536 }] },
    { proposals: [{ localPort: 49151, localEnd: 49152 }] }, { proposals: [{ localPort: 49152, localEnd: 49153 }, { localPort: 49152, localEnd: 49153 }] },
  ])('rejects partial, mismatched or malformed data %j', patch => { expect(() => readPortProposals({ ...proposal, ...patch }, source)).toThrow() })
})

it('retains a bounded exhausted observation with no usable candidates, including no fitting starting window', () => {
  const exhausted = { ...proposal, code: 'listener_proposals_exhausted', fromPort: 65535, attempts: 0, proposals: [], stopReason: 'port_range' }
  expect(readPortProposals(exhausted, source)).toEqual(exhausted)
  expect(() => readPortProposals({ ...exhausted, proposals: proposal.proposals }, source)).toThrow()
})

it('keeps Japanese and English proposal and capacity labels aligned', () => {
  expect(Object.keys(portProposalJapanese)).toEqual(Object.keys(portProposalEnglish))
  for (const locale of ['en', 'ja'] as const) {
    for (const key of ['portProposalResults', 'portProposalAttempts', 'portProposalBinds', 'portProposalSeconds']) expect(policyLabel(locale, key)).not.toBe(key)
    for (const key of ['windows', 'proposals', 'bind checks']) expect(policyText(locale, key)).toBeTruthy()
  }
})
