import type { Locale } from './api'

export const favoritesEnglish = {
  title: 'Favorites', intro: 'Quick links to saved services and groups. Marks do not start services, grant permission or approve startup.',
  addTarget: 'Service or group to favorite', choose: 'Choose a saved service or group', services: 'Services', groups: 'Groups', service: 'Service', group: 'Group',
  add: 'Add favorite', remove: 'Remove favorite', open: 'Select favorite', filter: 'Favorite services only', empty: 'No favorites yet. Choose a saved service or group below.',
  missing: 'Definition missing', missingHint: 'Missing references stay here until you remove their marks. Reload saved services if the definition has changed.',
  reload: 'Reload favorites', loading: 'Loading favorites…', saved: 'Favorite marks updated',
  loadFailed: 'Favorites could not be loaded. Reload to retry. Ordinary saved-service navigation is still available.',
  changeFailed: 'The change could not be confirmed. Reload favorites and review the current marks before trying again.',
  conflict: 'Favorites changed elsewhere. Reload favorites and review the current marks before trying again.',
  stale: 'The connection needs a refresh. When it is current, reload favorites before changing marks.',
  uncertain: 'These marks may not survive a restart. Review the current list; explicitly adding or removing a mark tries to save it again.',
} as const
export const favoritesJapanese: Record<keyof typeof favoritesEnglish, string> = {
  title: 'お気に入り', intro: '保存済みサービスとグループへの近道です。登録してもサービスの開始・通信許可・自動開始の承認は行いません。',
  addTarget: 'お気に入りにするサービス・グループ', choose: '保存済みサービスまたはグループを選択', services: 'サービス', groups: 'グループ', service: 'サービス', group: 'グループ',
  add: 'お気に入りに追加', remove: 'お気に入りから削除', open: 'お気に入りを選択', filter: 'お気に入りのサービスだけ表示', empty: 'お気に入りはまだありません。下から保存済みサービスまたはグループを選んでください。',
  missing: '定義なし', missingHint: '参照先がなくなっても、登録は明示的に削除するまで残ります。定義が変わった場合は保存済みサービスを再読み込みしてください。',
  reload: 'お気に入りを再読み込み', loading: 'お気に入りを読み込み中…', saved: 'お気に入りを更新しました',
  loadFailed: 'お気に入りを読み込めませんでした。再読み込みしてください。通常の保存済みサービスの操作は引き続き利用できます。',
  changeFailed: '変更を確認できませんでした。お気に入りを再読み込みし、現在の登録を確認してからもう一度お試しください。',
  conflict: '別の操作でお気に入りが変わりました。再読み込みして現在の登録を確認してからもう一度お試しください。',
  stale: '接続情報の更新が必要です。接続が回復したら、お気に入りを再読み込みしてから変更してください。',
  uncertain: '再起動後に登録が残るか確認できません。現在の一覧を確認してください。明示的に追加・削除すると保存を再試行します。',
}
export function favoriteText(locale: Locale, key: keyof typeof favoritesEnglish) { return (locale === 'ja' ? favoritesJapanese : favoritesEnglish)[key] }
