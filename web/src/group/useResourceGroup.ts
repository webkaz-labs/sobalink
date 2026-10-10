import { useSyncExternalStore } from 'react'
import type { ResourceGroupController } from './controller'
export function useResourceGroup(controller: ResourceGroupController) { return useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot) }
