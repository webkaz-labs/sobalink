import type { LANStartReview, Locale, Network } from './api'

export interface NetworkResumeReview {
  revision: string
  mode: Network
  trustedDevices: number
  pairedDevices: number
  hostname: string
  backends: Network[]
  externalControlTraffic: boolean
  lan?: Pick<LANStartReview, 'relay' | 'policy' | 'preparedRelays' | 'wanCandidates'>
  directLAN?: { listen: string; prefixes: string[] }
  automaticReceivers: { peerId: string; name: string; directory: string }[]
  startupSuppressed: boolean
  savedProxiesSuppressed: boolean
  pendingStartup: string[]
}
export const resumeEnglish = {
  title: 'Resume the saved connection', review: 'Review saved connection', start: 'Resume this connection',
  hint: 'Local management is open, but the saved network is stopped. Review its scope before starting it.',
  impact: 'Starts only the saved network, with its existing peer permissions and automatic receive settings. It does not change endpoints, renew permissions or restart previous transfers.',
  suppressed: 'Automatic service connections and saved proxies remain paused for this offline-started session.',
  external: 'This connection contacts the selected external control service. Sign-in or device approval may still be needed.',
  accepted: 'Start requested. Check the current network status; peer reachability and application success are not yet confirmed.',
  trusted: 'Trusted devices', paired: 'Saved pairing records',
  details: 'Saved endpoints and permissions', receivers: 'Automatic receive destinations', none: 'None',
  backends: 'Connection methods', relay: 'Relay endpoint', listener: 'Local listener', prefixes: 'Allowed networks',
  preparedRelays: 'Prepared relay destinations', pending: 'Saved startup definitions (not started)',
  failed: 'The saved connection could not be reviewed. Refresh the status and try again.',
} as const
export const resumeJapanese: Record<keyof typeof resumeEnglish, string> = {
  title: '保存済みの接続を再開', review: '保存済みの接続を確認', start: 'この接続を再開',
  hint: 'ローカル管理画面は開いていますが、保存済みの接続は停止中です。開始前に接続範囲を確認してください。',
  impact: '保存済みの接続だけを、既存の相手の許可と自動受信設定で開始します。端点の変更、許可の延長、以前の転送の再開はしません。',
  suppressed: 'オフラインで起動した今回の本体では、サービスの自動接続と保存済みプロキシは引き続き停止したままです。',
  external: '選択した外部の制御サービスへ通信します。ログインや端末承認が必要になる場合があります。',
  accepted: '開始を要求しました。現在の接続状態を確認してください。相手への到達やアプリの動作はまだ確認できていません。',
  trusted: '信頼済みの相手', paired: '保存済みのペアリング記録',
  details: '保存済みの端点と許可', receivers: '自動受信先', none: 'なし', backends: '接続方式', relay: '中継の端点', listener: 'ローカルの待受', prefixes: '許可するネットワーク',
  preparedRelays: '準備済みの中継先', pending: '保存済みの起動定義（開始しません）', failed: '保存済みの接続を確認できませんでした。状態を更新してから再試行してください。',
}
export function resumeText(locale: Locale, key: keyof typeof resumeEnglish) { return (locale === 'ja' ? resumeJapanese : resumeEnglish)[key] }
