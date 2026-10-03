import type { Locale } from './api'

export const transferEnglish = {
  removeEntry: 'Remove from batch',
  changeFolder: 'Change folder',
  batchDestinationHint: 'This folder is used for this batch only. The default folder and automatic receiving settings stay the same.',
  useDefaultFolder: 'Use default folder',
} as const
export type TransferTextKey = keyof typeof transferEnglish
export const transferJapanese: Record<TransferTextKey, string> = {
  removeEntry: 'バッチから削除',
  changeFolder: '保存先を変更',
  batchDestinationHint: 'このバッチだけに使う保存先です。既定の保存先や自動受信の設定は変わりません。',
  useDefaultFolder: '既定の保存先を使う',
}
export function transferTranslator(locale: Locale) { return (key: TransferTextKey) => (locale === 'ja' ? transferJapanese : transferEnglish)[key] }
