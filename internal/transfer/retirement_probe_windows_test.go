package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

const probeShare = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
const probeDirectoryFlags = windows.FILE_FLAG_BACKUP_SEMANTICS | windows.FILE_FLAG_OPEN_REPARSE_POINT

// This is diagnostic only: production never selects a primitive from its result.
func TestWindowsRetirementDirectoryBarrierProbe(t *testing.T) {
	v := windows.RtlGetVersion()
	t.Logf("OS=%d.%d build=%d platform=%d product=%d Go=%s target=%s/%s x/sys=v0.48.0", v.MajorVersion, v.MinorVersion, v.BuildNumber, v.PlatformId, v.ProductType, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	profile, destination := t.TempDir(), t.TempDir()
	if err := config.Protect(profile, true); err != nil {
		t.Fatal(err)
	}
	probeVolume(t, "profile", profile)
	probeVolume(t, "destination", destination)
	ordinary, err := probeToken(t, "process", windows.GetCurrentProcessToken())
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: token query: %v", err)
	}
	// Finish every diagnostic row before running CHOSEN assertions.
	probeDirectoryMatrix(t, profile, destination)
	probeChosenDirectories(t, profile, destination)
	probeGuardCycles(t, profile)
	if ordinary && err == nil {
		t.Log("ordinary-user dimension: measured process token already lacks elevation, enabled administrative groups and backup/restore privileges")
	} else {
		t.Log("process results are NOT ordinary-user proof; trying same-user thread restriction")
		probeRestrictedIO(t, profile, destination)
	}
	t.Run("chosen-production-receive-cancel-reopen", TestWindowsRetirementSavedPartialCleanup)
	t.Log("Native API usability only; process/hardware/storage crash durability is not established by this probe")
}

func probeVolume(t *testing.T, label, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Errorf("filesystem facts %s: %v", label, err)
		return
	}
	var volume, fs [261]uint16
	var serial, maxComponent, flags uint32
	err = windows.GetVolumeInformationByHandle(windows.Handle(f.Fd()), &volume[0], uint32(len(volume)), &serial, &maxComponent, &flags, &fs[0], uint32(len(fs)))
	t.Logf("filesystem %s path=%q name=%q volume=%q serial=%#x maxComponent=%d flags=%#x query=%v close=%v", label, path, windows.UTF16ToString(fs[:]), windows.UTF16ToString(volume[:]), serial, maxComponent, flags, err, f.Close())
	if err != nil {
		t.Errorf("filesystem facts required for %s: %v", label, err)
	}
}

// Independent fixtures prevent a prior successful flush from hiding a failed
// candidate. The 'both' row also compares the APIs on the identical handle.
func probeDirectoryMatrix(t *testing.T, profile, destination string) {
	t.Helper()
	for _, role := range []string{"profile", "destination", "owned-root", "private-stage"} {
		for _, candidate := range []string{"win32", "nt-normal0", "both"} {
			base := destination
			if role == "profile" {
				base = profile
			}
			path := filepath.Join(base, role+"-"+candidate)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Errorf("fixture %s: %v", path, err)
				continue
			}
			if role != "destination" {
				if err := config.Protect(path, true); err != nil {
					t.Errorf("private DACL %s: %v", path, err)
					continue
				}
			}
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Errorf("retained Root %s: %v", path, err)
				continue
			}
			pinned, err := root.Open(".")
			if err != nil {
				t.Errorf("retained handle %s: %v", path, err)
				if err := root.Close(); err != nil {
					t.Error(err)
				}
				continue
			}
			id, identityErr := accountingIdentity(pinned)
			t.Logf("fixture role=%s candidate=%s retained=%q identityErr=%v privateDACL=%t", role, candidate, id, identityErr, accountingPrivate(pinned, nil))
			_, releasePins := probeRemovalShape(t, root)
			p, _ := windows.UTF16PtrFromString(path)
			h, openErr := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, probeShare, nil, windows.OPEN_EXISTING, probeDirectoryFlags, 0)
			t.Logf("Win32 role=%s candidate=%s access=%#x share=%#x disposition=%#x flags=%#x open=%#v", role, candidate, uint32(windows.GENERIC_READ|windows.GENERIC_WRITE), uint32(probeShare), uint32(windows.OPEN_EXISTING), uint32(probeDirectoryFlags), openErr)
			if openErr == nil {
				probeHandleIdentity(t, h)
				if candidate != "nt-normal0" {
					t.Logf("Win32 FlushFileBuffers=%#v", windows.FlushFileBuffers(h))
				}
				if candidate != "win32" {
					probeNTFlush(t, h)
				}
				t.Logf("Win32 close=%#v", windows.CloseHandle(h))
			} else {
				probeRelativeCandidates(t, pinned)
			}
			probeFileControls(t, root)
			t.Logf("deletion pins close=%v", releasePins())
			t.Logf("fixture pinned close=%v Root.Close=%v", pinned.Close(), root.Close())
		}
	}
}

