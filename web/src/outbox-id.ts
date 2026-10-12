// Durable request keys carry a bounded admission era. Never refresh an ID
// after an upload or commit may have reached the agent.
export function outboxRequestTime(value: unknown): number | undefined {
  if (typeof value !== 'string' || !/^ob1_\d{13}_[a-f0-9]{32}$/.test(value)) return undefined
  const millis = Number(value.slice(4, 17))
  return millis >= 1_000_000_000_000 && millis + 8 * 86400000 + 5 * 60000 <= 9_999_999_999_999 ? millis : undefined
}
export function newOutboxRequestID(now = Date.now()): string {
  if (!Number.isSafeInteger(now) || !/^\d{13}$/.test(String(now)) || now < 1_000_000_000_000 || now + 8 * 86400000 + 5 * 60000 > 9_999_999_999_999) throw new RangeError('Invalid outbox creation time')
  const nonce = crypto.getRandomValues(new Uint8Array(16))
  return `ob1_${now}_${Array.from(nonce, byte => byte.toString(16).padStart(2, '0')).join('')}`
}
