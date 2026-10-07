package endpointmeta

import "time"

// These placeholders carry only the exact encoded widths of fields calculated
// later. Preflight neither hashes/signs metadata nor clones the input snapshot.
const digestPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"
const signaturePlaceholder = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func counterPlaceholder(s string) string {
	n := len(s)
	if n == 0 || n > 20 {
		return digestPlaceholder[:20]
	}
	carry := true
	for i := range s {
		if s[i] != '9' {
			carry = false
			break
		}
	}
	if carry && n < 20 {
		n++
	}
	return digestPlaceholder[:n]
}

func timePlaceholder(now time.Time) string {
	var buf [64]byte
	b := now.UTC().AppendFormat(buf[:0], time.RFC3339Nano)
	return digestPlaceholder[:len(b)]
}

func fitsFence(s Snapshot, m Mutation, reviewDigest, transactionID string, now time.Time, budget int) bool {
	if budget <= 0 {
		return false
	}
	w := wireSizer{left: budget}
	next := s
	next.Revision = counterPlaceholder(s.Revision)
	next.PendingChange = nil
	if !next.measure(&w) || !w.reserve(len(`,"pending_change":`)) {
		return false
	}
	p := PendingChange{TransactionID: transactionID, FencedAt: timePlaceholder(now), PairBindings: []string{}, BaseRevision: s.Revision, BaseDigest: digestPlaceholder, ReviewDigest: reviewDigest, Mutation: m}
	if !p.measure(&w) {
		return false
	}
	if m.Kind != "local-endpoint" {
		return w.str(m.PairBinding)
	}
	count := 0
	for _, r := range s.Peers {
		if r.EndpointState != nil {
			if count > 0 && !w.reserve(1) || !w.str(r.EndpointState.PairBinding) {
				return false
			}
			count++
		}
	}
	return true
}

func fitsFinish(s Snapshot, cancel bool, now time.Time, budget int) bool {
	if budget <= 0 || s.PendingChange == nil {
		return false
	}
	w := wireSizer{left: budget}
	next := s
	next.PendingChange = nil
	next.Revision = counterPlaceholder(s.Revision)
	next.ObservedAt = timePlaceholder(now)
	m := s.PendingChange.Mutation
	if m.Kind == "local-endpoint" {
		next.PreviousLocalEndpoint = s.LocalPeer.Endpoint
		next.LocalPeer.Endpoint = m.LocalEndpoint
		return next.measureReplacing(&w, -1, nil, true)
	}
	i, err := findRecord(s, m.PairBinding)
	if err != nil || m.State == nil {
		return false
	}
	r := s.Peers[i]
	state := *m.State
	if cancel {
		state.Approval = nil
		state.ReceiveStatus = "cancelled"
	}
	if state.Approval != nil && state.ReceivedProof != nil {
		u, a := state.ReceivedProof.Update, state.Approval
		if !currentForSize(u.Issued, u.Lifetime, u.Expires, now) || !currentForSize(a.Granted, a.Lifetime, a.Expires, now) {
			state.Approval = nil
			state.ReceiveStatus = "expired"
		}
	}
	var follow FollowApproval
	if state.Follow != nil {
		follow = *state.Follow
		if follow.Active && !currentForSize(follow.Granted, follow.Lifetime, follow.Expires, now) {
			follow.Active = false
		}
		state.Follow = &follow
	}
	r.EndpointState = &state
	r.Peer.Endpoint = state.LastEndpoint
	r.Revision = next.Revision
	r.UpgradePending = nil
	return next.measureReplacing(&w, i, &r, false)
}

func fitsExport(s Snapshot, remoteKey string, u UpdateBody, now time.Time, budget int) bool {
	if budget <= 0 {
		return false
	}
	i, err := recordForKey(s, remoteKey)
	if err != nil || s.Peers[i].EndpointState == nil {
		return false
	}
	w := wireSizer{left: budget}
	next := s
	next.Revision = counterPlaceholder(s.Revision)
	next.ObservedAt = timePlaceholder(now)
	r := s.Peers[i]
	state := *r.EndpointState
	proof := Envelope{Update: u, Signature: signaturePlaceholder}
	state.IssuedVersion = 1
	state.IssuedHighwater = counterPlaceholder(state.IssuedHighwater)
	state.IssuedProof = &proof
	r.EndpointState = &state
	r.Revision = next.Revision
	r.UpgradePending = nil
	return next.measureReplacing(&w, i, &r, false)
}

// The sizing pass needs to know which approvals reconciliation will remove.
// Decode canonical UTC instants without error construction or formatting. Full
// state validation still runs after preflight and decides whether data is valid.
func currentForSize(issued, lifetime, expires string, now time.Time) bool {
	t, ok := instantForSize(issued)
	if !ok || now.IsZero() || now.Before(t) {
		return false
	}
	if lifetime == "until-revoked" {
		return true
	}
	e, ok := instantForSize(expires)
	return ok && now.Before(e)
}

func instantForSize(s string) (time.Time, bool) {
	if len(s) < 20 || len(s) > 30 || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' || s[len(s)-1] != 'Z' {
		return time.Time{}, false
	}
	fields := [6]int{}
	for i, bounds := range [6][2]int{{0, 4}, {5, 7}, {8, 10}, {11, 13}, {14, 16}, {17, 19}} {
		for j := bounds[0]; j < bounds[1]; j++ {
			if s[j] < '0' || s[j] > '9' {
				return time.Time{}, false
			}
			fields[i] = fields[i]*10 + int(s[j]-'0')
		}
	}
	nanos := 0
	if len(s) != 20 {
		if len(s) < 22 || s[19] != '.' || s[len(s)-2] == '0' {
			return time.Time{}, false
		}
		for j := 20; j < len(s)-1; j++ {
			if s[j] < '0' || s[j] > '9' {
				return time.Time{}, false
			}
			nanos = nanos*10 + int(s[j]-'0')
		}
		for j := len(s) - 21; j < 9; j++ {
			nanos *= 10
		}
	}
	t := time.Date(fields[0], time.Month(fields[1]), fields[2], fields[3], fields[4], fields[5], nanos, time.UTC)
	y, m, d := t.Date()
	h, min, sec := t.Clock()
	ok := !t.IsZero() && y == fields[0] && int(m) == fields[1] && d == fields[2] && h == fields[3] && min == fields[4] && sec == fields[5]
	return t, ok
}
