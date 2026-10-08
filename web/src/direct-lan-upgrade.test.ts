import { describe, expect, it } from 'vitest'
import { upgradeDeadline, upgradeStates, upgradeRunning, validUpgradeDeadline, validUpgradeReview, validUpgradeStatus } from './direct-lan-upgrade'
import { upgradeText } from './upgrade-i18n'
const deadline = () => new Date(Date.now() + 240000).toISOString()
const review = () => ({ revision: 'synthetic-revision', peerId: 'synthetic-peer', deadline: deadline(), localEndpoint: '192.168.50.10:48444', peerEndpoint: '192.168.50.20:48444', scope: { family: 'ipv4', prefixes: ['192.168.50.0/24'] }, restartRequired: false, resumePreparation: false })
describe('bounded upgrade review contract', () => {
  it('emits canonical RFC3339Nano without trailing fractional zeroes', () => {
    expect(upgradeDeadline(Date.parse('2026-01-01T00:00:00.000Z'))).toBe('2026-01-01T00:05:00Z')
    expect(upgradeDeadline(Date.parse('2026-01-01T00:00:00.120Z'))).toBe('2026-01-01T00:05:00.12Z')
  })
  it('requires an absolute future deadline no more than five minutes away', () => {
    expect(validUpgradeDeadline(deadline())).toBe(true)
    expect(validUpgradeDeadline('300')).toBe(false)
    expect(validUpgradeDeadline('2026-01-01')).toBe(false)
    expect(validUpgradeDeadline(new Date(Date.now() - 1).toISOString())).toBe(false)
    expect(validUpgradeDeadline(new Date(Date.now() + 301000).toISOString())).toBe(false)
  })
  it('binds review to the exact peer and original deadline', () => {
    const r = review()
    expect(validUpgradeReview(r, r.peerId, r.deadline)).toBe(true)
    expect(validUpgradeReview(r, 'other-peer', r.deadline)).toBe(false)
    expect(validUpgradeReview(r, r.peerId, deadline() + 'x')).toBe(false)
    expect(validUpgradeReview({ ...r, scope: '' }, r.peerId, r.deadline)).toBe(false)
    expect(validUpgradeReview({ ...r, resumePreparation: true }, r.peerId, r.deadline)).toBe(false)
    expect(validUpgradeReview({ ...r, resumePreparation: true, previousDeadline: '2026-01-01T00:00:00Z' }, r.peerId, r.deadline)).toBe(true)
  })
  it('localizes every progress state and keeps backend start separate from readiness', () => {
    for (const state of upgradeStates) for (const locale of ['en', 'ja'] as const) expect(upgradeText(locale, state)).toBeTruthy()
    expect(upgradeRunning('local-confirmed')).toBe(false)
    expect(upgradeRunning('network-started')).toBe(false)
    expect(validUpgradeStatus({ state: 'network-started', peerId: 'synthetic-peer', deadline: deadline() })).toBe(true)
    expect(validUpgradeStatus({ state: 'invented-success' })).toBe(false)
    expect(validUpgradeStatus({ state: 'idle' })).toBe(true)
  })
})
