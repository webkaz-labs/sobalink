package transfer

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

// Queries are checked explicitly: IsElevated's false-on-query-error would make
// missing evidence look like an ordinary-user result.
func probeToken(t *testing.T, label string, token windows.Token) (bool, error) {
	t.Helper()
	var elevated, elevationType, size uint32
	for class, value := range map[uint32]*uint32{windows.TokenElevation: &elevated, windows.TokenElevationType: &elevationType} {
		if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(value)), 4, &size); err != nil {
			return false, err
		}
		if size != 4 {
			return false, fmt.Errorf("token class %d size=%d", class, size)
		}
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false, err
	}
	admin := false
	for _, g := range groups.AllGroups() {
		// Include built-in administrators, backup operators, power users and
		// domain administrator/enterprise administrator groups.
		s := g.Sid.String()
		t.Logf("token %s group=%s attributes=%#x", label, s, g.Attributes)
		if g.Attributes&windows.SE_GROUP_ENABLED != 0 && (s == "S-1-5-32-544" || s == "S-1-5-32-547" || s == "S-1-5-32-551" || hasAdminRID(g.Sid)) {
			admin = true
		}
	}
	// TokenPrivileges is variable-sized; bounded storage is enough for the
	// measured returned size and errors do not turn into 'no privileges'.
	var needed uint32
	err = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &needed)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || needed < 4 || needed > 64*1024 {
		return false, fmt.Errorf("token privileges size=%d: %v", needed, err)
	}
	data := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &data[0], needed, &needed); err != nil {
		return false, err
	}
	privs := (*windows.Tokenprivileges)(unsafe.Pointer(&data[0]))
	if uint64(privs.PrivilegeCount)*uint64(unsafe.Sizeof(windows.LUIDAndAttributes{}))+uint64(unsafe.Offsetof(privs.Privileges)) > uint64(len(data)) {
		return false, fmt.Errorf("invalid privilege count")
	}
	var backup, restore windows.LUID
	for name, luid := range map[string]*windows.LUID{"SeBackupPrivilege": &backup, "SeRestorePrivilege": &restore} {
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return false, err
		}
		if err := windows.LookupPrivilegeValue(nil, p, luid); err != nil {
			return false, err
		}
	}
	power := false
	for _, p := range privs.AllPrivileges() {
		t.Logf("token %s privilege LUID=%#x:%#x attributes=%#x backup=%t restore=%t", label, p.Luid.HighPart, p.Luid.LowPart, p.Attributes, p.Luid == backup, p.Luid == restore)
		if p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 && (p.Luid == backup || p.Luid == restore) {
			power = true
		}
	}
	t.Logf("token %s elevation=%d elevationType=%d enabledAdmin=%t enabledBackupRestore=%t", label, elevated, elevationType, admin, power)
	runtime.KeepAlive(data)
	return elevated == 0 && !admin && !power, nil
}

func hasAdminRID(sid *windows.SID) bool {
	if sid.SubAuthorityCount() < 2 {
		return false
	}
	// Domain SIDs begin S-1-5-21; these well-known RIDs convey admin rights.
	if sid.SubAuthority(0) != 21 {
		return false
	}
	rid := sid.SubAuthority(uint32(sid.SubAuthorityCount()) - 1)
	return rid == 512 || rid == 518 || rid == 519
}

