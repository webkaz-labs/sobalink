import { useLayoutEffect, useSyncExternalStore } from 'react'
import type { ResourceContext, ResourceSettingsController } from './controller'

export function useResourceSettings(controller: ResourceSettingsController) {
  return useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot)
}
// Call at App scope, above login/modal conditionals. Layout invalidation clears
// controls before paint; the controller also checks its epoch at dispatch.
export function useResourceContext(controller: ResourceSettingsController, context: ResourceContext) {
  useLayoutEffect(() => { controller.updateContext(context) }, [controller, context])
}
