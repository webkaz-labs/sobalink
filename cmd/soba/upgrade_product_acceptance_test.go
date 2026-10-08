//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

// Source-only real product owner substitution for two explicitly selected
// happy paths. No synthetic status backend or authorization bypass is used.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"golang.org/x/sys/unix"
)

type productFixtureManifest struct {
	PeerID  string `json:"peerId"`
	OwnerID string `json:"ownerId"`
}
type productPrivateState struct {
	Version               int                         `json:"version"`
	Identity              directlan.Identity          `json:"identity"`
	Selection             core.DirectLANSelection     `json:"selection"`
	Revision              string                      `json:"revision"`
	PreviousLocalEndpoint string                      `json:"previous_local_endpoint"`
	ObservedAt            string                      `json:"observed_at"`
	Peers                 []endpointmeta.PeerRecord   `json:"peers"`
	PendingChange         *endpointmeta.PendingChange `json:"pending_change,omitempty"`
}

func productMode() bool {
	mode := os.Getenv("SOBA_ACTIVATION_CASE")
	return os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") == "1" && (mode == "product-core-web" || mode == "product-core-cli")
}
func productSelfOriginAllowed(child *activationChild) bool {
	return productMode() && child != nil && child.role == "helper" && !child.exited
}
func productPrivateJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return config.AtomicWritePrivate(path, raw)
}
func readProductJSON(path string, value any, limit int64) error {
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > limit {
		return errors.New("invalid private fixture file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) {
		return errors.New("private fixture file changed")
	}
	decoder := json.NewDecoder(io.LimitReader(f, limit+1))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing private fixture data")
	}
	return nil
}

func runProductActivationDispatch(dir string) int {
	if !productMode() {
		return 97
	}
	if len(os.Args) == 2 && os.Args[1] == "--web-activation-supervisor" {
		if runActivationSupervisor(dir) != nil {
			return 95
		}
		return 0
	}
	watchdog := time.AfterFunc(80*time.Second, func() { os.Exit(92) })
	defer watchdog.Stop()
	launchLoginBrowser = func(_ context.Context, url string) error {
		if !validUpgradeUIURL(url) {
			return errors.New("invalid verified browser destination")
		}
		return os.WriteFile(filepath.Join(dir, "browser-opened"), []byte("verified explicit bare URL"), 0600)
	}
	if len(os.Args) == 2 && os.Args[1] == "__upgrade-handoff" {
		if registerActivationChild(dir, "helper") != nil {
			return 96
		}
		if runProductWebHelper(dir) != nil {
			return 94
		}
		return 0
	}
	if len(os.Args) == 2 && os.Args[1] == "--product-activation-cli" {
		if registerActivationChild(dir, "helper") != nil {
			return 96
		}
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		defer cancel()
		if runProductCLIDriver(ctx, dir) != nil {
			return 94
		}
		return 0
	}
	old := len(os.Args) == 2 && os.Args[1] == "--web-activation-fixture"
	successor := len(os.Args) == 7 && os.Args[1] == "--state-dir" && os.Args[2] == dir && os.Args[3] == "--locale" && os.Args[4] == "en" && os.Args[5] == "run" && os.Args[6] == "--offline"
	if !old && !successor {
		return 93
	}
	role := "old"
	if successor {
		role = "successor"
	}
	if registerActivationChild(dir, role) != nil {
		return 96
	}
	if runProductOwner(dir, successor) != nil {
		return 94
	}
	return 0
}

