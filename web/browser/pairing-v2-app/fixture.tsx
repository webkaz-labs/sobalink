// Synthetic UI-only fixture. This never creates networking, identities or enrollment.
import { createRoot } from 'react-dom/client'
import { NearbySetup } from '../../src/components/NearbySetup'
import { idleSetup, inspectedRequestSetup, liveRequestSetup, reviewRequestSetup, setupScopes } from '../../src/nearby-fixture'
import type { Server } from '../../src/useServer'
import type { SetupSnapshot } from '../../src/nearby-setup'
import '../../src/styles.css'
const query = new URL(location.href).searchParams, locale = query.get('locale') === 'ja' ? 'ja' : 'en'
let snapshot = query.get('case') === 'receive' ? reviewRequestSetup() : query.get('case') === 'target' ? inspectedRequestSetup() : query.get('case') === 'outbound' ? liveRequestSetup('outbound') : idleSetup()
const longAlias = locale === 'ja' ? '試験用のとても長い接続先端末の表示名を確認' : 'Synthetic-receiving-device-with-a-deliberately-long-unbroken-display-name'
const display = () => { for (const candidate of snapshot.candidates) candidate.contact.alias = longAlias; if (snapshot.target) snapshot.target.alias = longAlias; if (snapshot.request) snapshot.request.alias = longAlias; if (snapshot.selected) snapshot.selected.alias = longAlias; if (snapshot.plan) snapshot.plan.participants.find(p => p.session_id !== snapshot.sessionId)!.alias = longAlias }; display()
let revoked = [{ peerKey: '8'.repeat(64), generation: 7, verification: 'full-sha256' }]
const commands: { name: string; payload: unknown }[] = []
;(window as any).pairingFixture = { commands, replace: (value: SetupSnapshot) => { snapshot = value }, get: () => structuredClone(snapshot) }
const server = { auth: 'ready', stale: false, state: null, busy: new Set(), run: async (name: string, payload: any) => {
  if (name === 'setup.scopes') return { ok: true, result: setupScopes }
  if (name === 'setup.revoked') return { ok: true, result: { kind: 'sobalink-setup-revoked', version: 1, records: revoked } }
  if (name !== 'setup.status') commands.push({ name, payload })
  if (name === 'setup.reenroll') { revoked = []; return { ok: true, result: { kind: 'sobalink-setup-reenroll', version: 1, peerKey: payload.peerKey, generation: payload.expectedGeneration + 1, verification: 'full-sha256', paired: false, trusted: false } } }
  if (name === 'setup.start') { snapshot = liveRequestSetup(payload.mode, payload.verification); snapshot.alias = payload.alias; if (snapshot.contact) snapshot.contact.alias = payload.alias }
  if (name === 'setup.inspect-target') snapshot = inspectedRequestSetup()
  if (name === 'setup.request') snapshot = { ...reviewRequestSetup('request'), localApproved: true }
  if (name === 'setup.approve') snapshot = { ...snapshot, localApproved: true }
  if (name === 'setup.reject') snapshot = liveRequestSetup('receive')
  if (name === 'setup.cancel') snapshot = { ...snapshot, state: 'cancelled', discovery: { state: 'stopped' } }
  display(); return { ok: true, result: snapshot }
} } as unknown as Server
createRoot(document.getElementById('root')!).render(<main style={{ margin: '0 auto', padding: 12, maxWidth: 640 }}><NearbySetup server={server} locale={locale} /></main>)
