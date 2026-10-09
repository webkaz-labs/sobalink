package directlan

import (
	"context"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

const managementExchangeTimeout = 15 * time.Second

// ManageRemote performs exactly one typed application request within one fixed
// 15-second end-to-end budget. Existing authenticated control recovery is allowed
// only before that request. An uncertain apply is never replayed or retransmitted.
// The expected relationship is oriented as the remote grant, not caller authority.
func (n *Node) ManageRemote(ctx context.Context, expected resourcegrant.Relationship, request resourcegrant.ManagementRequest) (resourcegrant.ManagementReply, error) {
	var empty resourcegrant.ManagementReply
	if n == nil || ctx == nil || expected.Validate() != nil {
		return empty, resourcegrant.ErrInvalid
	}
	// Freeze caller-owned pointers before any network or transport locking.
	frame, err := resourcegrant.ManagementRequestFrame(request)
	if err != nil {
		return empty, resourcegrant.ErrInvalid
	}
	request, err = resourcegrant.DecodeManagementRequest(frame[4:])
	if err != nil {
		return empty, resourcegrant.ErrInvalid
	}
	run, cancel := context.WithTimeout(ctx, managementExchangeTimeout)
	defer cancel()
	if run.Err() != nil {
		return empty, ErrUnavailable
	}
	selected, err := n.CapturePeer(expected.TargetKey)
	if err != nil {
		return empty, ErrUnavailable
	}
	g, err := selected.origin.capture()
	if err != nil {
		return empty, ErrUnavailable
	}
	defer selected.origin.callDone()
	work, err := g.acquireWork(cancel, false)
	if err != nil {
		return empty, ErrUnavailable
	}
	defer work.finish()
	g.mu.Lock()
	peer := g.peerRegistrations[selected.registration]
	g.mu.Unlock()
	authentication := n.captureManagedSession(peer)
	if authentication == nil || authentication.registration != uint64(selected.registration) || authentication.generation != g || authentication.peer != peer ||
		expected != (resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: peer.peer.Key, PeerKey: g.cfg.Identity.PublicKey(), PairBinding: authentication.binding}) {
		return empty, ErrUnavailable
	}
	// This existing one-dial helper performs at most one extra rate-limited
	// managed-session wake while TCP is pending. No application frame is retried.
	connection, err := n.dialInspectionPeer(run, g, peer, authentication)
	if err != nil {
		return empty, ErrUnavailable
	}
	defer connection.Close()
	stop := watchConnection(run, connection)
	defer stop()
	client, ok := captureInspectionClient(run, connection, authentication)
	if !ok {
		return empty, ErrUnavailable
	}
	deadline, _ := run.Deadline()
	if err := client.setDeadline(deadline); err != nil {
		return empty, ErrUnavailable
	}
	hello := resourcegrant.ManagementHelloRequest()
	if _, err := client.Write(hello[:]); err != nil {
		return empty, ErrUnavailable
	}
	if err := resourcegrant.ReadManagementHelloResponse(client); err != nil {
		if err == resourcegrant.ErrUnsupported {
			return empty, err
		}
		return empty, ErrUnavailable
	}
	if _, err := client.Write(frame); err != nil {
		return empty, ErrUnavailable
	}
	reply, err := resourcegrant.ReadManagementReply(client)
	if err != nil || run.Err() != nil || !client.current() || !managementReplyMatches(request, reply) {
		return empty, ErrUnavailable
	}
	return reply, nil
}
