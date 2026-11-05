//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
)

// This test-only snapshot has no error, owner, path, or identifier. The first
// failed call is retained; it does not replace or suppress later close errors.
type productCloseObservation struct {
	mu        sync.Mutex
	component string
	class     string
}

func (c *Core) observeProductClose(component string, err error) error {
	if err == nil || os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") != "1" {
		return err
	}
	c.closeObservation.mu.Lock()
	defer c.closeObservation.mu.Unlock()
	if c.closeObservation.component == "" {
		switch component {
		case "prepared-transfer", "resource-groups", "context-control", "peer-server", "web", "transfers", "node", "message-store", "outbox-root":
		default:
			component = "other"
		}
		c.closeObservation.component = component
		c.closeObservation.class = ProductCloseErrorClass(err)
	}
	return err
}

// ProductCloseFailure reports only the first observed failed Core cleanup call.
// It is not a shutdown predicate and is absent from ordinary builds.
func (c *Core) ProductCloseFailure() (component, class string) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") != "1" {
		return "none", "none"
	}
	c.closeObservation.mu.Lock()
	defer c.closeObservation.mu.Unlock()
	if c.closeObservation.component == "" {
		return "none", "none"
	}
	return c.closeObservation.component, c.closeObservation.class
}

// ProductCloseErrorClass reports matching sentinel families, not error text or
// a root cause. Unknown joined errors can coexist with a matching sentinel.
func ProductCloseErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	class := "other"
	for _, candidate := range []struct {
		err   error
		class string
	}{
		{context.Canceled, "context-canceled"}, {context.DeadlineExceeded, "context-deadline"},
		{net.ErrClosed, "net-closed"}, {http.ErrServerClosed, "http-closed"},
		{os.ErrClosed, "file-closed"}, {io.ErrClosedPipe, "closed-pipe"}, {io.EOF, "eof"},
	} {
		if errors.Is(err, candidate.err) {
			if class != "other" {
				return "multiple"
			}
			class = candidate.class
		}
	}
	return class
}
