import type { Locale } from './api'
export const en = {
  appTagline: 'A little closer, wherever you are.', devices: 'Devices', all: 'All', nearby: 'Nearby', tailnet: 'Tailnet', lan: 'LAN',
  searchDevices: 'Find a device', addDevice: 'Set up network', settings: 'Preferences', back: 'Back to devices',
  language: 'Language', automatic: 'Automatic', appearance: 'Appearance', system: 'System', light: 'Light', dark: 'Dark',
  online: 'Online', offline: 'Offline', verified: 'Identity verified', unverified: 'Identity unverified', trusted: 'Trusted here',
  notTrusted: 'Permission needed', direct: 'Direct', relay: 'Relayed', unknown: 'Path unknown', ordinaryPeer: 'Tailscale device',
  bridgePeer: 'sobalink device', noDevices: 'Your devices will appear here', noDevicesHint: 'Connect a network to find devices. Choose a device to send a message, share files, or reach a service.',
  noMatches: 'No matching devices', noMatchesHint: 'Try another name or network.', selectDevice: 'Make room for a connection',
  selectDeviceHint: 'Choose a device on the left. Messages, files, and shared services stay together in one place.',
  chooseNetwork: 'Choose how to connect', networkHint: 'Activate only the network you want to use. Each device grants its own communication permissions.',
  tailnetHint: 'Reach devices on your Tailscale network, including ordinary Tailscale services.',
  lanHint: 'Find nearby sobalink devices and pair with their verified identity.', disabledNetwork: 'No network active',
  activate: 'Activate', disconnect: 'Disconnect network', deviceName: 'This device name', signInTailscale: 'Sign in to Tailscale',
  continueSignIn: 'Continue sign-in', signInLinkHint: 'Open the official sign-in page. This page refreshes after you finish.',
  localAccess: 'Your private connection space', localAccessHint: 'Enter the local access code shown by sobalink to open this device’s controls.',
  accessCode: 'Local access code', unlock: 'Open sobalink', codeHint: 'Find this code in the terminal where sobalink is running.',
  loginSecurity: 'This control page stays on this device. Your code is not saved in the browser.',
  introMessages: 'A quick hello. A useful note.', introMessagesHint: 'Send text when you choose to, with every conversation tied to a device.',
  introFiles: 'Files travel together.', introFilesHint: 'Review a whole batch, then send. Accept received files in one step.',
  introServices: 'Your services, within reach.', introServicesHint: 'Connect selected ports with a clear scope and an expiry.',
  checking: 'Connecting to sobalink…', refresh: 'Refresh', retry: 'Retry', close: 'Close', cancel: 'Cancel', save: 'Save',
  stale: 'Connection to this device was interrupted. Displayed status may be out of date.', reconnect: 'Reconnect',
  sessionExpired: 'Your local session ended. Enter the current access code to continue.', loading: 'Loading…', working: 'Working…',
  network_error: 'Could not reach sobalink. Check that it is running, then refresh. A submitted action may still have completed.',
  invalid_response: 'The response could not be read. Refresh to check the current status.', request_failed: 'The request was not accepted. Check the details and try again.',
  upload_failed: 'The batch could not be staged. Check its status before trying again.', unauthenticated: 'Enter the current local access code to continue.',
  invalid_code: 'That code was not accepted. Use the current code shown by sobalink.', invalid_ports: 'Use ports from 1 to 65535, separated by commas or a range such as 8000-8004.',
  empty_ports: 'Choose at least one port after exclusions.', too_many_ports: 'A connection can open at most 64 local ports. Choose a smaller range.',
  invalid_mapping: 'A remapped start needs one contiguous range, and all mapped ports must stay within 1-65535.',
  unsafe_path: 'One selected path cannot be safely transferred. Rename it or choose another file.', too_many_files: 'This selection exceeds the batch entry limit.',
  too_large: 'This selection exceeds the batch size limit.', unreadable: 'The browser could not read an item. Select it again.', duplicate_path: 'Two items have the same path. Select them as separate batches.',
  details: 'Device details', connection: 'Connection', identity: 'Identity', fingerprint: 'Fingerprint', address: 'Address', networks: 'Networks',
  trustDevice: 'Allow communication', revokeTrust: 'Revoke permission', trustHint: 'Allow messages and file offers from this exact identity. The other device must separately allow yours.',
  needsTrust: 'Allow communication with this device to send messages and files.', needsVerification: 'This identity is not verified. Reconnect before allowing communication.',
  needsOnline: 'This device is offline. New messages and files can be sent when it reconnects.', noBridge: 'Connect to services on this Tailscale device. Messages and files need sobalink on both devices.',
  noConversation: 'Start with a hello', noConversationHint: 'Send a message or drop in a few files. Nothing is sent until you choose Send.',
  messagePlaceholder: 'Write a message…', send: 'Send', sendMessage: 'Send message', composerHint: 'Ctrl / ⌘ + Enter to send · Enter for a new line',
  attach: 'Attach files', attachFolder: 'Attach folder', pasteHint: 'Pasted text stays in this box until you send it. Pasted images are added to the file preview.',
  dropHere: 'Drop files or folders here', preview: 'Review your batch', sendBatch: 'Send batch', to: 'To', items: 'items', files: 'files', folders: 'folders',
  total: 'Total', remove: 'Remove', folderLimit: 'Folder selection may omit empty folders. Drag folders here to preserve them when your browser supports it.',
  batchLimit: 'Batch limit', noSelection: 'No readable files were selected.', staging: 'Preparing batch on this device', stagingHint: 'This is local upload progress. Delivery appears in the conversation after the offer is accepted.',
  cancelledUpload: 'Local preparation stopped. Refresh to check whether a batch offer was already created.', awaitingAcceptance: 'Waiting for acceptance',
  incomingOffer: 'Would like to send you', outgoingOffer: 'You shared', acceptBatch: 'Accept batch', decline: 'Decline', cancelTransfer: 'Cancel transfer', retryTransfer: 'Retry transfer',
  offered: 'Offered', 'awaiting-acceptance': 'Waiting for acceptance', queued: 'Queued', transferring: 'Transferring', saving: 'Saving', completed: 'Saved', cancelled: 'Cancelled', failed: 'Failed', declined: 'Declined',
  sent: 'Sent', received: 'Received', pending: 'Pending', showFiles: 'View files', storedAt: 'Saved to', transferDetails: 'Transfer details',
  autosave: 'Receive automatically', autosaveHint: 'Opt in for this trusted device only. Future file offers will be accepted into your chosen directory.',
  receiveDirectory: 'Receive directory', directoryPlaceholder: 'An existing directory on this device', enableAutosave: 'Enable for this device', pause: 'Pause', resume: 'Resume', revoke: 'Turn off',
  paused: 'Paused', enabled: 'Enabled', off: 'Off', autosaveScope: 'Only this device · files only · no automatic opening',
  acceptHint: 'Accepting saves the entire batch to your configured receive directory. Files are never opened automatically.',
  services: 'Services', connectService: 'Connect to a service', shareService: 'Share a service', stop: 'Stop',
  serviceIntro: 'Choose the exact ports and lifetime. Application access still depends on the service and network policy.',
  shareIntro: 'Share only services on this device’s loopback address with the devices you select.',
  ruleName: 'Connection name', ruleNamePlaceholder: 'A name you will recognize', protocol: 'Protocol', ports: 'Ports', excludePorts: 'Exclude ports',
  portsPlaceholder: '8000, 8080-8090', localStart: 'Local starting port', samePorts: 'Same as selected ports', expiry: 'Expires after',
  minutes15: '15 minutes', hour1: '1 hour', hours4: '4 hours', hours24: '24 hours', advanced: 'More options', purpose: 'Purpose',
  generic: 'General', web: 'Web app', ssh: 'SSH', desktop: 'Remote desktop', discoverable: 'Show this service to selected devices',
  discoverableHint: 'Shares protocol, public port and expiry. Local paths and rule names stay private.',
  previewScope: 'Connection preview', selectedDevices: 'Selected devices', local: 'This device', remote: 'Remote device',
  listeners: 'local ports', scope: 'Scope', loopbackOnly: 'Local loopback only', selectedPeersOnly: 'Selected devices only',
  mapping: 'Port mapping', startConnection: 'Start connection', startSharing: 'Start sharing', appUnverified: 'Application not verified',
  ordinaryConnection: 'The remote device only needs Tailscale and its service. It does not need sobalink.',
  noServices: 'No connections yet', active: 'Active', stopped: 'Stopped', saved: 'Saved configuration', expired: 'Expired', endpoint: 'Endpoint', expiresAt: 'Expires',
  noExpiry: 'No expiry reported', unavailable: 'Unavailable', choosePeers: 'Choose at least one device.',
  trustRevoked: 'Permission changes are reflected after the device confirms them.',
  refreshFailed: 'Could not refresh status.', noStoredPath: 'No saved location reported.', directoryRequired: 'Choose a receive directory.',
  emptyMessage: 'Write a message first.', messageLimit: 'Messages can contain up to 16 KiB of text. Shorten this message or send it as a text file.', actionAccepted: 'Request accepted. Checking current status…',
  lastUpdated: 'Updated', preferencesHint: 'Language and appearance update immediately. Save to keep these preferences and the receive directory on this device.', selectionChanged: 'The selected device changed. Review this batch’s recipient before sending.',
  allNetworks: 'All networks', closeDetails: 'Close device details', openDetails: 'Open device details', byteUnit: 'B',
  availableServices: 'Available services', manualPorts: 'Enter ports manually', useService: 'Use this service', forgetTransfer: 'Remove from history', forgetHint: 'Saved files stay in place.', destinationHint: 'Choose an existing directory on this device for this batch.', settingsSaved: 'Preferences saved',
  reconnecting: 'Reconnecting', networkReady: 'Network ready', starting: 'Starting', loginRequired: 'Sign-in required', nameHint: '1–64 letters, digits, hyphens or underscores; start with a letter or digit', hostnameHint: 'Letters, digits and hyphens; start with a letter or digit (up to 63 characters)', reservedPorts: 'Ports 54543–54545 and reported internal ports are always excluded. The backend confirms the final scope.', csrf: 'The local session token changed. Refresh the page, then retry.', local_only: 'Open the exact local address printed by sobalink.', origin: 'Open this page directly using the local address printed by sobalink.', busy: 'sobalink is busy. Wait a moment and refresh.',
  peerPaused: 'Messages and file transfers with this device are paused. Resume to send or receive again.', pauseScope: 'Pause stops messages and file transfers in both directions for this device. Existing service connections are separate.', pausePeer: 'Pause messages and files', resumePeer: 'Resume messages and files', samePortShare: 'Shared ports map to the same ports on this device’s loopback address.',
  serviceUnavailable: 'This service is no longer advertised. Choose another service or review its ports manually.', serviceUnavailableChoice: 'Selected service is unavailable',
} as const
export type TextKey = keyof typeof en
export const ja: Record<TextKey, string> = {
  appTagline: '離れていても、すぐそばに。', devices: 'デバイス', all: 'すべて', nearby: '近く', tailnet: 'Tailnet', lan: 'LAN',
  searchDevices: 'デバイスを検索', addDevice: 'ネットワークを設定', settings: '表示設定', back: 'デバイス一覧へ',
  language: '言語', automatic: '自動', appearance: '外観', system: 'システム', light: 'ライト', dark: 'ダーク',
  online: 'オンライン', offline: 'オフライン', verified: '接続先の識別情報を確認済み', unverified: '識別情報が未確認', trusted: 'この端末で許可済み',
  notTrusted: '通信の許可が必要', direct: '直接接続', relay: '中継接続', unknown: '経路が未確認', ordinaryPeer: 'Tailscale デバイス',
  bridgePeer: 'sobalink デバイス', noDevices: 'デバイスがここに表示されます', noDevicesHint: 'ネットワークに接続するとデバイスを探せます。相手を選んで、メッセージやファイルの送信、サービスへの接続ができます。',
  noMatches: '一致するデバイスがありません', noMatchesHint: '名前やネットワークの条件を変えてみてください。', selectDevice: 'つながる相手を選びましょう',
  selectDeviceHint: '左の一覧からデバイスを選ぶと、メッセージ、ファイル、共有サービスをひとつの場所で確認できます。',
  chooseNetwork: '接続方法を選択', networkHint: '使うネットワークだけを有効にします。通信の許可はそれぞれのデバイスで行います。',
  tailnetHint: 'Tailscale ネットワークのデバイスや、通常の Tailscale サービスに接続します。',
  lanHint: '近くの sobalink デバイスを見つけ、識別情報を確認してペアリングします。', disabledNetwork: 'ネットワーク未接続',
  activate: '有効にする', disconnect: 'ネットワークを切断', deviceName: 'このデバイスの名前', signInTailscale: 'Tailscale にサインイン',
  continueSignIn: 'サインインに進む', signInLinkHint: '公式のサインインページを開きます。完了後、この画面の状態が更新されます。',
  localAccess: 'あなたの接続スペース', localAccessHint: 'sobalink に表示されたローカルアクセスコードを入力すると、このデバイスを操作できます。',
  accessCode: 'ローカルアクセスコード', unlock: 'sobalink を開く', codeHint: 'sobalink を実行しているターミナルにコードが表示されています。',
  loginSecurity: '操作画面はこのデバイス内だけで使います。コードはブラウザーに保存しません。',
  introMessages: 'ひとことも、大切なメモも。', introMessagesHint: '送るタイミングは自分で選択。デバイスごとの会話にまとめます。',
  introFiles: 'ファイルは、まとめて。', introFilesHint: '内容を確認して一括送信。受信も一度の操作で。',
  introServices: '必要なサービスを、手元に。', introServicesHint: 'ポート、相手、有効期限を選んで接続します。',
  checking: 'sobalink に接続中…', refresh: '更新', retry: '再試行', close: '閉じる', cancel: 'キャンセル', save: '保存',
  stale: 'このデバイスとの接続が途切れました。表示は最新ではない可能性があります。', reconnect: '再接続',
  sessionExpired: 'ローカルセッションが終了しました。現在のアクセスコードを入力してください。', loading: '読み込み中…', working: '処理中…',
  network_error: 'sobalink に接続できません。起動していることを確認し、更新してください。送信した操作は完了している可能性があります。',
  invalid_response: '応答を読み取れませんでした。更新して現在の状態を確認してください。', request_failed: '操作は受け付けられませんでした。内容を確認してやり直してください。',
  upload_failed: 'バッチを準備できませんでした。状態を確認してから再試行してください。', unauthenticated: '現在のローカルアクセスコードを入力してください。',
  invalid_code: 'コードが確認できません。sobalink に表示されている現在のコードを入力してください。', invalid_ports: '1〜65535 のポートを、カンマ区切りや 8000-8004 の形式で入力してください。',
  empty_ports: '除外後にポートが1つ以上残るようにしてください。', too_many_ports: '1つの接続で開けるローカルポートは64個までです。範囲を狭めてください。',
  invalid_mapping: '開始ポートを変更する場合は連続した1つの範囲を指定し、変更後も1〜65535に収めてください。',
  unsafe_path: '安全に転送できないパスが含まれています。名前を変更するか、別のファイルを選んでください。', too_many_files: 'バッチ内の項目数が上限を超えています。',
  too_large: 'バッチの容量が上限を超えています。', unreadable: 'ブラウザーが項目を読み取れませんでした。選び直してください。', duplicate_path: '同じパスの項目が重複しています。別々のバッチで送ってください。',
  details: 'デバイスの詳細', connection: '接続', identity: '識別情報', fingerprint: 'フィンガープリント', address: 'アドレス', networks: 'ネットワーク',
  trustDevice: '通信を許可', revokeTrust: '許可を取り消す', trustHint: 'この識別情報からのメッセージとファイルの受信を許可します。相手側でも、このデバイスの許可が必要です。',
  needsTrust: 'メッセージやファイルを送るには、このデバイスとの通信を許可してください。', needsVerification: '識別情報が未確認です。再接続してから通信を許可してください。',
  needsOnline: 'このデバイスはオフラインです。再接続後にメッセージやファイルを送信できます。', noBridge: 'この Tailscale デバイスのサービスに接続できます。メッセージとファイルには、両方のデバイスで sobalink が必要です。',
  noConversation: 'ひとことから、はじめましょう', noConversationHint: 'メッセージを書いたり、ファイルをドロップしたり。「送信」を選ぶまで相手には送られません。',
  messagePlaceholder: 'メッセージを入力…', send: '送信', sendMessage: 'メッセージを送信', composerHint: 'Ctrl / ⌘ + Enter で送信 · Enter で改行',
  attach: 'ファイルを添付', attachFolder: 'フォルダーを添付', pasteHint: '貼り付けた文章は送信するまで入力欄に残ります。画像は送信前のプレビューに追加されます。',
  dropHere: 'ファイルやフォルダーをドロップ', preview: '送信内容を確認', sendBatch: 'まとめて送信', to: '送信先', items: '項目', files: 'ファイル', folders: 'フォルダー',
  total: '合計', remove: '削除', folderLimit: 'フォルダー選択では空のフォルダーが含まれない場合があります。対応ブラウザーではドラッグして空のフォルダーも追加できます。',
  batchLimit: 'バッチの上限', noSelection: '読み取れるファイルが選ばれていません。', staging: 'このデバイスでバッチを準備中', stagingHint: 'これはローカルの準備状況です。相手が受信を許可すると、会話に転送状況が表示されます。',
  cancelledUpload: 'ローカルの準備を停止しました。更新して、送信の申し出が作成済みか確認してください。', awaitingAcceptance: '相手の承認待ち',
  incomingOffer: '受信の申し出', outgoingOffer: '送信したバッチ', acceptBatch: 'まとめて受信', decline: '辞退', cancelTransfer: '転送を中止', retryTransfer: '転送を再試行',
  offered: '申し出済み', 'awaiting-acceptance': '受信の承認待ち', queued: '待機中', transferring: '転送中', saving: '保存中', completed: '保存済み', cancelled: '中止済み', failed: '失敗', declined: '辞退済み',
  sent: '送信済み', received: '受信済み', pending: '処理待ち', showFiles: 'ファイルを表示', storedAt: '保存先', transferDetails: '転送の詳細',
  autosave: '自動で受信', autosaveHint: 'この許可済みデバイスだけを対象にします。今後のファイルの申し出を自動承認し、指定したフォルダーに保存します。',
  receiveDirectory: '受信フォルダー', directoryPlaceholder: 'このデバイスにある既存のフォルダー', enableAutosave: 'このデバイスから自動受信', pause: '一時停止', resume: '再開', revoke: '解除',
  paused: '一時停止中', enabled: '有効', off: '無効', autosaveScope: 'この相手だけ · ファイルのみ · 自動で開きません',
  acceptHint: '受信すると設定済みの受信フォルダーに全項目を保存します。ファイルを自動で開くことはありません。',
  services: 'サービス', connectService: 'サービスに接続', shareService: 'サービスを共有', stop: '停止',
  serviceIntro: 'ポートと有効期間を指定します。利用にはアプリの認証とネットワークの許可も必要です。',
  shareIntro: 'このデバイスのループバックアドレスにあるサービスだけを、選んだ相手と共有します。',
  ruleName: '接続の名前', ruleNamePlaceholder: 'わかりやすい名前', protocol: 'プロトコル', ports: 'ポート', excludePorts: '除外するポート',
  portsPlaceholder: '8000, 8080-8090', localStart: 'ローカルの開始ポート', samePorts: '選択したポートと同じ', expiry: '有効期間',
  minutes15: '15分', hour1: '1時間', hours4: '4時間', hours24: '24時間', advanced: '詳細オプション', purpose: '用途',
  generic: '汎用', web: 'Web アプリ', ssh: 'SSH', desktop: 'リモートデスクトップ', discoverable: '選択した相手にサービスを表示',
  discoverableHint: 'プロトコル、共有ポート、有効期限を伝えます。ローカルのパスや接続名は伝えません。',
  previewScope: '接続のプレビュー', selectedDevices: '選択したデバイス', local: 'このデバイス', remote: '接続先',
  listeners: 'ローカルポート', scope: '範囲', loopbackOnly: 'ローカルループバックのみ', selectedPeersOnly: '選択したデバイスのみ',
  mapping: 'ポートの対応', startConnection: '接続を開始', startSharing: '共有を開始', appUnverified: 'アプリの動作は未確認',
  ordinaryConnection: '相手には Tailscale と対象のサービスがあれば接続できます。sobalink は不要です。',
  noServices: '接続はまだありません', active: '有効', stopped: '停止済み', saved: '設定を保存済み', expired: '期限切れ', endpoint: '接続先', expiresAt: '有効期限',
  noExpiry: '有効期限の情報なし', unavailable: '利用できません', choosePeers: 'デバイスを1つ以上選んでください。',
  trustRevoked: '許可の変更はデバイスが処理した後に反映されます。',
  refreshFailed: '状態を更新できませんでした。', noStoredPath: '保存場所の情報がありません。', directoryRequired: '受信フォルダーを指定してください。',
  emptyMessage: 'メッセージを入力してください。', messageLimit: 'メッセージは16 KiBまでです。文章を短くするか、テキストファイルとして送ってください。', actionAccepted: '受け付けました。現在の状態を確認中…',
  lastUpdated: '更新', preferencesHint: '言語と外観はすぐに反映されます。「保存」で受信フォルダーと一緒にこのデバイスへ保存できます。', selectionChanged: '選択中のデバイスが変わりました。送信先を確認してください。',
  allNetworks: 'すべてのネットワーク', closeDetails: '詳細を閉じる', openDetails: '詳細を表示', byteUnit: 'B',
  availableServices: '利用できるサービス', manualPorts: 'ポートを手動で入力', useService: 'このサービスを使う', forgetTransfer: '履歴から削除', forgetHint: '保存したファイルは残ります。', destinationHint: 'このバッチの保存先として、このデバイスにある既存のフォルダーを指定してください。', settingsSaved: '設定を保存しました',
  reconnecting: '再接続中', networkReady: 'ネットワーク接続済み', starting: '起動中', loginRequired: 'サインインが必要', nameHint: '文字・数字で始まる1〜64文字（ハイフン・アンダースコアも使用可）', hostnameHint: '文字・数字で始まる63文字以内（ハイフンも使用可）', reservedPorts: '54543〜54545番と内部通信用のポートは常に除外します。最終的な範囲はバックエンドが確定します。', csrf: 'ローカルセッションの情報が更新されました。画面を再読み込みしてやり直してください。', local_only: 'sobalink に表示されたローカルアドレスをそのまま開いてください。', origin: 'sobalink に表示されたローカルアドレスから直接この画面を開いてください。', busy: 'sobalink は処理中です。少し待ってから更新してください。',
  peerPaused: 'この相手とのメッセージとファイル転送を一時停止しています。送受信するには再開してください。', pauseScope: '一時停止すると、この相手とのメッセージとファイル転送を双方向で止めます。サービスの接続は別に管理します。', pausePeer: 'メッセージとファイルを停止', resumePeer: 'メッセージとファイルを再開', samePortShare: '共有ポートは、このデバイスのループバックアドレスの同じ番号のポートにつながります。',
  serviceUnavailable: 'このサービスの公開情報は利用できなくなりました。別のサービスを選ぶか、ポートを手動で確認してください。', serviceUnavailableChoice: '選択したサービスは利用できません',
}
export function detectLocale(languages: readonly string[] = navigator.languages): Locale {
  for (const language of languages) {
    const match = /^(en|ja)(?:[-_]|$)/i.exec(language)
    if (match) return match[1].toLowerCase() as Locale
  }
  return 'en'
}
export function translator(locale: Locale) { return (key: TextKey) => (locale === 'ja' ? ja : en)[key] }
export type Translate = ReturnType<typeof translator>
export function networkLabel(status: string, t: Translate) {
  const value = status.toLowerCase()
  if (['running', 'online', 'ready'].includes(value)) return t('networkReady')
  if (['needslogin', 'needs-login', 'needsmachineauth'].includes(value)) return t('loginRequired')
  if (['starting', 'connecting'].includes(value)) return t('starting')
  if (['none', 'stopped', 'idle', ''].includes(value)) return t('disabledNetwork')
  return status
}
export function bytes(value: number, locale: Locale) {
  const safe = Number.isFinite(value) ? Math.max(0, value) : 0
  const index = safe ? Math.min(3, Math.floor(Math.log(safe) / Math.log(1024))) : 0
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: index ? 1 : 0 }).format(safe / 1024 ** index)} ${['B', 'KiB', 'MiB', 'GiB'][index]}`
}
export function timestamp(value: string, locale: Locale) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }).format(date)
}
export function errorText(error: unknown, t: Translate) {
  const object = error as { code?: string; message?: string }
  if (object?.message && object.code && ['upload_failed', 'request_failed', 'invalid_response', 'unavailable'].includes(object.code)) return `${t(object.code as TextKey)} ${object.message}`
  return object?.code && object.code in en ? t(object.code as TextKey) : object?.message || t('request_failed')
}
