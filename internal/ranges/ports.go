// Package ranges implements compact, explicitly scoped service-port policies.
// A range never creates listeners or initiates probes for its individual ports.
package ranges

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	MaxPolicies                     = 64
	MaxIntervals                    = 256
	MaxMaterializedListeners        = 64
	MaxPolicyPeers                  = 32
	DiscoveryPort            uint16 = 54543
	PeerTransferPort         uint16 = 54544
	PairingPort              uint16 = 54545
)

// Interval is inclusive. Zero is never a service port.
type Interval struct{ First, Last uint16 }

// Set is an immutable normalized union of inclusive intervals. Its zero value
// is empty. Constructors and accessors never retain or expose a mutable slice.
type Set struct{ intervals []Interval }

// NewSet sorts and coalesces intervals without expanding them into ports.
func NewSet(intervals []Interval) (Set, error) {
	if len(intervals) > MaxIntervals {
		return Set{}, errors.New("too many port intervals")
	}
	out := append([]Interval(nil), intervals...)
	for _, r := range out {
		if r.First == 0 || r.Last < r.First {
			return Set{}, errors.New("ports must be in 1..65535 with ascending range endpoints")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].First < out[j].First || (out[i].First == out[j].First && out[i].Last < out[j].Last)
	})
	n := 0
	for _, r := range out {
		if n > 0 && uint32(r.First) <= uint32(out[n-1].Last)+1 {
			if r.Last > out[n-1].Last {
				out[n-1].Last = r.Last
			}
		} else {
			out[n] = r
			n++
		}
	}
	return Set{intervals: out[:n:n]}, nil
}

// Parse accepts a scalar, a comma-separated list, or inclusive ranges, for
// example "22,80,443,8000-8100". Whitespace around entries is ignored.
func Parse(text string) (Set, error) {
	if len(text) > MaxIntervals*12 {
		return Set{}, errors.New("port expression is too long")
	}
	parts := strings.Split(text, ",")
	if len(parts) > MaxIntervals {
		return Set{}, errors.New("too many port intervals")
	}
	intervals := make([]Interval, 0, len(parts))
	for _, part := range parts {
		ends := strings.Split(strings.TrimSpace(part), "-")
		if len(ends) < 1 || len(ends) > 2 {
			return Set{}, errors.New("expected a port or ascending port range")
		}
		first, err := parsePort(ends[0])
		if err != nil {
			return Set{}, err
		}
		last := first
		if len(ends) == 2 {
			last, err = parsePort(ends[1])
			if err != nil {
				return Set{}, err
			}
		}
		intervals = append(intervals, Interval{first, last})
	}
	return NewSet(intervals)
}

func parsePort(text string) (uint16, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("empty port")
	}
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, errors.New("port must contain decimal digits")
		}
	}
	n, err := strconv.ParseUint(text, 10, 16)
	if err != nil || n == 0 {
		return 0, errors.New("port must be in 1..65535")
	}
	return uint16(n), nil
}

func (s Set) Intervals() []Interval { return append([]Interval(nil), s.intervals...) }
func (s Set) IntervalCount() int    { return len(s.intervals) }
func (s Set) Empty() bool           { return len(s.intervals) == 0 }
func (s Set) Contains(port uint16) bool {
	i := sort.Search(len(s.intervals), func(i int) bool { return s.intervals[i].Last >= port })
	return i < len(s.intervals) && s.intervals[i].First <= port
}
func (s Set) Count() uint32 {
	var count uint32
	for _, r := range s.intervals {
		count += uint32(r.Last) - uint32(r.First) + 1
	}
	return count
}
func (s Set) String() string {
	parts := make([]string, 0, len(s.intervals))
	for _, r := range s.intervals {
		if r.First == r.Last {
			parts = append(parts, strconv.Itoa(int(r.First)))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.First, r.Last))
		}
	}
	return strings.Join(parts, ",")
}

// Excluding subtracts intervals, with work bounded by interval counts. It
// returns an error if the normalized result would exceed the interval bound.
func (s Set) Excluding(excluded Set) (Set, error) {
	out := make([]Interval, 0, len(s.intervals))
	for _, r := range s.intervals {
		next, last := uint32(r.First), uint32(r.Last)
		for _, x := range excluded.intervals {
			if uint32(x.Last) < next {
				continue
			}
			if uint32(x.First) > last {
				break
			}
			if uint32(x.First) > next {
				out = append(out, Interval{uint16(next), x.First - 1})
			}
			if len(out) > MaxIntervals {
				return Set{}, errors.New("too many effective port intervals")
			}
			next = uint32(x.Last) + 1
			if next > last {
				break
			}
		}
		if next <= last {
			out = append(out, Interval{uint16(next), uint16(last)})
		}
		if len(out) > MaxIntervals {
			return Set{}, errors.New("too many effective port intervals")
		}
	}
	return Set{intervals: out}, nil
}

func (s Set) Overlaps(other Set) bool {
	i, j := 0, 0
	for i < len(s.intervals) && j < len(other.intervals) {
		a, b := s.intervals[i], other.intervals[j]
		if a.Last < b.First {
			i++
		} else if b.Last < a.First {
			j++
		} else {
			return true
		}
	}
	return false
}

// Expand is only for explicitly materialized UDP or OS-local listeners. The
// caller must also account for already-running listeners in its aggregate cap.
// Rejection happens before allocation or enumeration, even for 1-65535.
func (s Set) Expand(limit int) ([]uint16, error) {
	if limit < 1 || limit > MaxMaterializedListeners {
		return nil, errors.New("materialized listener limit must be in 1..64")
	}
	if s.Count() > uint32(limit) {
		return nil, errors.New("port range exceeds materialized listener limit")
	}
	ports := make([]uint16, 0, int(s.Count()))
	for _, r := range s.intervals {
		for p := uint32(r.First); p <= uint32(r.Last); p++ {
			ports = append(ports, uint16(p))
		}
	}
	return ports, nil
}

func equalSets(a, b Set) bool {
	if len(a.intervals) != len(b.intervals) {
		return false
	}
	for i := range a.intervals {
		if a.intervals[i] != b.intervals[i] {
			return false
		}
	}
	return true
}
