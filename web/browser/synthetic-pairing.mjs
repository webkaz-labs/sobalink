import assert from 'node:assert/strict'

// Intentionally not a parseable invitation. Only this exact synthetic payload
// may receive a pairing response; no real enrollment is authorized by this API.
export const SYNTHETIC_PAIRING_INVITATION = 'synthetic-ui-review-only-not-a-private-invitation'
const pairingCommands = ['lan.inspect', 'lan.join']
const enrollmentCommands = ['network.login', 'lan.invite', 'lan.join']
const failureMessage = 'Synthetic pairing interception failed; request and response details withheld'

export function createSyntheticPairingInterception(scenario) {
  const intercepted = []
  let installed = false, failed = false
  return Object.freeze({
    async install(page, respond) {
      assert.ok(scenario === 'offline', 'Synthetic pairing requires the isolated offline scenario')
      assert.ok(!installed && typeof respond === 'function', 'Synthetic pairing requires one explicit responder')
      installed = true
      try {
        await page.route('**/api/command', async route => {
          try {
            const interceptedRequest = route.request()
            assert.ok(interceptedRequest.method() === 'POST')
            const request = interceptedRequest.postDataJSON()
            assert.ok(request && Object.keys(request).length === 3 && pairingCommands.includes(request.name))
            assert.ok(typeof request.requestId === 'string' && request.requestId.length > 0 && request.requestId.length <= 256)
            assert.ok(request.payload && Object.keys(request.payload).length === 1 && request.payload.invitation === SYNTHETIC_PAIRING_INVITATION)
            const command = Object.freeze({ name: request.name, requestId: request.requestId, payload: Object.freeze({ invitation: SYNTHETIC_PAIRING_INVITATION }) })
            // The responder sees only validated synthetic data, never a Route,
            // Request, headers, fetch, continue or fallback capability.
            const result = await respond(command)
            assert.ok(result && Object.keys(result).length === 1)
            let outcome
            if (Object.hasOwn(result, 'abort') && result.abort === 'failed') {
              await route.abort('failed')
              outcome = 'aborted'
            } else {
              assert.ok(Object.hasOwn(result, 'json') && result.json && typeof result.json === 'object' && !Array.isArray(result.json))
              await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(result.json) })
              outcome = 'fulfilled'
            }
            // Only a completed terminal action is evidence of interception.
            intercepted.push(Object.freeze({ command, outcome }))
          } catch {
            failed = true
            try { await route.abort('blockedbyclient') } catch { /* Still fail the audit if a terminal action could not finish. */ }
            throw new Error(failureMessage)
          }
        })
      } catch {
        failed = true
        throw new Error(failureMessage)
      }
    },
    records() { return Object.freeze(intercepted.slice()) },
    async verify(count) {
      assert.ok(!failed, failureMessage)
      for (const name of new Set([...enrollmentCommands, ...(installed ? pairingCommands : [])])) {
        const controlled = intercepted.filter(record => record.command.name === name).length
        assert.equal(await count(name), controlled, 'Every enrollment attempt must match a completed, fixture-controlled synthetic interception')
      }
    },
  })
}

export async function verifyCommandsBeforeCleanup(interception, count, cleanup) {
  const errors = []
  try { if (count) await interception.verify(count) } catch (error) { errors.push(error) }
  try { await cleanup() } catch (error) { errors.push(error) }
  if (errors.length === 1) throw errors[0]
  if (errors.length > 1) throw new AggregateError(errors, 'Command safety verification and fixture cleanup both failed')
}
