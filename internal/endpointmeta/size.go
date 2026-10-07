package endpointmeta

import "unicode/utf8"

// Even the shortest permitted prefix (::1/128) needs nine JSON bytes and an
// array separator. The object and array delimiters exceed the saved separator,
// so this byte-budget-derived bound cannot exclude a fitting valid scope.
const maxScopePrefixes = MaxFrameBytes / (len("::1/128") + 3)

// wireSizer counts the exact encoding/json representation without allocating.
// All wire fields are mandatory and every shape has a fixed field order.
type wireSizer struct{ left int }

// The closed type switch keeps the budget on the stack, including for pointer
// inputs used by decoders. No interface method receives the budget pointer.
func fitsWire(v wireValue) bool {
	w := wireSizer{left: MaxFrameBytes}
	switch v := v.(type) {
	case Scope:
		return v.measure(&w)
	case *Scope:
		return v.measure(&w)
	case PeerWire:
		return v.measure(&w)
	case *PeerWire:
		return v.measure(&w)
	case PairContext:
		return v.measure(&w)
	case *PairContext:
		return v.measure(&w)
	case Invitation:
		return v.measure(&w)
	case *Invitation:
		return v.measure(&w)
	case UpdateBody:
		return v.measure(&w)
	case *UpdateBody:
		return v.measure(&w)
	case Envelope:
		return v.measure(&w)
	case *Envelope:
		return v.measure(&w)
	case PairRequest:
		return v.measure(&w)
	case *PairRequest:
		return v.measure(&w)
	case PrepareRequest:
		return v.measure(&w)
	case *PrepareRequest:
		return v.measure(&w)
	case BoundRequest:
		return v.measure(&w)
	case *BoundRequest:
		return v.measure(&w)
	case PairReply:
		return v.measure(&w)
	case *PairReply:
		return v.measure(&w)
	case PrepareReply:
		return v.measure(&w)
	case *PrepareReply:
		return v.measure(&w)
	case ContextReply:
		return v.measure(&w)
	case *ContextReply:
		return v.measure(&w)
	case SessionReply:
		return v.measure(&w)
	case *SessionReply:
		return v.measure(&w)
	case UpdateReply:
		return v.measure(&w)
	case *UpdateReply:
		return v.measure(&w)
	case ErrorReply:
		return v.measure(&w)
	case *ErrorReply:
		return v.measure(&w)
	default:
		return false
	}
}

func (w *wireSizer) reserve(n int) bool {
	if n < 0 || n > w.left {
		return false
	}
	w.left -= n
	return true
}

func (w *wireSizer) str(s string) bool {
	if w.left < 2 || len(s) > w.left-2 {
		return false
	}
	w.left -= len(s) + 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			extra := 0
			switch c {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				extra = 1
			case '<', '>', '&':
				extra = 5
			default:
				if c < 0x20 {
					extra = 5
				}
			}
			if !w.reserve(extra) {
				return false
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			return false
		} else if r == '\u2028' || r == '\u2029' {
			if !w.reserve(3) {
				return false
			}
		}
		i += n
	}
	return true
}

func (w *wireSizer) strings(values ...string) bool {
	for _, s := range values {
		if !w.str(s) {
			return false
		}
	}
	return true
}

func (w *wireSizer) integer(n int) bool {
	size := 1
	if n < 0 {
		size++
	}
	for n <= -10 || n >= 10 {
		n /= 10
		size++
	}
	return w.reserve(size)
}

func (w *wireSizer) boolean(b bool) bool {
	if b {
		return w.reserve(4)
	}
	return w.reserve(5)
}

func (s Scope) measure(w *wireSizer) bool {
	if len(s.Prefixes) > maxScopePrefixes || !w.reserve(len(`{"family":,"prefixes":}`)) || !w.str(s.Family) {
		return false
	}
	if s.Prefixes == nil {
		return w.reserve(4)
	}
	if !w.reserve(2) || len(s.Prefixes) > 0 && !w.reserve(len(s.Prefixes)-1) {
		return false
	}
	return w.strings(s.Prefixes...)
}

