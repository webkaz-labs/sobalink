package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// LogoutResult reports acknowledgement by the embedded node's local API. It
// does not claim that an administrator removed the node from the tailnet.
type LogoutResult struct {
	State               string `json:"state"`
	LogoutConfirmed     bool   `json:"logoutConfirmed"`
	LocalTrafficStopped bool   `json:"localTrafficStopped"`
	ApplicationState    string `json:"applicationState"`
}

// logoutTailnet runs with c.op held. Stop application traffic before contacting
// the backend, but keep the management context alive until the result is ready:
// cancelling it first would also abort the IPC/Web response during logout.
func (c *Core) logoutTailnet(ctx context.Context) (LogoutResult, error) {
	if err := ctx.Err(); err != nil {
		return LogoutResult{}, err
	}
	node := c.nodeCopy()
	if c.profileCopy().Settings.Network != "tailnet" || node == nil {
		return LogoutResult{}, &localCommandError{"tailnet_logout_unavailable", "activate the saved Tailnet network before logging out; LAN pairing is managed separately"}
	}
	logoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.networkReady.Store(false)
	c.mu.Lock()
	c.closing = true
	c.networkState = "logging-out"
	ps := c.peerServer
	c.peerServer = nil
	batches := make([]*outgoingBatch, 0, len(c.outgoing))
	for _, batch := range c.outgoing {
		batches = append(batches, batch)
	}
	services := make([]*activeService, 0, len(c.active))
	for id, service := range c.active {
		service.ready.Store(false)
		service.cancel()
		services = append(services, service)
		c.serviceStates[id] = "stopped"
		delete(c.active, id)
	}
	proxies := make([]proxyServer, 0, len(c.proxies))
	for id, proxy := range c.proxies {
		proxy.cancel()
		if proxy.server != nil {
			proxies = append(proxies, proxy.server)
		}
		delete(c.proxies, id)
	}
	clear(c.confirmed)
	clear(c.discovered)
	c.mu.Unlock()
	defer c.cancel()
	// Cancel every traffic source before waiting for any one of them. Cleanup
	// callbacks may block; no callback may hold the logout response indefinitely.
	tasks := []func() error{c.transfers.Close}
	for _, proxy := range proxies {
		tasks = append(tasks, proxy.Close)
	}
	for _, service := range services {
		for _, server := range service.servers {
			tasks = append(tasks, server.Close)
		}
	}
	if ranges := c.rangeState; ranges != nil {
		c.mu.Lock()
		c.rangeState = nil
		c.mu.Unlock()
		_ = ranges.engine.Close()
		tasks = append(tasks, func() error {
			ranges.unregister()
			return ranges.engine.Wait(logoutCtx)
		})
	}
	if ps != nil {
		tasks = append(tasks, ps.Close)
	}
	for _, batch := range batches {
		tasks = append(tasks, func() error {
			batch.stop()
			batch.mu.Lock()
			running, done := batch.running, batch.runDone
			batch.mu.Unlock()
			if !running {
				return nil
			}
			select {
			case <-done:
				return nil
			case <-logoutCtx.Done():
				return logoutCtx.Err()
			}
		})
	}
	results := make(chan error, len(tasks))
	for _, task := range tasks {
		go func() { results <- task() }()
	}
	var cleanupErr error
	for range tasks {
		select {
		case err := <-results:
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return c.logoutDrainFailed()
			}
			if !errors.Is(err, net.ErrClosed) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		case <-logoutCtx.Done():
			return c.logoutDrainFailed()
		}
	}
	err := node.Logout(logoutCtx)
	result := LogoutResult{State: "logged-out", LogoutConfirmed: err == nil, LocalTrafficStopped: cleanupErr == nil, ApplicationState: "stopping"}
	var resultErr error
	if err != nil {
		result.State = "logout-unconfirmed"
		resultErr = &localCommandError{"tailnet_logout_unconfirmed", "application traffic has been stopped and the application is stopping; backend logout is unconfirmed. Start soba with the saved Tailnet network and retry logout. Node removal is a separate administrator action"}
		if cleanupErr != nil {
			resultErr = &localCommandError{"tailnet_logout_unconfirmed", "local traffic shutdown was requested and the application is stopping; listener cleanup and backend logout are unconfirmed. Wait for exit, then start with the saved Tailnet network and retry logout"}
		}
	} else if cleanupErr != nil {
		resultErr = &localCommandError{"logout_cleanup_unconfirmed", "backend logout is confirmed and the application is stopping; local listener cleanup reported an error. Check that soba exits before restarting"}
	}
	c.recordLogoutResult(result, resultErr)
	return result, resultErr
}

func (c *Core) logoutDrainFailed() (LogoutResult, error) {
	result := LogoutResult{State: "logout-unconfirmed", ApplicationState: "stopping"}
	err := &localCommandError{"tailnet_logout_unconfirmed", "local traffic shutdown was requested but service or transfer cleanup is unconfirmed; backend logout was not attempted. The application is stopping. Wait for exit, then start with the saved Tailnet network and retry logout"}
	c.recordLogoutResult(result, err)
	return result, err
}

func (c *Core) recordLogoutResult(result LogoutResult, resultErr error) {
	c.mu.Lock()
	c.networkState = result.State
	c.networkError, c.networkErrorCode = "", ""
	if resultErr != nil {
		c.networkError, c.networkErrorCode = resultErr.Error(), networkErrorCode(resultErr)
	}
	c.mu.Unlock()
}
