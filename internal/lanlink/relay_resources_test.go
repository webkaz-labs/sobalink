package lanlink

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"reflect"
	"syscall"
	"testing"
	"time"

	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

func TestRelayResourcesDefaultsAndFiniteOverrides(t *testing.T) {
	got, err := (RelayResources{}).WithDefaults()
	if err != nil || got != (RelayResources{4, 4, 64, 16}) {
		t.Fatal("compatibility defaults changed", got, err)
	}
	got, err = (RelayResources{8, 7, 128, 32}).WithDefaults()
	if err != nil || got != (RelayResources{8, 7, 128, 32}) {
		t.Fatal("finite overrides rejected", err)
	}
	for _, bad := range []RelayResources{{CandidateAttempts: -1}, {PresenceConnections: tailcat.RelayRegionNamespace + 1}, {TLSConnections: -1}, {AdmissionConnections: -1}} {
		if _, err := bad.WithDefaults(); err == nil {
			t.Fatal("invalid resource accepted")
		}
	}
}
func TestRelayFivePlusStoredOfferedAndApprovedWithoutWidening(t *testing.T) {
	a, b, now, _ := routeNodesFixture(t)
	before := b.RemoteSnapshot()[0]
	var candidates []RouteCandidate
	for i := 0; i < 7; i++ {
		candidates = append(candidates, routeFixture("external", fmt.Sprintf("192.0.2.%d:443", 20+i)))
	}
	raw, review := routeExportReview(t, a, b, candidates, now)
	if len(review.Update.Candidates) != 7 {
		t.Fatal("offer was silently truncated")
	}
	ids := []string{candidates[0].ID(), candidates[4].ID(), candidates[5].ID(), candidates[6].ID()}
	if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, ids, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	got := routeSnapshot(b.RemoteSnapshot()[0], now)
	if len(got.Candidates) != 7 || len(got.Permitted) != 4 {
		t.Fatal("metadata/approval count changed")
	}
	after := b.RemoteSnapshot()[0]
	if !reflect.DeepEqual(before.ClientPrivate, after.ClientPrivate) || before.IncomingClientKey != after.IncomingClientKey || before.Peer != after.Peer {
		t.Fatal("more candidates changed the pair")
	}
	if got.Lifetime != RouteLifetimeFinite || !got.NextExpiry.Equal(now.Add(time.Minute)) {
		t.Fatal("legacy finite offer widened lifetime")
	}
	all := []string{}
	for _, c := range candidates {
		all = append(all, c.ID())
	}
	if err := b.approveRoutes(a.PublicKey(), review.Digest, all, now.Add(time.Minute), now); err != nil {
		t.Fatal("five-plus explicit grants rejected", err)
	}
	if len(routeSnapshot(b.RemoteSnapshot()[0], now).Permitted) != 7 {
		t.Fatal("explicit grants truncated")
	}
	if err := b.RevokeRoutes(a.PublicKey(), all[1:]); err != nil {
		t.Fatal(err)
	}
	if len(routeSnapshot(b.RemoteSnapshot()[0], now).Permitted) != 1 {
		t.Fatal("five-plus exact revocation widened remaining grants")
	}
}
func TestRelayRouteEnvelopeStaysBounded(t *testing.T) {
	a, b, ab, _ := routePairFixture()
	var candidates []RouteCandidate
	for i := 0; i < 250; i++ {
		candidates = append(candidates, routeFixture("external", fmt.Sprintf("192.0.2.20:%d", 10000+i)))
	}
	_, err := SealRouteUpdateWithLifetime(a, ab, 1, candidates, RouteLifetimeUntilRevoked, time.Now(), time.Time{})
	var capacity *RouteEnvelopeCapacityError
	if !errors.As(err, &capacity) || capacity.Limit != maxPairPlaintext {
		t.Fatal("oversized exchange was not rejected explicitly", err)
	}
	_ = b
}
func TestRelayAttemptsRotateFairlyBeyondFour(t *testing.T) {
	r, anchor, _ := routeRuntimeFixture(t, time.Time{})
	var candidates []RouteCandidate
	for i := 0; i < 7; i++ {
		candidates = append(candidates, routeFixture("external", fmt.Sprintf("192.0.2.%d:443", 20+i)))
	}
	r.candidateAttempts = 4
	var tried []string
	r.makeClient = func(address tailcat.Addr) peerTransport {
		info, err := tailcat.ParseAddr(address)
		if err != nil {
			t.Fatal(err)
		}
		host := info.Region[0].Nodes[0].HostName
		tried = append(tried, host)
		if host == "192.0.2.24" {
			return &fakePeerTransport{}
		}
		return &fakePeerTransport{probeErr: syscall.ECONNREFUSED}
	}
	if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dial(context.Background(), "tcp", 8080); !errors.Is(err, syscall.ECONNREFUSED) || len(tried) != 4 {
		t.Fatal("per-call attempts exceeded chosen budget", tried, err)
	}
	// Even after every hold-down expires, the cursor must reach the fifth relay.
	for key := range r.failures {
		r.failures[key] = time.Now().Add(-time.Second)
	}
	conn, err := r.dial(context.Background(), "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(tried) != 5 || tried[4] != "192.0.2.24" {
		t.Fatal("later approved candidate starved", tried)
	}
	if len(r.candidates) != 7 {
		t.Fatal("resource budget truncated permissions")
	}
}
func TestRelayUnknownTimeoutAndDenialNeverFallback(t *testing.T) {
	for name, failure := range map[string]error{"timeout": context.DeadlineExceeded, "cancel": context.Canceled, "unknown": errors.New("synthetic unknown proof"), "pin": ErrRelayMismatch, "permission": ErrRoutePermission, "certificate": x509.UnknownAuthorityError{}, "joined-denial": errors.Join(syscall.ECONNREFUSED, ErrUntrusted), "joined-unknown": errors.Join(syscall.ECONNREFUSED, errors.New("unknown")), "joined-timeout": errors.Join(syscall.ECONNREFUSED, context.DeadlineExceeded)} {
		t.Run(name, func(t *testing.T) {
			r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
			calls := 0
			r.makeClient = func(tailcat.Addr) peerTransport { calls++; return &fakePeerTransport{probeErr: failure} }
			if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.dial(context.Background(), "tcp", 8080); err == nil || calls != 1 {
				t.Fatal("terminal error selected another route", err, calls)
			}
		})
	}
	for _, cause := range []error{syscall.ECONNREFUSED, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}, errors.Join(syscall.ECONNRESET, syscall.ENETUNREACH)} {
		if !relayAvailabilityError(cause) {
			t.Fatal("positively typed availability rejected", cause)
		}
	}
}
func TestRelayConfiguredRegionsBudgetNotMetadataCeiling(t *testing.T) {
	cfg := NodeConfig{Relay: routeFixture("external", "192.0.2.20:443").Relay}
	for i := 0; i < 7; i++ {
		cfg.Candidates = append(cfg.Candidates, routeFixture("external", fmt.Sprintf("192.0.2.%d:443", 20+i)))
	}
	if _, err := configuredRegions(cfg); err == nil {
		t.Fatal("default active presence exceeded")
	}
	cfg.RelayResources.PresenceConnections = 7
	if regions, err := configuredRegions(cfg); err != nil || len(regions) != 7 {
		t.Fatal("raised active budget did not activate exact candidates", err)
	}
}

