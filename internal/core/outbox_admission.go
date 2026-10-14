package core

// Core.mu is held. Immediate transfer history has its own retention contract;
// only unfinished work and retained physical owners consume pending slots.
func (c *Core) outgoingPendingLocked(peer string, ignore *transferPreparation) (pending, perPeer int) {
	for _, b := range c.outgoing {
		b.mu.Lock()
		active := b.running || b.staging || (b.State != "completed" && b.State != "cancelled" && b.State != "failed" && b.State != "declined")
		if active {
			pending++
			if b.PeerID == peer {
				perPeer++
			}
		}
		b.mu.Unlock()
	}
	for _, p := range c.transferPreparations {
		if p == ignore || p.receipt != nil {
			continue
		}
		pending++
		for _, target := range p.peers {
			if target.ID == peer {
				perPeer++
				break
			}
		}
	}
	return
}

// The coordinator mutex is held. Prepared children and terminal receipt queries
// retain the same peer admission charge as ordinary nonterminal deliveries.
func (o *outboxCoordinator) activePeerCountLocked(peer string) int {
	pending := 0
	for _, row := range o.state.Intents {
		for i, child := range row.View.Children {
			if child.PeerID == peer && o.childActiveLocked(row, i) {
				pending++
			}
		}
	}
	for _, prep := range o.prepared {
		if !prep.discarded || prep.staging || o.frozen != "" {
			for _, child := range prep.intent.View.Children {
				if child.PeerID == peer {
					pending++
				}
			}
		}
	}
	return pending
}
