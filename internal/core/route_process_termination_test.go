//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

const routeChildMarker = "SOBA_ROUTE_CHILD_TERMINATION "

type routeChildCloseErrors struct {
	Closed     bool `json:"closed"`
	Canceled   bool `json:"canceled"`
	Deadline   bool `json:"deadline"`
	Permission bool `json:"permission"`
	Other      bool `json:"other"`
}
type routeChildFixtureEvidence struct {
	Schema        int                   `json:"schema"`
	ExitRequested bool                  `json:"exitRequested"`
	ReplyWritten  bool                  `json:"replyWritten"`
	TestFailed    bool                  `json:"testFailed"`
	CoreClose     string                `json:"coreClose"`
	CoreErrors    routeChildCloseErrors `json:"coreErrors"`
	LockClose     string                `json:"lockClose"`
	LockErrors    routeChildCloseErrors `json:"lockErrors"`
}
type routeChildRuntimeFlags struct {
	Race            bool `json:"race"`
	Panic           bool `json:"panic"`
	Fatal           bool `json:"fatal"`
	TestTimeout     bool `json:"testTimeout"`
	OutputTruncated bool `json:"outputTruncated"`
	MarkerInvalid   bool `json:"markerInvalid"`
	MarkerRepeated  bool `json:"markerRepeated"`
}
type routeChildRuntimeAreas struct {
	Fixture       bool `json:"fixture"`
	CoreLifecycle bool `json:"coreLifecycle"`
	PeerAPI       bool `json:"peerAPI"`
	Setup         bool `json:"setup"`
	LANBackend    bool `json:"lanBackend"`
	Web           bool `json:"web"`
	Config        bool `json:"config"`
	Transfer      bool `json:"transfer"`
	Engine        bool `json:"engine"`
	HTTPServer    bool `json:"httpServer"`
}
type routeChildTerminationEvidence struct {
	Wait    string                     `json:"wait"`
	Runtime routeChildRuntimeFlags     `json:"runtime"`
	Areas   routeChildRuntimeAreas     `json:"areas"`
	Fixture *routeChildFixtureEvidence `json:"fixture,omitempty"`
}

func routeCloseErrors(err error) (v routeChildCloseErrors) {
	var visit func(error, int)
	visit = func(e error, depth int) {
		if e == nil {
			return
		}
		if depth > 16 {
			v.Other = true
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child, depth+1)
			}
			return
		}
		if wrapped, ok := e.(interface{ Unwrap() error }); ok {
			visit(wrapped.Unwrap(), depth+1)
			return
		}
		switch {
		case errors.Is(e, net.ErrClosed) || errors.Is(e, http.ErrServerClosed):
			v.Closed = true
		case errors.Is(e, context.Canceled):
			v.Canceled = true
		case errors.Is(e, context.DeadlineExceeded):
			v.Deadline = true
		case errors.Is(e, os.ErrPermission):
			v.Permission = true
		default:
			v.Other = true
		}
	}
	visit(err, 0)
	return
}
func validRouteChildFixture(v routeChildFixtureEvidence) bool {
	if v.Schema != 1 {
		return false
	}
	for _, state := range []string{v.CoreClose, v.LockClose} {
		switch state {
		case "not_run", "running", "passed", "failed":
		default:
			return false
		}
	}
	return true
}
func validRouteChildTermination(v routeChildTerminationEvidence) bool {
	switch v.Wait {
	case "not_observed", "success", "exit_one", "exit_two", "race_exit", "other_exit", "signaled", "context_canceled", "context_deadline", "io_wait_timeout", "shutdown_timeout", "wait_error":
	default:
		return false
	}
	return v.Fixture == nil || !v.Runtime.MarkerInvalid && !v.Runtime.MarkerRepeated && validRouteChildFixture(*v.Fixture)
}
func routeChildWaitKind(err error) string {
	if err == nil {
		return "success"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case -1:
			return "signaled"
		case 1:
			return "exit_one"
		case 2:
			return "exit_two"
		case 66:
			return "race_exit"
		default:
			return "other_exit"
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	case errors.Is(err, exec.ErrWaitDelay):
		return "io_wait_timeout"
	default:
		return "wait_error"
	}
}

