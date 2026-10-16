import { useSyncExternalStore } from 'react'
import type { ResourceSettingsController } from './controller'

export function useResourceSettings(controller: ResourceSettingsController) {
  return useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot)
}
