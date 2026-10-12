import { describe, expect, it } from 'vitest'
import { readDispatchResult, type DispatchContext } from './dispatch'
const expected: DispatchContext = { processId: 'a'.repeat(64), preparationId: 'request-one', peerIds: ['peer-a', 'peer-b'], selection: { entries: [{ path: 'sample.txt', kind: 'file', size: 3 }], files: [] } }
const prepared = () => ({ version: 1, processId: expected.processId, preparationId: expected.preparationId, status: 'prepared', reviewRevision: 'b'.repeat(64), peerIds: [...expected.peerIds], entries: [{ id: '1', path: 'sample.txt', kind: 'file', size: 3, sha256: 'c'.repeat(64) }], totalBytes: 3 })
const committed = () => ({ version: 1, processId: expected.processId, preparationId: expected.preparationId, dispatchId: expected.preparationId, status: 'committed', children: [{ peerId: 'peer-a', transferId: 'child-a' }, { peerId: 'peer-b', transferId: 'child-b' }] })
describe('dispatch response binding', () => {
  it('accepts exact preparations, receipts and discard results without treating preparation as delivery', () => {
    expect(readDispatchResult(prepared(), expected).status).toBe('prepared')
    expect(readDispatchResult(committed(), expected).status).toBe('committed')
    expect(readDispatchResult({ version: 1, processId: expected.processId, preparationId: expected.preparationId, status: 'discarded' }, expected).status).toBe('discarded')
  })
  it.each([
    { processId: 'd'.repeat(64) }, { preparationId: 'other' }, { peerIds: ['peer-b', 'peer-a'] },
    { peerIds: ['peer-a', 'peer-a'] }, { totalBytes: 4 }, { reviewRevision: '' },
    { entries: [{ ...prepared().entries[0], path: 'other.txt' }] }, { entries: [{ ...prepared().entries[0], size: 4 }] },
    { entries: [{ ...prepared().entries[0], sha256: '' }] }, { entries: [] }, { status: 'delivered' },
  ])('rejects mismatched preparation %#', change => { expect(() => readDispatchResult({ ...prepared(), ...change }, expected)).toThrow() })
  it.each([
    { dispatchId: 'other' }, { children: [] }, { children: [...committed().children].reverse() },
    { children: [{ peerId: 'peer-a', transferId: 'child-a' }, { peerId: 'peer-b', transferId: 'child-a' }] },
  ])('rejects mismatched committed membership %#', change => { expect(() => readDispatchResult({ ...committed(), ...change }, expected)).toThrow() })
})
