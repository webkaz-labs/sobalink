import type { Locale } from './api'
export const loginEnglish = {
  check: 'Check sign-in status', waiting: 'Waiting for sign-in', noLink: 'The sign-in link is not ready yet. Status will update while this window is open.', connected: 'This device is connected', approval: 'Device approval is required', approvalHint: 'Ask a Tailnet administrator to approve this device. Another sign-in request is not needed.', qr: 'Show sign-in QR code', hideQR: 'Hide QR code', qrAlt: 'Private Tailscale sign-in QR code', qrHint: 'Scan this code on another device to sign in. Keep the link and QR code private.', refresh: 'Request a new sign-in link', dismiss: 'Hide sign-in details',
  login_network_required: 'Activate the Tailnet network before requesting sign-in.', login_request_failed: 'The sign-in request failed. Check the network and try again.', login_url_invalid: 'The sign-in address was unexpected. Request a new official sign-in link.', login_qr_failed: 'The QR code could not be generated. Use the private sign-in link.',
} as const
export const loginJapanese: Record<keyof typeof loginEnglish, string> = {
  check: 'ログイン状態を確認', waiting: 'ログインを待っています', noLink: 'ログインリンクはまだ準備中です。この画面を開いている間、状態を更新します。', connected: 'この端末は接続済みです', approval: '端末の承認が必要です', approvalHint: 'Tailnetの管理者にこの端末の承認を依頼してください。ログインをやり直す必要はありません。', qr: 'ログイン用QRコードを表示', hideQR: 'QRコードを隠す', qrAlt: '非公開のTailscaleログイン用QRコード', qrHint: '別の端末で読み取ってログインできます。リンクとQRコードは他の人に共有しないでください。', refresh: '新しいログインリンクを取得', dismiss: 'ログイン情報を隠す',
  login_network_required: 'ログインを要求する前にTailnet接続を有効にしてください。', login_request_failed: 'ログインを要求できませんでした。接続状態を確認して再試行してください。', login_url_invalid: '想定外のログイン先です。公式のログインリンクを新しく取得してください。', login_qr_failed: 'QRコードを生成できませんでした。非公開のログインリンクを使用してください。',
}
export function loginText(locale: Locale, key: string) { return (locale === 'ja' ? loginJapanese : loginEnglish)[key as keyof typeof loginEnglish] || '' }
