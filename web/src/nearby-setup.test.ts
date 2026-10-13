import { describe, expect, it } from 'vitest'
import { contactText, idleSetup, liveSetup, reviewSetup, setupScopes } from './nearby-fixture'
import { MAX_SETUP_TEXT_BYTES, readSetupContact, readSetupFingerprint, readSetupScopes, readSetupSnapshot, SETUP_VERIFY_PREFIX } from './nearby-setup'

describe('bounded, typed nearby setup DTOs', () => {
  it('accepts idle, discovery, reviewed plan and exact-session recovery', () => {
    for (const fixture of [idleSetup(), liveSetup(), reviewSetup()]) expect(readSetupSnapshot(fixture)).toEqual(fixture)
    const live = liveSetup(); expect(readSetupSnapshot(live, live.sessionId)).toEqual(live)
    expect(readSetupSnapshot(live, 'f'.repeat(64))).toBeUndefined()
    expect(readSetupScopes(setupScopes)).toEqual(setupScopes.scopes)
  })
  it('rejects unknown fields, false success, invalid scope and persistent approval', () => {
    for (const mutate of [
      (s: any) => { s.secret = 'synthetic-forbidden' },
      (s: any) => { s.state = 'connected' },
      (s: any) => { s.connected = true },
      (s: any) => { s.process.approvalsSurviveRestart = true },
      (s: any) => { s.scope.localAddress = 'example.invalid' },
      (s: any) => { s.contact.alias = 'different' },
      (s: any) => { s.candidates[0].verification = 'verified' },
      (s: any) => { s.candidates = Array(65).fill(s.candidates[0]) },
    ]) { const fixture = liveSetup(); mutate(fixture); expect(readSetupSnapshot(fixture)).toBeUndefined() }
  })
  it('requires complete identities, plan revision, pinned relay and both approval facts for connected', () => {
    for (const mutate of [
      (s: any) => { s.plan.revision = 0 },
      (s: any) => { s.plan.fingerprint = '123456' },
      (s: any) => { s.plan.verificationText = SETUP_VERIFY_PREFIX + 'f'.repeat(64) },
      (s: any) => { s.plan.participants[1].key = s.plan.participants[0].key },
      (s: any) => { s.plan.relay.certificateSHA256 = 'f'.repeat(64) },
      (s: any) => { s.plan.participants[0].allow_host = false },
      (s: any) => { s.plan.participants[0].scope.prefix = '10.55.0.0/24' },
    ]) { const fixture = reviewSetup(); mutate(fixture); expect(readSetupSnapshot(fixture)).toBeUndefined() }
    const success = { ...reviewSetup(), state: 'connected' as const, paired: true, connected: true, localApproved: true, remoteApproved: true }
    expect(readSetupSnapshot(success)).toEqual(success)
    expect(readSetupSnapshot({ ...success, remoteApproved: false })).toBeUndefined()
    // Pre-existing receiving trust can be reported; setup does not create it.
    expect(readSetupSnapshot({ ...success, trusted: true })).toBeDefined()
  })
  it('parses only canonical, bounded contacts and full digests', () => {
    const fixture = liveSetup(), text = fixture.contactText!
    expect(readSetupContact(text)).toEqual(fixture.contact)
    for (const input of [text + '=', ' ' + text, 'https://example.invalid', 'x'.repeat(MAX_SETUP_TEXT_BYTES + 1), contactText({ ...fixture.contact!, alias: '\u202ePeer' }), contactText({ ...fixture.contact!, alias: 'Peer\u200bA' }), contactText({ ...fixture.contact!, bootstrap: '8.8.8.8:48445' })]) expect(readSetupContact(input)).toBeUndefined()
    expect(readSetupFingerprint('e'.repeat(64))).toBe('e'.repeat(64))
    expect(readSetupFingerprint(SETUP_VERIFY_PREFIX + 'e'.repeat(64))).toBe('e'.repeat(64))
    for (const input of ['123456', 'E'.repeat(64), '0'.repeat(64), 'e'.repeat(64) + 'x', text]) expect(readSetupFingerprint(input)).toBeUndefined()
  })
})
