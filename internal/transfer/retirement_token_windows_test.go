package transfer

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/webkaz-labs/sobalink/internal/config"
	"golang.org/x/sys/windows"
)

// Queries are checked explicitly: IsElevated's false-on-query-error would make
// missing evidence look like an ordinary-user result.
func probeToken(t *testing.T, label string, token windows.Token) (bool, error) {
	t.Helper()
	luids, err := probePrivilegeLUIDs()
	if err != nil {
		return false, fmt.Errorf("token %s: %w", label, err)
	}
	return probeTokenWithLUIDs(t, label, token, luids)
}

type probePrivilegeIDs struct {
	backup, restore, notify windows.LUID
}

func probePrivilegeLUIDs() (probePrivilegeIDs, error) {
	var luids probePrivilegeIDs
	for name, luid := range map[string]*windows.LUID{
		"SeBackupPrivilege": &luids.backup, "SeRestorePrivilege": &luids.restore, "SeChangeNotifyPrivilege": &luids.notify,
	} {
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return luids, fmt.Errorf("privilege name %s: %w", name, err)
		}
		if err := windows.LookupPrivilegeValue(nil, p, luid); err != nil {
			return luids, fmt.Errorf("LookupPrivilegeValue(%s): %w", name, err)
		}
	}
	return luids, nil
}

func probeTokenWithLUIDs(t *testing.T, label string, token windows.Token, luids probePrivilegeIDs) (bool, error) {
	t.Helper()
	var elevated, elevationType, size uint32
	for class, value := range map[uint32]*uint32{windows.TokenElevation: &elevated, windows.TokenElevationType: &elevationType} {
		if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(value)), 4, &size); err != nil {
			return false, fmt.Errorf("token %s GetTokenInformation(class=%d): %w", label, class, err)
		}
		if size != 4 {
			return false, fmt.Errorf("token %s GetTokenInformation(class=%d) size=%d, want 4", label, class, size)
		}
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false, fmt.Errorf("token %s GetTokenGroups: %w", label, err)
	}
	admin := false
	for _, g := range groups.AllGroups() {
		// Include built-in administrators, backup operators, power users and
		// domain administrator/enterprise administrator groups.
		s := g.Sid.String()
		if s == "" {
			return false, fmt.Errorf("token %s group SID conversion failed", label)
		}
		t.Logf("token %s group=%s attributes=%#x", label, s, g.Attributes)
		if g.Attributes&windows.SE_GROUP_ENABLED != 0 && (s == "S-1-5-32-544" || s == "S-1-5-32-547" || s == "S-1-5-32-551" || hasAdminRID(s)) {
			admin = true
		}
	}
	privs, err := probeTokenPrivileges(token)
	if err != nil {
		return false, fmt.Errorf("token %s: %w", label, err)
	}
	power := false
	for _, p := range privs {
		t.Logf("token %s privilege LUID=%#x:%#x attributes=%#x backup=%t restore=%t", label, p.Luid.HighPart, p.Luid.LowPart, p.Attributes, p.Luid == luids.backup, p.Luid == luids.restore)
		if p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 && (p.Luid == luids.backup || p.Luid == luids.restore) {
			power = true
		}
	}
	t.Logf("token %s elevation=%d elevationType=%d enabledAdmin=%t enabledBackupRestore=%t", label, elevated, elevationType, admin, power)
	return elevated == 0 && !admin && !power, nil
}

// TokenPrivileges is variable-sized; keep the query bounded and distinguish
// query failures from an actual empty privilege list.
func probeTokenPrivileges(token windows.Token) ([]windows.LUIDAndAttributes, error) {
	var needed uint32
	err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &needed)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || needed < 4 || needed > 64*1024 {
		return nil, fmt.Errorf("GetTokenInformation(TokenPrivileges, size): size=%d: %v", needed, err)
	}
	data := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &data[0], uint32(len(data)), &needed); err != nil {
		return nil, fmt.Errorf("GetTokenInformation(TokenPrivileges, data): %w", err)
	}
	if needed < 4 || uint64(needed) > uint64(len(data)) {
		return nil, fmt.Errorf("GetTokenInformation(TokenPrivileges, data): returned size=%d, buffer=%d", needed, len(data))
	}
	count := *(*uint32)(unsafe.Pointer(&data[0]))
	const offset = unsafe.Offsetof(windows.Tokenprivileges{}.Privileges)
	if uint64(count)*uint64(unsafe.Sizeof(windows.LUIDAndAttributes{}))+uint64(offset) > uint64(needed) {
		return nil, fmt.Errorf("GetTokenInformation(TokenPrivileges, data): invalid privilege count=%d, size=%d", count, needed)
	}
	if count == 0 {
		return nil, nil
	}
	return unsafe.Slice((*windows.LUIDAndAttributes)(unsafe.Pointer(&data[offset])), count), nil
}

