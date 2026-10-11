package core

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// These are isolated final-disclosure tests, not managed activation or native
// retirement evidence. The fake separates ordinary request ownership from the
// counted publication gate; channels select each interleaving without sleeps.
type catalogPublicationTestOrigin struct {
	mu           sync.Mutex
	token        *transportorigin.Token
	stopped      chan struct{}
	retired      chan struct{}
	acquired     chan struct{}
	resume       <-chan struct{}
	sealed       bool
	deny         bool
	ordinary     int
	permits      int
	acquisitions int
	releases     int
}

func newCatalogPublicationTestOrigin() *catalogPublicationTestOrigin {
	return &catalogPublicationTestOrigin{token: transportorigin.NewToken(), stopped: make(chan struct{}), retired: make(chan struct{}), acquired: make(chan struct{})}
}
func (o *catalogPublicationTestOrigin) Identity() *transportorigin.Token { return o.token }
func (o *catalogPublicationTestOrigin) StopRequested() <-chan struct{}   { return o.stopped }
func (o *catalogPublicationTestOrigin) Acquire(ctx context.Context) (transportorigin.Lease, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed || ctx.Err() != nil {
		return nil, transportorigin.ErrStopped
	}
	o.ordinary++
	return &catalogPublicationTestLease{ctx: ctx, release: func() {
		o.mu.Lock()
		o.ordinary--
		o.mu.Unlock()
	}}, nil
}
func (o *catalogPublicationTestOrigin) AcquirePublication(ctx context.Context) (transportorigin.Lease, error) {
	o.mu.Lock()
	o.acquisitions++
	if o.deny || o.sealed || ctx.Err() != nil {
		o.mu.Unlock()
		return nil, transportorigin.ErrStopped
	}
	o.permits++
	o.mu.Unlock()
	close(o.acquired)
	if o.resume != nil {
		<-o.resume
	}
	return &catalogPublicationTestLease{ctx: ctx, release: func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.permits--
		o.releases++
		if o.sealed && o.permits == 0 {
			close(o.retired)
		}
	}}, nil
}
func (o *catalogPublicationTestOrigin) retire() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sealed = true
	close(o.stopped)
	if o.permits == 0 {
		close(o.retired)
	}
}

type catalogPublicationTestLease struct {
	ctx     context.Context
	once    sync.Once
	release func()
}

func (l *catalogPublicationTestLease) Context() context.Context { return l.ctx }
func (l *catalogPublicationTestLease) Release()                 { l.once.Do(l.release) }

type catalogPublicationTestWriter struct {
	*httptest.ResponseRecorder
	t        *testing.T
	core     *Core
	origin   *catalogPublicationTestOrigin
	writes   int
	entered  chan struct{}
	resume   <-chan struct{}
	writeErr error
}

func (w *catalogPublicationTestWriter) checkReleased() {
	w.t.Helper()
	if !w.core.op.TryLock() {
		w.t.Error("network writer holds Core.op")
	} else {
		w.core.op.Unlock()
	}
	w.origin.mu.Lock()
	permits := w.origin.permits
	w.origin.mu.Unlock()
	if permits != 0 {
		w.t.Error("network writer holds an original publication permit")
	}
}
func (w *catalogPublicationTestWriter) WriteHeader(status int) {
	w.checkReleased()
	w.ResponseRecorder.WriteHeader(status)
}
func (w *catalogPublicationTestWriter) Write(p []byte) (int, error) {
	w.checkReleased()
	w.writes++
	if w.entered != nil {
		close(w.entered)
		<-w.resume
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(p)
}

func catalogPublicationFixture(t *testing.T) (*Core, *catalogPublicationTestOrigin, *peerIncomingResponse, *catalogPublicationTestWriter, catalogViewDisclosure) {
	t.Helper()
	now := time.Now()
	record := resourcegrant.CatalogViewRecord{
		ID: strings.Repeat("a", 32), Revision: 1,
		Target:       resource.Target{SchemaVersion: 1, ResourceID: strings.Repeat("b", 32)},
		Relationship: resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: strings.Repeat("c", 64), PeerKey: strings.Repeat("d", 64), PairBinding: strings.Repeat("e", 64)},
		ServiceIDs:   []string{strings.Repeat("f", 32)}, IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), State: resourcegrant.Active,
	}
	fence, err := resourcegrant.NewCatalogViewFenceBefore(record, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d := catalogViewDisclosure{payload: []byte(`{"services":[{"synthetic":"bounded"}]}`), fence: fence, selector: record.Selector(), relationship: record.Relationship, serviceIDs: record.ServiceIDs}
	c := &Core{}
	o := newCatalogPublicationTestOrigin()
	w := &catalogPublicationTestWriter{ResponseRecorder: httptest.NewRecorder(), t: t, core: c, origin: o}
	response := newPeerIncomingResponse(&peerHTTPAuthority{origin: o.stopped}, w)
	return c, o, response, w, d
}

func catalogPublicationWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("disclosure synchronization did not complete")
	}
}

func TestCatalogViewPublicationRejectsAfterOrdinaryAdmission(t *testing.T) {
	c, o, response, w, d := catalogPublicationFixture(t)
	lease, err := o.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	// The sampled current/stop predicate and ordinary lease still succeed,
	// but the distinct final publication permit rejects this old origin.
	o.deny = true
	if channelClosed(o.StopRequested()) {
		t.Fatal("fixture stopped before current sample")
	}
	c.op.Lock()
	c.finishCatalogViewDisclosure(response, context.Background(), o, d, true)
	if w.Code != http.StatusForbidden || bytes.Contains(w.Body.Bytes(), d.payload) || o.acquisitions != 1 || o.permits != 0 || o.releases != 0 || o.ordinary != 1 {
		t.Fatal("ordinary ownership bypassed rejected publication")
	}
	if response.borrows != 0 {
		t.Fatal("rejected disclosure leaked response borrow")
	}
}

func TestCatalogViewPublicationPermitWinsBeforeRetirement(t *testing.T) {
	c, o, response, w, d := catalogPublicationFixture(t)
	resumePermit, resumeWrite := make(chan struct{}), make(chan struct{})
	o.resume = resumePermit
	w.entered, w.resume = make(chan struct{}), resumeWrite
	done := make(chan struct{})
	go func() {
		c.op.Lock()
		c.finishCatalogViewDisclosure(response, context.Background(), o, d, true)
		close(done)
	}()
	catalogPublicationWait(t, o.acquired)
	o.retire()
	select {
	case <-o.retired:
		t.Fatal("retirement completed with a publication permit held")
	default:
	}
	if c.op.TryLock() {
		c.op.Unlock()
		t.Fatal("publication admission released Core.op early")
	}
	close(resumePermit)
	catalogPublicationWait(t, w.entered)
	// The network writer is deliberately blocked. Retirement waits for the
	// bounded admission permit, not the later network Write.
	catalogPublicationWait(t, o.retired)
	close(resumeWrite)
	catalogPublicationWait(t, done)
	if w.Code != http.StatusOK || w.writes != 1 || !bytes.Equal(w.Body.Bytes(), d.payload) || o.acquisitions != 1 || o.releases != 1 || response.borrows != 0 {
		t.Fatal("pre-retirement permit did not finish exactly one bounded disclosure")
	}
	_, err := o.AcquirePublication(context.Background())
	if !errors.Is(err, transportorigin.ErrStopped) {
		t.Fatal("retired origin admitted later publication")
	}
}

func TestCatalogViewPublicationReleasesRejectedPermit(t *testing.T) {
	for _, reason := range []string{"cancellation", "fence", "selector", "relationship", "service"} {
		t.Run(reason, func(t *testing.T) {
			c, o, response, w, d := catalogPublicationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resume := make(chan struct{})
			o.resume = resume
			switch reason {
			case "selector":
				d.selector.GrantRevision++
			case "relationship":
				d.relationship.PairBinding = strings.Repeat("0", 64)
			case "service":
				d.serviceIDs = []string{strings.Repeat("0", 32)}
			}
			done := make(chan struct{})
			go func() {
				c.op.Lock()
				c.finishCatalogViewDisclosure(response, ctx, o, d, true)
				close(done)
			}()
			catalogPublicationWait(t, o.acquired)
			if reason == "cancellation" {
				cancel()
			}
			if reason == "fence" {
				d.fence.Close()
			}
			close(resume)
			catalogPublicationWait(t, done)
			if w.Code != http.StatusForbidden || bytes.Contains(w.Body.Bytes(), d.payload) || o.acquisitions != 1 || o.releases != 1 || o.permits != 0 || response.borrows != 0 {
				t.Fatal("failed admission retained a permit or disclosed the payload")
			}
		})
	}
}

