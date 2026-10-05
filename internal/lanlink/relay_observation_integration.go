//go:build lanlink_integration

package lanlink

// RelayHasNodeForIntegration reports actual DERP registration, not merely local
// permission or TCP listener readiness. It exists only in native test builds.
func RelayHasNodeForIntegration(relay *LocalRelay, node *Node) bool {
	return relay != nil && node != nil && relay.derp.IsClientConnectedForTest(node.cfg.Identity.Key.Public())
}
