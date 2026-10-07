package directlan

import "net"

// RequestClose permanently closes admission and signals the existing generation
// without joining it. A participant may call this while it still owns a lease;
// that participant must release its work before calling Close for the real join.
// No application callback, endpoint I/O, Node.mu acquisition or wait occurs here.
// Close rechecks current, initial and detached owners under Node.mu to cover
// racing registration. No stopped owner is ever reopened.
func (n *Node) RequestClose() {
	n.closing.Store(true)
	if staged := n.staged.Load(); staged != nil {
		staged.Abort()
	}
	if build := n.building.Load(); build != nil {
		build.RequestStop()
	}
	if g := n.generation.Load(); g != nil {
		g.requestStop(net.ErrClosed)
	}
}
