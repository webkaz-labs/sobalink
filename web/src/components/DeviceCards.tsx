import { useEffect, useRef, useState, type FormEvent } from 'react'
import type { Locale } from '../api'
import type { Server } from '../useServer'
import { cardDigest, MAX_CARD_INPUT_BYTES, readCardExport, readCardFile, readCardInput, readCardInspection, validCardName, type DeviceCardExport, type DeviceCardInspection, type DeviceCardMode } from '../device-cards'
import { deviceCardText, type DeviceCardTextKey } from '../device-card-i18n'
import { Button, useAlive } from './ui'
import './DeviceCards.css'

type Props = {
  server: Server; locale: Locale; mode: DeviceCardMode; publicKey?: string
  binding: string; disabled: boolean; canUseRecipient: boolean
  onUseRecipient: (recipient: { publicKey: string; name: string }) => void
}
// Closing, changed setup, or newer navigation destroys all card drafts and
// asynchronous work. Only the explicit Use action touches an existing draft.
export function DeviceCards(props: Props) {
  const [open, setOpen] = useState(false), [used, setUsed] = useState(false)
  const c = (key: DeviceCardTextKey) => deviceCardText(props.locale, key)
  const boundary = JSON.stringify([props.mode, props.publicKey, props.binding, props.disabled, props.canUseRecipient, props.server.auth, props.server.stale])
  return <section className="device-cards" aria-label={c('title')}>
    <Button type="button" variant="ghost" aria-expanded={open} onClick={() => { setUsed(false); setOpen(value => !value) }}>{c(open ? 'close' : 'open')}</Button>
    {used && <p role="status" className="scope-note">{c('used')}</p>}
    {open && <CardPanel key={boundary} {...props} onUseRecipient={recipient => { setUsed(true); props.onUseRecipient(recipient) }} disabled={props.disabled || props.server.stale || props.server.auth !== 'ready'} />}
  </section>
}
function CardPanel({ server, locale, mode, publicKey, disabled, canUseRecipient, onUseRecipient }: Props) {
  const c = (key: DeviceCardTextKey) => deviceCardText(locale, key)
  const alive = useAlive(), sequence = useRef(0), pending = useRef(false)
  const field = useRef<HTMLTextAreaElement>(null), reviewRegion = useRef<HTMLDivElement>(null)
  const [busy, setBusy] = useState(false)
  const [name, setName] = useState(''), [wantQR, setWantQR] = useState(false), [input, setInput] = useState('')
  const [error, setError] = useState<DeviceCardTextKey>(), [notice, setNotice] = useState<DeviceCardTextKey>()
  const [exported, setExported] = useState<DeviceCardExport>(), [review, setReview] = useState<{ input: string; generation: number; data: DeviceCardInspection }>()
  useEffect(() => { if (review) reviewRegion.current?.focus() }, [review])
  const invalidate = () => { ++sequence.current; setExported(undefined); setReview(undefined); setError(undefined); setNotice(undefined) }
  const current = (generation: number) => alive.current && sequence.current === generation
  const run = async (action: (generation: number) => Promise<void>) => {
    if (disabled || pending.current) return
    pending.current = true; setBusy(true); setError(undefined); setNotice(undefined)
    const generation = ++sequence.current
    try { await action(generation) }
    catch { if (current(generation)) setError('failed') }
    finally { pending.current = false; if (alive.current) setBusy(false) }
  }
  const exportCard = (event: FormEvent) => {
    event.preventDefault()
    if (disabled || pending.current || !publicKey || exported) return
    setExported(undefined); setReview(undefined)
    if (!validCardName(name)) { invalidate(); setError('invalidAlias'); return }
    void run(async generation => {
      const result = await server.run('device-card.export', { mode, name, includeEndpointHint: false, ...(wantQR ? { qr: true } : {}) }, `device-card.export:${mode}`)
      if (!current(generation)) return
      if (!result) { setError('failed'); return }
      const card = readCardExport(result.result, mode, name, publicKey, wantQR)
      if (!card) { setError('invalidResponse'); return }
      setExported(card)
    })
  }
  const inspect = (event: FormEvent) => {
    event.preventDefault()
    if (disabled || pending.current || review) return
    setReview(undefined); setExported(undefined)
    const parsed = readCardInput(input, mode)
    if (typeof parsed === 'string') { invalidate(); setInput(''); setError(parsed); return }
    void run(async generation => {
      const digest = await cardDigest(parsed.text)
      if (!current(generation)) return
      const result = await server.run('device-card.inspect', { card: parsed.text, expectedMode: mode }, `device-card.inspect:${mode}`)
      if (!current(generation)) return
      if (!result) { setError('failed'); return }
      const data = readCardInspection(result.result, parsed.card, digest)
      if (!data) { setError('invalidResponse'); return }
      setReview({ input, generation, data })
    })
  }
  const useRecipient = () => {
    if (disabled || busy || !canUseRecipient || !review || !current(review.generation) || review.input !== input) return
    const { publicKey, name } = review.data
    invalidate(); setInput('')
    onUseRecipient({ publicKey, name })
  }
  const selectFile = (file: File | undefined) => {
    if (!file || disabled) return
    invalidate(); setInput('')
    if (file.size > MAX_CARD_INPUT_BYTES) { setError('tooLarge'); return }
    const generation = sequence.current
    void readCardFile(file).then(value => {
      if (!current(generation)) return
      const parsed = readCardInput(value, mode)
      if (typeof parsed === 'string') { setError(parsed); return }
      setInput(parsed.text)
    }).catch(error => { if (current(generation)) setError(error instanceof Error && error.message === 'tooLarge' ? 'tooLarge' : 'invalid') })
  }
  const copy = async () => {
    if (!exported || disabled) return
    const generation = sequence.current
    try {
      if (!navigator.clipboard?.writeText) throw new Error()
      await navigator.clipboard.writeText(exported.card)
      if (current(generation)) setNotice('copied')
    } catch {
      if (current(generation)) { setNotice('copyFailed'); field.current?.focus(); field.current?.select() }
    }
  }
  const save = () => {
    if (!exported || disabled) return
    let url: string | undefined
    try {
      url = URL.createObjectURL(new Blob([exported.card], { type: 'text/plain;charset=utf-8' }))
      const link = document.createElement('a')
      link.href = url; link.download = `sobalink-${mode}-device-card.txt`
      document.body.appendChild(link)
      try { link.click() } finally { link.remove() }
    } catch { setNotice('downloadFailed') }
    finally { if (url) URL.revokeObjectURL(url) }
  }
  return <div className="form-stack device-card-panel">
    <p className="small muted">{c('intro')}</p><p className="small muted">{c('privacy')}</p>
    <p className="small"><strong>{c('mode')}:</strong> {c(mode)}</p>
    {error && <p role="alert" className="scope-note">{c(error)}</p>}
    {notice && <p role="status" className="scope-note">{c(notice)}</p>}
    <form className="form-stack" onSubmit={exportCard} aria-label={c('exportTitle')}>
      <label className="field">{c('alias')}<input value={name} onChange={event => { invalidate(); setName(event.target.value) }} maxLength={80} autoComplete="off" /><small className="muted">{c('aliasHint')}</small></label>
      <label className="checkbox-field"><input type="checkbox" checked={wantQR} onChange={event => { invalidate(); setWantQR(event.target.checked) }} disabled={disabled} />{c('qr')}</label>
      {!publicKey && <p className="small muted">{c('identity')}</p>}
      <Button type="submit" disabled={disabled || !publicKey || !name || Boolean(exported)} busy={busy}>{c('export')}</Button>
    </form>
    {exported && <div className="form-stack">
      <label className="field">{c('exported')}<textarea ref={field} className="code-value" value={exported.card} readOnly rows={4} spellCheck={false} /></label>
      <div className="device-card-actions"><Button type="button" disabled={disabled} onClick={copy}>{c('copy')}</Button><Button type="button" disabled={disabled} onClick={save}>{c('download')}</Button></div>
      {exported.qr && <figure className="device-card-qr"><svg role="img" aria-label={c('qrAlt')} viewBox={`0 0 ${exported.qr.length} ${exported.qr.length}`} shapeRendering="crispEdges"><rect width="100%" height="100%" fill="#fff" /><path fill="#000" d={exported.qr.flatMap((row, y) => row.flatMap((pixel, x) => pixel ? [`M${x} ${y}h1v1h-1z`] : [])).join('')} /></svg></figure>}
    </div>}
    <form className="form-stack" onSubmit={inspect} aria-label={c('importTitle')}>
      <label className="field">{c('input')}<textarea value={input} onChange={event => {
        invalidate()
        if (event.target.value.length > MAX_CARD_INPUT_BYTES || new TextEncoder().encode(event.target.value).byteLength > MAX_CARD_INPUT_BYTES) { setInput(''); setError('tooLarge') }
        else setInput(event.target.value)
      }} rows={4} autoComplete="off" spellCheck={false} className="code-value private-copy" /></label>
      <label className="field">{c('file')}<input type="file" accept="text/plain,.txt" disabled={disabled} onChange={event => { const file = event.target.files?.[0]; event.target.value = ''; selectFile(file) }} /></label>
      <p className="small muted">{c('fallback')}</p>
      <Button type="submit" disabled={disabled || !input || Boolean(review)} busy={busy}>{c('inspect')}</Button>
    </form>
    {review && <div className="invitation-card device-card-review" role="region" aria-label={c('review')} tabIndex={-1} ref={reviewRegion}>
      <h3>{c('review')}</h3><p className="scope-note">{c('unverified')} · {c('unknown')}</p>
      <dl><dt>{c('name')}</dt><dd>{review.data.name}</dd><dt>{c('key')}</dt><dd className="code-value">{review.data.publicKey}</dd>
        {(review.data.endpoint || review.data.relay) && <><dt>{c('endpoint')}</dt><dd className="code-value">{review.data.endpoint || review.data.relay?.address}</dd></>}
        {review.data.relay && <><dt>{c('pin')}</dt><dd className="code-value">{review.data.relay.certificateSHA256}</dd></>}
      </dl><p className="small muted">{c('warning')}</p><p className="small muted">{c('impact')}</p>
      {!canUseRecipient && <p className="scope-note">{c('unavailable')}</p>}
      <div className="device-card-actions"><Button type="button" onClick={invalidate}>{c('cancel')}</Button><Button type="button" variant="primary" disabled={disabled || busy || !canUseRecipient} onClick={useRecipient}>{c('use')}</Button></div>
    </div>}
  </div>
}
