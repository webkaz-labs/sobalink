import type { Locale } from './api'

export const transferEnglish = {
  removeEntry: 'Remove from batch',
  changeFolder: 'Change folder',
  batchDestinationHint: 'This folder is used for this batch only. The default folder and automatic receiving settings stay the same.',
  useDefaultFolder: 'Use default folder',
  discardBatch: 'Discard batch…',
  discardTitle: 'Discard this batch?',
  discardHint: 'Temporary sending copies and retry history will be discarded.',
  discardImpact: 'This removes the batch from history and deletes any temporary sending copies. You cannot retry this batch afterward; select the files again to resend.',
  discardPreservedFiles: 'Your original files and any files already saved by the receiver stay in place.',
  discardConfirm: 'Discard copies and history',
} as const
export type TransferTextKey = keyof typeof transferEnglish
export const transferJapanese: Record<TransferTextKey, string> = {
  removeEntry: 'バッチから削除',
  changeFolder: '保存先を変更',
  batchDestinationHint: 'このバッチだけに使う保存先です。既定の保存先や自動受信の設定は変わりません。',
  useDefaultFolder: '既定の保存先を使う',
  discardBatch: 'バッチを破棄…',
  discardTitle: 'このバッチを破棄しますか？',
  discardHint: '送信用の一時コピーと再試行用の履歴を破棄します。',
  discardImpact: '履歴からバッチを削除し、残っている送信用の一時コピーを削除します。このバッチは再試行できなくなります。再送するにはファイルを選び直してください。',
  discardPreservedFiles: '元のファイルと、受信側ですでに保存されたファイルは残ります。',
  discardConfirm: '一時コピーと履歴を破棄',
}
export function transferTranslator(locale: Locale) { return (key: TransferTextKey) => (locale === 'ja' ? transferJapanese : transferEnglish)[key] }
