import { expect, it, vi } from 'vitest'
import { newOutboxRequestID, outboxRequestTime } from './outbox-id'
import { fixtureOutbox, fixtureOutboxID } from './outbox-fixture'
import { readOutbox } from './outbox'
it('uses the explicit preparation timestamp and sixteen cryptorandom bytes', () => {
 const now = Date.parse('2026-10-12T10:00:00Z')
 const random = vi.spyOn(crypto, 'getRandomValues').mockImplementation(value => { (value as Uint8Array).fill(0xab); return value })
 expect(newOutboxRequestID(now)).toBe(`ob1_${now}_${'ab'.repeat(16)}`)
 expect(random).toHaveBeenCalledTimes(1); expect(outboxRequestTime(newOutboxRequestID(now))).toBe(now)
})
it.each([`ob1_0176022720000_${'a'.repeat(32)}`, `ob1_9999999999999_${'a'.repeat(32)}`, 'request-a', 'ob1_123456789012_abcd', `ob1_1234567890123_${'A'.repeat(32)}`, `ob1_12345678901234_${'a'.repeat(32)}`, `ob1_1234567890123_${'a'.repeat(31)}`])('rejects noncanonical durable key %s', value => {
 expect(outboxRequestTime(value)).toBeUndefined()
 expect(() => readOutbox({ ...fixtureOutbox(), requestId: value, outboxId: value })).toThrow()
})
it('bounds the deadline by key time and does not reject old retained status by current time', () => {
 const record = fixtureOutbox(), created = outboxRequestTime(fixtureOutboxID)!
 record.expiresAt = new Date(created + 7 * 86400000 + 1).toISOString()
 expect(() => readOutbox(record)).toThrow()
 record.expiresAt = new Date(created + 7 * 86400000).toISOString()
 vi.spyOn(Date, 'now').mockReturnValue(created + 90 * 86400000)
 expect(readOutbox(record).requestId).toBe(fixtureOutboxID)
})
