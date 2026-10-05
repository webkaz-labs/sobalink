package lanlink

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

// handoffCloseTransport holds engine teardown without opening any sockets. It
// lets the test observe whether a replacement using the same saved role key
// starts before the retiring engine has finished closing.
type handoffCloseTransport struct {
	fakePeerTransport
	closeStarted chan struct{}
	allowClose   chan struct{}
	startOnce    sync.Once
	closeErr     error
}

func (f *handoffCloseTransport) Close() error {
	f.startOnce.Do(func() { close(f.closeStarted) })
	<-f.allowClose
	return errors.Join(f.fakePeerTransport.Close(), f.closeErr)
}

func TestRouteHandoffExpiryHonorsCreatorDeadline(t *testing.T) {
	issuer, receiver, now, candidates := routeNodesFixture(t)
	routeApplyFixture(t, issuer, receiver, candidates, now)
	peer := issuer.PublicKey()
	retiring := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(retiring.allowClose) }) }
	defer release()
	previous := receiver.clients[peer]
	previous.makeClient = func(tailcat.Addr) peerTransport { return retiring }
	existing, err := receiver.DialPeer(context.Background(), peer, "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	// Deterministically expire only the LAN approval and the runtime evidence.
	// The external approval stays valid. No timer or network scheduling decides
	// which caller first notices expiry and creates the replacement runtime.
	expired := time.Now().Add(-time.Second)
	receiver.mu.Lock()
	for i := range previous.remote.Routes.Approvals {
		if previous.remote.Routes.Approvals[i].CandidateID == candidates[1].ID() {
			previous.remote.Routes.Approvals[i].Expires = expired
		}
	}
	previous.startMu.Lock()
	previous.expires = expired
	previous.publishObservation("ready", "relay", now)
	previous.startMu.Unlock()
	receiver.mu.Unlock()
	waiting, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dialDone := make(chan error, 1)
	go func() {
		conn, err := receiver.DialPeer(waiting, peer, "tcp", 8080)
		if conn != nil {
			conn.Close()
		}
		dialDone <- err
	}()
	select {
	case <-retiring.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("expiry did not start retirement")
	}
	receiver.mu.Lock()
	replacement := receiver.clients[peer]
	started := make(chan struct{}, 1)
	replacement.makeClient = func(tailcat.Addr) peerTransport {
		started <- struct{}{}
		return &fakePeerTransport{}
	}
	receiver.mu.Unlock()
	select {
	case err := <-dialDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("expiry creator did not honor its deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry creator waited for engine teardown past its deadline")
	}
	select {
	case <-started:
		t.Fatal("expired engine overlapped replacement startup")
	default:
	}
	if _, err := existing.Write([]byte("blocked")); !errors.Is(err, ErrUntrusted) && !errors.Is(err, ErrRoutePermission) {
		t.Fatal("retiring flow retained authority", err)
	}
	release()
	if err := previous.shutdown(); err != nil {
		t.Fatal(err)
	}
	conn, err := receiver.DialPeer(context.Background(), peer, "tcp", 8080)
	if err != nil {
		t.Fatal("remaining approval did not recover", err)
	}
	conn.Close()
	if len(replacement.candidates) != 1 || replacement.candidates[0] != candidates[0] || retiring.closed.Load() != 1 {
		t.Fatal("expiry lost remaining authority or repeated engine teardown")
	}
}

func TestRouteHandoffCloseErrorKeepsSuccessorsBlocked(t *testing.T) {
	issuer, receiver, now, candidates := routeNodesFixture(t)
	_, review := routeApplyFixture(t, issuer, receiver, candidates, now)
	peer := issuer.PublicKey()
	closeErr := errors.New("fixture close failed")
	retiring := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{}), closeErr: closeErr}
	close(retiring.allowClose)
	previous := receiver.clients[peer]
	previous.makeClient = func(tailcat.Addr) peerTransport { return retiring }
	existing, err := receiver.DialPeer(context.Background(), peer, "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	if err := receiver.RevokeRoutes(peer, []string{candidates[1].ID()}); !errors.Is(err, closeErr) {
		t.Fatal("retirement failure was hidden", err)
	}
	for generation := range 2 {
		if generation != 0 {
			if err := receiver.ApproveRoutes(peer, review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute)); !errors.Is(err, closeErr) {
				t.Fatal("successor lost unresolved teardown failure", err)
			}
		}
		next := receiver.clients[peer]
		started := false
		next.makeClient = func(tailcat.Addr) peerTransport { started = true; return &fakePeerTransport{} }
		conn, err := receiver.DialPeer(context.Background(), peer, "tcp", 8080)
		if conn != nil {
			conn.Close()
		}
		if started || !errors.Is(err, ErrRoutePermission) || !errors.Is(err, closeErr) {
			t.Fatal("replacement started after unconfirmed engine closure", err)
		}
	}
	if retiring.closed.Load() != 1 {
		t.Fatal("failed engine Close was repeated")
	}
}

