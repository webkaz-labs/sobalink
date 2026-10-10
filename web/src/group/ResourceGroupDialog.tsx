import { useEffect, useId, useState } from 'react'
import type { Locale } from '../api'
import type { TransferChoice } from '../resource/types'
import type { Translate } from '../i18n'
import { Button, Modal } from '../components/ui'
import { SettingsValuesView } from '../resource/ResourceSettingsDialog'
import { resourceText } from '../resource/i18n'
import { parseSettingsInput, type ChoiceInput } from '../resource/input'
import { isRefreshEligible } from './decode'
import { ResourceGroupController } from './controller'
import { parseMemberInput, type OverrideInput } from './input'
import { groupText, type GroupTextKey } from './i18n'
import type { GroupSummary } from './types'
import { useResourceGroup } from './useResourceGroup'
import './group.css'
const choiceLabel = (choice: TransferChoice, locale: Locale) => choice.mode === 'default' ? resourceText(locale, 'default') : String(choice.value)
const fields = ['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const
const summaryFields: readonly [keyof GroupSummary, GroupTextKey][] = [
  ['selected', 'selectedCount'], ['executable', 'executableCount'], ['excluded', 'excludedCount'], ['reviewFailures', 'reviewFailures'],
  ['notAttempted', 'notAttempted'], ['dispatching', 'dispatching'], ['dispatchObserved', 'dispatchObserved'], ['dispatchUnknown', 'dispatchUnknown'],
  ['applied', 'applied'], ['failed', 'failed'], ['canceled', 'canceled'], ['savedNotApplied', 'savedNotApplied'], ['targetUnknown', 'targetUnknown'],
  ['targetUnobserved', 'targetUnobserved'], ['targetNonDurable', 'targetNonDurable'], ['localNonDurable', 'localNonDurable'], ['statusFailures', 'statusFailures'],
  ['admissionFinished', 'admissionFinished'], ['reconciliationRequired', 'reconciliationRequired'], ['allApplied', 'allApplied'],
]
function ChoiceEditor({ label, value, inherit = false, disabled, onChange, locale }: { label: string; value: OverrideInput | ChoiceInput; inherit?: boolean; disabled: boolean; onChange: (value: OverrideInput) => void; locale: Locale }) {
  const id = useId()
  return <div className="group-choice"><label htmlFor={id}>{label}</label><select id={id} value={value.mode} disabled={disabled} onChange={event => onChange({ mode: event.target.value as OverrideInput['mode'], value: value.value })}>
    {inherit && <option value="inherit">{groupText(locale, 'inherit')}</option>}<option value="default">{resourceText(locale, 'default')}</option><option value="limited">{resourceText(locale, 'limited')}</option>
  </select>{value.mode === 'limited' && <label>{label}: {resourceText(locale, 'limitValue')}<input inputMode="numeric" autoComplete="off" maxLength={16} disabled={disabled} value={value.value} onChange={event => onChange({ ...value, value: event.target.value })} /></label>}</div>
}
export function ResourceGroupDialog({ controller, locale, t, onClose, onSelectSingle }: { controller: ResourceGroupController; locale: Locale; t: Translate; onClose: () => void; onSelectSingle?: (peerKey: string) => void }) {
  const snapshot = useResourceGroup(controller), text = (key: GroupTextKey) => groupText(locale, key), resource = (key: Parameters<typeof resourceText>[1]) => resourceText(locale, key)
  const [peerKey, setPeerKey] = useState(''), id = useId(), busy = snapshot.busy !== null
  useEffect(() => { controller.open(); return () => controller.close() }, [controller])
  const close = () => { controller.close(); onClose() }
  const editable = snapshot.context.remoteAvailable && !busy && !snapshot.preparationUnknown
  const peerName = (key: string) => snapshot.context.peers.find(peer => peer.key === key)?.name || key
  const validDraft = parseSettingsInput(snapshot.draft.template) && snapshot.draft.members.length > 0 && snapshot.draft.members.every(member => parseMemberInput(member))
  const prepared = snapshot.prepared, run = snapshot.run
  return <Modal title={text('title')} t={t} onClose={close} wide><div className="resource-group">
    <p>{text('scope')}</p><p>{text('authority')}</p><p>{resource('admission')}</p>
    {!snapshot.context.localAvailable ? <p role="status">{text('localUnavailable')}</p> : <>
      {!snapshot.context.remoteAvailable && <p role="status">{text('remoteUnavailable')}</p>}
      {snapshot.notice && <p role="alert">{text(snapshot.notice)}</p>}{snapshot.blocked && <p role="status">{text('blocked')}</p>}{snapshot.capacityReached && <p role="status">{text('limit')}</p>}
      <section aria-label={text('peers')}><h3>{text('peers')}</h3><div className="group-actions"><label htmlFor={`${id}-peer`}>{text('choosePeer')}</label><select id={`${id}-peer`} value={peerKey} disabled={!editable} onChange={event => setPeerKey(event.target.value)}>
        <option value="">{text('choosePeer')}</option>{snapshot.context.peers.filter(peer => !snapshot.draft.members.some(member => member.peerKey === peer.key)).map(peer => <option key={peer.key} value={peer.key}>{peer.name || peer.key}</option>)}
      </select><Button disabled={!editable || !peerKey || snapshot.draft.members.length >= 16} onClick={() => { if (controller.addPeer(peerKey)) setPeerKey('') }}>{text('addPeer')}</Button></div>
      <ul className="group-members">{snapshot.draft.members.map(member => <li key={member.peerKey}><h4>{peerName(member.peerKey)}</h4>
        <div className="group-actions"><Button disabled={!editable} onClick={() => controller.removePeer(member.peerKey)}>{text('removePeer')}</Button>{onSelectSingle && <Button disabled={!editable} onClick={() => onSelectSingle(member.peerKey)}>{text('single')}</Button>}</div>
        {!parseMemberInput(member) && <p role="status">{text('missing')}</p>}
        <details><summary>{text('advanced')}</summary><p className="code-value">{member.peerKey}</p>{(['resourceId', 'grantId', 'grantRevision'] as const).map(field => <label key={field}>{resource(field)}<input value={member.selector[field]} disabled={!editable} autoComplete="off" spellCheck={false} inputMode={field === 'grantRevision' ? 'numeric' : 'text'} maxLength={field === 'grantRevision' ? 16 : 32} onChange={event => controller.setSelector(member.peerKey, { ...member.selector, [field]: event.target.value })} /></label>)}</details>
        <fieldset disabled={!editable}><legend>{text('override')}</legend>{fields.map(field => <ChoiceEditor key={field} locale={locale} label={resource(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')} value={member[field]} inherit disabled={!editable} onChange={value => controller.setOverride(member.peerKey, field, value)} />)}</fieldset>
      </li>)}</ul></section>
      <fieldset disabled={!editable}><legend>{text('template')}</legend>{fields.map(field => <ChoiceEditor key={field} locale={locale} label={resource(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')} value={snapshot.draft.template[field]} disabled={!editable} onChange={value => { if (value.mode !== 'inherit') controller.setTemplate(field, { mode: value.mode, value: value.value }) }} />)}</fieldset>
      <p className="small muted">{resource('validNumbers')}</p><div className="group-actions"><Button disabled={!editable || !validDraft || snapshot.blocked || snapshot.capacityReached} onClick={() => void controller.preview()}>{text('preview')}</Button>
        {snapshot.recoveryAvailable && <Button disabled={busy || !snapshot.context.remoteAvailable || snapshot.blocked} onClick={() => void controller.recoverPreview()}>{text('recoverPreview')}</Button>}
        {snapshot.subsetRecoveryAvailable && <Button disabled={busy || !snapshot.context.remoteAvailable || snapshot.blocked} onClick={() => void controller.recoverSubset()}>{text('recoverSubset')}</Button>}
        <Button disabled={busy} onClick={() => void controller.currentReview()}>{text('current')}</Button></div><p className="small muted">{text('currentHint')}</p>
      {prepared && <section aria-label={text('review')}><h3>{text('review')}</h3><p>{text('partial')}</p>{prepared.initializesLocalEvidence && <p role="status">{text('initialize')}</p>}<p>{resource('migration')}</p>
        <label>{text('runId')}<input readOnly value={prepared.reviewId} /></label><p>{text('keepRunId')}</p><details><summary>{resource('reviewRevision')}</summary><p className="code-value">{prepared.review.revision}</p></details>
        <p>{text('subsetHint')}</p><ul className="group-review-rows">{prepared.review.rows.map(row => { const member = prepared.review.selection.members.find(item => item.peerKey === row.peerKey)!; return <li key={row.peerKey}>
          <h4>{peerName(row.peerKey)}</h4><p>{text(row.state)}</p><p>{text(prepared.review.executionPeers.includes(row.peerKey) ? 'selected' : 'excluded')}</p>
          <label><input type="checkbox" disabled={busy || row.state !== 'ready' || prepared.admissionState === 'canceled'} checked={snapshot.executionPeers.includes(row.peerKey)} onChange={event => controller.setExecutionPeers(event.target.checked ? [...snapshot.executionPeers, row.peerKey] : snapshot.executionPeers.filter(peer => peer !== row.peerKey))} />{text('selected')}: {peerName(row.peerKey)}</label>
          <dl>{fields.map(field => <div key={field}><dt>{text('template')}: {resource(field === 'transferConcurrentFiles' ? 'files' : 'perPeer')}</dt><dd>{choiceLabel(prepared.review.selection.template.settings[field], locale)}</dd><dt>{text('override')}</dt><dd>{member.override?.[field] ? choiceLabel(member.override[field]!, locale) : text('inherit')}</dd></div>)}</dl>
          {row.state === 'ready' && <SettingsValuesView values={row.reply.preview} text={resource} />}
          <details><summary>{text('advanced')}</summary><dl><dt>{resource('resourceId')}</dt><dd>{member.selector.target.resourceId}</dd><dt>{resource('grantId')}</dt><dd>{member.selector.grantId}</dd><dt>{resource('grantRevision')}</dt><dd>{member.selector.grantRevision}</dd>{row.state === 'ready' && <><dt>{resource('operationId')}</dt><dd>{row.reply.preview.operationId}</dd><dt>{resource('baseRevision')}</dt><dd>{row.reply.preview.baseRevision}</dd><dt>{resource('reviewRevision')}</dt><dd>{row.reply.preview.reviewRevision}</dd></>}</dl></details>
        </li> })}</ul>
        <Button disabled={busy || !snapshot.context.remoteAvailable || prepared.admissionState === 'canceled' || snapshot.blocked} onClick={() => void controller.selectExecution()}>{text('updateSubset')}</Button>
        <label className="group-confirm"><input type="checkbox" disabled={!controller.canConfirm()} checked={snapshot.confirmed} onChange={event => controller.setConfirmed(event.target.checked)} />{text('confirm')}</label><Button variant="primary" disabled={!snapshot.confirmed || !controller.canConfirm()} onClick={() => void controller.apply()}>{text('apply')}</Button>
        <Button disabled={busy} onClick={() => void controller.cancel()}>{text('cancel')}</Button><p>{text('cancelHint')}</p>
      </section>}
      <section aria-label={text('history')}><h3>{text('history')}</h3><p>{resource('currentSeparate')}</p>
        <label>{text('runId')}<input autoComplete="off" spellCheck={false} maxLength={32} value={snapshot.runIdInput} disabled={busy} onChange={event => controller.setRunIdInput(event.target.value)} /></label><Button disabled={busy || !/^[0-9a-f]{32}$/.test(snapshot.runIdInput)} onClick={() => void controller.status()}>{text('status')}</Button>
        {snapshot.retainedRunIds.length > 0 && <details><summary>{text('retained')}</summary>{snapshot.retainedRunIds.map(runId => <div key={runId}><span className="code-value">{runId}</span><Button disabled={busy} onClick={() => void controller.status(runId)}>{text('status')}</Button></div>)}</details>}
        {run && <><p>{text('activity')}: {text(run.activity)} · {text('localDurability')}: {text(run.localDurability)}</p><details><summary>{text('summary')}</summary><dl>{summaryFields.map(([field, label]) => <div key={field}><dt>{text(label)}</dt><dd>{typeof run.summary[field] === 'boolean' ? text(run.summary[field] ? 'yes' : 'no') : String(run.summary[field])}</dd></div>)}</dl></details>
          <ul className="group-evidence">{run.evidence.members.map(row => <li key={row.peerKey}><h4>{peerName(row.peerKey)}</h4><p>{text(row.review)} · {text(row.execution)}</p><details><summary>{resource('originalTarget')}</summary><p className="code-value">{row.peerKey}</p>{row.request && <dl><dt>{resource('resourceId')}</dt><dd>{row.request.target.resourceId}</dd><dt>{resource('grantId')}</dt><dd>{row.request.grantId}</dd><dt>{resource('grantRevision')}</dt><dd>{row.request.grantRevision}</dd><dt>{resource('operationId')}</dt><dd>{row.request.apply.operationId}</dd><dt>{resource('baseRevision')}</dt><dd>{row.request.apply.baseRevision}</dd><dt>{resource('reviewRevision')}</dt><dd>{row.request.apply.reviewRevision}</dd></dl>}{run.review.rows.filter(reviewRow => reviewRow.peerKey === row.peerKey).map(reviewRow => reviewRow.state === 'ready' ? <SettingsValuesView key={reviewRow.peerKey} values={reviewRow.reply.preview} text={resource} /> : null)}</details><dl>
            <dt>{text('dispatch')}</dt><dd>{text(row.dispatch)}</dd><dt>{text('localDurability')}</dt><dd>{text(row.localDurability)}</dd><dt>{text('admissionStop')}</dt><dd>{text(row.admissionStop)}</dd>
            <dt>{text('target')}</dt><dd>{row.target ? resource(row.target.outcome.status) : text('unobserved')}</dd>
            {row.target && <><dt>{resource(row.target.evidenceDurable ? 'durable' : 'notDurable')}</dt><dd>{text(row.target.evidenceDurable ? 'yes' : 'no')}</dd>{(['configuration', 'accounting', 'transfer'] as const).map(stage => <div key={stage}><dt>{resource(stage)}</dt><dd>{text(row.target!.outcome[stage])}</dd></div>)}</>}
            <dt>{text('latest')}</dt><dd>{text(row.status.state)}</dd>{row.status.state === 'observed' && <><dt>{text('latestTarget')}</dt><dd>{resource(row.status.operation.outcome.status)}</dd><dt>{text('latestDurable')}</dt><dd>{text(row.status.operation.evidenceDurable ? 'yes' : 'no')}</dd></>}<dt>{text('sequence')}</dt><dd>{row.status.sequence}</dd><dt>{text('observedAt')}</dt><dd>{row.status.observedAt ? new Date(row.status.observedAt * 1000).toLocaleString(locale) : '—'}</dd>
          </dl>{isRefreshEligible(row) && <label><input type="checkbox" disabled={busy} checked={snapshot.refreshPeers.includes(row.peerKey)} onChange={event => controller.setRefreshPeers(event.target.checked ? [...snapshot.refreshPeers, row.peerKey] : snapshot.refreshPeers.filter(peer => peer !== row.peerKey))} />{text('refreshSelect')}: {peerName(row.peerKey)}</label>}</li>)}</ul>
          <Button disabled={busy || !snapshot.refreshPeers.length} onClick={() => void controller.refreshStatus()}>{text('refresh')}</Button>
        </>}
        {(run || snapshot.retainedRunIds.includes(snapshot.runIdInput)) && <><Button disabled={snapshot.busy === 'cancel'} onClick={() => void controller.cancel()}>{text('cancel')}</Button><p>{text('cancelHint')}</p></>}
      </section>
      {busy && <div className="group-actions"><Button onClick={() => controller.stopWaiting()}>{resource('stopWaiting')}</Button><p>{resource('stopHint')}</p></div>}
    </>}<p className="small muted">{text('memory')}</p>
  </div></Modal>
}
