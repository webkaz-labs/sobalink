package nearby

import (
	"encoding/binary"
	"net/netip"
)

// Winsock ancillary layouts are encoded explicitly rather than casting
// untrusted bytes with unsafe. The documented WSACMSGHDR contains SIZE_T,
// INT level, INT type, then pointer-aligned data; IN_PKTINFO is IPv4 + ULONG.
// See Microsoft's IN_PKTINFO and WSACMSGHDR documentation and ws2ipdef.h.
const (
	winIPPacketInfo    = 19
	winIPRecvTTL       = 21
	winIPInterfaceList = 28
	winIPAddInterface  = 29
	winIPTTL           = 4
)

func winAligned(n, word int) int { return (n + word - 1) &^ (word - 1) }
func winControl(typ uint32, data []byte, word int) []byte {
	header := word + 8
	out := make([]byte, winAligned(header+len(data), word))
	if word == 8 {
		binary.LittleEndian.PutUint64(out, uint64(header+len(data)))
	} else {
		binary.LittleEndian.PutUint32(out, uint32(header+len(data)))
	}
	binary.LittleEndian.PutUint32(out[word+4:], typ) // IPPROTO_IP is zero
	copy(out[header:], data)
	return out
}
func winSendControl(s Scope, word int) []byte {
	data := make([]byte, 8)
	copy(data, s.LocalAddress.AsSlice())
	binary.LittleEndian.PutUint32(data[4:], uint32(s.InterfaceIndex))
	return winControl(winIPPacketInfo, data, word)
}
func winReceiveControl(oob []byte, word int) (dst netip.Addr, index, ttl int, ok bool) {
	if (word != 4 && word != 8) || len(oob) > 128 {
		return
	}
	header := word + 8
	seenInfo, seenTTL := false, false
	count := 0
	for len(oob) > 0 {
		count++
		if count > 4 || len(oob) < header {
			return netip.Addr{}, 0, 0, false
		}
		var length uint64
		if word == 8 {
			length = binary.LittleEndian.Uint64(oob)
		} else {
			length = uint64(binary.LittleEndian.Uint32(oob))
		}
		if length < uint64(header) || length > uint64(len(oob)) {
			return netip.Addr{}, 0, 0, false
		}
		level := binary.LittleEndian.Uint32(oob[word:])
		typ := binary.LittleEndian.Uint32(oob[word+4:])
		data := oob[header:int(length)]
		if level != 0 {
			return netip.Addr{}, 0, 0, false
		}
		switch typ {
		case winIPPacketInfo:
			if seenInfo || len(data) != 8 {
				return netip.Addr{}, 0, 0, false
			}
			seenInfo = true
			dst = netip.AddrFrom4([4]byte(data[:4]))
			value := binary.LittleEndian.Uint32(data[4:])
			if value == 0 || uint64(value) > uint64(^uint(0)>>1) {
				return netip.Addr{}, 0, 0, false
			}
			index = int(value)
		case winIPTTL, winIPRecvTTL:
			if seenTTL || len(data) != 4 {
				return netip.Addr{}, 0, 0, false
			}
			seenTTL = true
			value := binary.LittleEndian.Uint32(data)
			if value > 255 {
				return netip.Addr{}, 0, 0, false
			}
			ttl = int(value)
		default:
			return netip.Addr{}, 0, 0, false
		}
		next := winAligned(int(length), word)
		if next > len(oob) {
			if int(length) != len(oob) {
				return netip.Addr{}, 0, 0, false
			}
			oob = nil
		} else {
			oob = oob[next:]
		}
	}
	return dst, index, ttl, seenInfo && seenTTL
}