// Observe the actual helper descriptor through private anonymous pipes before
// forwarding it unchanged in value. Production pipe validation and helper HTTP
// authentication still run; this observer never manufactures a descriptor.
func runProductWebHelper(dir string) error {
	originalIn, originalOut := os.Stdin, os.Stdout
	for _, file := range []*os.File{originalIn, originalOut} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return errors.New("private bootstrap pipes required")
		}
	}
	input, err := io.ReadAll(io.LimitReader(originalIn, 8193))
	if err != nil || len(input) > 8192 {
		return errors.New("invalid private bootstrap")
	}
	var bootstrap upgradeHandoffBootstrap
	if json.Unmarshal(input, &bootstrap) != nil {
		return errors.New("invalid private bootstrap")
	}
	if err := productPrivateJSON(filepath.Join(dir, "product-review.json"), bootstrap.Intent); err != nil {
		return err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inR.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		inW.Close()
		return err
	}
	defer outR.Close()
	defer outW.Close()
	inputDone := make(chan error, 1)
	go func() { _, e := inW.Write(input); inputDone <- errors.Join(e, inW.Close()); clear(input) }()
	outputDone := make(chan error, 1)
	go func() {
		var descriptor upgradeHandoffDescriptor
		if err := json.NewDecoder(io.LimitReader(outR, 2048)).Decode(&descriptor); err != nil {
			outputDone <- err
			return
		}
		if !validUpgradeUIURL(descriptor.URL) || descriptor.Deadline != bootstrap.Intent.Deadline {
			outputDone <- errors.New("invalid actual helper descriptor")
			return
		}
		if err := registerActivationOrigin(dir, "helper", descriptor.URL); err != nil {
			outputDone <- err
			return
		}
		outputDone <- json.NewEncoder(originalOut).Encode(descriptor)
	}()
	os.Stdin, os.Stdout = inR, outW
	code, handled := runUpgradeHandoffProcess()
	os.Stdin, os.Stdout = originalIn, originalOut
	_ = outW.Close()
	_ = inR.Close()
	inputErr := <-inputDone
	outputErr := <-outputDone
	if !handled || code != 0 {
		return errors.New("production helper failed")
	}
	return errors.Join(inputErr, outputErr)
}