func TestRelayConfiguredSlotsExhaustCloseAndRecover(t *testing.T) {
	resources, err := (RelayResources{TLSConnections: 2, AdmissionConnections: 1}).WithDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{resources.TLSConnections, resources.AdmissionConnections} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			listener := newFakeListener()
			limited := &limitedRelayListener{Listener: listener, slots: make(chan struct{}, limit)}
			defer listener.Close()
			var active []net.Conn
			for i := 0; i < limit; i++ {
				listener.accept <- newFakeDatagrams(6200 + i)
				conn, err := limited.Accept()
				if err != nil {
					t.Fatal(err)
				}
				active = append(active, conn)
			}
			rejected := newFakeDatagrams(6300)
			listener.accept <- rejected
			result := make(chan net.Conn, 1)
			finished := make(chan error, 1)
			go func() {
				conn, err := limited.Accept()
				if err == nil {
					result <- conn
				}
				finished <- err
			}()
			select {
			case <-rejected.closed:
			case <-time.After(time.Second):
				t.Fatal("excess connection retained")
			}
			if len(limited.slots) != limit {
				t.Fatal("exhaustion released another connection's slot")
			}
			active[0].Close()
			replacement := newFakeDatagrams(6301)
			listener.accept <- replacement
			select {
			case conn := <-result:
				conn.Close()
			case <-time.After(time.Second):
				t.Fatal("released slot could not admit new connection")
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			for _, conn := range active {
				conn.Close()
			}
			if len(limited.slots) != 0 {
				t.Fatal("closed connections retained resources")
			}
		})
	}
}

func TestRelayApplicationDenialCannotBeConvertedByHealthFailure(t *testing.T) {
	for _, failure := range []error{ErrRoutePermission, ErrRelayMismatch, errors.New("unknown application error"), errors.Join(syscall.ECONNREFUSED, ErrUntrusted)} {
		r, anchor, candidates := routeRuntimeFixture(t, time.Time{})
		calls := 0
		fake := &fakePeerTransport{dialErr: failure}
		probeCount := 0
		fake.probe = func(context.Context) error {
			probeCount++
			if probeCount > 1 {
				return syscall.ECONNREFUSED
			}
			return nil
		}
		r.makeClient = func(tailcat.Addr) peerTransport { calls++; return fake }
		if err := r.prepare(anchor, false, RouteSnapshot{Permitted: candidates}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.dial(context.Background(), "tcp", 8080); !errors.Is(err, failure) || calls != 1 || probeCount != 1 {
			t.Fatal("application denial reclassified by health check", err, calls, probeCount)
		}
	}
}

type testDERPPhaseError struct {
	phase string
	cause error
}

func (e testDERPPhaseError) Error() string            { return "synthetic typed DERP failure" }
func (e testDERPPhaseError) Unwrap() error            { return e.cause }
func (e testDERPPhaseError) DERPFailurePhase() string { return e.phase }
func TestRelayDERPPhasePrecedesNestedSocketCause(t *testing.T) {
	for _, phase := range []string{"tls", "protocol", "transport", "unknown", ""} {
		if relayAvailabilityError(testDERPPhaseError{phase, syscall.ECONNRESET}) {
			t.Fatal("non-dial DERP phase permitted another candidate", phase)
		}
	}
	dial := testDERPPhaseError{"dial", syscall.ECONNREFUSED}
	if !relayAvailabilityError(dial) {
		t.Fatal("typed dial availability rejected")
	}
	if relayAvailabilityError(errors.Join(dial, testDERPPhaseError{"tls", syscall.ECONNRESET})) {
		t.Fatal("mixed TLS denial admitted fallback")
	}
}
