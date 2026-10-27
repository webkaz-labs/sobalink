// Reconstruct only observed process facts. Never retain a spawn Error, child
// output, session content, command line or private path in acceptance artifacts.
const signals = new Set(['none', 'SIGABRT', 'SIGBUS', 'SIGFPE', 'SIGHUP', 'SIGILL', 'SIGINT', 'SIGKILL', 'SIGPIPE', 'SIGSEGV', 'SIGTERM', 'SIGTRAP', 'other'])
const keys = ['stage', 'outcome', 'code', 'signal']
export function fixtureStartDiagnostic(exit, timedOut = false) {
  return {
    stage: 'session-wait',
    outcome: timedOut ? 'timeout' : exit?.signal === 'spawn-error' ? 'spawn-error' : 'exited',
    code: Number.isInteger(exit?.code) && exit.code >= 0 && exit.code <= 255 ? exit.code : null,
    signal: exit?.signal == null || exit.signal === 'spawn-error' ? 'none' : signals.has(exit.signal) ? exit.signal : 'other',
  }
}
export function readFixtureStartDiagnostic(annotations = []) {
  const records = annotations.filter(item => item.type === 'fixture-start-diagnostic')
  if (records.length !== 1 || typeof records[0].description !== 'string' || records[0].description.length > 256) return undefined
  let value
  try { value = JSON.parse(records[0].description) } catch { return undefined }
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length !== keys.length || !keys.every(key => Object.hasOwn(value, key))) return undefined
  if (value.stage !== 'session-wait' || !['exited', 'spawn-error', 'timeout'].includes(value.outcome) || !(value.code === null || Number.isInteger(value.code) && value.code >= 0 && value.code <= 255) || !signals.has(value.signal)) return undefined
  if (value.outcome !== 'exited' && (value.code !== null || value.signal !== 'none')) return undefined
  return { stage: value.stage, outcome: value.outcome, code: value.code, signal: value.signal }
}
