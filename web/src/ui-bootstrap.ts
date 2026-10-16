// Run once before React mounts. The fragment is a short-lived capability, never
// a route, persisted preference, log message, or automatic retry instruction.
export async function bootstrapLocalAccess(): Promise<void> {
  const fragment = window.location.hash
  if (!fragment) return
  try {
    window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search)
  } catch {
    // Do not exchange a capability while it remains in the visible URL/history.
    return
  }
  const match = /^#handoff=([A-Za-z0-9_-]{32})$/.exec(fragment)
  if (!match || window.location.protocol !== 'http:' || window.location.hostname !== '127.0.0.1' || !window.location.port || window.location.pathname !== '/' || window.location.search) return
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 10_000)
  try {
    await fetch('/api/session', {
      method: 'POST', credentials: 'same-origin', redirect: 'error', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: match[1] }), signal: controller.signal,
    })
  } catch {
    // Outcome may be uncertain: normal initial state lookup recovers a delivered
    // cookie. Otherwise the existing manual sign-in offers a fresh soba ui code.
  } finally { clearTimeout(timeout) }
}
