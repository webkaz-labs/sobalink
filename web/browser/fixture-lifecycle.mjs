// Keep the backend alive until every intercepted request has settled.
// Cleanup is mandatory, and neither failure may hide the other.
export async function drainRoutesBeforeCleanup(page, cleanup) {
  const errors = []
  try { await page.unrouteAll({ behavior: 'wait' }) } catch (error) { errors.push(error) }
  try { await cleanup() } catch (error) { errors.push(error) }
  if (errors.length === 1) throw errors[0]
  if (errors.length > 1) throw new AggregateError(errors, 'Route draining and fixture cleanup both failed')
}