func runProductOwner(dir string, successor bool) (result error) {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	role := "old"
	if successor {
		role = "successor"
	}
	diagnostic := newProductNativeDiagnostic(dir, role)
	diagnostic.stage("owner-start")
	defer func() { diagnostic.finish(result) }()
	var observationFailed atomic.Bool
	observeForegroundWebURL = func(url string) {
		if registerActivationOrigin(dir, role, url) != nil {
			observationFailed.Store(true)
		}
	}
	defer func() { observeForegroundWebURL = nil }()
	var manifest productFixtureManifest
	if readProductJSON(filepath.Join(dir, "product-manifest.json"), &manifest, 4096) != nil {
		return errors.New("fixture manifest unavailable")
	}
	var peer *core.Core
	var peerLock io.Closer
	if successor {
		diagnostic.stage("peer-open")
		peerDir := filepath.Join(dir, "peer")
		lock, err := config.AcquireLock(peerDir)
		if err != nil {
			return err
		}
		peerLock = lock
		peer, err = core.Open(ctx, core.Options{Directory: peerDir, Version: "synthetic-product-peer", SkipNetworkStart: true})
		if err != nil {
			return errors.Join(err, lock.Close())
		}
		defer func() { cancel(); result = errors.Join(result, peer.Close(), peerLock.Close()) }()
		intent := core.UpgradeIntent{PeerID: manifest.OwnerID, Deadline: time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339Nano)}
		invoke := func(name string, in any) (any, error) {
			raw, _ := json.Marshal(in)
			return peer.Command(ctx, webui.Command{RequestID: randomProductRequest(), Name: name, Payload: raw})
		}
		diagnostic.stage("peer-review")
		reviewed, err := invoke("direct-lan.upgrade.review", intent)
		if err != nil {
			return err
		}
		intent.ExpectedRevision = reviewed.(core.UpgradeReview).Revision
		diagnostic.stage("peer-apply")
		if _, err := invoke("direct-lan.upgrade.run", intent); err != nil {
			return err
		}
	}
	diagnostic.stage("foreground")
	monitorDone := make(chan error, 1)
	go func() {
		err := monitorProductOwner(ctx, cancel, dir, successor, manifest, peer, &observationFailed, diagnostic)
		if err != nil {
			cancel()
		}
		monitorDone <- err
	}()
	// Actual foreground entry: Core.Open, full embedded assets, real handoff and
	// lifecycle handler, ordered Core/HTTP/IPC/lock closure and acknowledgement.
	err := runForeground(ctx, dir, successor, false, io.Discard)
	cancel()
	monitorErr := <-monitorDone
	return errors.Join(err, monitorErr)
}
func randomProductRequest() string { token, _ := upgradeRandomToken(); return "product-" + token }
func productIPC(ctx context.Context, dir, command string, out any) error {
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return control.Call(call, dir, command, out)
}
func monitorProductOwner(ctx context.Context, cancel context.CancelFunc, dir string, successor bool, manifest productFixtureManifest, peer *core.Core, failed *atomic.Bool, diagnostic *productNativeDiagnostic) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	announced, cliStarted, proofWritten := false, false, false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if _, err := os.Stat(filepath.Join(dir, "stop-fixture")); err == nil {
			cancel()
			return nil
		}
		if failed.Load() {
			cancel()
			return errors.New("actual Web origin registration failed")
		}
		var identity upgradeIdentity
		if productIPC(ctx, dir, lifecycleIdentityCommand, &identity) != nil || identity.ProcessID != os.Getpid() {
			continue
		}
		diagnostic.stage("identity")
		if !announced {
			if successor {
				if !identity.Offline {
					return errors.New("successor was not opened offline")
				}
				// Actual restartManagedUpgradeProcess already verifies initial no-attempt/no-ready before reapplying; this later observer may race that legitimate apply.
				if os.WriteFile(filepath.Join(dir, "successor-started"), []byte("actual offline Core management"), 0600) != nil {
					return errors.New("fixture readiness write failed")
				}
			} else {
				if identity.Offline || !identity.AttemptedNetwork {
					return errors.New("old Core did not attempt actual network startup")
				}
				var ui struct {
					URL  string `json:"url"`
					Code string `json:"code"`
				}
				if productIPC(ctx, dir, "ui", &ui) != nil || !validUpgradeUIURL(ui.URL) || ui.Code == "" {
					continue
				}
				if productPrivateJSON(filepath.Join(dir, "session.json"), map[string]any{"url": ui.URL, "code": ui.Code, "productCore": true, "peerId": manifest.PeerID}) != nil {
					return errors.New("private readiness write failed")
				}
			}
			announced = true
			diagnostic.stage("management-announced")
		}
		if !successor && os.Getenv("SOBA_ACTIVATION_CASE") == "product-core-cli" && !cliStarted {
			if _, err := os.Stat(filepath.Join(dir, "product-cli-start")); err == nil {
				diagnostic.stage("cli-launch")
				tty := os.NewFile(3, "private-product-pty")
				if tty == nil {
					return errors.New("private PTY missing")
				}
				// ExtraFiles deliberately inherited FD3 into this owner. Restrict
				// the next exec to the explicit stdout duplicate: otherwise the
				// CLI's successor inherits a second slave and prevents PTY EOF.
				if _, err := unix.FcntlInt(tty.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
					return errors.Join(err, tty.Close())
				}
				exe, err := os.Executable()
				if err != nil {
					return errors.Join(err, tty.Close())
				}
				cmd := exec.Command(exe, "--product-activation-cli")
				cmd.Env = os.Environ()
				cmd.Dir = dir
				cmd.Stdout = tty
				startErr := cmd.Start()
				// Start has duplicated this owned slave into the child's stdout.
				// Release the parent's copy exactly once on either result.
				closeErr := tty.Close()
				if startErr != nil {
					return errors.Join(startErr, closeErr)
				}
				_ = cmd.Process.Release()
				if closeErr != nil {
					return closeErr
				}
				cliStarted = true
			}
		}
		if successor && !proofWritten && peer != nil {
			diagnostic.stage("review-read")
			var intent core.UpgradeIntent
			if readProductJSON(filepath.Join(dir, "product-review.json"), &intent, 4096) != nil {
				continue
			}
			raw, _ := json.Marshal(webui.Command{RequestID: randomProductRequest(), Name: "direct-lan.upgrade.status", Payload: json.RawMessage(`{}`)})
			var status core.UpgradeProgress
			diagnostic.stage("owner-status")
			if productIPC(ctx, dir, string(raw), &status) != nil {
				continue
			}
			diagnostic.status(false, status.State)
			if status.State != "network-started" {
				continue
			}
			diagnostic.stage("peer-status")
			peerStatus, err := peer.Command(ctx, webui.Command{RequestID: randomProductRequest(), Name: "direct-lan.upgrade.status", Payload: json.RawMessage(`{}`)})
			if err == nil {
				diagnostic.status(true, peerStatus.(core.UpgradeProgress).State)
			}
			if err != nil || peerStatus.(core.UpgradeProgress).State != "network-started" {
				continue
			}
			diagnostic.stage("review-binding")
			if status.PeerID != intent.PeerID || status.Deadline != intent.Deadline || intent.PeerID != manifest.PeerID || intent.ExpectedRevision == "" {
				return errors.New("actual original review binding changed")
			}
			diagnostic.stage("network-ready")
			if productIPC(ctx, dir, lifecycleIdentityCommand, &identity) != nil || !identity.NetworkReady || !peer.ManagedLifecycleState().NetworkReady {
				continue
			}
			diagnostic.stage("persisted-context")
			var ownerState, peerState productPrivateState
			if readProductJSON(filepath.Join(dir, "direct-lan.json"), &ownerState, 1<<20) != nil || readProductJSON(filepath.Join(dir, "peer", "direct-lan.json"), &peerState, 1<<20) != nil {
				continue
			}
			if len(ownerState.Peers) != 1 || len(peerState.Peers) != 1 || !ownerState.Peers[0].ContextConfirmed || !peerState.Peers[0].ContextConfirmed || ownerState.Peers[0].PairContext == nil || peerState.Peers[0].PairContext == nil {
				continue
			}
			if ownerState.Identity.PublicKey() != manifest.OwnerID || peerState.Identity.PublicKey() != manifest.PeerID || ownerState.Peers[0].Peer.Key != manifest.PeerID || peerState.Peers[0].Peer.Key != manifest.OwnerID || ownerState.PendingChange != nil || peerState.PendingChange != nil || ownerState.Peers[0].UpgradePending != nil || peerState.Peers[0].UpgradePending != nil {
				return errors.New("actual saved owner identity or completion changed")
			}
			diagnostic.stage("pair-binding")
			first, e1 := ownerState.Peers[0].PairContext.Binding()
			second, e2 := peerState.Peers[0].PairContext.Binding()
			if e1 != nil || e2 != nil || first != second || ownerState.Peers[0].EndpointState == nil || peerState.Peers[0].EndpointState == nil || ownerState.Peers[0].EndpointState.PairBinding != first || peerState.Peers[0].EndpointState.PairBinding != second {
				return errors.New("saved actual pair contexts differ")
			}
			if productPrivateJSON(filepath.Join(dir, "product-activation.json"), map[string]any{"schema": 1, "peerConfirmed": true, "ownerConfirmed": true, "ordinaryReady": true, "originalReviewPreserved": true}) != nil {
				return errors.New("private evidence write failed")
			}
			proofWritten = true
			diagnostic.stage("proof-written")
		}
	}
}

