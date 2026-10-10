import { describe, expect, it } from 'vitest'
import {
  createTemplate, groupReviewRevision, MAX_GROUP_EVIDENCE_BYTES, MAX_GROUP_INPUT_BYTES, MAX_GROUP_RESPONSE_BYTES,
  MAX_GROUP_REVIEW_BYTES, MAX_GROUP_SELECTION_BYTES, readApplyInput, readCancelInput, readCurrentReviewInput, readExecutionPeers,
  readGroupSelection, readGroupSettings, readPreviewInput, readRefreshInput, readSelectInput, readStatusInput,
  requireGroupByteLimit, resolveSelection, reviewCanonicalJSON, templateCanonicalJSON,
} from './canonical'
import { groupPrepared, groupReview, groupRunId, groupSelection, groupSettings, groupTemplateRevision, mutableGroup } from './fixtures.test-support'

describe('fixed group canonical data and exact inputs', () => {
  it('ports the exact Go template and review SHA-256 vectors', async () => {
    expect(templateCanonicalJSON(groupSettings)).toBe('{"schemaVersion":1,"settings":{"transferConcurrentFiles":{"mode":"default"},"transferConcurrentPerPeer":{"mode":"limited","value":3}}}')
    expect((await createTemplate(groupSettings)).revision).toBe(groupTemplateRevision)
    const review = await groupReview(1)
    expect(await groupReviewRevision(review)).toBe('2430a6298e0e188c3b9e9a3c7e6555933c54e501204737c886b3be24a3749214')
    expect(reviewCanonicalJSON(review)).not.toContain('reviewId')
    expect(reviewCanonicalJSON(review)).not.toContain('confirm')
  })
  it('canonicalizes independent peer arrays without changing a review digest', async () => {
    const review = await groupReview(3), reordered = mutableGroup(review)
    reordered.selection.members.reverse(); reordered.rows.reverse(); reordered.executionPeers.reverse()
    expect(await groupReviewRevision(reordered)).toBe(review.revision)
    const selection = mutableGroup(groupSelection(3)); selection.members.reverse()
    expect((await readGroupSelection(selection)).members.map(member => member.peerKey)).toEqual(groupSelection(3).members.map(member => member.peerKey))
    expect(selection.members[0].peerKey).toBe(groupSelection(3).members[2].peerKey)
  })
  it('preserves explicit default and equal overrides separately from inheritance', async () => {
    const selection = mutableGroup(groupSelection(3))
    selection.members[0].override = { transferConcurrentPerPeer: { mode: 'default' } }
    selection.members[1].override = { transferConcurrentPerPeer: { mode: 'limited', value: 3 } }
    const resolved = await resolveSelection(selection)
    expect(resolved.members[0].requested.transferConcurrentPerPeer).toEqual({ mode: 'default' })
    expect(resolved.members[1].requested).toEqual(resolved.members[2].requested)
    expect(resolved.members[1].override).toBeDefined(); expect(resolved.members[2].override).toBeUndefined()
    expect(Object.isFrozen(resolved.members[0].override)).toBe(true)
    expect(Object.isFrozen(resolved.members[0].requested.transferConcurrentPerPeer)).toBe(true)
  })
  it('accepts exactly 1–16 unique remote v2 peers and rejects local membership', async () => {
    expect((await readGroupSelection(groupSelection(16))).members).toHaveLength(16)
    for (const count of [0, 17]) await expect(readGroupSelection(groupSelection(count))).rejects.toThrow()
    const duplicate = mutableGroup(groupSelection()); duplicate.members[1].peerKey = duplicate.members[0].peerKey
    await expect(readGroupSelection(duplicate)).rejects.toThrow()
    const selection = groupSelection(1)
    await expect(readGroupSelection(selection, selection.members[0].peerKey)).rejects.toThrow()
    for (const update of [{ peerKey: '*' }, { peerKey: 'A'.repeat(64) }, { selector: { ...selection.members[0].selector, protocolVersion: 1 } }, { selector: { ...selection.members[0].selector, grantRevision: 0 } }, { override: {} }, { override: null }, { override: { transferConcurrentFiles: null } }]) {
      await expect(readGroupSelection({ ...selection, members: [{ ...selection.members[0], ...update }] })).rejects.toThrow()
    }
  })
  it('requires both choices and finite positive safe integers without extra or implicit fields', async () => {
    for (const value of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, '2', null]) {
      expect(() => readGroupSettings({ ...groupSettings, transferConcurrentFiles: { mode: 'limited', value } })).toThrow()
    }
    expect(readGroupSettings({ ...groupSettings, transferConcurrentFiles: { mode: 'limited', value: Number.MAX_SAFE_INTEGER } }).transferConcurrentFiles).toEqual({ mode: 'limited', value: Number.MAX_SAFE_INTEGER })
    for (const value of [{ transferConcurrentFiles: { mode: 'default' } }, { ...groupSettings, other: true }, { ...groupSettings, transferConcurrentFiles: { mode: 'unlimited' } }, { ...groupSettings, transferConcurrentFiles: { mode: 'default', value: 3 } }]) expect(() => readGroupSettings(value)).toThrow()
    const changed = mutableGroup(groupSelection()); changed.template.settings.transferConcurrentFiles = { mode: 'limited', value: 9 }
    await expect(readGroupSelection(changed)).rejects.toThrow()
    expect((await createTemplate(changed.template.settings)).revision).not.toBe(groupTemplateRevision)
  })
  it('copies and freezes draft data before asynchronous hashing', async () => {
    const input = mutableGroup(groupSelection()), promise = readGroupSelection(input)
    input.members[0].selector.grantRevision = 7
    input.template.settings.transferConcurrentFiles = { mode: 'limited', value: 8 }
    const selection = await promise
    expect(selection.members[0].selector.grantRevision).toBe(1)
    expect(selection.template.settings).toEqual(groupSettings)
    for (const value of [selection, selection.members, selection.members[0], selection.members[0].selector, selection.template, selection.template.settings]) expect(Object.isFrozen(value)).toBe(true)
  })
  it('enforces closed data properties and dense arrays without invoking accessors', async () => {
    let invoked = false
    const selection = groupSelection(1), member = { ...selection.members[0] }
    Object.defineProperty(member, 'peerKey', { enumerable: true, get: () => { invoked = true; return selection.members[0].peerKey } })
    await expect(readGroupSelection({ ...selection, members: [member] })).rejects.toThrow()
    expect(invoked).toBe(false)
    const sparse = new Array(1)
    await expect(readGroupSelection({ ...selection, members: sparse })).rejects.toThrow()
    const extra = [...selection.members] as unknown[] & { other?: boolean }; extra.other = true
    await expect(readGroupSelection({ ...selection, members: extra })).rejects.toThrow()
    await expect(readGroupSelection({ ...selection, [Symbol('hidden')]: true })).rejects.toThrow()
    await expect(readGroupSelection({ ...selection, toJSON() { throw new Error('Must not invoke toJSON') } })).rejects.toThrow()
  })
  it('bounds bytes as UTF-8 at every documented cap', () => {
    for (const limit of [MAX_GROUP_SELECTION_BYTES, MAX_GROUP_REVIEW_BYTES, MAX_GROUP_EVIDENCE_BYTES, MAX_GROUP_INPUT_BYTES, MAX_GROUP_RESPONSE_BYTES]) {
      expect(() => requireGroupByteLimit('a'.repeat(limit), limit)).not.toThrow()
      expect(() => requireGroupByteLimit('a'.repeat(limit + 1), limit)).toThrow()
      expect(() => requireGroupByteLimit('あ'.repeat(Math.floor(limit / 3)), limit)).not.toThrow()
      expect(() => requireGroupByteLimit('あ'.repeat(Math.floor(limit / 3) + 1), limit)).toThrow()
    }
  })
  it('keeps the original six command payloads exact and bounded', async () => {
    const prepared = await groupPrepared(), select = { schemaVersion: 1, reviewId: groupRunId, reviewRevision: prepared.review.revision, executionPeers: [...prepared.review.executionPeers].reverse() }
    expect((await readPreviewInput({ schemaVersion: 1, selection: groupSelection(), replaceReviewId: groupRunId })).replaceReviewId).toBe(groupRunId)
    expect(readSelectInput(select).executionPeers).toEqual(prepared.review.executionPeers)
    expect(readApplyInput({ ...select, confirm: true }).confirm).toBe(true)
    expect(readStatusInput({ schemaVersion: 1, runId: groupRunId }).runId).toBe(groupRunId)
    expect(readRefreshInput({ schemaVersion: 1, runId: groupRunId, peers: prepared.review.executionPeers }).peers).toEqual(prepared.review.executionPeers)
    expect(readCancelInput({ schemaVersion: 1, reviewId: groupRunId }).reviewId).toBe(groupRunId)
    const cases: [unknown, (value: unknown) => unknown][] = [
      [select, readSelectInput], [{ ...select, confirm: true }, readApplyInput],
      [{ schemaVersion: 1, runId: groupRunId }, readStatusInput], [{ schemaVersion: 1, runId: groupRunId, peers: prepared.review.executionPeers }, readRefreshInput],
      [{ schemaVersion: 1, reviewId: groupRunId }, readCancelInput],
    ]
    for (const [input, read] of cases) {
      expect(() => read({ ...input as object, action: 'other' })).toThrow()
      expect(() => read({ ...input as object, schemaVersion: 2 })).toThrow()
      expect(() => read(null)).toThrow()
    }
    await expect(readPreviewInput({ schemaVersion: 1, selection: groupSelection(), command: 'other' })).rejects.toThrow()
    await expect(readPreviewInput({ schemaVersion: 1, selection: groupSelection(), replaceReviewId: '' })).rejects.toThrow()
    expect(readSelectInput({ ...select, executionPeers: [] }).executionPeers).toEqual([])
    for (const confirm of [false, null, 1, undefined]) expect(() => readApplyInput({ ...select, confirm })).toThrow()
    expect(() => readApplyInput({ ...select, confirm: true, executionPeers: [] })).toThrow()
    expect(() => readRefreshInput({ schemaVersion: 1, runId: groupRunId, peers: [] })).toThrow()
    for (const peers of [null, ['*'], [prepared.review.executionPeers[0], prepared.review.executionPeers[0]], new Array(17).fill('a'.repeat(64))]) expect(() => readExecutionPeers(peers)).toThrow()
  })
  it('adds the separately reviewed current-review input with only schemaVersion', () => {
    expect(readCurrentReviewInput({ schemaVersion: 1 })).toEqual({ schemaVersion: 1 })
    for (const input of [null, {}, { schemaVersion: 2 }, { schemaVersion: 1, runId: groupRunId }, { schemaVersion: 1, reviewId: groupRunId }, { schemaVersion: 1, peerKey: '1'.repeat(64) }, { schemaVersion: 1, reset: true }]) expect(() => readCurrentReviewInput(input)).toThrow()
    expect(Object.isFrozen(readCurrentReviewInput({ schemaVersion: 1 }))).toBe(true)
  })
})