func probeRestrictedIO(t *testing.T, profile, destination string) {
	t.Helper()
	runtime.LockOSThread()
	unlock := true
	defer func() {
		if unlock {
			runtime.UnlockOSThread()
		}
	}()
	// Never overwrite a pre-existing impersonation context.
	var existing windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &existing)
	if err == nil {
		if err := existing.Close(); err != nil {
			t.Error(err)
		}
		t.Error("ordinary-user dimension PENDING: thread already impersonating")
		return
	}
	if err != windows.ERROR_NO_TOKEN {
		t.Errorf("ordinary-user dimension PENDING: thread query: %v", err)
		return
	}
	var original windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &original); err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	defer func() {
		if err := original.Close(); err != nil {
			t.Error(err)
		}
	}()
	groups, err := original.GetTokenGroups()
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	var deny []windows.SIDAndAttributes
	for _, g := range groups.AllGroups() {
		if g.Attributes&windows.SE_GROUP_ENABLED != 0 && g.Attributes&windows.SE_GROUP_INTEGRITY == 0 {
			deny = append(deny, g)
		}
	}
	var denyPtr *windows.SIDAndAttributes
	if len(deny) > 0 {
		denyPtr = &deny[0]
	}
	proc := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	if err := proc.Find(); err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	var restricted windows.Token
	// DISABLE_MAX_PRIVILEGE=1 removes all privileges except change-notify.
	// Every enabled non-integrity group becomes deny-only. No authority is added.
	r, _, callErr := proc.Call(uintptr(original), 1, uintptr(len(deny)), uintptr(unsafe.Pointer(denyPtr)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	runtime.KeepAlive(groups)
	runtime.KeepAlive(deny)
	if r == 0 {
		t.Errorf("ordinary-user dimension PENDING: CreateRestrictedToken: %v", callErr)
		return
	}
	defer func() {
		if err := restricted.Close(); err != nil {
			t.Error(err)
		}
	}()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	defer func() {
		if err := impersonation.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		t.Errorf("ordinary-user dimension PENDING: SetThreadToken: %v", err)
		return
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Keep this thread out of the scheduler if Windows refuses cleanup.
			// Go destroys a locked thread when its goroutine exits.
			unlock = false
			t.Errorf("RevertToSelf FAILED: %v; locked thread will be retired", err)
		}
	}()
	effective := windows.GetCurrentThreadEffectiveToken()
	u, err := effective.GetTokenUser()
	if err != nil {
		t.Error(err)
		return
	}
	originalUser, err := original.GetTokenUser()
	if err != nil || u.User.Sid.String() != originalUser.User.Sid.String() {
		t.Errorf("same-user restriction not established: %v", err)
		return
	}
	_, err = probeToken(t, "restricted-thread", effective)
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	actualGroups, err := effective.GetTokenGroups()
	if err != nil {
		t.Error(err)
		return
	}
	for _, g := range actualGroups.AllGroups() {
		if g.Attributes&windows.SE_GROUP_ENABLED != 0 && g.Attributes&windows.SE_GROUP_INTEGRITY == 0 {
			t.Error("restricted token retained enabled group authority")
			return
		}
	}
	// Verify all enabled privileges, not only backup/restore, are absent except
	// change-notify. probeToken already emits the numeric privilege matrix.
	var notify windows.LUID
	p, _ := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	if err := windows.LookupPrivilegeValue(nil, p, &notify); err != nil {
		t.Error(err)
		return
	}
	var size uint32
	if err := windows.GetTokenInformation(effective, windows.TokenPrivileges, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER || size < 4 || size > 64*1024 {
		t.Error("restricted privilege query failed")
		return
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(effective, windows.TokenPrivileges, &data[0], size, &size); err != nil {
		t.Error(err)
		return
	}
	privs := (*windows.Tokenprivileges)(unsafe.Pointer(&data[0]))
	if uint64(privs.PrivilegeCount)*uint64(unsafe.Sizeof(windows.LUIDAndAttributes{}))+uint64(unsafe.Offsetof(privs.Privileges)) > uint64(len(data)) {
		t.Error("invalid restricted privilege count")
		return
	}
	for _, p := range privs.AllPrivileges() {
		if p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 && p.Luid != notify {
			t.Error("restricted token retained enabled privilege")
			return
		}
	}
	runtime.KeepAlive(data)
	t.Log("ordinary-rights exact-IO dimension: verified same-user thread token, all non-integrity enabled groups denied, all enabled privileges removed except change-notify; elevated process is NOT an ordinary-user process run")
	// Synchronous calls stay on this thread. Manager's goroutines are measured
	// separately on the process token, never relabeled as restricted execution.
	profile, destination = filepath.Join(profile, "restricted"), filepath.Join(destination, "restricted")
	for _, path := range []string{profile, destination} {
		if err := config.SecureDir(path); err != nil {
			t.Errorf("ordinary-user dimension PENDING: private fixture: %v", err)
			return
		}
	}
	probeDirectoryMatrix(t, profile, destination)
	probeChosenDirectories(t, profile, destination)
	probeGuardCycles(t, profile)
}
