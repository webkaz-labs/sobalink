import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale } from '../api'
import { translator } from '../i18n'
import { ResourceAttemptRegistry } from '../resource/attempt-registry'
import { resourceEnglish as resourceEn, resourceJapanese as resourceJa } from '../resource/i18n'
import { ResourceGroupController } from './controller'
import type { GroupContext } from './context'
import { ResourceGroupDialog } from './ResourceGroupDialog'
import { groupEnglish as en, groupJapanese as ja, groupText } from './i18n'
import { groupEvidence, groupOperation, groupOtherRunId, groupPrepared, groupRun, groupRunId, groupSelection, mutableGroup, reviseGroupReview } from './fixtures.test-support'
import type { GroupCommandName, GroupCommandPayloads, GroupTransport, PreparedView } from './types'

type Call = Readonly<{ name: GroupCommandName; payload: GroupCommandPayloads[GroupCommandName]; signal: AbortSignal }>
function deferred<T = unknown>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
async function setup(options: Readonly<{ locale?: Locale; count?: number; empty?: boolean; prepared?: PreparedView; handler?: (call: Call, prepared: PreparedView) => unknown | Promise<unknown>; strict?: boolean }> = {}) {
  const locale = options.locale ?? 'en', count = options.count ?? 2
  let prepared = options.prepared ?? await groupPrepared(count)
  const selection = groupSelection(count), calls: Call[] = [], registry = new ResourceAttemptRegistry()
  const context: GroupContext = { localAvailable: true, remoteAvailable: true, revision: 'synthetic-dialog-context', localPeerKey: 'f'.repeat(64), peers: selection.members.map((member, i) => ({ key: member.peerKey, name: `Synthetic device ${i + 1}` })) }
  const transport: GroupTransport = async (name, payload, signal) => {
    const call: Call = { name, payload, signal }; calls.push(call)
    if (options.handler) return options.handler(call, prepared)
    if (name === 'resource.group.review.current') return { schemaVersion: 1, state: 'current', prepared }
    if (name === 'resource.group.review.select') {
      const input = payload as GroupCommandPayloads['resource.group.review.select']
      prepared = { ...prepared, reviewId: groupOtherRunId, review: await reviseGroupReview({ ...prepared.review, executionPeers: input.executionPeers }), admissionState: input.executionPeers.length ? 'prepared' : 'unavailable' }
      return prepared
    }
    if (name === 'resource.group.apply' || name === 'resource.group.status') {
      const evidence = mutableGroup(groupEvidence(prepared.review))
      for (const row of evidence.members) if (row.execution === 'selected') { row.dispatch = 'observed'; row.target = mutableGroup(groupOperation(row)) }
      return { ...groupRun(prepared.review, evidence), runId: prepared.reviewId, activity: 'idle' }
    }
    if (name === 'resource.group.cancel') return { schemaVersion: 1, prepared: { ...prepared, admissionState: 'canceled' } }
    return prepared
  }
  const controller = new ResourceGroupController(transport, () => {}, registry)
  controller.updateContext(context); controller.open()
  if (!options.empty) {
    controller.setTemplate('transferConcurrentPerPeer', { mode: 'limited', value: '3' })
    for (const member of selection.members) controller.addSelection({ kind: 'remote', peerKey: member.peerKey, selector: member.selector })
  }
  const close = vi.fn(), single = vi.fn(), element = <ResourceGroupDialog controller={controller} locale={locale} t={translator(locale)} onClose={close} onSelectSingle={single} />
  const view = render(options.strict ? <StrictMode>{element}</StrictMode> : element)
  return { ...view, controller, calls, context, close, single, prepared, registry }
}
async function normalFlow(locale: Locale) {
  const text = locale === 'ja' ? ja : en, resource = locale === 'ja' ? resourceJa : resourceEn, h = await setup({ locale })
  expect(h.calls).toHaveLength(0)
  expect(screen.getByText(text.scope)).toBeVisible(); expect(screen.getByText(text.authority)).toBeVisible()
  expect(screen.queryByRole('option', { name: /unlimited|無制限/ })).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: text.preview }))
  const review = await screen.findByRole('region', { name: text.review })
  expect(within(review).getByRole('textbox', { name: text.runId })).toHaveValue(groupRunId)
  expect(within(review).getByText(text.keepRunId)).toBeVisible()
  expect(within(review).getByText(text.partial)).toBeVisible()
  expect(within(review).getAllByText(resource.requested)).toHaveLength(4)
  expect(within(review).getAllByText(resource.effective)).toHaveLength(4)
  expect(within(review).getByRole('button', { name: text.apply })).toBeDisabled()
  expect(h.calls.some(call => call.name === 'resource.group.apply')).toBe(false)
  await userEvent.click(within(review).getByRole('checkbox', { name: text.confirm }))
  await userEvent.click(within(review).getByRole('button', { name: text.apply }))
  await waitFor(() => expect(h.controller.getSnapshot().run?.summary.allApplied).toBe(true))
  expect(h.calls.filter(call => call.name === 'resource.group.apply')).toHaveLength(1)
  expect(screen.getByText(text.memory)).toBeVisible()
}

