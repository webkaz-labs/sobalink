import { useEffect, useState } from 'react'
import type { Peer } from './api'

// Polling normally supplies fresh renders. A pending request or hidden page
// must not keep a time-limited observation looking current indefinitely.
export function useRouteClock(peers: Peer[] | undefined) {
  const [, tick] = useState(0)
  const timed = Boolean(peers?.some(peer => peer.route?.state === 'ready'))
  useEffect(() => {
    if (!timed) return
    const timer = setInterval(() => tick(value => value + 1), 1000)
    return () => clearInterval(timer)
  }, [timed])
  return Date.now()
}