func (p PeerWire) measure(w *wireSizer) bool {
	return w.reserve(len(`{"key":,"name":,"endpoint":,"tunnel_key":}`)) && w.strings(p.Key, p.Name, p.Endpoint, p.TunnelKey)
}

func (p PairContext) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"host_key":,"joiner_key":,"host_tunnel_key":,"joiner_tunnel_key":,"host_nonce":,"joiner_nonce":,"host_endpoint":,"joiner_endpoint":,"host_scope":,"joiner_scope":}`)) && w.integer(p.Version) && w.strings(p.HostKey, p.JoinerKey, p.HostTunnelKey, p.JoinerTunnelKey, p.HostNonce, p.JoinerNonce, p.HostEndpoint, p.JoinerEndpoint) && p.HostScope.measure(w) && p.JoinerScope.measure(w)
}

func (i Invitation) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"host":,"recipient_key":,"token":,"expires":,"host_nonce":,"host_scope":}`)) && w.integer(i.Version) && i.Host.measure(w) && w.strings(i.RecipientKey, i.Token, i.Expires, i.HostNonce) && i.HostScope.measure(w)
}

func (u UpdateBody) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"domain":,"pair_binding":,"issuer":,"recipient":,"issuer_tunnel_key":,"recipient_tunnel_key":,"sequence":,"prior_endpoint":,"operation":,"endpoint":,"scope_digest":,"issued":,"lifetime":,"expires":}`)) && w.integer(u.Version) && w.strings(u.Domain, u.PairBinding, u.Issuer, u.Recipient, u.IssuerTunnelKey, u.RecipientTunnelKey, u.Sequence, u.PriorEndpoint, u.Operation, u.Endpoint, u.ScopeDigest, u.Issued, u.Lifetime, u.Expires)
}

func (e Envelope) measure(w *wireSizer) bool {
	return w.reserve(len(`{"update":,"signature":}`)) && e.Update.measure(w) && w.str(e.Signature)
}

func (r PairRequest) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"token":,"peer":,"joiner_nonce":,"joiner_scope":}`)) && w.integer(r.Version) && w.strings(r.Operation, r.Token) && r.Peer.measure(w) && w.str(r.JoinerNonce) && r.JoinerScope.measure(w)
}

func (r PrepareRequest) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"sender":,"recipient":,"sender_tunnel_key":,"recipient_tunnel_key":,"sender_nonce":,"sender_endpoint":,"recipient_endpoint":,"sender_scope":}`)) && w.integer(r.Version) && w.strings(r.Operation, r.Sender, r.Recipient, r.SenderTunnelKey, r.RecipientTunnelKey, r.SenderNonce, r.SenderEndpoint, r.RecipientEndpoint) && r.SenderScope.measure(w)
}

func (r BoundRequest) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"pair_binding":}`)) && w.integer(r.Version) && w.strings(r.Operation, r.PairBinding)
}

func (r PairReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"peer":,"pair_context":,"pair_binding":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && r.Peer.measure(w) && r.PairContext.measure(w) && w.str(r.PairBinding)
}

func (r PrepareReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"pair_context":,"pair_binding":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && r.PairContext.measure(w) && w.str(r.PairBinding)
}

func (r ContextReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"pair_binding":,"state":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && w.strings(r.PairBinding, r.State)
}

func (r SessionReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"pair_binding":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && w.str(r.PairBinding)
}

func (r UpdateReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"pair_binding":,"sequence":,"update_digest":,"outcome":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && w.strings(r.PairBinding, r.Sequence, r.UpdateDigest, r.Outcome)
}

func (r ErrorReply) measure(w *wireSizer) bool {
	return w.reserve(len(`{"version":,"operation":,"ok":,"code":}`)) && w.integer(r.Version) && w.str(r.Operation) && w.boolean(r.OK) && w.str(r.Code)
}
