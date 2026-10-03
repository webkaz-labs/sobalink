import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from './api'

export function useServer() {
  const [state, setState] = useState<api.State | null>(null)
  const [auth, setAuth] = useState<'checking' | 'locked' | 'ready'>('checking')
  const [stale, setStale] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<Set<string>>(new Set())
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const active = useRef(new Set<string>())
  const uncertain = useRef(new Map<string, { signature: string; requestId: string }>())
  const controller = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const authEpoch = useRef(0)
  const live = useRef(true)
  const knownState = useRef<api.State | null>(null)
  const handleError = useCallback((value: unknown) => {
    if (value instanceof api.ApiError && value.code === 'unauthenticated') {
      ++generation.current; ++authEpoch.current; controller.current?.abort(); controller.current = null
      uncertain.current.clear()
      setAuth('locked'); setState(null); knownState.current = null; api.setCSRFToken('')
    } else setError(value)
  }, [])
  const refresh = useCallback(async (report = false) => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const current = ++generation.current
    try {
      const next = await api.getState(request.signal)
      if (!live.current || current !== generation.current) return null
      api.setCSRFToken(next.csrfToken)
      knownState.current = next
      setState(next); setAuth('ready'); setStale(false); setUpdatedAt(new Date())
      return next
    } catch (value) {
      if (!live.current || request.signal.aborted || current !== generation.current) return null
      if (value instanceof api.ApiError && value.code === 'unauthenticated') handleError(value)
      else { setStale(true); if (report || !knownState.current) setError(value) }
      return null
    }
  }, [handleError])
  useEffect(() => {
    live.current = true
    void refresh()
    return () => { live.current = false; ++generation.current; controller.current?.abort() }
  }, [refresh])
  useEffect(() => {
    if (auth !== 'ready') return
    let timer: ReturnType<typeof setTimeout>
    let cancelled = false
    const tick = async () => {
      if (document.visibilityState !== 'hidden') await refresh()
      if (!cancelled) timer = setTimeout(tick, 3000)
    }
    timer = setTimeout(tick, 3000)
    const visible = () => { if (document.visibilityState === 'visible') void refresh() }
    document.addEventListener('visibilitychange', visible)
    return () => { cancelled = true; clearTimeout(timer); document.removeEventListener('visibilitychange', visible) }
  }, [auth, refresh])
  const run = useCallback(async <N extends api.CommandName>(name: N, payload: api.CommandPayloads[N], key = name as string): Promise<api.CommandResult | undefined> => {
    if (active.current.has(key)) return
    active.current.add(key); setBusy(new Set(active.current)); setError(null)
    const signature = JSON.stringify({ name, payload })
    const epoch = authEpoch.current
    const previous = uncertain.current.get(key)
    const requestId = previous?.signature === signature ? previous.requestId : api.requestID()
    uncertain.current.set(key, { signature, requestId })
    try {
      const result = await api.command(name, payload, requestId)
      if (!live.current || epoch !== authEpoch.current) return undefined
      uncertain.current.delete(key)
      await refresh(true)
      if (!live.current || epoch !== authEpoch.current) return undefined
      return result
    } catch (value) {
      if (!(value instanceof api.ApiError) || !['network_error', 'invalid_response'].includes(value.code)) uncertain.current.delete(key)
      if (live.current && epoch === authEpoch.current) handleError(value)
      return undefined
    }
    finally { active.current.delete(key); if (live.current) setBusy(new Set(active.current)) }
  }, [refresh, handleError])
  return { state, auth, stale, error, setError, busy, refresh, run, updatedAt, handleError }
}
export type Server = ReturnType<typeof useServer>
