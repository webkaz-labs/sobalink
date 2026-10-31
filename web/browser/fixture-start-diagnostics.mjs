import { open, lstat } from 'node:fs/promises'
// Reconstruct only observed process facts. Never retain a spawn Error, child
// output, session content, command line or private path in acceptance artifacts.
const signals = new Set(['none', 'SIGABRT', 'SIGBUS', 'SIGFPE', 'SIGHUP', 'SIGILL', 'SIGINT', 'SIGKILL', 'SIGPIPE', 'SIGSEGV', 'SIGTERM', 'SIGTRAP', 'other'])
const keys = ['stage', 'outcome', 'code', 'signal']
const startupStages = new Set(['unknown', 'arguments', 'temporary-directory', 'fixture-files', 'primary-lock', 'primary-core', 'receive-settings', 'secondary-lock', 'secondary-core', 'network-configure', 'peer-trust', 'peer-reconnect', 'seed-messages', 'seed-transfer', 'seed-share', 'discovery-refresh', 'assets', 'service-reservation', 'web-server', 'access-code', 'session-write'])
const startupCategories = new Set(['canceled', 'deadline', 'permission', 'address-in-use', 'address-unavailable', 'connection-refused', 'not-exist', 'unclassified', 'multiple-causes', 'lan_listener_conflict', 'lan_listener_address_unavailable', 'lan_listener_permission_denied', 'lan_state_capacity', 'lan_listener_capacity', 'lan_start_failed'])
function validStartup(value) {
  return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 2 && Object.hasOwn(value, 'stage') && Object.hasOwn(value, 'category') && startupStages.has(value.stage) && startupCategories.has(value.category)
}
// Read at most a fixed bound. No raw sidecar contents or read errors escape.
export async function readStartupSidecar(path) {
  let file
  try {
    const entry = await lstat(path)
    if (!entry.isFile() || entry.isSymbolicLink()) return undefined
    file = await open(path, 'r')
    const info = await file.stat()
    if (!info.isFile() || info.size > 256 || (info.mode & 0o077) !== 0) return undefined
    const bytes = Buffer.alloc(257)
    const { bytesRead } = await file.read(bytes, 0, bytes.length, 0)
    if (bytesRead > 256) return undefined
    const value = JSON.parse(bytes.subarray(0, bytesRead).toString('utf8'))
    return validStartup(value) ? { stage: value.stage, category: value.category } : undefined
  } catch { return undefined } finally { await file?.close().catch(() => {}) }
}
export function fixtureStartDiagnostic(exit, timedOut = false, startup) {
  return {
    stage: 'session-wait',
    outcome: timedOut ? 'timeout' : exit?.signal === 'spawn-error' ? 'spawn-error' : 'exited',
    code: Number.isInteger(exit?.code) && exit.code >= 0 && exit.code <= 255 ? exit.code : null,
    signal: exit?.signal == null || exit.signal === 'spawn-error' ? 'none' : signals.has(exit.signal) ? exit.signal : 'other',
    ...(!timedOut && exit?.signal !== 'spawn-error' && validStartup(startup) ? { startup: { stage: startup.stage, category: startup.category } } : {}),
  }
}
export function readFixtureStartDiagnostic(annotations = []) {
  const records = annotations.filter(item => item.type === 'fixture-start-diagnostic')
  if (records.length !== 1 || typeof records[0].description !== 'string' || records[0].description.length > 512) return undefined
  let value
  try { value = JSON.parse(records[0].description) } catch { return undefined }
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length !== keys.length + (Object.hasOwn(value, 'startup') ? 1 : 0) || !keys.every(key => Object.hasOwn(value, key))) return undefined
  if (value.stage !== 'session-wait' || !['exited', 'spawn-error', 'timeout'].includes(value.outcome) || !(value.code === null || Number.isInteger(value.code) && value.code >= 0 && value.code <= 255) || !signals.has(value.signal)) return undefined
  if (value.outcome !== 'exited' && (value.code !== null || value.signal !== 'none')) return undefined
  if (Object.hasOwn(value, 'startup') && (value.outcome !== 'exited' || !validStartup(value.startup))) return undefined
  return { stage: value.stage, outcome: value.outcome, code: value.code, signal: value.signal, ...(value.startup ? { startup: { stage: value.startup.stage, category: value.startup.category } } : {}) }
}