type productSupervisorResources struct {
	dir           string
	master, slave *os.File
	outputDone    chan struct{}
	mu            sync.Mutex
	output        []byte
	outputErr     error
	once          sync.Once
	closeErr      error
	resultWritten bool
	failure       string
}

func prepareProductSupervisor(dir string, command *exec.Cmd) (activationSupervisorExtra, error) {
	if !productMode() {
		return nil, nil
	}
	resources := &productSupervisorResources{dir: dir}
	// One exact TCP allocation and exact same-port UDP bind per synthetic peer.
	// No port scan/retry, address probing, external destination or alias change.
	var reservations []io.Closer
	closeReservations := func() error {
		var errs []error
		for _, r := range reservations {
			errs = append(errs, r.Close())
		}
		return errors.Join(errs...)
	}
	var endpoints [2]netip.AddrPort
	for i := range endpoints {
		tcp, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, errors.Join(err, closeReservations())
		}
		reservations = append(reservations, tcp)
		endpoint := tcp.Addr().(*net.TCPAddr).AddrPort()
		if endpoint.Addr() != netip.MustParseAddr("127.0.0.1") || endpoint.Port() < 1024 || endpoint.Port() == 54543 || endpoint.Port() == 54544 || endpoint.Port() == 54545 {
			return nil, errors.Join(errors.New("invalid fixture endpoint"), closeReservations())
		}
		udp, err := net.ListenPacket("udp4", endpoint.String())
		if err != nil {
			return nil, errors.Join(err, closeReservations())
		}
		reservations = append(reservations, udp)
		endpoints[i] = endpoint
	}
	identities := [2]directlan.Identity{{Seed: strings.Repeat("61", 32)}, {Seed: strings.Repeat("62", 32)}}
	dirs := [2]string{dir, filepath.Join(dir, "peer")}
	for i, path := range dirs {
		if err := config.SecureDir(path); err != nil {
			return nil, errors.Join(err, closeReservations())
		}
		state := productPrivateState{Version: 3, Identity: identities[i], Selection: core.DirectLANSelection{Listen: endpoints[i].String(), Prefixes: []string{"127.0.0.1/32"}}, Revision: "1", PreviousLocalEndpoint: endpoints[i].String(), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{{Peer: endpointmeta.PeerWire{Key: identities[1-i].PublicKey(), TunnelKey: identities[1-i].TunnelKey(), Endpoint: endpoints[1-i].String(), Name: "synthetic-product-peer"}, Revision: "1"}}}
		profile := core.Profile{Version: 1, Settings: core.Settings{Locale: "en", Theme: "system", Network: "direct-lan", Hostname: "synthetic-product-owner"}, Peers: []core.Trust{}, Services: []core.ServiceSpec{}}
		if err := productPrivateJSON(filepath.Join(path, "direct-lan.json"), state); err != nil {
			return nil, errors.Join(err, closeReservations())
		}
		if err := productPrivateJSON(filepath.Join(path, "sobalink.json"), profile); err != nil {
			return nil, errors.Join(err, closeReservations())
		}
	}
	if err := productPrivateJSON(filepath.Join(dir, "product-manifest.json"), productFixtureManifest{PeerID: identities[1].PublicKey(), OwnerID: identities[0].PublicKey()}); err != nil {
		return nil, errors.Join(err, closeReservations())
	}
	if err := closeReservations(); err != nil {
		return nil, err
	}
	if os.Getenv("SOBA_ACTIVATION_CASE") == "product-core-cli" {
		master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
		if err != nil {
			return nil, err
		}
		resources.master = master
		if err = unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
			master.Close()
			return nil, err
		}
		number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
		if err != nil {
			master.Close()
			return nil, err
		}
		slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
		if err != nil {
			master.Close()
			return nil, err
		}
		resources.slave = slave
		command.ExtraFiles = []*os.File{slave} // owned old process receives only FD3
		resources.outputDone = make(chan struct{})
		go func() {
			defer close(resources.outputDone)
			buffer := make([]byte, 1024)
			for {
				n, err := master.Read(buffer)
				if n > 0 {
					resources.mu.Lock()
					if len(resources.output)+n > 65536 {
						resources.outputErr = errors.New("private terminal output budget exceeded")
					} else {
						resources.output = append(resources.output, buffer[:n]...)
					}
					resources.mu.Unlock()
				}
				if err != nil {
					if !errors.Is(err, io.EOF) && !errors.Is(err, unix.EIO) && !errors.Is(err, os.ErrClosed) {
						resources.mu.Lock()
						resources.outputErr = err
						resources.mu.Unlock()
					}
					return
				}
			}
		}()
	}
	return resources, nil
}
func (r *productSupervisorResources) FailureStage() string {
	if r.failure == "" {
		return "none"
	}
	return r.failure
}
func (r *productSupervisorResources) noteFailure(stage string, err error) error {
	if err != nil && r.failure == "" {
		r.failure = stage
	}
	return err
}
func (r *productSupervisorResources) Observe(s *activationSupervisor) error {
	if r.slave != nil {
		if err := r.slave.Close(); err != nil {
			return r.noteFailure("slave-close", err)
		}
		r.slave = nil
	}
	if r.outputDone == nil || r.resultWritten {
		return nil
	}
	s.mu.Lock()
	helperExited := false
	helperFailed := false
	for pid, child := range s.children {
		if child.role == "helper" {
			success, reaped := s.reapedOK[pid]
			helperExited = child.exited && reaped && success
			helperFailed = reaped && !success
		}
	}
	s.mu.Unlock()
	if helperFailed {
		return r.noteFailure("helper-exit", errors.New("actual CLI helper did not exit successfully"))
	}
	if !helperExited {
		return nil
	}
	select {
	case <-r.outputDone:
	default:
		return nil
	}
	r.mu.Lock()
	data := append([]byte(nil), r.output...)
	captureErr := r.outputErr
	r.mu.Unlock()
	defer clear(data)
	if captureErr != nil {
		return r.noteFailure("capture-error", captureErr)
	}
	// Production CLI writes its fresh code only to this real private terminal.
	// Parse fixed presentation fields privately; no terminal byte is exported.
	var address, code string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "Local management: ") {
			address = strings.TrimSpace(strings.TrimPrefix(line, "Local management: "))
		}
		if strings.HasPrefix(line, "Fresh one-time code (5 minutes): ") {
			code = strings.TrimSpace(strings.TrimPrefix(line, "Fresh one-time code (5 minutes): "))
		}
	}
	if !validUpgradeUIURL(address) || code == "" {
		return r.noteFailure("result-parse", errors.New("actual private CLI result unavailable"))
	}
	if err := productPrivateJSON(filepath.Join(r.dir, "cli-result.json"), map[string]any{"url": address, "code": code, "completed": true}); err != nil {
		return r.noteFailure("result-write", err)
	}
	r.resultWritten = true
	return nil
}
func (r *productSupervisorResources) Close() error {
	r.once.Do(func() {
		if r.slave != nil {
			r.closeErr = errors.Join(r.closeErr, r.noteFailure("slave-close", r.slave.Close()))
			r.slave = nil
		}
		if r.master != nil {
			r.closeErr = errors.Join(r.closeErr, r.noteFailure("master-close", r.master.Close()))
			<-r.outputDone
		}
		r.mu.Lock()
		clear(r.output)
		r.output = nil
		r.closeErr = errors.Join(r.closeErr, r.noteFailure("capture-error", r.outputErr))
		r.mu.Unlock()
	})
	return r.closeErr
}

