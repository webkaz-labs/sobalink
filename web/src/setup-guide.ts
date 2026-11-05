import type { Locale, State } from './api'

const english = {
  title: 'Connect in three steps',
  network: 'Prepare the connection',
  choose: 'Choose a device and purpose',
  check: 'Use the local endpoint',
  first: 'Choose a connection method. For Tailscale, start the connection, then complete sign-in and any device approval yourself.',
  stopped: 'Your saved network is stopped. Review and resume it without replacing its settings.',
  waiting: 'The network is not ready yet. Review its status, sign-in or approval before starting a service.',
  ready: 'The network reports ready. This does not yet confirm that a remote device or service can be reached.',
  stale: 'Current status is unavailable. Refresh before relying on the last known state or starting anything.',
  chooseHint: 'Select a device in the list, then a service purpose. Or reuse saved connections and groups after reviewing their scope.',
  checkHint: 'A ready listener is not an application test. Use the displayed localhost endpoint in your app and check the result there.',
  saved: 'Choose saved connections',
  devices: 'Choose a device',
  refresh: 'Refresh current status',
  boundary: 'No VM or administrator setup for these service connections. Local listeners stay on this device; no system-wide routing is installed.',
  tailnet: 'Tailscale setup & sign-in',
  tailnetHint: 'Start the saved or selected Tailscale connection, then sign in explicitly. Opening this view starts nothing.',
} as const
const japanese: Record<keyof typeof english, string> = {
  title: '3つの手順で接続', network: '接続を準備', choose: '相手と用途を選ぶ', check: 'ローカルの接続先を使う',
  first: '接続方式を選びます。Tailscale では接続を開始してから、自分でログインと必要な端末承認を行ってください。',
  stopped: '保存済みのネットワークは停止中です。設定を置き換えず、内容を確認して再開できます。',
  waiting: 'ネットワークはまだ準備できていません。サービスを開始する前に、状態・ログイン・承認を確認してください。',
  ready: 'ネットワークは準備完了と報告しています。相手やサービスへの到達はまだ確認していません。',
  stale: '現在の状態を取得できません。前回の状態を頼りに操作せず、まず更新してください。',
  chooseHint: '一覧から相手を選び、サービスの用途を指定します。保存済みの接続やグループも、範囲を確認して再利用できます。',
  checkHint: '待受の準備完了とアプリの動作確認は別です。表示された localhost の接続先をアプリで使い、結果を確認してください。',
  saved: '保存済みの接続を選ぶ', devices: '相手を選ぶ', refresh: '現在の状態を更新',
  boundary: 'このサービス接続に VM や管理者向けセットアップは不要です。待受はこの端末内だけで、システム全体の経路は追加しません。',
  tailnet: 'Tailscale の設定・ログイン', tailnetHint: '保存済み、または選択した Tailscale 接続を開始した後、明示的にログインします。この画面を開くだけでは開始しません。',
}
export function setupText(locale: Locale, key: keyof typeof english) { return (locale === 'ja' ? japanese : english)[key] }
export function setupNetworkStage(state: State, stale: boolean): 'stale' | 'first' | 'stopped' | 'ready' | 'waiting' {
  if (stale) return 'stale'
  if (!state.settings?.network || state.settings.network === 'none') return 'first'
  if (state.self.guidance?.code === 'network_stopped') return 'stopped'
  if (['online', 'running', 'ready'].includes(state.self.status.toLowerCase())) return 'ready'
  return 'waiting'
}
