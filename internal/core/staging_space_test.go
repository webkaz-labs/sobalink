package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestStagingDiskGuardChecksBeforeAndDuringBrowserAndCLIWrites(t *testing.T) {
	for _, method := range []string{"browser", "cli"} {
		for _, during := range []bool{false, true} {
			for _, unknown := range []bool{false, true} {
				t.Run(method+map[bool]string{false: "-before", true: "-during"}[during]+map[bool]string{false: "-low", true: "-unknown"}[unknown], func(t *testing.T) {
					// This fixture uses net.Pipe only, with no operating-system listener.
					p := newCorePair(t)
					trustPair(t, p)
					observedBytes := int64(0)
					p.a.diskSpace = diskspace.New(func(file *os.File) (uint64, error) {
						info, err := file.Stat()
						if err != nil {
							return 0, err
						}
						if !during || info.Mode().IsRegular() && info.Size() > 0 {
							observedBytes = info.Size()
							if unknown {
								return 0, errors.New("private probe error")
							}
							return 0, nil
						}
						return 1 << 40, nil
					})
					payload := strings.Repeat("x", 3*diskspace.ChunkBytes)
					expected := diskspace.ErrLow
					if unknown {
						expected = diskspace.ErrUnknown
					}
					if method == "browser" {
						entry := transfer.Entry{ID: "1", Path: "payload", Kind: transfer.File, Size: int64(len(payload))}
						request, body := multipartTransfer(t, "space-test", []transfer.Entry{entry}, payload, false)
						defer body.Close()
						response := httptest.NewRecorder()
						p.a.Upload(response, request)
						var failure struct {
							Code string `json:"code"`
						}
						if json.Unmarshal(response.Body.Bytes(), &failure) != nil || response.Code != http.StatusInsufficientStorage || failure.Code != expected.ErrorCode() {
							t.Fatalf("response %d %s", response.Code, response.Body.String())
						}
					} else {
						source := filepath.Join(t.TempDir(), "original")
						if err := os.WriteFile(source, []byte(payload), 0600); err != nil {
							t.Fatal(err)
						}
						if _, err := p.a.SendPaths(context.Background(), "peer-b", []string{source}); !errors.Is(err, expected) {
							t.Fatalf("send error %v", err)
						}
						if data, err := os.ReadFile(source); err != nil || string(data) != payload {
							t.Fatal("source changed")
						}
					}
					if during && (observedBytes <= 0 || observedBytes > diskspace.ChunkBytes) {
						t.Fatalf("guard did not stop after bounded write: %d", observedBytes)
					}
					p.a.mu.RLock()
					retained := len(p.a.outgoing)
					p.a.mu.RUnlock()
					if retained != 0 {
						t.Fatal("failed staging retained an active reservation")
					}
					entries, err := os.ReadDir(filepath.Join(p.a.dir, "outgoing"))
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					if len(entries) != 0 {
						t.Fatal("failed staging left active spool")
					}
				})
			}
		}
	}
}

