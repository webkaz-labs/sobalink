import type { CapacityChoice } from '../api'
import type { PngPreflightBudget } from './png-preflight'

// Admission limits, not limits on WASM memory growth or browser-native memory.
// One active job per panel, no queue; every completion disposes its worker.
export const PNG_RESOURCE_DEFAULTS = Object.freeze({
  pngEncodedBytes: 8 * 1024 * 1024,
  pngPixels: 4 * 1024 * 1024,
  pngWorkingBytes: 192 * 1024 * 1024,
  pngDeadlineMilliseconds: 10000,
})
export const PNG_RESOURCE_MAXIMUMS = Object.freeze({
  pngEncodedBytes: 64 * 1024 * 1024,
  pngPixels: 16 * 1024 * 1024,
  pngWorkingBytes: 512 * 1024 * 1024,
  pngDeadlineMilliseconds: 60000,
})
export const READER_WASM_BYTES = 966895
export const READER_WASM_SHA256 = 'aecc1876de036c62c8419f67a5e1a16b1698a325bcd190aa84810d516e263931'
export type PngPolicy = Pick<PngPreflightBudget, 'maxEncodedBytes' | 'maxPixels' | 'maxWorkingBytes' | 'deadlineMs'>
const fields = { pngEncodedBytes: 'maxEncodedBytes', pngPixels: 'maxPixels', pngWorkingBytes: 'maxWorkingBytes', pngDeadlineMilliseconds: 'deadlineMs' } as const
export function pngPolicy(resources?: Record<string, CapacityChoice>): PngPolicy | undefined {
  const policy = {} as PngPolicy
  for (const key of Object.keys(fields) as (keyof typeof fields)[]) {
    const choice = resources?.[key]
    const value = !choice || choice.mode === 'default' ? PNG_RESOURCE_DEFAULTS[key] : choice.mode === 'limited' ? choice.value : undefined
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value <= 0 || value > PNG_RESOURCE_MAXIMUMS[key]) return undefined
    policy[fields[key]] = value
  }
  return policy
}
export function validPngPolicy(value: unknown): value is PngPolicy {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const data = value as Record<string, unknown>
  return Object.keys(data).length === 4 && (Object.keys(fields) as (keyof typeof fields)[]).every(key => {
    const item = data[fields[key]]
    return typeof item === 'number' && Number.isSafeInteger(item) && item > 0 && item <= PNG_RESOURCE_MAXIMUMS[key]
  })
}
export function preflightBudget(policy: PngPolicy, encodedBytes: number): PngPreflightBudget {
  return { ...policy,
    // Official initial 340 WASM pages, plus its transferred binary. WASM can
    // grow to 2 GiB; this admission allowance does not alter that maximum.
    fixedDecoderBytes: 22282240 + READER_WASM_BYTES,
    // Additional stripped PNG + Blob storage, with a provisional 32 MiB margin.
    decoderScratchBytes: 2 * encodedBytes + 32 * 1024 * 1024,
  }
}
