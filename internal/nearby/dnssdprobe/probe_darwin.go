//go:build darwin && cgo

// Package dnssdprobe is imported only by the hosted native diagnostic test.
// It creates no sockets, retains no pointers and does not enter product builds.
package dnssdprobe

/*
#cgo CFLAGS: -std=c11
#define _DARWIN_C_SOURCE 1
#include <sys/socket.h>
#include <netinet/in.h>
#include <fcntl.h>
#include <errno.h>
#include <stdint.h>
#include <string.h>

// Values: 0 mismatch, 1 matched, 2 unavailable. No local identifiers escape.
struct soba_socket_snapshot { int bound, bound_if, multicast_if, multicast_addr, ttl, loop, nonblocking; };
static struct soba_socket_snapshot soba_snapshot(int fd, const unsigned char *address, uint16_t port, unsigned int index) {
    struct soba_socket_snapshot s = {2,2,2,2,2,2,2};
    struct sockaddr_in local; memset(&local, 0, sizeof(local));
    socklen_t size = sizeof(local);
    if (getsockname(fd, (struct sockaddr *)&local, &size) == 0)
        s.bound = size == sizeof(local) && local.sin_family == AF_INET &&
            memcmp(&local.sin_addr, address, 4) == 0 && ntohs(local.sin_port) == port;
    unsigned int value = 0; size = sizeof(value);
    if (getsockopt(fd, IPPROTO_IP, IP_BOUND_IF, &value, &size) == 0 && size == sizeof(value)) s.bound_if = value == index;
    value = 0; size = sizeof(value);
    if (getsockopt(fd, IPPROTO_IP, IP_MULTICAST_IFINDEX, &value, &size) == 0 && size == sizeof(value)) s.multicast_if = value == index;
    struct in_addr multicast_address; memset(&multicast_address, 0, sizeof(multicast_address)); size = sizeof(multicast_address);
    if (getsockopt(fd, IPPROTO_IP, IP_MULTICAST_IF, &multicast_address, &size) == 0 && size == sizeof(multicast_address)) s.multicast_addr = memcmp(&multicast_address, address, 4) == 0;
    unsigned char ttl = 0; size = sizeof(ttl);
    if (getsockopt(fd, IPPROTO_IP, IP_MULTICAST_TTL, &ttl, &size) == 0 && size == sizeof(ttl)) s.ttl = ttl == 255;
    unsigned int loop = 0; size = sizeof(loop);
    if (getsockopt(fd, IPPROTO_IP, IP_MULTICAST_LOOP, &loop, &size) == 0 && size == sizeof(loop)) s.loop = loop == 1;
    int flags = fcntl(fd, F_GETFL);
    if (flags != -1) s.nonblocking = (flags & O_NONBLOCK) != 0;
    return s;
}
static int soba_snapshot_ready(struct soba_socket_snapshot s) {
    return s.bound == 1 && s.bound_if == 1 && s.multicast_if == 1 && s.multicast_addr != 2 && s.ttl == 1 && s.loop == 1 && s.nonblocking == 1;
}
struct soba_send_result { long count; int error; int attempted; struct soba_socket_snapshot snapshot; };
static struct soba_send_result soba_send_once(int fd, const unsigned char *data, size_t length, const unsigned char *address, uint16_t port, unsigned int index) {
    struct soba_send_result result = {0};
    result.snapshot = soba_snapshot(fd, address, port, index);
    if (!soba_snapshot_ready(result.snapshot) || length < 12 || length > 512 || port < 49152 || index == 0) {
        result.error = EINVAL;
        return result;
    }
    struct sockaddr_in destination; memset(&destination, 0, sizeof(destination));
    destination.sin_len = sizeof(destination);
    destination.sin_family = AF_INET;
    destination.sin_port = htons(5353);
    destination.sin_addr.s_addr = htonl(0xe00000fbU);
    result.attempted = 1;
    result.count = sendto(fd, data, length, 0, (struct sockaddr *)&destination, sizeof(destination));
    if (result.count < 0) result.error = errno;
    return result;
}
*/
import "C"

import (
	"syscall"
	"unsafe"
)

type Snapshot [7]int

func snapshot(s C.struct_soba_socket_snapshot) Snapshot {
	return Snapshot{int(s.bound), int(s.bound_if), int(s.multicast_if), int(s.multicast_addr), int(s.ttl), int(s.loop), int(s.nonblocking)}
}
func Readback(fd uintptr, address [4]byte, port uint16, index int) Snapshot {
	return snapshot(C.soba_snapshot(C.int(fd), (*C.uchar)(unsafe.Pointer(&address[0])), C.uint16_t(port), C.uint(index)))
}

// Send invokes at most one nonblocking C sendto. EINTR/EAGAIN are terminal.
// RawConn.Control must own fd for the entire call; data is never retained.
func Send(fd uintptr, data []byte, address [4]byte, port uint16, index int) (int, bool, Snapshot, error) {
	if len(data) < 12 || len(data) > 512 {
		return 0, false, Snapshot{}, syscall.EINVAL
	}
	r := C.soba_send_once(C.int(fd), (*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data)), (*C.uchar)(unsafe.Pointer(&address[0])), C.uint16_t(port), C.uint(index))
	var err error
	if r.error != 0 {
		err = syscall.Errno(r.error)
	}
	return int(r.count), r.attempted != 0, snapshot(r.snapshot), err
}
