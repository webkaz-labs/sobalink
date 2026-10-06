import type { DiagnosticGuidance, Locale } from './api'

export const networkGuidanceEnglish = { review_network: 'Review network setup', review_capacity: 'Review capacity settings', refresh_state: 'Refresh status' } as const
export const networkGuidanceJapanese: Record<keyof typeof networkGuidanceEnglish, string> = { review_network: '接続設定を確認', review_capacity: '容量設定を確認', refresh_state: '状態を更新' }
export function networkGuidanceAction(locale: Locale, action: keyof typeof networkGuidanceEnglish): string {
  return (locale === 'ja' ? networkGuidanceJapanese : networkGuidanceEnglish)[action]
}
export function readNetworkGuidance(value: unknown): DiagnosticGuidance | undefined {
  if (!value || typeof value !== 'object') return undefined
  const g = value as DiagnosticGuidance
  if (typeof g.code !== 'string' || !g.code || typeof g.category !== 'string' || !g.category || !['review_network', 'review_capacity', 'refresh_state', 'wait'].includes(g.action)) return undefined
  for (const text of [g.summary, g.nextSteps]) {
    if (!text || typeof text.en !== 'string' || !text.en || typeof text.ja !== 'string' || !text.ja) return undefined
  }
  return g
}
