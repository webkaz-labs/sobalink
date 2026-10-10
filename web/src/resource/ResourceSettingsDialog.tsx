import { useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import type { Locale } from '../api'
import type { Translate } from '../i18n'
import { Button, Modal } from '../components/ui'
import { ResourceSettingsController, type CurrentObservation, type SettingsAttempt, type SettingsReview } from './controller'
import { emptySelector, parseSelectorInput, parseSettingsInput, settingsInput, type SelectorInput, type SettingsInput } from './input'
import { resourceText, type ResourceTextKey } from './i18n'
import type { RemoteSelection, ResourceSelection, SettingsValues, TransferChoice, TransferSettings } from './types'
import { useResourceSettings } from './useResourceSettings'
import './resource.css'

type Text = (key: ResourceTextKey) => string
function choiceText(choice: TransferChoice, text: Text) { return choice.mode === 'default' ? text('default') : String(choice.value) }
export function SettingsValuesView({ values, text }: { values: SettingsValues; text: Text }) {
  return <div className="resource-values">{(['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const).map(field =>
    <section key={field}><h4>{text(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')}</h4><dl>
      <dt>{text('requested')}</dt><dd>{choiceText(values.requested[field], text)}</dd>
      <dt>{text('effective')}</dt><dd>{String(values.effective[field])}</dd>
    </dl></section>)}</div>
}
function TargetDetails({ selection, text }: { selection: ResourceSelection; text: Text }) {
  return <dl className="resource-identities">
    <dt>{text('target')}</dt><dd>{text(selection.kind === 'local' ? 'local' : 'remote')}</dd>
    {selection.kind === 'remote' && <><dt>{text('peer')}</dt><dd>{selection.peerKey}</dd>
      <dt>{text('protocol')}</dt><dd>{text(selection.selector.protocolVersion === 1 ? 'inspection' : 'management')}</dd>
      <dt>{text('grantId')}</dt><dd>{selection.selector.grantId}</dd><dt>{text('grantRevision')}</dt><dd>{String(selection.selector.grantRevision)}</dd></>}
    <dt>{text('resourceId')}</dt><dd>{selection.kind === 'local' ? selection.target.resourceId : selection.selector.target.resourceId}</dd>
  </dl>
}
function Observation({ observation, text, locale }: { observation: CurrentObservation; text: Text; locale: Locale }) {
  return <section aria-label={text('lastObserved')}>
    <h3>{text('lastObserved')}</h3>
    {observation.state === 'unread' ? <p>{text('notChecked')}</p> : <>
      {observation.state === 'unconfirmed' && <p role="status">{text('unconfirmed')}</p>}
      {observation.checkedAt !== undefined && <p className="small muted"><time dateTime={new Date(observation.checkedAt).toISOString()}>{new Date(observation.checkedAt).toLocaleString(locale)}</time></p>}
      {observation.values && <SettingsValuesView values={observation.values} text={text} />}
    </>}
  </section>
}
function SettingsEditor({ initial, text, disabled, controller }: { initial: TransferSettings; text: Text; disabled: boolean; controller: ResourceSettingsController }) {
  const [input, setInput] = useState<SettingsInput>(() => settingsInput(initial))
  const id = useId()
  const change = (field: keyof SettingsInput, update: Partial<SettingsInput[keyof SettingsInput]>) => {
    const next = { ...input, [field]: { ...input[field], ...update } }
    setInput(next)
    controller.setDraft(parseSettingsInput(next))
  }
  return <fieldset disabled={disabled} className="resource-editor"><legend>{text('edit')}</legend>
    {(['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const).map(field => {
      const label = text(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')
      return <div className="resource-choice" key={field}>
        <label htmlFor={`${id}-${field}`}>{label}</label>
        <select id={`${id}-${field}`} value={input[field].mode} onChange={event => change(field, { mode: event.target.value === 'limited' ? 'limited' : 'default' })}>
          <option value="default">{text('default')}</option><option value="limited">{text('limited')}</option>
        </select>
        {input[field].mode === 'limited' && <label>{`${label}: ${text('limitValue')}`}<input type="text" inputMode="numeric" autoComplete="off" maxLength={16} value={input[field].value} aria-invalid={!parseSettingsInput(input) || undefined} onChange={event => change(field, { value: event.target.value })} /></label>}
      </div>
    })}
    {!parseSettingsInput(input) && <p role="status">{text('validNumbers')}</p>}
  </fieldset>
}
function Review({ review, text, controller }: { review: SettingsReview; text: Text; controller: ResourceSettingsController }) {
  const [confirmed, setConfirmed] = useState(false)
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => { heading.current?.focus() }, [])
  return <section className="resource-review" aria-label={text('review')}>
    <h3 ref={heading} tabIndex={-1}>{text('review')}</h3><TargetDetails selection={review.selection} text={text} />
    <h4>{text('before')}</h4>{review.previous?.values ? <SettingsValuesView values={review.previous.values} text={text} /> : <p>{text('notChecked')}</p>}
    <p>{text('oldHint')}</p><h4>{text('proposed')}</h4><SettingsValuesView values={review.preview} text={text} />
    <dl className="resource-identities"><dt>{text('operationId')}</dt><dd>{review.preview.operationId}</dd><dt>{text('baseRevision')}</dt><dd>{review.preview.baseRevision}</dd>
      <dt>{text('reviewRevision')}</dt><dd>{'reviewRevision' in review.preview ? review.preview.reviewRevision : review.preview.revision}</dd></dl>
    {review.selection.kind === 'remote' && <><p>{text('managementScope')}</p><p className="resource-warning">{text('migration')}</p></>}
    <label className="resource-confirm"><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} />{text('confirmation')}</label>
    <div className="resource-actions"><Button variant="primary" disabled={!confirmed} onClick={() => { if (confirmed) void controller.confirmApply() }}>{text('apply')}</Button>
      <Button onClick={() => controller.backToEdit()}>{text('back')}</Button><Button variant="ghost" onClick={() => controller.backToEdit()}>{text('cancelReview')}</Button></div>
  </section>
}
function Attempt({ attempt, text, controller, busy }: { attempt: SettingsAttempt; text: Text; controller: ResourceSettingsController; busy: boolean }) {
  const evidence = attempt.evidence
  return <section className="resource-attempt" aria-label={`${text('history')}: ${attempt.operationId}`}>
    <h4>{attempt.dispatch === 'rejected' ? text('rejected') : evidence ? text(evidence.outcome.status) : text('unknown')}</h4>
    <dl className="resource-identities"><dt>{text('operationId')}</dt><dd>{attempt.operationId}</dd></dl>
    <details><summary>{text('originalTarget')}</summary><TargetDetails selection={attempt.selection} text={text} /></details>
    {evidence && <><p>{text(evidence.evidenceDurable ? 'durable' : 'notDurable')}</p><dl className="resource-identities">
      <dt>{text('configuration')}</dt><dd>{evidence.outcome.configuration}</dd><dt>{text('accounting')}</dt><dd>{evidence.outcome.accounting}</dd><dt>{text('transfer')}</dt><dd>{evidence.outcome.transfer}</dd>
    </dl></>}
    {attempt.statusObservation === 'unavailable' && <p role="status">{text('statusUnavailable')}</p>}
    {attempt.statusObservation === 'unconfirmed' && <p role="status">{text('statusUnconfirmed')}</p>}
    {attempt.dispatch !== 'rejected' && <Button disabled={busy} onClick={() => void controller.checkStatus(attempt.attemptId)}>{text('checkStatus')}</Button>}
  </section>
}

export function ResourceSettingsDialog({ controller, locale, t, onClose, initialPeerKey, onAddToGroup, addToGroupLabel }: { controller: ResourceSettingsController; locale: Locale; t: Translate; onClose: () => void; initialPeerKey?: string; onAddToGroup?: (selection: RemoteSelection) => void; addToGroupLabel?: string }) {
  const snapshot = useResourceSettings(controller)
  const text: Text = key => resourceText(locale, key)
  const [mode, setMode] = useState<'local' | 'remote'>(() => snapshot.selection?.kind === 'remote' || initialPeerKey ? 'remote' : 'local')
  const [selector, setSelector] = useState<SelectorInput>(() => {
    const selected = snapshot.selection
    return selected?.kind === 'remote' ? { peerKey: selected.peerKey, protocol: selected.selector.protocolVersion === 1 ? '1' : '2', resourceId: selected.selector.target.resourceId, grantId: selected.selector.grantId, grantRevision: String(selected.selector.grantRevision) } : initialPeerKey && snapshot.peers.some(peer => peer.key === initialPeerKey) ? { ...emptySelector, peerKey: initialPeerKey, protocol: '2' } : emptySelector
  })
  const editor = useRef<HTMLDivElement>(null)
  const hadReview = useRef(false)
  const id = useId()
  useEffect(() => { controller.open(); return () => controller.close() }, [controller])
  useLayoutEffect(() => {
    if (!snapshot.authenticated || snapshot.notice === 'contextChanged') setSelector(emptySelector)
  }, [snapshot.authenticated, snapshot.notice])
  useEffect(() => {
    if (hadReview.current && !snapshot.review && snapshot.busy !== 'apply') editor.current?.querySelector<HTMLSelectElement>('select')?.focus()
    hadReview.current = Boolean(snapshot.review)
  }, [snapshot.review, snapshot.busy])
  const close = () => { controller.close(); onClose() }
  const changeMode = (value: 'local' | 'remote') => { controller.clearSelection(); setMode(value); setSelector(emptySelector) }
  const changeSelector = (update: Partial<SelectorInput>) => { controller.clearSelection(); setSelector({ ...selector, ...update }) }
  const remote = parseSelectorInput(selector)
  const selectRemote = (event: FormEvent) => {
    event.preventDefault()
    if (remote && controller.select(remote)) void controller.inspect()
  }
  const busy = snapshot.busy !== null
  const editable = snapshot.selection && (snapshot.selection.kind === 'local' || snapshot.selection.selector.protocolVersion === 2)
  return <Modal title={text('title')} t={t} onClose={close} wide><div className="resource-settings">
    {!snapshot.authenticated || !snapshot.available ? <p>{text('contextUnavailable')}</p> : <>
      <p>{text('scopeHint')}</p><p>{text('admission')}</p>
      <div className="resource-actions" aria-label={text('target')}><Button aria-pressed={mode === 'local'} onClick={() => changeMode('local')}>{text('local')}</Button><Button aria-pressed={mode === 'remote'} onClick={() => changeMode('remote')}>{text('remote')}</Button></div>
      {mode === 'local' ? <section aria-label={text('local')}>
        <Button disabled={busy} onClick={() => void controller.listLocal()}>{text('loadLocal')}</Button>
        {snapshot.resources?.length === 0 && <p>{text('empty')}</p>}
        {snapshot.resources?.map(resource => <div className="resource-local" key={resource.resourceId}><span>{resource.resourceId}</span><Button disabled={busy} onClick={() => { if (controller.select({ kind: 'local', target: { schemaVersion: 1, resourceId: resource.resourceId } })) void controller.inspect() }}>{text('selectLocal')}</Button></div>)}
      </section> : <form onSubmit={selectRemote} className="resource-selector" autoComplete="off">
        <p>{text('selectorHint')}</p>
        {!snapshot.managedDirectLAN || !snapshot.peers.length ? <p>{text('noPeers')}</p> : <>
          <label htmlFor={`${id}-peer`}>{text('peer')}</label><select id={`${id}-peer`} value={selector.peerKey} onChange={event => changeSelector({ peerKey: event.target.value })}>
            <option value="">{text('choosePeer')}</option>{snapshot.peers.map(peer => <option key={peer.key} value={peer.key}>{peer.name || peer.key}</option>)}
          </select>
          <label htmlFor={`${id}-protocol`}>{text('protocol')}</label><select id={`${id}-protocol`} value={selector.protocol} onChange={event => changeSelector({ protocol: event.target.value === '1' ? '1' : event.target.value === '2' ? '2' : '' })}>
            <option value="">{text('chooseProtocol')}</option><option value="1">{text('inspection')}</option><option value="2">{text('management')}</option>
          </select>
          {(['resourceId', 'grantId', 'grantRevision'] as const).map(field => <label key={field}>{text(field)}<input type="text" autoComplete="off" spellCheck={false} inputMode={field === 'grantRevision' ? 'numeric' : 'text'} maxLength={field === 'grantRevision' ? 16 : 32} value={selector[field]} onChange={event => changeSelector({ [field]: event.target.value })} /></label>)}
          <p className="small muted">{text('invalidSelector')}</p><Button type="submit" disabled={!remote || busy}>{text('selectRemote')}</Button>
        </>}
      </form>}
      {snapshot.notice && <p role="alert">{text(snapshot.notice)}</p>}
      {snapshot.selection && <>
        <TargetDetails selection={snapshot.selection} text={text} />
        {onAddToGroup && addToGroupLabel && snapshot.selection.kind === 'remote' && snapshot.selection.selector.protocolVersion === 2 && <Button disabled={busy} onClick={() => { const selection = controller.getSnapshot().selection; if (selection?.kind === 'remote' && selection.selector.protocolVersion === 2) onAddToGroup(selection) }}>{addToGroupLabel}</Button>}
        <Button disabled={busy} onClick={() => void controller.inspect()}>{text('refresh')}</Button>
        <Observation observation={snapshot.current} text={text} locale={locale} />
        {snapshot.attempts.length > 0 && <section aria-label={text('history')}><h3>{text('history')}</h3><p>{text('currentSeparate')}</p>
          {snapshot.attempts.map(attempt => <Attempt key={attempt.attemptId} attempt={attempt} text={text} controller={controller} busy={busy} />)}
        </section>}
        {editable ? <>
          {snapshot.blocked && <p role="status">{text(snapshot.evidenceLimitReached ? 'limit' : 'blocked')}</p>}
          {snapshot.review ? <Review key={snapshot.review.preview.operationId} review={snapshot.review} text={text} controller={controller} /> : <div ref={editor}>
            {snapshot.current.values && <SettingsEditor key={`${snapshot.current.checkedAt}:${JSON.stringify(snapshot.current.values)}:${JSON.stringify(snapshot.selection)}`} initial={snapshot.draft || snapshot.current.values.requested} text={text} disabled={busy || snapshot.blocked} controller={controller} />}
            <Button disabled={!snapshot.draft || busy || snapshot.blocked} onClick={() => void controller.preview()}>{text('preview')}</Button>
          </div>}
        </> : <p>{text('readOnly')}</p>}
      </>}
      {busy && <div className="resource-wait"><Button onClick={() => controller.stopWaiting()}>{text('stopWaiting')}</Button><p>{text('stopHint')}</p></div>}
      <p className="small muted">{text('memory')}</p>
    </>}
  </div></Modal>
}