describe('fixed-group settings dialog', () => {
  it('keeps the English review and apply explicit with the known ID visible first', async () => { await normalFlow('en') })
  it('keeps the Japanese review and apply explicit without translating machine identifiers', async () => { await normalFlow('ja') })
  it('uses a saved-device picker and guided missing selectors without commands or raw peer entry', async () => {
    const h = await setup({ empty: true, strict: true })
    expect(h.calls).toHaveLength(0)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: en.choosePeer }), h.context.peers[0].key)
    await userEvent.click(screen.getByRole('button', { name: en.addPeer }))
    expect(screen.getByText(en.missing)).toBeVisible()
    expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
    expect(screen.getByRole('combobox', { name: en.choosePeer })).not.toHaveValue(h.context.peers[0].key)
    expect(screen.queryByRole('textbox', { name: /peer key/i })).not.toBeInTheDocument()
    expect(document.querySelector('textarea')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: en.single }))
    expect(h.single).toHaveBeenCalledWith(h.context.peers[0].key)
    expect(h.calls).toHaveLength(0)
  })
  it('edits template and per-device inheritance locally and rejects unsafe numeric choices', async () => {
    const h = await setup(), template = screen.getByRole('group', { name: en.template })
    await userEvent.selectOptions(within(template).getByRole('combobox', { name: resourceEn.files }), 'limited')
    const limit = within(template).getByRole('textbox', { name: `${resourceEn.files}: ${resourceEn.limitValue}` })
    for (const value of ['0', '-1', '1.5', '1e3', '9007199254740992']) {
      fireEvent.change(limit, { target: { value } }); expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
    }
    fireEvent.change(limit, { target: { value: '7' } }); expect(screen.getByRole('button', { name: en.preview })).toBeEnabled()
    const first = within(screen.getByRole('region', { name: en.peers })).getAllByRole('listitem')[0]
    await userEvent.selectOptions(within(first).getByRole('combobox', { name: resourceEn.perPeer }), 'default')
    expect(h.controller.getSnapshot().draft.members[0].transferConcurrentPerPeer.mode).toBe('default')
    await userEvent.selectOptions(within(first).getByRole('combobox', { name: resourceEn.perPeer }), 'inherit')
    expect(h.controller.getSnapshot().draft.members[0].transferConcurrentPerPeer.mode).toBe('inherit')
    expect(h.calls).toHaveLength(0)
  })
  it('retains every ready excluded and failed review row with separate requested and effective values', async () => {
    const original = await groupPrepared(3), review = await reviseGroupReview({ ...original.review, rows: [original.review.rows[0], original.review.rows[1], { schemaVersion: 1, peerKey: original.review.rows[2].peerKey, state: 'unsupported' }], executionPeers: [original.review.rows[0].peerKey] })
    await setup({ count: 3, prepared: { ...original, review } })
    await userEvent.click(screen.getByRole('button', { name: en.current }))
    const region = await screen.findByRole('region', { name: en.review }), rows = within(region).getAllByRole('listitem')
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent(en.excluded); expect(rows[2]).toHaveTextContent(en.unsupported)
    expect(within(rows[1]).getByRole('checkbox')).not.toBeChecked()
    expect(within(rows[2]).getByRole('checkbox')).toBeDisabled()
    expect(within(rows[0]).getAllByText(resourceEn.requested)).toHaveLength(2)
    expect(within(rows[0]).getAllByText(resourceEn.effective)).toHaveLength(2)
    expect(within(rows[0]).getByText('8')).toBeVisible()
  })
  it('clears confirmation on checklist changes and requires explicit subset update before applying', async () => {
    const h = await setup(); await userEvent.click(screen.getByRole('button', { name: en.preview }))
    let review = await screen.findByRole('region', { name: en.review })
    await userEvent.click(within(review).getByRole('checkbox', { name: en.confirm }))
    await userEvent.click(within(review).getByRole('checkbox', { name: `${en.selected}: Synthetic device 2` }))
    expect(within(review).getByRole('checkbox', { name: en.confirm })).not.toBeChecked()
    expect(within(review).getByRole('checkbox', { name: en.confirm })).toBeDisabled()
    expect(h.calls).toHaveLength(1)
    await userEvent.click(within(review).getByRole('button', { name: en.updateSubset }))
    await waitFor(() => expect(h.controller.getSnapshot().prepared?.reviewId).toBe(groupOtherRunId))
    review = screen.getByRole('region', { name: en.review })
    expect(within(review).getByRole('textbox', { name: en.runId })).toHaveValue(groupOtherRunId)
    expect(within(review).getByRole('checkbox', { name: en.confirm })).not.toBeChecked()
    expect(within(review).getByRole('button', { name: en.apply })).toBeDisabled()
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.preview', 'resource.group.review.select'])
  })
  it('keeps Stop waiting distinct from explicit stop of future calls and ignores late apply success', async () => {
    const waiting = deferred(), h = await setup({ handler: (call, prepared) => {
      if (call.name === 'resource.group.apply') return waiting.promise
      if (call.name === 'resource.group.cancel') {
        const evidence = mutableGroup(groupEvidence(prepared.review)); for (const row of evidence.members) row.dispatch = 'unknown'
        return { schemaVersion: 1, run: { ...groupRun(prepared.review, evidence), activity: 'idle' } }
      }
      return prepared
    } })
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('checkbox', { name: en.confirm }))
    await userEvent.click(screen.getByRole('button', { name: en.apply }))
    expect(screen.getByText(resourceEn.stopHint)).toBeVisible()
    expect(screen.getByRole('button', { name: en.cancel })).toBeEnabled()
    await userEvent.click(screen.getByRole('button', { name: resourceEn.stopWaiting }))
    expect(h.calls.filter(call => call.name === 'resource.group.cancel')).toHaveLength(0)
    expect(h.controller.getSnapshot().run).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: en.cancel }))
    await waitFor(() => expect(h.controller.getSnapshot().run?.summary.dispatchUnknown).toBe(2))
    expect(screen.getByText(en.cancelHint)).toBeVisible()
    await act(async () => { waiting.resolve({ malformed: true }); await waiting.promise })
    expect(h.controller.getSnapshot().run?.summary.dispatchUnknown).toBe(2)
    expect(h.calls.filter(call => call.name === 'resource.group.apply')).toHaveLength(1)
  })
  it('renders a durable historical success beside a later failed status observation', async () => {
    const h = await setup({ count: 1, handler: (_call, prepared) => {
      const evidence = mutableGroup(groupEvidence(prepared.review)), row = evidence.members[0]
      row.dispatch = 'observed'; row.target = mutableGroup(groupOperation(row)); row.status = { state: 'query_failed', sequence: 2, observedAt: 1700000000 }
      return { ...groupRun(prepared.review, evidence), activity: 'idle' }
    } })
    const history = screen.getByRole('region', { name: en.history })
    await userEvent.type(within(history).getByRole('textbox', { name: en.runId }), groupRunId)
    await userEvent.click(within(history).getByRole('button', { name: en.status }))
    await waitFor(() => expect(h.controller.getSnapshot().run?.summary.allApplied).toBe(true))
    const row = within(history).getAllByRole('listitem')[0]
    expect(row).toHaveTextContent(en.target); expect(row).toHaveTextContent(resourceEn.applied)
    expect(row).toHaveTextContent(en.latest); expect(row).toHaveTextContent(en.query_failed)
    expect(row).toHaveTextContent(en.localDurability); expect(row).toHaveTextContent(en.admissionStop)
    expect(h.calls.map(call => call.name)).toEqual(['resource.group.status'])
  })
  it('hides resource details on authentication loss and restores opener focus after Escape', async () => {
    const opener = document.createElement('button'); opener.textContent = 'Synthetic group opener'; document.body.append(opener); opener.focus()
    const h = await setup(); await userEvent.click(screen.getByRole('button', { name: en.current }))
    await screen.findByRole('region', { name: en.review })
    act(() => h.controller.updateContext({ ...h.context, localAvailable: false, remoteAvailable: false, revision: 'synthetic-auth-lost', peers: [] }))
    expect(screen.getByText(en.localUnavailable)).toBeVisible()
    expect(screen.queryByDisplayValue(groupRunId)).not.toBeInTheDocument()
    fireEvent(screen.getByRole('dialog'), new Event('cancel', { cancelable: true }))
    expect(h.close).toHaveBeenCalledOnce(); h.unmount(); expect(opener).toHaveFocus(); opener.remove()
    expect(h.calls).toHaveLength(1)
  })
  it('uses aligned EN and JA keys and a safe English fallback for unknown locale', () => {
    expect(Object.keys(ja).sort()).toEqual(Object.keys(en).sort())
    expect(Object.values(ja).every(value => value.length > 0)).toBe(true)
    expect(groupText('unknown' as Locale, 'title')).toBe(en.title)
  })
})
