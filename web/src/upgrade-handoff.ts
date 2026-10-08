import { createUpgradeHandoff, type Locale } from './api'
import type { UpgradeReview } from './direct-lan-upgrade'

export function validHandoffDescriptor(value: unknown, deadline: string): value is { url: string; token: string; deadline: string } {
  if (!value || typeof value !== 'object') return false
  const v = value as { url: string; token: string; deadline: string }
  if (v.deadline !== deadline || typeof v.token !== 'string' || !/^[a-f0-9]{64}$/.test(v.token)) return false
  try {
    const u = new URL(v.url)
    return u.protocol === 'http:' && u.hostname === '127.0.0.1' && Boolean(u.port) && Number(u.port) > 0 && !u.username && !u.password && !u.search && !u.hash && u.pathname === '/' && v.url === u.origin
  } catch { return false }
}

// Open only from the deliberate click. A blocked popup prevents any request.
// Neither history nor browser storage receives the one-use capability.
export function openUpgradeHandoff(review: UpgradeReview, locale: Locale, onError: () => void): () => void {
  const popup = window.open('about:blank', '_blank')
  if (!popup) { onError(); return () => {} }
  const controller = new AbortController()
  let descriptor: { url: string; token: string; deadline: string } | undefined
  let origin = '', transferred = false, closed = false
  const cleanup = () => { window.removeEventListener('message', receive); clearInterval(timer); controller.abort(); descriptor = undefined }
  const cancel = () => { closed = true; cleanup(); if (!transferred) popup.close() }
  const ended = () => { cancel(); onError() }
  const receive = (event: MessageEvent) => {
    if (closed || event.source !== popup || event.origin !== origin) return
    if (transferred) {
      if (event.data?.kind === 'sobalink-upgrade-ended') ended()
      return
    }
    if (!descriptor || event.data?.kind !== 'sobalink-upgrade-ready') return
    if (Date.parse(descriptor.deadline) <= Date.now()) { ended(); return }
    popup.postMessage({ kind: 'sobalink-upgrade-transfer', token: descriptor.token }, descriptor.url)
    transferred = true; descriptor = undefined; controller.abort()
    // Keep only the non-secret origin/deadline/window until closure or expiry.
    // This allows a fresh explicit review after declining in the popup.
  }
  const timer = setInterval(() => { if (popup.closed || Date.parse(review.deadline) <= Date.now()) ended() }, 500)
  window.addEventListener('message', receive)
  void createUpgradeHandoff({ peerId: review.peerId, deadline: review.deadline, expectedRevision: review.revision }, locale, controller.signal).then(result => {
    if (closed) return
    if (!validHandoffDescriptor(result, review.deadline) || popup.closed) throw new Error('invalid handoff')
    descriptor = result; origin = result.url
    popup.location.replace(result.url)
  }).catch(() => { if (!closed) ended() })
  return cancel
}
