import type { ResourceGroupController } from './group/controller'
import { groupContextFromSession } from './group/context'
import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from './api'
import { resourceContextFromSession } from './resource/app-context'
import type { ResourceSettingsController } from './resource/controller'
import type { ResourceCatalogController } from './catalog/controller'
import { catalogContextFromSession } from './catalog/context'

// Never evict a delivery guard to make room: Core's request cache is finite
// too. Only this page lifetime is protected; nothing here claims durable dedupe.
export const MAX_UNCERTAIN_MESSAGES = 256
export type MessageBlock = 'message_resend_blocked' | 'message_completion_interrupted' | 'message_safety_limit' | 'message_safety_unavailable'
async function messageFingerprint(peerId: string, text: string) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(JSON.stringify([peerId, text])))
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
}

export function useServer(resourceObserver?: ResourceSettingsController, catalogObserver?: ResourceCatalogController, groupObserver?: ResourceGroupController) {
  const [state, setState] = useState<api.State | null>(null)
  const [auth, setAuth] = useState<'checking' | 'locked' | 'ready'>('checking')
  const [stale, setStale] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState<Set<string>>(new Set())
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const active = useRef(new Set<string>())
  const uncertain = useRef(new Map<string, { signature: string; requestId: string }>())
  const guardedMessages = useRef(new Map<string, 'message_resend_blocked' | 'message_completion_interrupted'>())
  const pendingMessages = useRef(new Set<string>())
  const [messageGuardRevision, setMessageGuardRevision] = useState(0)
  const controller = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const authEpoch = useRef(0)
  const live = useRef(true)
  const knownState = useRef<api.State | null>(null)
  const knownStale = useRef(false)
  const resourceObserverRef = useRef(resourceObserver)
  resourceObserverRef.current = resourceObserver
  const catalogObserverRef = useRef(catalogObserver)
  catalogObserverRef.current = catalogObserver
  const groupObserverRef = useRef(groupObserver)
  groupObserverRef.current = groupObserver
  // One fixed internal observation bridge. It cannot request transport or
  // change authentication/retry policy, and never recreates polling effects.
  const observeResourceContext = useCallback(() => {
    const observedGeneration = generation.current, observedLive = live.current
    const observation = { state: knownState.current, authenticated: observedLive && knownState.current !== null,
      stale: knownStale.current, authEpoch: authEpoch.current }
    const unchanged = () => generation.current === observedGeneration && live.current === observedLive
      && authEpoch.current === observation.authEpoch && knownState.current === observation.state && knownStale.current === observation.stale
    resourceObserverRef.current?.updateContext(resourceContextFromSession(observation))
    // A subscriber can recursively clear auth. Never deliver the older context
    // to the next owner after that newer invalidation has already reached it.
    if (!unchanged()) return
    catalogObserverRef.current?.updateContext(catalogContextFromSession(observation))
    if (!unchanged()) return
    groupObserverRef.current?.updateContext(groupContextFromSession(observation))
    if (!unchanged()) return
  }, [])
  const handleError = useCallback((value: unknown) => {
    if (value instanceof api.ApiError && value.code === 'unauthenticated') {
      ++generation.current; ++authEpoch.current; controller.current?.abort(); controller.current = null
      uncertain.current.clear()
      knownState.current = null; api.setCSRFToken(''); observeResourceContext()
      setAuth('locked'); setState(null)
    } else setError(value)
  }, [observeResourceContext])
  const refresh = useCallback(async (report = false) => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const current = ++generation.current
    try {
      const next = await api.getState(request.signal)
      if (!live.current || current !== generation.current) return null
      api.setCSRFToken(next.csrfToken)
      knownState.current = next; knownStale.current = false; observeResourceContext()
      // Observation notifies synchronous controller subscribers. They may
      // invalidate this request (including auth loss) before UI publication.
      if (!live.current || current !== generation.current) return null
      setState(next); setAuth('ready'); setStale(false); setUpdatedAt(new Date())
      return next
    } catch (value) {
      if (!live.current || request.signal.aborted || current !== generation.current) return null
      if (value instanceof api.ApiError && value.code === 'unauthenticated') handleError(value)
      else {
        knownStale.current = true; observeResourceContext()
        if (!live.current || current !== generation.current) return null
        setStale(true); if (report || !knownState.current) setError(value)
      }
      return null
    }
  }, [handleError, observeResourceContext])
  useEffect(() => {
    live.current = true
    observeResourceContext()
    void refresh()
    return () => { live.current = false; ++generation.current; controller.current?.abort(); observeResourceContext() }
  }, [refresh, observeResourceContext])
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
  const messageBlock = useCallback(async (peerId: string, text: string): Promise<MessageBlock | null> => {
    if (!guardedMessages.current.size) return null
    try {
      const blocked = guardedMessages.current.get(await messageFingerprint(peerId, text))
      if (blocked) return blocked
      return guardedMessages.current.size >= MAX_UNCERTAIN_MESSAGES ? 'message_safety_limit' : null
    } catch { return 'message_safety_unavailable' }
  }, [])
  const run = useCallback(async <N extends api.CommandName>(name: N, payload: api.CommandPayloads[N], key = name as string): Promise<api.CommandResult | undefined> => {
    if (active.current.has(key)) return
    active.current.add(key); setBusy(new Set(active.current)); setError(null)
    const epoch = authEpoch.current
    let fingerprint: string | undefined
    let messageClaimed = false
    let requested = false
    // Favorites use fresh reads/review after any uncertain write. Keeping their
    // one-shot UI keys here would retain unreachable entries on repeated errors
    // and could replay an obsolete preference view. Other retry guards stay put.
    // Public-card reads have no effect to retry or retain. Keep this exact
    // allowlist separate from mutating commands and their uncertainty guards.
    const cardRead = name === 'device-card.export' || name === 'device-card.inspect'
    const retainUncertain = !name.startsWith('favorites.') && !cardRead
    try {
      if (name === 'message.send') {
        const message = payload as api.CommandPayloads['message.send']
        try { fingerprint = await messageFingerprint(message.peerId, message.text) }
        catch { throw new api.ApiError('message_safety_unavailable', '') }
        if (!live.current || epoch !== authEpoch.current) return undefined
        const blocked = guardedMessages.current.get(fingerprint)
        if (blocked) throw new api.ApiError(blocked, '')
        // Claim by peer+text, not by caller-selected UI key, before any request.
        if (pendingMessages.current.has(fingerprint)) return undefined
        if (guardedMessages.current.size + pendingMessages.current.size >= MAX_UNCERTAIN_MESSAGES) throw new api.ApiError('message_safety_limit', '')
        pendingMessages.current.add(fingerprint)
        messageClaimed = true
      }
      const signature = JSON.stringify({ name, payload })
      const previous = retainUncertain ? uncertain.current.get(key) : undefined
      const requestId = previous?.signature === signature ? previous.requestId : api.requestID()
      if (retainUncertain) uncertain.current.set(key, { signature, requestId })
      requested = true
      const result = await api.command(name, payload, requestId)
      if (!live.current || epoch !== authEpoch.current) return undefined
      if (retainUncertain) uncertain.current.delete(key)
      const refreshed = await refresh(true)
      if (!live.current || epoch !== authEpoch.current) return undefined
      // Discovery is the first step of a reviewed connection. Do not let its
      // response authorize that next step when the current snapshot is unknown.
      if (name === 'discovery.refresh' && !refreshed) return undefined
      return result
    } catch (value) {
      if (messageClaimed && fingerprint && value instanceof api.ApiError && ['message_history_unavailable', 'message_peer_storage_unavailable', 'message_completion_interrupted'].includes(value.code)) {
        guardedMessages.current.set(fingerprint, value.code === 'message_completion_interrupted' ? 'message_completion_interrupted' : 'message_resend_blocked')
        if (live.current) setMessageGuardRevision(current => current + 1)
      }
      if (retainUncertain && requested && (!(value instanceof api.ApiError) || !['network_error', 'invalid_response'].includes(value.code))) uncertain.current.delete(key)
      if (live.current && epoch === authEpoch.current) handleError(cardRead ? new api.ApiError(value instanceof api.ApiError ? value.code : 'request_failed', '') : value)
      return undefined
    }
    finally {
      if (messageClaimed && fingerprint) pendingMessages.current.delete(fingerprint)
      active.current.delete(key); if (live.current) setBusy(new Set(active.current))
    }
  }, [refresh, handleError])
  return { state, auth, stale, error, setError, busy, refresh, run, updatedAt, handleError, messageBlock, messageGuardRevision }
}
export type Server = ReturnType<typeof useServer>
