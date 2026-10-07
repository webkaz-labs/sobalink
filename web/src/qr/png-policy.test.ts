import { describe, expect, it } from 'vitest'
import { pngPolicy, preflightBudget, validPngPolicy, PNG_RESOURCE_DEFAULTS, PNG_RESOURCE_MAXIMUMS } from './png-policy'

describe('finite configurable PNG admission policy', () => {
  it('uses explicit finite defaults and accounts extra encoded storage without a heap claim', () => {
    const policy = pngPolicy()!
    expect(policy).toEqual({ maxEncodedBytes: 8388608, maxPixels: 4194304, maxWorkingBytes: 201326592, deadlineMs: 10000 })
    expect(preflightBudget(policy, 1024)).toMatchObject({ fixedDecoderBytes: 23249135, decoderScratchBytes: 33556480 })
    expect(validPngPolicy(policy)).toBe(true)
  })
  it.each(Object.keys(PNG_RESOURCE_DEFAULTS) as (keyof typeof PNG_RESOURCE_DEFAULTS)[])('enforces %s without unlimited/wrapping/clamping', key => {
    for (const value of [0, -1, NaN, Infinity, 0.1, PNG_RESOURCE_MAXIMUMS[key] + 1]) expect(pngPolicy({ [key]: { mode: 'limited', value } })).toBeUndefined()
    expect(pngPolicy({ [key]: { mode: 'unlimited' } })).toBeUndefined()
    expect(pngPolicy({ [key]: { mode: 'limited', value: PNG_RESOURCE_MAXIMUMS[key] } })).toBeDefined()
    expect(pngPolicy({ [key]: { mode: 'limited', value: 1 } })).toBeDefined()
  })
  it('rejects extra or missing worker policy fields', () => {
    expect(validPngPolicy({ ...pngPolicy(), extra: true })).toBe(false)
    expect(validPngPolicy({ deadlineMs: 1000 })).toBe(false)
  })
})