func TestCatalogViewPublicationRejectsBeforePermit(t *testing.T) {
	for _, reason := range []string{"current", "cancellation", "borrow", "origin"} {
		t.Run(reason, func(t *testing.T) {
			c, o, response, w, d := catalogPublicationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			current := true
			var original transportorigin.Origin = o
			switch reason {
			case "current":
				current = false
			case "cancellation":
				cancel()
			case "borrow":
				response.closing = true
			case "origin":
				original = nil
			}
			c.op.Lock()
			c.finishCatalogViewDisclosure(response, ctx, original, d, current)
			if bytes.Contains(w.Body.Bytes(), d.payload) || o.acquisitions != 0 || response.borrows != 0 {
				t.Fatal("failed prerequisite reached publication")
			}
			if !c.op.TryLock() {
				t.Fatal("failed prerequisite retained Core.op")
			}
			c.op.Unlock()
		})
	}
}

func TestCatalogViewPublicationWriteFailureReleasesOwnership(t *testing.T) {
	c, o, response, w, d := catalogPublicationFixture(t)
	w.writeErr = errors.New("synthetic network write failure")
	c.op.Lock()
	c.finishCatalogViewDisclosure(response, context.Background(), o, d, true)
	if w.writes != 1 || o.releases != 1 || o.permits != 0 || response.borrows != 0 {
		t.Fatal("failed network write retained ownership or retried")
	}
}

func TestCatalogViewPublicationRetainsFinalCurrentChecks(t *testing.T) {
	// The behavioral tests isolate the actual final publication/writer path.
	// This wiring assertion separately keeps original transport, current share
	// selection and cancellation checks at its sole production call site.
	data, err := os.ReadFile("resource_catalog_view_exchange.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "func (c *Core) serveCatalogView(")
	if start < 0 {
		t.Fatal("missing catalog handler")
	}
	end := strings.Index(source[start:], "func (c *Core) finishCatalogViewDisclosure(")
	if end < 0 {
		t.Fatal("missing catalog publication path")
	}
	body := source[start : start+end]
	current := strings.Index(body, "current := r.Context().Err() == nil && c.catalogViewTransportCurrent(r.Context(), peer, original) && c.catalogViewSelectionCurrent(c.catalogViews.state.ActiveRecord())")
	finish := strings.Index(body, "c.finishCatalogViewDisclosure(response, r.Context(), original, disclosure, current)")
	if current < 0 || finish <= current || strings.Contains(body[current:finish], "c.op.Unlock()") || strings.Contains(body[current:finish], "Acquire(") {
		t.Fatal("final publication lost original current/selection checks under Core.op")
	}
}

func TestCatalogViewReceiptPublicationRejectsAfterOrdinaryAdmission(t *testing.T) {
	c, o, _, _, _ := catalogPublicationFixture(t)
	lease, err := o.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	o.deny = true
	receipt := &catalogViewReceipt{original: o}
	c.op.Lock()
	err = c.publishCatalogViewReceipt(context.Background(), receipt)
	c.op.Unlock()
	if err == nil || c.catalogViewReceived != nil || o.acquisitions != 1 || o.releases != 0 || o.ordinary != 1 {
		t.Fatal("ordinary request ownership published a rejected receipt")
	}
}

func TestCatalogViewReceiptPublicationPermitAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "permit-before-retirement", true: "cancel-after-permit"}[cancelled], func(t *testing.T) {
			c, o, _, _, _ := catalogPublicationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resume := make(chan struct{})
			o.resume = resume
			receipt := &catalogViewReceipt{original: o}
			done := make(chan struct{})
			var err error
			go func() {
				c.op.Lock()
				err = c.publishCatalogViewReceipt(ctx, receipt)
				c.op.Unlock()
				close(done)
			}()
			catalogPublicationWait(t, o.acquired)
			if c.op.TryLock() {
				c.op.Unlock()
				t.Fatal("receipt publication did not retain Core.op")
			}
			if cancelled {
				cancel()
			} else {
				o.retire()
			}
			select {
			case <-o.retired:
				t.Fatal("receipt retirement passed its held permit")
			default:
			}
			close(resume)
			catalogPublicationWait(t, done)
			if o.acquisitions != 1 || o.releases != 1 || o.permits != 0 {
				t.Fatal("receipt publication leaked its permit")
			}
			if cancelled {
				if err == nil || c.catalogViewReceived != nil {
					t.Fatal("cancelled receipt was published")
				}
			} else {
				catalogPublicationWait(t, o.retired)
				if err != nil || c.catalogViewReceived != receipt || c.catalogViewReceived.original != o {
					t.Fatal("admitted receipt lost its original attribution")
				}
			}
		})
	}
}
