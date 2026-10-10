import { useSyncExternalStore } from 'react'
import type { ResourceCatalogController } from './controller'
export function useResourceCatalog(controller: ResourceCatalogController) { return useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot) }
