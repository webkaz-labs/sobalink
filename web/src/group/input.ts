import { parseSettingsInput, parseSelectorInput, settingsInput, type ChoiceInput, type SettingsInput, type SelectorInput } from '../resource/input'
import type { RemoteSelection, TransferChoice } from '../resource/types'
import type { GroupMember, GroupOverride } from './types'
export type OverrideInput = Readonly<{ mode: 'inherit' | 'default' | 'limited'; value: string }>
export type MemberInput = Readonly<{ peerKey: string; selector: SelectorInput; transferConcurrentFiles: OverrideInput; transferConcurrentPerPeer: OverrideInput }>
export type GroupDraft = Readonly<{ template: SettingsInput; members: readonly MemberInput[] }>
export const emptyGroupDraft = (): GroupDraft => Object.freeze({ template: settingsInput({ transferConcurrentFiles: { mode: 'default' }, transferConcurrentPerPeer: { mode: 'default' } }), members: Object.freeze([]) })
export function newMemberInput(peerKey: string): MemberInput {
  return Object.freeze({ peerKey, selector: Object.freeze({ peerKey, protocol: '2', resourceId: '', grantId: '', grantRevision: '' }), transferConcurrentFiles: Object.freeze({ mode: 'inherit', value: '' }), transferConcurrentPerPeer: Object.freeze({ mode: 'inherit', value: '' }) })
}
export function selectorInput(selection: RemoteSelection): SelectorInput { return Object.freeze({ peerKey: selection.peerKey, protocol: String(selection.selector.protocolVersion) as '1' | '2', resourceId: selection.selector.target.resourceId, grantId: selection.selector.grantId, grantRevision: String(selection.selector.grantRevision) }) }
export function parseMemberInput(member: MemberInput): GroupMember | null {
  const selection = parseSelectorInput(member.selector)
  if (!selection || selection.selector.protocolVersion !== 2 || selection.peerKey !== member.peerKey) return null
  const override: { transferConcurrentFiles?: TransferChoice; transferConcurrentPerPeer?: TransferChoice } = {}
  for (const key of ['transferConcurrentFiles', 'transferConcurrentPerPeer'] as const) {
    const field = member[key]
    if (field.mode === 'inherit') continue
    const choice: ChoiceInput = { mode: field.mode, value: field.value }
    const parsed = parseSettingsInput({ transferConcurrentFiles: choice, transferConcurrentPerPeer: choice })
    if (!parsed) return null
    override[key] = parsed[key]
  }
  return Object.freeze({ peerKey: member.peerKey, selector: Object.freeze({ ...selection.selector, protocolVersion: 2 }), ...(Object.keys(override).length ? { override: Object.freeze(override) as GroupOverride } : {}) })
}
export type SettingField = keyof SettingsInput
export function copyChoice(value: ChoiceInput): ChoiceInput { return Object.freeze({ mode: value.mode, value: value.value }) }