func probeHandleIdentity(t *testing.T, h windows.Handle) {
	var i windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(h, &i)
	t.Logf("opened attributes=%#x volume=%#x file=%x:%x identityQuery=%#v", i.FileAttributes, i.VolumeSerialNumber, i.FileIndexHigh, i.FileIndexLow, err)
}

func probeNTFlush(t *testing.T, h windows.Handle) (bool, error) {
	t.Helper()
	proc := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtFlushBuffersFileEx")
	if err := proc.Find(); err != nil {
		t.Logf("NtFlushBuffersFileEx flags=0 resolve=%v", err)
		return false, err
	}
	// Sentinel distinguishes an unwritten completion from STATUS_SUCCESS. A
	// pending return is NOT success; this synchronous API must complete inline.
	iosb := windows.IO_STATUS_BLOCK{Status: windows.NTStatus(0xdeadbeef)}
	r, _, _ := proc.Call(uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(&iosb)))
	status := windows.NTStatus(uint32(r))
	t.Logf("NtFlushBuffersFileEx flags=0 parameters=nil size=0 NTSTATUS=%#08x completion=%#08x information=%d complete=%t mapped=%v", uint32(status), uint32(iosb.Status), iosb.Information, status == 0 && iosb.Status == 0, status.Errno())
	return status == 0 && iosb.Status == 0, nil
}

func probeRelativeCandidates(t *testing.T, pinned *os.File) {
	name, err := windows.NewNTUnicodeString(".")
	if err != nil {
		t.Error(err)
		return
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(pinned.Fd()), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	read := uint32(windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA | windows.READ_CONTROL | windows.SYNCHRONIZE)
	for _, access := range []uint32{read, read | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA} {
		const options = windows.FILE_DIRECTORY_FILE | windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_REPARSE_POINT
		var h windows.Handle
		iosb := windows.IO_STATUS_BLOCK{Status: windows.NTStatus(0xdeadbeef)}
		err := windows.NtCreateFile(&h, access, &oa, &iosb, nil, 0, probeShare, windows.FILE_OPEN, options, 0, 0)
		t.Logf("diagnostic relative NtCreateFile access=%#x share=%#x disposition=%#x options=%#x return=%v completion=%#08x", access, uint32(probeShare), uint32(windows.FILE_OPEN), uint32(options), err, uint32(iosb.Status))
		if err == nil {
			probeHandleIdentity(t, h)
			probeNTFlush(t, h)
			t.Logf("relative close=%#v", windows.CloseHandle(h))
		}
	}
	runtime.KeepAlive(name)
}

func probeFileControls(t *testing.T, root *os.Root) {
	t.Helper()
	for _, writable := range []bool{true, false} {
		name := fmt.Sprintf("control-%t", writable)
		if err := root.WriteFile(name, []byte("finite flush control"), 0600); err != nil {
			t.Error(err)
			continue
		}
		access := uint32(windows.GENERIC_READ)
		if writable {
			access |= windows.GENERIC_WRITE
		}
		p, _ := windows.UTF16PtrFromString(filepath.Join(root.Name(), name))
		h, err := windows.CreateFile(p, access, probeShare, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		t.Logf("regular control writable=%t access=%#x share=%#x flags=%#x open=%#v", writable, access, uint32(probeShare), uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT), err)
		if err != nil {
			t.Errorf("required file control: %v", err)
			continue
		}
		flushErr := windows.FlushFileBuffers(h)
		t.Logf("regular control writable=%t FlushFileBuffers=%#v", writable, flushErr)
		ntOK, ntErr := probeNTFlush(t, h)
		closeErr := windows.CloseHandle(h)
		t.Logf("regular control writable=%t close=%#v", writable, closeErr)
		if closeErr != nil || writable && flushErr != nil || !writable && flushErr == nil {
			t.Errorf("CHOSEN Win32 positive/negative control mismatch: writable=%t Win32=%v NtSuccess=%t NtError=%v close=%v", writable, flushErr, ntOK, ntErr, closeErr)
		}
		if ntErr != nil || writable != ntOK {
			t.Logf("DIAGNOSTIC Nt normal0 candidate control not validated: writable=%t success=%t resolve=%v; production choice unchanged", writable, ntOK, ntErr)
		}
	}
}

func probeRemovalShape(t *testing.T, root *os.Root) (ok bool, release func() error) {
	t.Helper()
	release = func() error { return nil }
	if err := root.Mkdir("empty-stage", 0700); err != nil {
		t.Error(err)
		return false, release
	}
	if err := config.Protect(filepath.Join(root.Name(), "empty-stage"), true); err != nil {
		t.Error(err)
		return false, release
	}
	stage, err := root.OpenRoot("empty-stage")
	if err != nil {
		t.Error(err)
		return false, release
	}
	var marker *os.File
	release = func() error {
		var err error
		if marker != nil {
			err = marker.Close()
		}
		return errors.Join(err, stage.Close())
	}
	ok = true
	for _, name := range []string{"part-prior", receiveOwnerMarker} {
		f, err := stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			t.Error(err)
			ok = false
			continue
		}
		n, writeErr := f.Write([]byte("prior nonempty fixture"))
		syncErr := f.Sync()
		var closeErr error
		if name == receiveOwnerMarker {
			marker = f
		} else {
			closeErr = f.Close()
		}
		removeErr := stage.Remove(name)
		_, absentErr := stage.Lstat(name)
		t.Logf("Root.Remove name=%q bytes=%d write=%v fileSync=%v remove=%v absence=%v partialClose=%v markerRetained=%t", name, n, writeErr, syncErr, removeErr, absentErr, closeErr, name == receiveOwnerMarker)
		if writeErr != nil || n != len("prior nonempty fixture") || syncErr != nil || closeErr != nil || removeErr != nil || !errors.Is(absentErr, os.ErrNotExist) {
			ok = false
		}
	}
	d, err := stage.Open(".")
	if err != nil {
		t.Error(err)
		return false, release
	}
	names, readErr := d.Readdirnames(-1)
	closeErr := d.Close()
	t.Logf("prior partial removed before empty stage: names=%v read=%v listingClose=%v", names, readErr, closeErr)
	if len(names) != 0 || readErr != nil || closeErr != nil {
		ok = false
	}
	before, beforeErr := root.Lstat("empty-stage")
	removeErr := root.Remove("empty-stage")
	_, absentErr := root.Lstat("empty-stage")
	t.Logf("retained-stage-and-marker Root.Remove directory=%t stat=%v remove=%v absence=%v", before != nil && before.IsDir(), beforeErr, removeErr, absentErr)
	if beforeErr != nil || before == nil || !before.IsDir() || removeErr != nil || !errors.Is(absentErr, os.ErrNotExist) {
		ok = false
	}
	return ok, release
}