// Parse only fixed markers and runtime/source categories. Every original line,
// stack path, argument, panic message and arbitrary error remains private.
// The line buffer stays bounded even if the retained private prefix is full.
func (d *routeProcessDiagnostics) observeTermination(p []byte) {
	for _, b := range p {
		if b != '\n' {
			if len(d.line) < 2048 && !d.dropLine {
				d.line = append(d.line, b)
			} else {
				if !d.dropLine && strings.HasPrefix(strings.TrimSpace(string(d.line)), routeChildMarker) {
					if d.markerSeen {
						d.fixed.Runtime.MarkerRepeated = true
					}
					d.markerSeen = true
					d.fixed.Runtime.MarkerInvalid = true
					d.fixed.Fixture = nil
				}
				d.dropLine = true
				d.fixed.Runtime.OutputTruncated = true
			}
			continue
		}
		if !d.dropLine {
			d.observeTerminationLine(string(d.line))
		}
		d.line = d.line[:0]
		d.dropLine = false
	}
}
func (d *routeProcessDiagnostics) observeTerminationLine(line string) {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, routeChildMarker):
		if d.markerSeen {
			d.fixed.Runtime.MarkerRepeated = true
			d.fixed.Fixture = nil
			return
		}
		d.markerSeen = true
		raw := []byte(strings.TrimPrefix(line, routeChildMarker))
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var marker routeChildFixtureEvidence
		if decoder.Decode(&marker) != nil || decoder.Decode(new(any)) != io.EOF || !validRouteChildFixture(marker) {
			d.fixed.Runtime.MarkerInvalid = true
			return
		}
		canonical, err := json.Marshal(marker)
		// The one producer writes this canonical fixed DTO. Byte equality also
		// rejects missing/duplicate fields instead of defaulting absent booleans.
		if err != nil || !bytes.Equal(raw, canonical) {
			d.fixed.Runtime.MarkerInvalid = true
			return
		}
		d.fixed.Fixture = &marker
	case line == "WARNING: DATA RACE":
		d.fixed.Runtime.Race = true
	case strings.HasPrefix(line, "panic:"):
		d.fixed.Runtime.Panic = true
		d.fixed.Runtime.TestTimeout = d.fixed.Runtime.TestTimeout || strings.HasPrefix(line, "panic: test timed out after ")
	case strings.HasPrefix(line, "fatal error:"):
		d.fixed.Runtime.Fatal = true
	}
	// Only known stack-file suffixes can set these finite area flags.
	areas := []struct {
		part string
		flag *bool
	}{
		{"/internal/core/route_process_integration_test.go:", &d.fixed.Areas.Fixture},
		{"/internal/core/model.go:", &d.fixed.Areas.CoreLifecycle},
		{"/internal/core/peers.go:", &d.fixed.Areas.PeerAPI},
		{"/internal/core/setup_", &d.fixed.Areas.Setup},
		{"/internal/lanlink/", &d.fixed.Areas.LANBackend},
		{"/internal/webui/", &d.fixed.Areas.Web},
		{"/internal/config/", &d.fixed.Areas.Config},
		{"/internal/transfer/", &d.fixed.Areas.Transfer},
		{"/internal/routecat/", &d.fixed.Areas.Engine},
		{"/net/http/server.go:", &d.fixed.Areas.HTTPServer},
	}
	if !strings.Contains(line, ".go:") {
		return
	}
	for _, area := range areas {
		if strings.Contains(line, area.part) {
			*area.flag = true
		}
	}
}
func (d *routeProcessDiagnostics) termination(wait string) routeChildTerminationEvidence {
	if d == nil {
		return routeChildTerminationEvidence{Wait: wait}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// cmd.Wait joins its output-copy goroutine. Only an observed completion can
	// seal EOF: another still-running peer may have written just part of a line.
	if wait != "not_observed" && wait != "shutdown_timeout" {
		if !d.dropLine && len(d.line) != 0 {
			line := strings.TrimSpace(string(d.line))
			if strings.HasPrefix(line, strings.TrimSpace(routeChildMarker)) {
				if d.markerSeen {
					d.fixed.Runtime.MarkerRepeated = true
				}
				d.markerSeen = true
				d.fixed.Runtime.MarkerInvalid = true
				d.fixed.Fixture = nil
			} else {
				d.observeTerminationLine(line)
			}
		}
		d.line = d.line[:0]
		d.dropLine = false
	}
	v := d.fixed
	v.Wait = wait
	if d.fixed.Fixture != nil {
		copy := *d.fixed.Fixture
		v.Fixture = &copy
	}
	return v
}
func (p *routeCoreProcess) childTermination() routeChildTerminationEvidence {
	wait := p.waitKind
	if wait == "" {
		wait = "not_observed"
	}
	return p.diagnostics.termination(wait)
}
func emitRouteChildFixture(v routeChildFixtureEvidence) {
	if !validRouteChildFixture(v) {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > 1024 {
		return
	}
	// One fixed marker after deferred Core/lock closure. In particular this
	// captures testing.T failure state whose normal text appears on stdout
	// after the protocol JSON reply, without exporting or draining that text.
	_, _ = os.Stderr.Write(bytes.Join([][]byte{[]byte(routeChildMarker), raw, []byte("\n")}, nil))
}

func TestRouteChildCloseErrorsPreserveJoinedFailure(t *testing.T) {
	v := routeCloseErrors(errors.Join(net.ErrClosed, context.Canceled, context.DeadlineExceeded, os.ErrPermission, errors.New("synthetic-private")))
	if !v.Closed || !v.Canceled || !v.Deadline || !v.Permission || !v.Other {
		t.Fatal("joined close failure lost a fixed cause")
	}
	if routeCloseErrors(nil) != (routeChildCloseErrors{}) {
		t.Fatal("nil close acquired an error")
	}
}
func TestRouteChildTerminationRetainsSafeFixtureAndRuntimeState(t *testing.T) {
	marker := routeChildFixtureEvidence{Schema: 1, ExitRequested: true, ReplyWritten: true, TestFailed: true, CoreClose: "failed", CoreErrors: routeChildCloseErrors{Closed: true, Other: true}, LockClose: "passed"}
	raw, _ := json.Marshal(marker)
	d := new(routeProcessDiagnostics)
	// Exhaust the old retained prefix and exercise arbitrary input chunking.
	d.Write([]byte(strings.Repeat("private", 12000) + "\n"))
	input := "WARNING: DATA RACE\npanic: test timed out after synthetic-private\nfatal error: synthetic-private\n/work/synthetic-private/internal/core/peers.go:99 +0x1\n" + routeChildMarker + string(raw) + "\n"
	for _, b := range []byte(input) {
		d.Write([]byte{b})
	}
	v := d.termination("exit_one")
	if !v.Runtime.Race || !v.Runtime.Panic || !v.Runtime.Fatal || !v.Runtime.TestTimeout || !v.Runtime.OutputTruncated || !v.Areas.PeerAPI || !reflect.DeepEqual(v.Fixture, &marker) || !validRouteChildTermination(v) {
		t.Fatal("fixed child evidence lost actual observations")
	}
	encoded, _ := json.Marshal(v)
	if strings.Contains(string(encoded), "synthetic-private") || strings.Contains(string(encoded), "/work/") {
		t.Fatal("private child data escaped fixed categories")
	}
	v.Fixture.TestFailed = false
	if !d.termination("exit_one").Fixture.TestFailed {
		t.Fatal("returned fixture marker aliases retained evidence")
	}
	d.Write(append(append([]byte(routeChildMarker), raw...), '\n'))
	if !d.termination("exit_one").Runtime.MarkerRepeated || d.termination("exit_one").Fixture != nil {
		t.Fatal("duplicate final marker was hidden")
	}
	oversized := new(routeProcessDiagnostics)
	oversized.Write([]byte(routeChildMarker + strings.Repeat("x", 3000) + "\n"))
	oversized.Write([]byte(routeChildMarker + string(raw) + "\n"))
	if got := oversized.termination("wait_error"); !got.Runtime.MarkerInvalid || !got.Runtime.MarkerRepeated || got.Fixture != nil {
		t.Fatal("oversized marker did not fail closed")
	}
	for _, bad := range []string{`{"schema":1,"coreClose":"passed","lockClose":"passed"}`, `{"schema":1,"coreClose":"private","lockClose":"passed"}`, `{"schema":1,"coreClose":"passed","lockClose":"passed","message":"private"}`} {
		d := new(routeProcessDiagnostics)
		d.Write([]byte(routeChildMarker + bad + "\n"))
		v := d.termination("wait_error")
		if !v.Runtime.MarkerInvalid || v.Fixture != nil {
			t.Fatal("unbounded child marker was accepted")
		}
	}
}
func TestRouteChildWaitKindsAreClosed(t *testing.T) {
	for _, test := range []struct {
		err  error
		kind string
	}{{nil, "success"}, {context.Canceled, "context_canceled"}, {context.DeadlineExceeded, "context_deadline"}, {exec.ErrWaitDelay, "io_wait_timeout"}, {errors.New("synthetic-private"), "wait_error"}} {
		if routeChildWaitKind(test.err) != test.kind {
			t.Fatal("wait failure classification changed")
		}
	}
	if validRouteChildTermination(routeChildTerminationEvidence{Wait: "synthetic-private"}) {
		t.Fatal("unbounded exit category accepted")
	}
}

func TestRouteChildTerminationSealsIncompleteMarkersOnlyAfterWait(t *testing.T) {
	marker := routeChildFixtureEvidence{Schema: 1, CoreClose: "passed", LockClose: "passed"}
	raw, _ := json.Marshal(marker)
	for _, test := range []struct {
		name, tail       string
		first, oversized bool
	}{
		{"duplicate_partial", routeChildMarker + string(raw), true, false},
		{"duplicate_prefix", strings.TrimSpace(routeChildMarker), true, false},
		{"malformed_partial", routeChildMarker + `{broken`, false, false},
		{"oversized_partial", routeChildMarker + strings.Repeat("x", 3000), false, true},
		{"duplicate_oversized", routeChildMarker + strings.Repeat("x", 3000), true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := new(routeProcessDiagnostics)
			if test.first {
				d.Write([]byte(routeChildMarker + string(raw) + "\n"))
			}
			d.Write([]byte(test.tail))
			if test.first && !test.oversized {
				for _, wait := range []string{"not_observed", "shutdown_timeout"} {
					if got := d.termination(wait); got.Fixture == nil || got.Runtime.MarkerInvalid || got.Runtime.MarkerRepeated {
						t.Fatal("unfinished child stream was prematurely sealed")
					}
				}
			}
			got := d.termination("exit_one")
			if !got.Runtime.MarkerInvalid || got.Runtime.MarkerRepeated != test.first || got.Fixture != nil || !validRouteChildTermination(got) {
				t.Fatal("incomplete marker did not fail closed at EOF")
			}
			if again := d.termination("exit_one"); !reflect.DeepEqual(got, again) || len(d.line) != 0 {
				t.Fatal("EOF observation changed on repeated reads")
			}
		})
	}
}

func TestRouteChildExitSummarySealsEveryObservedWait(t *testing.T) {
	marker := routeChildFixtureEvidence{Schema: 1, CoreClose: "passed", LockClose: "passed"}
	raw, _ := json.Marshal(marker)
	for _, result := range []error{nil, errors.New("private-wait-error")} {
		d := new(routeProcessDiagnostics)
		d.Write([]byte(routeChildMarker + string(raw) + "\n" + routeChildMarker + `{broken`))
		p := &routeCoreProcess{done: make(chan error, 1), diagnostics: d}
		p.done <- result
		if strings.Contains(p.exitSummary(), "private-wait-error") {
			t.Fatal("private wait error escaped")
		}
		v := d.termination("not_observed")
		if !v.Runtime.MarkerInvalid || !v.Runtime.MarkerRepeated || v.Fixture != nil {
			t.Fatal("observed child completion did not seal legacy summary")
		}
	}
}
