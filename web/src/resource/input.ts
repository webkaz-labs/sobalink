import { readSelection } from './decode'
import type { RemoteSelection, TransferChoice, TransferSettings } from './types'

export function positiveIntegerInput(value: string): number | null {
  if (!/^[1-9][0-9]{0,15}$/.test(value)) return null
  const number = Number(value)
  return Number.isSafeInteger(number) && number > 0 ? number : null
}
export type ChoiceInput = Readonly<{ mode: 'default' | 'limited'; value: string }>
export type SettingsInput = Readonly<{ transferConcurrentFiles: ChoiceInput; transferConcurrentPerPeer: ChoiceInput }>
export function choiceInput(choice: TransferChoice): ChoiceInput { return { mode: choice.mode, value: choice.mode === 'limited' ? String(choice.value) : '' } }
export function settingsInput(settings: TransferSettings): SettingsInput {
  return { transferConcurrentFiles: choiceInput(settings.transferConcurrentFiles), transferConcurrentPerPeer: choiceInput(settings.transferConcurrentPerPeer) }
}
export function parseSettingsInput(input: SettingsInput): TransferSettings | null {
  const parse = (choice: ChoiceInput): TransferChoice | null => {
    if (choice.mode === 'default') return { mode: 'default' }
    const value = positiveIntegerInput(choice.value)
    return value === null ? null : { mode: 'limited', value }
  }
  const transferConcurrentFiles = parse(input.transferConcurrentFiles), transferConcurrentPerPeer = parse(input.transferConcurrentPerPeer)
  return transferConcurrentFiles && transferConcurrentPerPeer ? { transferConcurrentFiles, transferConcurrentPerPeer } : null
}
export type SelectorInput = Readonly<{ peerKey: string; protocol: '' | '1' | '2'; resourceId: string; grantId: string; grantRevision: string }>
export const emptySelector: SelectorInput = { peerKey: '', protocol: '', resourceId: '', grantId: '', grantRevision: '' }
export function parseSelectorInput(input: SelectorInput): RemoteSelection | null {
  if (input.protocol !== '1' && input.protocol !== '2') return null
  const grantRevision = positiveIntegerInput(input.grantRevision)
  if (grantRevision === null) return null
  try {
    const selection = readSelection({ kind: 'remote', peerKey: input.peerKey, selector: { protocolVersion: Number(input.protocol), target: { schemaVersion: 1, resourceId: input.resourceId }, grantId: input.grantId, grantRevision } })
    return selection.kind === 'remote' ? selection : null
  } catch { return null }
}
