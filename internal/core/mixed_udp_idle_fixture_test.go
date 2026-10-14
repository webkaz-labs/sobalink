package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// reviewRetireIdleUDPSource joins the delivered source's counted work before
// aging it. ReadFrom has returned its delivery borrow, but enqueueAddress may
// still be returning its producer borrow. Recheck changed under p.mu so neither
// completion nor a wakeup can be missed. Age and retire under that same lock.
// waiting is an optional test handshake, closed once a held borrow is observed.
func reviewRetireIdleUDPSource(ctx context.Context, p *mixedPacket, address net.Addr, waiting chan<- struct{}) error {
	association, ok := address.(*mixedPacketAssociation)
	if !ok {
		return errors.New("idle fixture requires an exact UDP association")
	}
	owner, source := association.owner()
	if owner != p || source == nil {
		return errors.New("idle fixture requires a live UDP source")
	}
	for {
		p.mu.Lock()
		if !p.sourceOpenLocked(source) {
			p.mu.Unlock()
			return errors.New("UDP source stopped before idle fixture retirement")
		}
		if source.queued != 0 {
			p.mu.Unlock()
			return errors.New("idle fixture still has queued UDP packets")
		}
		if source.borrowing == 0 {
			source.lastActive = time.Time{}
			p.retireIdleLocked(time.Now())
			stopped := source.stopped
			p.mu.Unlock()
			if !stopped {
				return errors.New("quiescent expired UDP source was not retired")
			}
			return nil
		}
		changed := source.changed
		p.mu.Unlock()
		if waiting != nil {
			close(waiting)
			waiting = nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func reviewOpaqueUDPSource(t *testing.T) (*Core, *mixedPacket, *mixedPacketSource, net.Addr, string) {
	t.Helper()
	nodes := map[string]*mixedTestPacketNode{}
	c, _, owner, _ := mixedCorePair(t, func(name string, node *pipeNode) NetworkBackend {
		p := &mixedTestPacketNode{pipeNode: node}
		nodes[name] = p
		return p
	})
	conn, err := owner.ListenPacket("udp", netip.AddrPortFrom(owner.self, 7000).String())
	if err != nil {
		t.Fatal(err)
	}
	p := conn.(*mixedPacket)
	t.Cleanup(func() { _ = p.Close() })
	logical := mixedID("lan", "peer-b-lan")
	p.SetPeerFilter(func(id string) bool { return id == logical })
	// The regression controls the counted hold below. Finish this admission
	// synchronously so its precondition is independent of producer scheduling.
	p.enqueue("lan", nodes["lan"].latest(t), netip.MustParseAddrPort("100.64.0.2:32000"), []byte("admitted"))
	address := mixedPacketReceive(t, p, []byte("admitted"))
	association := address.(*mixedPacketAssociation)
	_, source := association.owner()
	if source == nil {
		t.Fatal("admitted UDP source missing")
	}
	p.mu.Lock()
	quiet := source.queued == 0 && source.borrowing == 0 && !source.stopped
	p.mu.Unlock()
	if !quiet {
		t.Fatal("UDP regression fixture is not initially quiescent and open")
	}
	if id, err := c.authenticated(context.Background(), source.alias); err != nil || id != logical {
		t.Fatalf("admitted UDP source rejected: %s %v", id, err)
	}
	return c, p, source, address, logical
}

func TestReviewOpaqueUDPIdleRetirementJoinsBorrow(t *testing.T) {
	c, p, source, address, logical := reviewOpaqueUDPSource(t)
	p.mu.Lock()
	p.borrowSourceLocked(source)
	p.mu.Unlock()
	held := true
	defer func() {
		if held {
			p.releaseBorrow(source)
		}
	}()

	// Reproduce the old fixture's missing precondition deterministically. Its
	// idle-retirement call is a no-op while this counted hold remains; successful
	// authentication here is expected because retirement has not happened.
	p.mu.Lock()
	source.lastActive = time.Time{}
	p.retireIdleLocked(time.Now())
	open := !source.stopped && source.queued == 0 && source.borrowing == 1 && p.aliases[source.key] == source
	p.mu.Unlock()
	if !open {
		t.Fatal("idle retirement did not preserve the held UDP source")
	}
	if id, err := c.authenticated(context.Background(), source.alias); err != nil || id != logical {
		t.Fatalf("held source was treated as already retired: %s %v", id, err)
	}

	// Keep the background watcher out of the proof. Only the corrected fixture
	// will age the source again, after joining the deliberately held borrow.
	p.mu.Lock()
	source.lastActive = time.Now()
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waiting := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- reviewRetireIdleUDPSource(ctx, p, address, waiting) }()
	select {
	case <-waiting:
	case err := <-result:
		t.Fatalf("idle fixture returned before observing the held borrow: %v", err)
	case <-ctx.Done():
		t.Fatal("idle fixture did not observe the held borrow")
	}
	select {
	case err := <-result:
		t.Fatalf("idle fixture returned before borrow release: %v", err)
	default:
	}
	p.releaseBorrow(source)
	held = false
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("idle fixture did not finish after borrow release")
	}
	p.mu.Lock()
	retired := source.stopped && source.borrowing == 0 && p.sources[source.alias] == nil && p.aliases[source.key] == nil
	p.mu.Unlock()
	if !retired {
		t.Fatal("joined UDP source was not idle-retired and cleaned up")
	}
	if _, err := c.authenticated(context.Background(), source.alias); err == nil {
		t.Fatal("idle retired alias authenticated")
	}
	if _, err := p.WriteTo([]byte("idle-retired"), address); err == nil {
		t.Fatal("idle retired alias routed reply")
	}
}

func TestReviewOpaqueUDPStoppedSourceRejectsRetainedRegistry(t *testing.T) {
	c, p, source, address, _ := reviewOpaqueUDPSource(t)
	p.mu.Lock()
	p.borrowSourceLocked(source)
	p.retireLocked(source.key, source)
	retained := source.stopped && source.borrowing == 1 && p.sources[source.alias] == source && p.aliases[source.key] == nil
	p.mu.Unlock()
	held := true
	defer func() {
		if held {
			p.releaseBorrow(source)
		}
	}()
	if !retained {
		t.Fatal("stopped UDP source was not retained behind its outstanding borrow")
	}
	p.owner.mu.Lock()
	_, registered := p.owner.sources[source.alias]
	p.owner.mu.Unlock()
	if !registered {
		t.Fatal("authentication registry disappeared before the rejection check")
	}
	select {
	case <-source.done:
		t.Fatal("stopped UDP source completed while its borrow was held")
	default:
	}
	if _, err := c.authenticated(context.Background(), source.alias); err == nil {
		t.Fatal("stopped alias authenticated through its retained registry")
	}
	if _, err := p.WriteTo([]byte("stopped"), address); err == nil {
		t.Fatal("stopped alias routed reply through its retained registry")
	}
	p.releaseBorrow(source)
	held = false
	p.mu.Lock()
	cleaned := source.borrowing == 0 && p.sources[source.alias] == nil
	p.mu.Unlock()
	p.owner.mu.Lock()
	_, registered = p.owner.sources[source.alias]
	p.owner.mu.Unlock()
	if !cleaned || registered {
		t.Fatal("stopped UDP source remained registered after its final borrow")
	}
	select {
	case <-source.done:
	default:
		t.Fatal("stopped UDP source did not acknowledge cleanup")
	}
}