func TestRouteRetirementChainAndConcurrentShutdown(t *testing.T) {
	retiring := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(retiring.allowClose) }) }
	defer release()
	first := &remoteClient{client: retiring}
	middle := &remoteClient{predecessor: first.retirementState()}
	last := &remoteClient{predecessor: middle.retirementState()}
	first.beginRetirement()
	<-retiring.closeStarted
	const callers = 16
	results := make(chan error, callers)
	for range callers {
		go func() { results <- middle.shutdown() }()
	}
	waiting, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := last.waitForPredecessor(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unused middle generation bypassed its predecessor", err)
	}
	release()
	for range callers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("shared retirement waiter did not finish")
		}
	}
	if err := last.waitForPredecessor(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.shutdown(); err != nil || retiring.closed.Load() != 1 {
		t.Fatal("concurrent retirement repeated engine Close", err)
	}
}

func TestRouteHandoffWaitsForRetiredEngineClose(t *testing.T) {
	for _, mutation := range []string{"revoke", "apply", "approve"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(mutation+"/"+network, func(t *testing.T) {
				issuer, receiver, now, candidates := routeNodesFixture(t)
				_, review := routeApplyFixture(t, issuer, receiver, candidates, now)
				peer := issuer.PublicKey()
				retainedID, revokedID := candidates[0].ID(), candidates[1].ID()
				grantExpiry := now.Add(10 * time.Minute)
				var mutate func() error
				switch mutation {
				case "revoke":
					mutate = func() error { return receiver.RevokeRoutes(peer, []string{revokedID}) }
				case "apply":
					raw, nextReview := routeExportReview(t, issuer, receiver, candidates, time.Now().UTC())
					mutate = func() error {
						return receiver.ApplyRouteUpdate(peer, raw, nextReview.Digest, []string{retainedID}, grantExpiry)
					}
				case "approve":
					mutate = func() error { return receiver.ApproveRoutes(peer, review.Digest, []string{retainedID}, grantExpiry) }
				}
				retiring := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(retiring.allowClose) }) }
				defer release()
				receiver.mu.Lock()
				previous := receiver.clients[peer]
				previous.makeClient = func(tailcat.Addr) peerTransport { return retiring }
				receiver.mu.Unlock()
				existing, err := receiver.DialPeer(context.Background(), peer, "tcp", 8080)
				if err != nil {
					t.Fatal("prepare retiring fake engine", err)
				}
				defer existing.Close()
				mutationDone := make(chan error, 1)
				go func() { mutationDone <- mutate() }()
				select {
				case <-retiring.closeStarted:
				case <-time.After(time.Second):
					t.Fatal("mutation did not reach retiring engine teardown")
				}
				receiver.mu.Lock()
				replacement := receiver.clients[peer]
				if replacement == previous || !replacement.remote.ClientPrivate.Equal(previous.remote.ClientPrivate) || replacement.remote.Address != previous.remote.Address {
					receiver.mu.Unlock()
					t.Fatal("mutation did not publish a replacement preserving the saved role")
				}
				started := make(chan struct{})
				var startOnce sync.Once
				fresh := &fakePeerTransport{}
				replacement.makeClient = func(tailcat.Addr) peerTransport {
					startOnce.Do(func() { close(started) })
					return fresh
				}
				receiver.mu.Unlock()
				type dialResult struct {
					conn net.Conn
					err  error
				}
				dialDone := make(chan dialResult, 1)
				waiting, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				go func() {
					conn, err := receiver.DialPeer(waiting, peer, network, 8080)
					dialDone <- dialResult{conn, err}
				}()
				select {
				case got := <-dialDone:
					if got.conn != nil {
						got.conn.Close()
					}
					if !errors.Is(got.err, context.DeadlineExceeded) {
						t.Errorf("queued replacement dial returned %v; want caller deadline while old Close is held", got.err)
					}
				case <-time.After(time.Second):
					t.Error("replacement admission ignored its caller deadline")
				}
				select {
				case <-started:
					t.Error("replacement factory started before the same-role retiring engine finished Close")
				default:
				}
				if retiring.closed.Load() != 0 {
					t.Fatal("fixture released old teardown before checking overlap")
				}
				release()
				select {
				case err := <-mutationDone:
					if err != nil {
						t.Fatal("route mutation failed", err)
					}
				case <-time.After(time.Second):
					t.Fatal("route mutation did not finish after teardown release")
				}
				// Teardown serialization must delay a legitimate replacement, not leave
				// its gate locked forever or silently discard the remaining permission.
				connected, err := receiver.DialPeer(context.Background(), peer, network, 8080)
				if err != nil {
					t.Fatal("approved route did not resume after teardown", err)
				}
				connected.Close()
			})
		}
	}
}
