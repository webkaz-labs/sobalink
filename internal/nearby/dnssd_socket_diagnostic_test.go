package nearby

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type dnssdSocketReadback struct {
	BoundEndpoint      string `json:"boundEndpoint"`
	BoundInterface     string `json:"boundInterface"`
	MulticastInterface string `json:"multicastInterface"`
	MulticastAddress   string `json:"multicastAddress"`
	MulticastTTL       string `json:"multicastTTL"`
	MulticastLoopback  string `json:"multicastLoopback"`
	Nonblocking        string `json:"nonblocking"`
}

func dnssdReadback(values [7]int) *dnssdSocketReadback {
	var out [7]string
	for i, v := range values {
		out[i] = "unavailable"
		if v == 0 {
			out[i] = "mismatch"
		}
		if v == 1 {
			out[i] = "matched"
		}
	}
	return &dnssdSocketReadback{out[0], out[1], out[2], out[3], out[4], out[5], out[6]}
}
func (r *dnssdSocketReadback) state() string {
	if r == nil {
		return "unavailable"
	}
	values := []string{r.BoundEndpoint, r.BoundInterface, r.MulticastInterface, r.MulticastTTL, r.MulticastLoopback, r.Nonblocking}
	if r.MulticastAddress == "unavailable" {
		return "unavailable"
	}
	for _, value := range values {
		if value == "unavailable" {
			return "unavailable"
		}
	}
	for _, value := range values {
		if value != "matched" {
			return "mismatch"
		}
	}
	// IP_MULTICAST_IF can report an interface's primary address. Only the
	// positive IFINDEX identifies egress; getsockname identifies our source.
	return "matched"
}

type dnssdRouteResult struct {
	Status             string `json:"status"`
	DestinationMatched bool   `json:"destinationMatched"`
	InterfaceMatched   bool   `json:"interfaceMatched"`
	Up                 bool   `json:"up"`
	Multicast          bool   `json:"multicast"`
	Scoped             bool   `json:"scoped"`
	Reject             bool   `json:"reject"`
	Blackhole          bool   `json:"blackhole"`
	OwnerJoined        bool   `json:"ownerJoined"`
}

// Never export route output or return its fields as strings. A snapshot does
// not determine the actual selected-interface multicast forwarding path.
func dnssdParseRoute(data []byte, selectedName string) dnssdRouteResult {
	r := dnssdRouteResult{Status: "invalid"}
	if len(data) > 8192 {
		r.Status = "output_limit"
		return r
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "route to", "interface", "flags":
			if seen[key] {
				return dnssdRouteResult{Status: "invalid"}
			}
			seen[key] = true
		default:
			continue
		}
		switch key {
		case "route to":
			r.DestinationMatched = value == "224.0.0.251"
		case "interface":
			r.InterfaceMatched = value == selectedName
		case "flags":
			if !strings.HasPrefix(value, "<") || !strings.HasSuffix(value, ">") {
				return dnssdRouteResult{Status: "invalid"}
			}
			for _, flag := range strings.Split(value[1:len(value)-1], ",") {
				switch flag {
				case "UP":
					r.Up = true
				case "MULTICAST":
					r.Multicast = true
				case "IFSCOPE":
					r.Scoped = true
				case "REJECT":
					r.Reject = true
				case "BLACKHOLE":
					r.Blackhole = true
				}
			}
		}
	}
	if len(seen) != 3 {
		return dnssdRouteResult{Status: "invalid"}
	}
	r.Status = "observed"
	return r
}

type dnssdRouteOwner struct {
	cancel context.CancelFunc
	done   chan struct{}
	result dnssdRouteResult
}

func startDNSServiceRouteOwner(ctx context.Context, run func(context.Context) dnssdRouteResult) *dnssdRouteOwner {
	child, cancel := context.WithCancel(ctx)
	owner := &dnssdRouteOwner{cancel: cancel, done: make(chan struct{})}
	go func() { defer close(owner.done); owner.result = run(child) }()
	return owner
}

// Both stdout and stderr share one finite private buffer. Its contents are
// parsed locally and discarded; even command failures export fixed status only.
type dnssdRouteBuffer struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
	cancel   context.CancelFunc
}

func (b *dnssdRouteBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := 8192 - len(b.data)
	n := min(room, len(data))
	b.data = append(b.data, data[:n]...)
	if n < len(data) {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	return len(data), nil
}
func TestDNSServiceSocketReadbackAndRoutePrivacy(t *testing.T) {
	matched := [7]int{1, 1, 1, 1, 1, 1, 1}
	if dnssdReadback(matched).state() != "matched" {
		t.Fatal("matched readback rejected")
	}
	for i := range matched {
		for _, v := range []int{0, 2} {
			bad := matched
			bad[i] = v
			want := "mismatch"
			if v == 2 {
				want = "unavailable"
			}
			if i == 3 && v == 0 {
				want = "matched"
			}
			if dnssdReadback(bad).state() != want {
				t.Fatal("readback guard mismatch")
			}
		}
	}
	data := []byte("route to: 224.0.0.251\n destination: 224.0.0.0\n interface: synthetic0\n flags: <UP,STATIC,IFSCOPE,MULTICAST>\n")
	r := dnssdParseRoute(data, "synthetic0")
	if r.Status != "observed" || !r.DestinationMatched || !r.InterfaceMatched || !r.Up || !r.Multicast || !r.Scoped || r.OwnerJoined {
		t.Fatal("fixed route projection failed")
	}
	for _, bad := range [][]byte{append(append([]byte{}, data...), []byte("interface: other\n")...), []byte("private diagnostic text"), make([]byte, 8193)} {
		if dnssdParseRoute(bad, "synthetic0").Status == "observed" {
			t.Fatal("malformed route accepted")
		}
	}
	r = dnssdParseRoute(data, "different0")
	if r.InterfaceMatched {
		t.Fatal("different interface accepted")
	}
	b := &dnssdRouteBuffer{}
	b.Write(make([]byte, 9000))
	b.Write([]byte("more"))
	if len(b.data) != 8192 || !b.overflow {
		t.Fatal("private capture budget exceeded")
	}
}
func TestDNSServiceRouteOwnerMustJoin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	owner := startDNSServiceRouteOwner(context.Background(), func(ctx context.Context) dnssdRouteResult {
		close(entered)
		<-ctx.Done()
		<-release
		return dnssdRouteResult{Status: "timeout", OwnerJoined: true}
	})
	<-entered
	owner.cancel()
	select {
	case <-owner.done:
		t.Fatal("cancellation fabricated worker completion")
	default:
	}
	close(release)
	dnssdWait(t, owner.done)
	if !owner.result.OwnerJoined {
		t.Fatal("joined route result missing")
	}
}
