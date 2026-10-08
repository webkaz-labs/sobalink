package core

import (
	"reflect"
	"testing"
	"time"
)

// Synthetic saved-model admission only. No publisher, Node constructor, Start,
// generation preparation, transport or live endpoint movement is invoked.
func TestCurrentStartupAdmissionPreservesProjectionAndOriginalBounds(t *testing.T) {
	c, s, key := localEndpointExportFixture(t, func(state *directLANState) { currentCoreProjectionSignedState(t, state) })
	a, err := s.captureActivationLocked(c, time.Now())
	if err != nil || !a.currentEndpoints || len(a.deadlines) != 2 || a.currentProjection == "" {
		t.Fatal("current startup data", err)
	}
	if a.projection.Peers[0].Key != key || a.projection.Peers[0].Endpoint.String() != "127.0.0.3:22003" || !reflect.DeepEqual(a.projection.PairContexts[key], *s.state.Metadata.Peers[0].PairContext) {
		t.Fatal("endpoint or immutable binding changed")
	}
	for key, bound := range a.deadlines {
		if bound != s.endpointDeadlines[key] || bound.monotonic.IsZero() {
			t.Fatal("original store deadline lost")
		}
	}
	if err := s.matchActivationLocked(a, time.Now()); err != nil {
		t.Fatal("unchanged current admission", err)
	}
}

func TestCurrentStartupAdmissionRejectsMissingOrElapsedOriginalBound(t *testing.T) {
	for _, change := range []string{"missing", "elapsed", "changed", "projection"} {
		t.Run(change, func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) { currentCoreProjectionSignedState(t, state) })
			now := time.Now()
			a, err := s.captureActivationLocked(c, now)
			if err != nil {
				t.Fatal(err)
			}
			var chosen directLANEndpointDeadlineKey
			for key := range a.deadlines {
				chosen = key
				break
			}
			switch change {
			case "missing":
				delete(s.endpointDeadlines, chosen)
			case "elapsed":
				bound := s.endpointDeadlines[chosen]
				bound.monotonic, bound.expired = now.Add(-time.Second), true
				s.endpointDeadlines[chosen] = bound
			case "changed":
				bound := s.endpointDeadlines[chosen]
				bound.monotonic = bound.monotonic.Add(time.Second)
				s.endpointDeadlines[chosen] = bound
			case "projection":
				a.currentProjection = "changed"
			}
			if err := s.matchActivationLocked(a, now); err == nil {
				t.Fatal("stale startup admission accepted")
			}
			if _, exists := s.endpointDeadlines[chosen]; change == "missing" && exists {
				t.Fatal("missing original cutoff reconstructed")
			}
		})
	}
}