type productReviewWriter struct{ bytes.Buffer }

func (w *productReviewWriter) Write(data []byte) (int, error) {
	if w.Len()+len(data) > 16384 {
		return 0, errors.New("private review output exceeded budget")
	}
	return w.Buffer.Write(data)
}
func runProductCLIDriver(ctx context.Context, dir string) (result error) {
	diagnostic := newProductNativeDiagnostic(dir, "cli")
	diagnostic.stage("cli-terminal")
	defer func() { diagnostic.finish(result) }()
	if !loginPrivateTerminal(os.Stdout) {
		return errors.New("actual private terminal required")
	}
	var manifest productFixtureManifest
	if readProductJSON(filepath.Join(dir, "product-manifest.json"), &manifest, 4096) != nil {
		return errors.New("fixture manifest unavailable")
	}
	deadline := time.Now().Add(45 * time.Second).UTC().Truncate(time.Second).Format(time.RFC3339)
	args := []string{"--state-dir", dir, "--locale", "en", "direct-lan", "upgrade", "--peer", manifest.PeerID, "--deadline", deadline}
	diagnostic.stage("cli-review")
	var out productReviewWriter
	if err := run(ctx, args, &out); err != nil {
		return err
	}
	diagnostic.stage("cli-review-binding")
	var review core.UpgradeReview
	if json.Unmarshal(out.Bytes(), &review) != nil || review.PeerID != manifest.PeerID || review.Deadline != deadline || review.Revision == "" || !review.RestartRequired {
		return errors.New("actual CLI review binding invalid")
	}
	intent := core.UpgradeIntent{PeerID: review.PeerID, Deadline: review.Deadline, ExpectedRevision: review.Revision}
	if err := productPrivateJSON(filepath.Join(dir, "product-review.json"), intent); err != nil {
		return err
	}
	diagnostic.stage("cli-apply")
	err := run(ctx, append(args, "--apply", "--review", review.Revision), os.Stdout)
	if err == nil {
		diagnostic.stage("cli-complete")
	}
	return err
}