func hasAdminRID(sid string) bool {
	// Domain SIDs begin S-1-5-21; these well-known RIDs convey admin rights.
	// Use the canonical SID string: x/sys SubAuthority accessors return native
	// interior pointers through uintptr, which fails under race/checkptr.
	if !strings.HasPrefix(sid, "S-1-5-21-") {
		return false
	}
	rid := sid[strings.LastIndexByte(sid, '-')+1:]
	return rid == "512" || rid == "518" || rid == "519"
}

func TestHasAdminRID(t *testing.T) {
	for _, tc := range []struct {
		sid  string
		want bool
	}{
		{"S-1-5-21-1643835476-1616584234-1346609752-513", false},
		{"S-1-5-21-1643835476-1616584234-1346609752-512", true},
		{"S-1-5-21-1643835476-1616584234-1346609752-518", true},
		{"S-1-5-21-1643835476-1616584234-1346609752-519", true},
		{"S-1-5-21-1643835476-1616584234-1346609752-1512", false},
		{"S-1-5-32-544", false},
		{"S-1-5-32-512", false},
		{"S-1-16-21-512", false},
		{"S-1-5-21", false},
		{"", false},
	} {
		t.Run(tc.sid, func(t *testing.T) {
			if got := hasAdminRID(tc.sid); got != tc.want {
				t.Errorf("hasAdminRID(%q) = %t, want %t", tc.sid, got, tc.want)
			}
		})
	}
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
			t.Errorf("existing thread token Close: %v", err)
		}
		t.Error("ordinary-user dimension PENDING: thread already impersonating")
		return
	}
	if err != windows.ERROR_NO_TOKEN {
		t.Errorf("ordinary-user dimension PENDING: initial OpenThreadToken(OpenAsSelf=true): %v", err)
		return
	}
	var original windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &original); err != nil {
		t.Errorf("ordinary-user dimension PENDING: OpenProcessToken: %v", err)
		return
	}
	defer func() {
		if err := original.Close(); err != nil {
			t.Errorf("original token Close: %v", err)
		}
	}()
	// Resolve privilege identifiers before impersonation: their lookup must not
	// require authority that the effective restricted token is intended to lack.
	luids, err := probePrivilegeLUIDs()
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: original context: %v", err)
		return
	}
	originalUser, err := original.GetTokenUser()
	if err != nil {
		t.Errorf("same-user restriction not established: original GetTokenUser: %v", err)
		return
	}
	originalSID := originalUser.User.Sid.String()
	if originalSID == "" {
		t.Error("same-user restriction not established: original user SID conversion failed")
		return
	}
	groups, err := original.GetTokenGroups()
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: original GetTokenGroups: %v", err)
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
		t.Errorf("ordinary-user dimension PENDING: resolve CreateRestrictedToken: %v", err)
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
			t.Errorf("restricted token Close: %v", err)
		}
	}()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Errorf("ordinary-user dimension PENDING: DuplicateTokenEx: %v", err)
		return
	}
	defer func() {
		if err := impersonation.Close(); err != nil {
			t.Errorf("impersonation token Close: %v", err)
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
	// OpenAsSelf uses the process context only for opening a query handle; the
	// handle still refers to this thread's actual impersonation token. No process
	// token fallback or revert is allowed while measuring restricted IO.
	var effective windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &effective); err != nil {
		t.Errorf("ordinary-user dimension PENDING: effective OpenThreadToken(OpenAsSelf=true): %v", err)
		return
	}
	defer func() {
		if err := effective.Close(); err != nil {
			t.Errorf("effective token Close: %v", err)
		}
	}()
	u, err := effective.GetTokenUser()
	if err != nil {
		t.Errorf("same-user restriction not established: effective GetTokenUser: %v", err)
		return
	}
	effectiveSID := u.User.Sid.String()
	if effectiveSID == "" || effectiveSID != originalSID {
		t.Error("same-user restriction not established: SID conversion failed or user differs")
		return
	}
	t.Logf("token restricted-thread user=%s originalUser=%s sameUser=true", effectiveSID, originalSID)
	_, err = probeTokenWithLUIDs(t, "restricted-thread", effective, luids)
	if err != nil {
		t.Errorf("ordinary-user dimension PENDING: %v", err)
		return
	}
	actualGroups, err := effective.GetTokenGroups()
	if err != nil {
		t.Errorf("effective GetTokenGroups restriction check: %v", err)
		return
	}
	for _, g := range actualGroups.AllGroups() {
		if g.Attributes&windows.SE_GROUP_ENABLED != 0 && g.Attributes&windows.SE_GROUP_INTEGRITY == 0 {
			t.Errorf("restricted token retained enabled group authority: SID=%s attributes=%#x", g.Sid.String(), g.Attributes)
			return
		}
	}
	// Verify all enabled privileges, not only backup/restore, are absent except
	// change-notify. probeToken already emits the numeric privilege matrix.
	privs, err := probeTokenPrivileges(effective)
	if err != nil {
		t.Errorf("effective privilege restriction check: %v", err)
		return
	}
	for _, p := range privs {
		if p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 && p.Luid != luids.notify {
			t.Errorf("restricted token retained enabled privilege: LUID=%#x:%#x attributes=%#x", p.Luid.HighPart, p.Luid.LowPart, p.Attributes)
			return
		}
	}
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
