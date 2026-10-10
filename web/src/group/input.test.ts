import { describe, expect, it } from 'vitest'
import * as api from '../api'
import { groupSelection } from './fixtures.test-support'
import { emptyGroupDraft, newMemberInput, parseMemberInput, selectorInput } from './input'
import { groupEnglish, groupJapanese, groupText } from './i18n'

// Compile-time-only command correlation examples. Never called or dispatched.
function correlatedCommands() {
  api.command('resource.group.review.current', { schemaVersion: 1 })
  api.command('resource.group.status', { schemaVersion: 1, runId: 'a'.repeat(32) })
  // @ts-expect-error A status selector is not an apply confirmation.
  api.command('resource.group.apply', { schemaVersion: 1, runId: 'a'.repeat(32) })
  // @ts-expect-error Current review accepts no ID or imported authority.
  api.command('resource.group.review.current', { schemaVersion: 1, reviewId: 'a'.repeat(32) })
  // @ts-expect-error Single-device inspection cannot take group membership.
  api.command('resource.remote.management.inspect', { schemaVersion: 1, selection: groupSelection() })
}
void correlatedCommands

describe('guided group input and localized labels', () => {
  it('starts with both default template fields and retains an incomplete explicit member', () => {
    expect(emptyGroupDraft()).toEqual({ template: { transferConcurrentFiles: { mode: 'default', value: '' }, transferConcurrentPerPeer: { mode: 'default', value: '' } }, members: [] })
    const member = newMemberInput(groupSelection(1).members[0].peerKey)
    expect(member.selector.protocol).toBe('2'); expect(parseMemberInput(member)).toBeNull()
  })
  it('distinguishes absent inherit overrides from explicit defaults and finite limits', () => {
    const selected = groupSelection(1).members[0], member = { ...newMemberInput(selected.peerKey), selector: selectorInput({ kind: 'remote', peerKey: selected.peerKey, selector: selected.selector }) }
    expect(parseMemberInput(member)).toEqual(selected)
    expect(parseMemberInput({ ...member, transferConcurrentFiles: { mode: 'default', value: '' } })?.override).toEqual({ transferConcurrentFiles: { mode: 'default' } })
    expect(parseMemberInput({ ...member, transferConcurrentPerPeer: { mode: 'limited', value: '3' } })?.override).toEqual({ transferConcurrentPerPeer: { mode: 'limited', value: 3 } })
  })
  it('rejects unsafe unlimited fractional malformed and mismatched selectors without dropping members', () => {
    const selected = groupSelection(1).members[0], member = { ...newMemberInput(selected.peerKey), selector: selectorInput({ kind: 'remote', peerKey: selected.peerKey, selector: selected.selector }) }
    for (const value of ['0', '-1', '1.5', 'Infinity', 'unlimited', '9007199254740992', '1e3', ' 3']) expect(parseMemberInput({ ...member, transferConcurrentFiles: { mode: 'limited', value } })).toBeNull()
    expect(parseMemberInput({ ...member, selector: { ...member.selector, peerKey: 'e'.repeat(64) } })).toBeNull()
    expect(parseMemberInput({ ...member, selector: { ...member.selector, protocol: '1' } })).toBeNull()
  })
  it('keeps English and Japanese keys aligned and preserves readable recovery and partial-result explanations', () => {
    expect(Object.keys(groupJapanese).sort()).toEqual(Object.keys(groupEnglish).sort())
    for (const key of Object.keys(groupEnglish) as (keyof typeof groupEnglish)[]) { expect(groupText('en', key).length).toBeGreaterThan(0); expect(groupText('ja', key).length).toBeGreaterThan(0) }
    expect(groupEnglish.noUnusedReview).toContain('earlier runs'); expect(groupJapanese.noUnusedReview).toContain('過去')
    expect(groupEnglish.memory).toContain('Full reload'); expect(groupJapanese.memory).toContain('再読み込み')
  })
})
