import type { Locale, ServiceConfiguration } from './api'

// Presentation only: matching never changes the reviewed service selection.
export function savedServicePeers(service: ServiceConfiguration): string[] {
  return service.peerIds || (service.peerId ? [service.peerId] : [])
}
const english = {
  label: 'Filter by device',
  all: 'All saved devices',
  unavailable: 'Not in the current device list',
  hint: 'Filters the list only. Selected services stay selected, including those for other devices. Choosing a group shows its full selection.',
} as const
const japanese: Record<keyof typeof english, string> = {
  label: '相手で絞り込む',
  all: '保存済みのすべての相手',
  unavailable: '現在の相手一覧にありません',
  hint: '一覧の表示だけを絞ります。他の相手のサービスも選択は保持します。グループを選ぶと全選択を表示します。',
}
export function savedPeerText(locale: Locale, key: keyof typeof english): string {
  return (locale === 'ja' ? japanese : english)[key]
}
