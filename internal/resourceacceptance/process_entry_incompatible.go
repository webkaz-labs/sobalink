//go:build resource_process_native && (resource_group_catalog_native || resource_inspection_native || web_activation_native || managed_restart_native || product_activation_native || directlan_context_fixture || soba_e2e)

package resourceacceptance

// Deliberately no executable fallback for mixed acceptance authority modes.
var _ = resourceProcessAcceptanceModesMustNotBeCombined
