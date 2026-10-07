// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Modified for Sobalink: explicit creator tickets, endpoint publication,
// cancellation checks, single cleanup ownership and terminal observation.
// See UPSTREAM.md and GVISOR_LICENSE.

package directlan

import (
	"errors"
	"net"
	"net/netip"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// dialOwned mirrors the pinned gonet endpoint creation sequence while keeping
// the endpoint visible to its creator before any bind/connect/wait. The stock
// hidden constructor must not be used by a live generation.
func (t *userspaceTunnel) dialOwned(c *endpointCreator, network string, local, remote netip.AddrPort) (net.Conn, tcpip.Endpoint, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, nil, err
	}
	var wq waiter.Queue
	protocol := tcp.ProtocolNumber
	if network == "udp" {
		protocol = udp.ProtocolNumber
	}
	ep, terr := t.stack.NewEndpoint(protocol, ipv6.ProtocolNumber, &wq)
	if terr != nil {
		return nil, nil, errors.New(terr.String())
	}
	c.PublishEndpoint(ep)
	// The caller still owns cleanup on every error, including cancellation
	// immediately after allocation. No endpoint is hidden behind gonet.Dial.
	if err := c.ctx.Err(); err != nil {
		return nil, ep, err
	}
	if network == "udp" {
		if terr = ep.Bind(fullAddress(local)); terr != nil {
			return nil, ep, errors.New(terr.String())
		}
		if err := c.ctx.Err(); err != nil {
			return nil, ep, err
		}
		if terr = ep.Connect(fullAddress(remote)); terr != nil {
			return nil, ep, errors.New(terr.String())
		}
		return gonet.NewUDPConn(&wq, ep), ep, c.ctx.Err()
	}
	entry, notify := waiter.NewChannelEntry(waiter.WritableEvents)
	wq.EventRegister(&entry)
	defer wq.EventUnregister(&entry)
	terr = ep.Connect(fullAddress(remote))
	if _, ok := terr.(*tcpip.ErrConnectStarted); ok {
		select {
		case <-c.ctx.Done():
			return nil, ep, c.ctx.Err()
		case <-notify:
		}
		if err := c.ctx.Err(); err != nil {
			return nil, ep, err
		}
		terr = ep.LastError()
	}
	if terr != nil {
		return nil, ep, errors.New(terr.String())
	}
	return gonet.NewTCPConn(&wq, ep), ep, c.ctx.Err()
}

// cleanupCreated runs only in the creator before handoff. No supervisor races
// an Abort against it, and its reservation stays charged through terminal wait.
func cleanupCreated(ep tcpip.Endpoint) {
	if ep == nil {
		return
	}
	if transport, ok := ep.(*tcp.Endpoint); ok {
		transport.Abort()
		transport.Close()
		transport.Wait()
		return
	}
	ep.Close()
}