func TestStagingDiskWriterHonorsReserveChangesAndCancellation(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "staging-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	c := &Core{capacity: capacity.Defaults(), diskSpace: diskspace.New(func(*os.File) (uint64, error) { return 1 << 30, nil })}
	writer := stagingDiskWriter{c, context.Background(), file}
	if _, err := writer.Write([]byte("kept")); err != nil {
		t.Fatal(err)
	}
	c.capacity.Resources["diskReserveBytes"] = capacity.Limited(2 << 30)
	if _, err := writer.Write([]byte("blocked")); !errors.Is(err, diskspace.ErrLow) {
		t.Fatal(err)
	}
	c.capacity.Resources["diskReserveBytes"] = capacity.Limited(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer.ctx = ctx
	if _, err := writer.Write([]byte("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	writer.ctx = context.Background()
	if _, err := writer.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file.Name()); err != nil || string(data) != "keptagain" {
		t.Fatalf("staging bytes %q %v", data, err)
	}
}

func TestDiskReservePolicyIsFiniteVisibleAndShared(t *testing.T) {
	p := capacity.Defaults()
	if p.Number("resources", "diskReserveBytes") != diskspace.DefaultReserveBytes {
		t.Fatal("default reserve mismatch")
	}
	for _, reserve := range []int64{1, 1 << 30, 1 << 40} {
		p.Resources["diskReserveBytes"] = capacity.Limited(reserve)
		if err := validateSupportedCapacity(p); err != nil {
			t.Fatal(err)
		}
		for _, budget := range []string{"transferSpoolBytes", "receiveReservedBytes"} {
			if got := transferLimitsFor(p, budget).DiskReserveBytes; got != reserve {
				t.Fatalf("reserve %d", got)
			}
		}
	}
	p.Resources["diskReserveBytes"] = capacity.Unlimited()
	if p.Validate() == nil {
		t.Fatal("unbounded reserve accepted")
	}
}

func TestPeerDiskFailureCodesAreBoundedAndDoNotReflectPrivateText(t *testing.T) {
	for _, test := range []struct{ body, code string }{
		{`{"code":"disk_space_low","error":"private/path"}`, "peer_disk_space_low"},
		{`{"code":"disk_space_unknown"}`, "peer_disk_space_unknown"},
		{`{"code":"untrusted"}`, "peer_storage_unavailable"},
		{strings.Repeat("x", 2048), "peer_storage_unavailable"},
	} {
		err := peerDiskSpaceError(strings.NewReader(test.body))
		if networkErrorCode(err) != test.code || strings.Contains(err.Error(), "private/path") {
			t.Fatalf("remote failure %v", err)
		}
	}
	for _, err := range []error{diskspace.ErrLow, diskspace.ErrUnknown} {
		recorder := httptest.NewRecorder()
		replyDiskSpace(recorder, err)
		raw, _ := io.ReadAll(recorder.Result().Body)
		if recorder.Code != http.StatusInsufficientStorage || !strings.Contains(string(raw), networkErrorCode(err)) {
			t.Fatalf("response %d %s", recorder.Code, raw)
		}
	}
}

func TestEmptyStagingFilesRecheckTheirDestinationVolume(t *testing.T) {
	for _, method := range []string{"browser", "cli"} {
		for _, unknown := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "-low", true: "-unknown"}[unknown], func(t *testing.T) {
				p := newCorePair(t)
				trustPair(t, p)
				stopped := false
				p.a.diskSpace = diskspace.New(func(file *os.File) (uint64, error) {
					if strings.HasPrefix(filepath.Base(file.Name()), "batch-") {
						entries, err := os.ReadDir(file.Name())
						if err != nil {
							return 0, err
						}
						if len(entries) > 0 {
							stopped = true
							if unknown {
								return 0, errors.New("probe failure")
							}
							return 0, nil
						}
					}
					return 1 << 40, nil
				})
				want := diskspace.ErrLow
				if unknown {
					want = diskspace.ErrUnknown
				}
				if method == "browser" {
					entries := []transfer.Entry{{ID: "1", Path: "one", Kind: transfer.File}, {ID: "2", Path: "two", Kind: transfer.File}}
					request, body := multipartTransfer(t, "empty-space", entries, "", false)
					defer body.Close()
					response := httptest.NewRecorder()
					p.a.Upload(response, request)
					var failure struct {
						Code string `json:"code"`
					}
					_ = json.Unmarshal(response.Body.Bytes(), &failure)
					if response.Code != http.StatusInsufficientStorage || failure.Code != want.ErrorCode() {
						t.Fatalf("response %d %s", response.Code, response.Body.String())
					}
				} else {
					var paths []string
					for _, name := range []string{"one", "two"} {
						path := filepath.Join(t.TempDir(), name)
						if err := os.WriteFile(path, nil, 0600); err != nil {
							t.Fatal(err)
						}
						paths = append(paths, path)
					}
					if _, err := p.a.SendPaths(context.Background(), "peer-b", paths); !errors.Is(err, want) {
						t.Fatalf("send error %v", err)
					}
					for _, path := range paths {
						if info, err := os.Stat(path); err != nil || info.Size() != 0 {
							t.Fatal("empty original changed")
						}
					}
				}
				if !stopped {
					t.Fatal("actual staging directory was not rechecked before next empty file")
				}
				entries, err := os.ReadDir(filepath.Join(p.a.dir, "outgoing"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed empty staging retained copies: %v %v", entries, err)
				}
			})
		}
	}
}
