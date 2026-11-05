//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
)

type privateCloseError struct{}

func (*privateCloseError) Error() string { panic("private close error formatted") }

func TestProductCoreCloseSentinelClasses(t *testing.T) {
	private := &privateCloseError{}
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {private, "other"}, {context.Canceled, "context-canceled"},
		{context.DeadlineExceeded, "context-deadline"}, {net.ErrClosed, "net-closed"},
		{http.ErrServerClosed, "http-closed"}, {os.ErrClosed, "file-closed"},
		{io.ErrClosedPipe, "closed-pipe"}, {io.EOF, "eof"},
		{errors.Join(private, context.DeadlineExceeded), "context-deadline"},
		{errors.Join(errors.Join(net.ErrClosed), private), "net-closed"},
		{errors.Join(context.Canceled, context.DeadlineExceeded), "multiple"},
	} {
		if got := ProductCloseErrorClass(test.err); got != test.want {
			t.Fatalf("classification = %s, want %s", got, test.want)
		}
	}
}

func TestProductCoreCloseFirstFailureAndPrivacy(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	for _, component := range []string{"prepared-transfer", "resource-groups", "context-control", "peer-server", "web", "transfers", "node", "message-store", "outbox-root", "synthetic-private-detail"} {
		c := &Core{}
		if c.observeProductClose(component, nil) != nil {
			t.Fatal("nil error changed")
		}
		if got, class := c.ProductCloseFailure(); got != "none" || class != "none" {
			t.Fatal("nil result invented failure")
		}
		first, later := &privateCloseError{}, errors.Join(&privateCloseError{}, context.DeadlineExceeded)
		if c.observeProductClose(component, first) != first || c.observeProductClose("web", later) != later {
			t.Fatal("underlying error changed")
		}
		want := component
		if component == "synthetic-private-detail" {
			want = "other"
		}
		if got, class := c.ProductCloseFailure(); got != want || class != "other" {
			t.Fatal("first failure was replaced or private component escaped")
		}
	}
}

func TestProductCoreCloseRequiresOptIn(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "")
	c := &Core{}
	private := &privateCloseError{}
	if c.observeProductClose("web", private) != private {
		t.Fatal("unobserved error changed")
	}
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	if component, class := c.ProductCloseFailure(); component != "none" || class != "none" {
		t.Fatal("unapproved observation retained")
	}
	c.observeProductClose("web", context.DeadlineExceeded)
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "")
	if component, class := c.ProductCloseFailure(); component != "none" || class != "none" {
		t.Fatal("observation escaped without opt-in")
	}
}

type productCloseFailureBackend struct {
	NetworkBackend
	err   error
	calls int
}

func (b *productCloseFailureBackend) Close() error { b.calls++; return b.err }

func TestProductCoreCloseActualCleanupPreservesErrorAndOnce(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	c, err := openOwnedTestCore(t, context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if err != nil {
		t.Fatal("synthetic Core preparation failed")
	}
	failure := &privateCloseError{}
	backend := &productCloseFailureBackend{err: errors.Join(failure, context.DeadlineExceeded)}
	c.mu.Lock()
	c.node = backend
	c.mu.Unlock()
	first := c.Close()
	if !errors.Is(first, failure) || !errors.Is(first, context.DeadlineExceeded) || first != c.Close() || backend.calls != 1 {
		t.Fatal("Core cleanup error or once behavior changed")
	}
	if component, class := c.ProductCloseFailure(); component != "node" || class != "context-deadline" {
		t.Fatal("actual Core close site not observed")
	}
}