func probeChosenDirectories(t *testing.T, profile, destination string) {
	t.Helper()
	for _, path := range []string{profile, destination, filepath.Join(destination, "chosen-owned"), filepath.Join(destination, "chosen-owned", "chosen-stage")} {
		if path != profile && path != destination {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Error(err)
				continue
			}
			if err := config.Protect(path, true); err != nil {
				t.Error(err)
				continue
			}
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Error(err)
			continue
		}
		ok, releasePins := probeRemovalShape(t, root)
		if !ok {
			t.Error("CHOSEN retained-handle deletion shape failed")
		}
		// The diagnostic candidates above never replace this production call.
		syncErr := retirementSyncDirectory(root)
		pinCloseErr := releasePins()
		if pinCloseErr != nil {
			t.Error(pinCloseErr)
		}
		closeErr := root.Close()
		t.Logf("CHOSEN production path=%q sync=%v close=%v", path, syncErr, closeErr)
		if syncErr != nil || closeErr != nil {
			t.Errorf("CHOSEN directory barrier failed: %v", errors.Join(syncErr, closeErr))
		}
	}
}

func probeGuardCycles(t *testing.T, profile string) {
	t.Helper()
	file := FileReceiveAccountingStore{Path: filepath.Join(profile, "guard-cycles.json")}
	root, err := os.OpenRoot(profile)
	if err != nil {
		t.Error(err)
		return
	}
	identity, err := rootIdentity(root)
	if closeErr := root.Close(); closeErr != nil {
		t.Error(closeErr)
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	before := ReceiveAccounting{Version: 2, Roots: []ReceiveRoot{}, Preparation: &ReceivePreparation{Destination: profile, DestinationIdentity: identity, Root: "sobalink-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Stage: ".incoming-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", OwnerToken: "cccccccccccccccccccccccccccccccc"}}
	after := ReceiveAccounting{Version: 2, Roots: []ReceiveRoot{}}
	g, err := retirementGuard(before, after, false)
	if err != nil {
		t.Error(err)
		return
	}
	for i := 0; i < 2; i++ {
		if err := file.SaveReceiveAccounting(before); err != nil {
			t.Error(err)
			return
		}
		lease, err := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
		t.Logf("CHOSEN guard cycle=%d acquire=%v", i, err)
		if err != nil {
			t.Error(err)
			return
		}
		duplicate, duplicateErr := file.AcquireReceiveRetirementGuard(g, accountingLimits(AccountingLimits{}))
		if duplicate != nil {
			if err := duplicate.Close(); err != nil {
				t.Error(err)
			}
		}
		if !errors.Is(duplicateErr, config.ErrAtomicBusy) {
			t.Errorf("exclusive guard creation: %v", duplicateErr)
		}
		if err := verifyRetirement(g, true); err != nil {
			t.Error(err)
			if err := lease.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		if err := file.SaveReceiveAccounting(after, lease); err != nil {
			t.Error(err)
			if err := lease.Close(); err != nil {
				t.Error(err)
			}
			return
		}
		callbacks := 0
		err = lease.Release(func() error { callbacks++; return verifyRetirement(g, false) })
		closeErr := lease.Close()
		_, absentErr := os.Lstat(file.Path + ".retirement")
		t.Logf("CHOSEN guard cycle=%d release=%v close=%v callbacks=%d absence=%v", i, err, closeErr, callbacks, absentErr)
		if err != nil || closeErr != nil || callbacks != 1 || !errors.Is(absentErr, os.ErrNotExist) {
			t.Error("guard cycle did not complete")
		}
	}
}
