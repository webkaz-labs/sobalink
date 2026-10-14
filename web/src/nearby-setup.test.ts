import { describe, expect, it } from 'vitest'
import { liveRequestSetup, inspectedRequestSetup, reviewRequestSetup, contactText, idleSetup, liveSetup, reviewSetup, setupScopes } from './nearby-fixture'
import { MAX_SETUP_TEXT_BYTES, validSetupTargetAddress, readSetupContact, readSetupFingerprint, readSetupScopes, readSetupSnapshot, SETUP_VERIFY_PREFIX } from './nearby-setup'
import { readSetupRevoked, readSetupReenroll } from './nearby-setup'

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

describe('v2 bounded request protocol', () => {
  it('reads mode, full transcript, pinned preflight and outbound zero-port participant', () => {
    for (const fixture of [liveRequestSetup(), liveRequestSetup('receive'), liveRequestSetup('outbound'), inspectedRequestSetup(), reviewRequestSetup(), reviewRequestSetup('request'), reviewRequestSetup('outbound'), reviewRequestSetup('receive', 'full-sha256')]) expect(readSetupSnapshot(fixture)).toEqual(fixture)
  })
  it('does not mix legacy contacts or downgrade an explicit v2 transcript', () => {
    for (const change of [
      (s: any) => { s.version = 1 }, (s: any) => { s.verification = 'full-sha256' },
      (s: any) => { s.contactText = contactText(liveSetup().contact!) }, (s: any) => { s.contactQR = [[true]] },
      (s: any) => { s.plan.participants[0].protocol = 1 }, (s: any) => { s.plan.participants[1].mode = 'receive' },
      (s: any) => { s.request.requestId = '7'.repeat(64) }, (s: any) => { s.request.peerKey = '7'.repeat(64) },
      (s: any) => { s.request.address = '10.44.0.9:48445' }, (s: any) => { s.plan.participants[1].allow_host = true },
    ]) { const fixture = reviewRequestSetup(); change(fixture); expect(readSetupSnapshot(fixture)).toBeUndefined() }
    const encodedV2 = contactText(liveRequestSetup().contact!); expect(readSetupContact(encodedV2)).toBeUndefined()
  })
  it('rejects malformed or unbound preflight relay and no-listener contradictions', () => {
    for (const change of [
      (s: any) => { delete s.target.relayAddress }, (s: any) => { s.target.relayCertificateSHA256 = 'short' },
      (s: any) => { s.target.peerKey = '7'.repeat(64) }, (s: any) => { s.target.relayAddress = '10.44.0.9:48443' },
      (s: any) => { s.target.scopePrefix = '10.55.0.0/24' },
    ]) { const fixture = reviewRequestSetup('request'); change(fixture); expect(readSetupSnapshot(fixture)).toBeUndefined() }
    for (const change of [
      (s: any) => { s.discovery.state = 'active' }, (s: any) => { s.contact = liveRequestSetup().contact },
      (s: any) => { s.plan.participants[0].bootstrap = '10.44.0.2:48445' }, (s: any) => { s.plan.participants[0].existing_host = true },
    ]) { const fixture = reviewRequestSetup('outbound'); change(fixture); expect(readSetupSnapshot(fixture)).toBeUndefined() }
  })
  it('accepts exact private numeric fallback and rejects URL, public, loopback, mapped or zoned inputs', () => {
    for (const address of ['10.44.0.3:48445', '192.168.1.3:65535', '[fd44::3]:48445']) expect(validSetupTargetAddress(address)).toBe(true)
    for (const address of ['127.0.0.1:48445', '[::1]:48445', '8.8.8.8:48445', 'http://10.44.0.3:48445', '10.44.0.3:80', '10.44.0.3:54544', 'peer.invalid:48445', '[::ffff:10.44.0.3]:48445', '[fd44::3%eth0]:48445', '10.044.0.3:48445', '10.44.0.3:48445/path', '10.44.0.3:0']) expect(validSetupTargetAddress(address)).toBe(false)
  })
})

it('requires an exact positive safe preflight revision', () => {
  for (const revision of [undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, '1']) {
    const fixture = inspectedRequestSetup() as any
    if (revision === undefined) delete fixture.target.revision
    else fixture.target.revision = revision
    expect(readSetupSnapshot(fixture)).toBeUndefined()
  }
})


describe('removed-key generation contracts', () => {
  const record = { peerKey: '4'.repeat(64), generation: 7, verification: 'full-sha256' as const }
  const response = { kind: 'sobalink-setup-revoked', version: 1, records: [record] }
  it('reads only unique complete keys, positive safe generations and known retained bases', () => {
    expect(readSetupRevoked(response)).toEqual([record])
    for (const verification of ['unknown', 'tofu', 'full-sha256']) expect(readSetupRevoked({ ...response, records: [{ ...record, verification }] })).toBeDefined()
    for (const change of [{ generation: 0 }, { generation: -1 }, { generation: 1.5 }, { generation: Number.MAX_SAFE_INTEGER + 1 }, { generation: '7' }, { peerKey: '4'.repeat(12) }, { verification: 'verified' }, { alias: 'Same name' }]) expect(readSetupRevoked({ ...response, records: [{ ...record, ...change }] })).toBeUndefined()
    expect(readSetupRevoked({ ...response, records: [record, record] })).toBeUndefined()
    expect(readSetupRevoked({ ...response, records: [record, { ...record, peerKey: '5'.repeat(64) }] })).toHaveLength(2)
  })
  it('accepts only a newer generation of the exact key and retained basis without restored pair or trust', () => {
    const result = { kind: 'sobalink-setup-reenroll', version: 1, ...record, generation: 8, paired: false, trusted: false }
    expect(readSetupReenroll(result, record)).toEqual(result)
    for (const change of [{ generation: 7 }, { generation: 6 }, { generation: 9 }, { generation: Number.MAX_SAFE_INTEGER + 1 }, { peerKey: '5'.repeat(64) }, { verification: 'tofu' }, { paired: true }, { trusted: true }, { connected: true }]) expect(readSetupReenroll({ ...result, ...change }, record)).toBeUndefined()
  })
})
