//go:build resource_process_native && linux && (amd64 || arm64)

package resourceacceptance

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	"golang.org/x/sys/unix"
)

func processEntrySupported() bool { return true }

func processEntryStat(file *os.File) (unix.Stat_t, bool) {
	var stat unix.Stat_t
	if file == nil {
		return stat, false
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return stat, false
	}
	var native error
	if err = raw.Control(func(fd uintptr) { native = unix.Fstat(int(fd), &stat) }); err != nil || native != nil {
		return stat, false
	}
	return stat, true
}

func processEntrySameFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Ino != 0
}
func processEntryStableFile(a, b unix.Stat_t) bool {
	return processEntrySameFile(a, b) && a.Mode == b.Mode && a.Size == b.Size && a.Mtim == b.Mtim
}

func processEntryStandardFiles() bool {
	files := [3]*os.File{os.Stdin, os.Stdout, os.Stderr}
	var identities [3]unix.Stat_t
	for i, file := range files {
		if file == nil {
			return false
		}
		raw, err := file.SyscallConn()
		if err != nil {
			return false
		}
		valid := false
		if err = raw.Control(func(fd uintptr) {
			if fd != uintptr(i) {
				return
			}
			if unix.Fstat(int(fd), &identities[i]) != nil || identities[i].Mode&unix.S_IFMT != unix.S_IFIFO || identities[i].Dev == 0 || identities[i].Ino == 0 {
				return
			}
			flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if err != nil {
				return
			}
			want := unix.O_WRONLY
			if i == 0 {
				want = unix.O_RDONLY
			}
			valid = flags&unix.O_ACCMODE == want
		}); err != nil || !valid {
			return false
		}
		for j := 0; j < i; j++ {
			if files[i] == files[j] || processEntrySameFile(identities[i], identities[j]) {
				return false
			}
		}
	}
	return true
}

func (v *processEntryVerifier) open(path string, flags int) *processEntryFile {
	slot := v.files.reserve()
	if slot == nil {
		return nil
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		v.files.publish(slot, nil)
		return nil
	}
	file := os.NewFile(uintptr(fd), "process-entry-owned")
	if !v.files.publish(slot, file) {
		return nil
	}
	return slot
}

func (v *processEntryVerifier) openAt(parent *processEntryFile, name string, flags int) *processEntryFile {
	if parent == nil || parent.file == nil || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil
	}
	slot := v.files.reserve()
	if slot == nil {
		return nil
	}
	raw, err := parent.file.SyscallConn()
	if err != nil {
		v.files.publish(slot, nil)
		return nil
	}
	fd := -1
	var native error
	consistent := false
	err = raw.Control(func(parentFD uintptr) {
		var before, opened, after unix.Stat_t
		native = unix.Fstatat(int(parentFD), name, &before, unix.AT_SYMLINK_NOFOLLOW)
		if native != nil {
			return
		}
		want := uint32(unix.S_IFREG)
		if flags&unix.O_DIRECTORY != 0 {
			want = unix.S_IFDIR
		}
		if before.Mode&unix.S_IFMT != want {
			return
		}
		fd, native = unix.Openat(int(parentFD), name, flags, 0)
		if native != nil {
			return
		}
		if native = unix.Fstat(fd, &opened); native != nil {
			return
		}
		if native = unix.Fstatat(int(parentFD), name, &after, unix.AT_SYMLINK_NOFOLLOW); native != nil {
			return
		}
		consistent = processEntrySameFile(before, opened) && processEntrySameFile(opened, after) && opened.Mode&unix.S_IFMT == want && after.Mode&unix.S_IFMT == want
		if want == unix.S_IFREG {
			consistent = consistent && processEntryStableFile(before, opened) && processEntryStableFile(opened, after)
		}
	})
	if fd < 0 {
		v.files.publish(slot, nil)
		return nil
	}
	file := os.NewFile(uintptr(fd), "process-entry-owned")
	if !v.files.publish(slot, file) || err != nil || native != nil || !consistent {
		return nil
	}
	return slot
}

const processEntryDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
const processEntryRegularFlags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK

// At most previous+next traversal handles exist; no component follows a link.
func (v *processEntryVerifier) openAbsolute(path string, directory bool) *processEntryFile {
	if !processEntryPath(path) {
		return nil
	}
	current := v.open("/", processEntryDirectoryFlags)
	if current == nil {
		return nil
	}
	parts := strings.Split(path[1:], "/")
	for i, part := range parts {
		flags := processEntryDirectoryFlags
		if !directory && i == len(parts)-1 {
			flags = processEntryRegularFlags
		}
		next := v.openAt(current, part, flags)
		if !v.files.release(current, v.deadline) {
			return nil
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func processEntryPrivateDirectory(file *processEntryFile) (unix.Stat_t, bool) {
	if file == nil {
		return unix.Stat_t{}, false
	}
	stat, ok := processEntryStat(file.file)
	return stat, ok && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&07777 == 0700 && stat.Uid == uint32(unix.Geteuid())
}

func processEntryDirectoryIdentity(stat unix.Stat_t, identity processmodel.DirectoryIdentity) bool {
	if identity.Kind != processmodel.UnixDirectoryIdentity || uint64(stat.Dev) != identity.DeviceOrVolume || stat.Ino == 0 || uint64(stat.Ino) != binary.BigEndian.Uint64(identity.FileID[:8]) {
		return false
	}
	for _, b := range identity.FileID[8:] {
		if b != 0 {
			return false
		}
	}
	return true
}

func (v *processEntryVerifier) recheckPath(path string, retained *processEntryFile, directory bool) bool {
	again := v.openAbsolute(path, directory)
	if again == nil {
		return false
	}
	a, okA := processEntryStat(retained.file)
	b, okB := processEntryStat(again.file)
	closed := v.files.release(again, v.deadline)
	return okA && okB && closed && processEntrySameFile(a, b)
}

func processEntryHashExecutable(file *os.File, initial unix.Stat_t) ([32]byte, bool) {
	var digest [32]byte
	if initial.Mode&unix.S_IFMT != unix.S_IFREG || initial.Size <= 0 || initial.Size > 256<<20 || initial.Uid != uint32(unix.Geteuid()) || initial.Mode&0022 != 0 {
		return digest, false
	}
	hash := sha256.New()
	var buffer [32768]byte
	remaining := initial.Size
	for remaining > 0 {
		size := int64(len(buffer))
		if remaining < size {
			size = remaining
		}
		n, err := io.ReadFull(file, buffer[:size])
		if err != nil || int64(n) != size {
			return digest, false
		}
		_, _ = hash.Write(buffer[:n])
		remaining -= int64(n)
	}
	after, ok := processEntryStat(file)
	if !ok || !processEntryStableFile(initial, after) {
		return digest, false
	}
	copy(digest[:], hash.Sum(nil))
	return digest, true
}

func processEntryVerifyNative(v *processEntryVerifier) bool {
	binding, args := v.binding, v.arguments
	target := processmodel.LinuxAMD64
	if runtime.GOARCH == "arm64" {
		target = processmodel.LinuxARM64
	}
	if binding.Target != target || binding.ChildPID != uint64(os.Getpid()) || binding.SupervisorPID != uint64(os.Getppid()) || !processEntryStandardFiles() {
		return false
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != args.root {
		return false
	}
	root := v.openAbsolute(args.root, true)
	rootStat, ok := processEntryPrivateDirectory(root)
	if !ok || !processEntryDirectoryIdentity(rootStat, binding.WorkingDirectoryIdentity) {
		return false
	}
	actualCWD := v.open(".", processEntryDirectoryFlags)
	cwdStat, ok := processEntryPrivateDirectory(actualCWD)
	if !ok || !processEntrySameFile(rootStat, cwdStat) {
		return false
	}
	state := v.openAt(root, "state", processEntryDirectoryFlags)
	if _, ok = processEntryPrivateDirectory(state); !ok {
		return false
	}
	role := v.openAt(state, filepath.Base(args.directory), processEntryDirectoryFlags)
	roleStat, ok := processEntryPrivateDirectory(role)
	if !ok || !processEntryDirectoryIdentity(roleStat, binding.RoleDirectoryIdentity) {
		return false
	}
	inputs := v.openAt(root, "inputs", processEntryDirectoryFlags)
	if _, ok = processEntryPrivateDirectory(inputs); !ok {
		return false
	}
	for _, name := range [...]string{"home", "config", "cache", "appdata", "localappdata", "tmp"} {
		file := v.openAt(root, name, processEntryDirectoryFlags)
		if _, ok = processEntryPrivateDirectory(file); !ok {
			return false
		}
		if !v.files.release(file, v.deadline) {
			return false
		}
	}
	// Recheck before retaining image/input handles so traversal stays <=10.
	if !v.recheckPath(args.root, root, true) || !v.recheckPath(args.directory, role, true) {
		return false
	}
	executable, err := os.Executable()
	if err != nil || executable != args.binary {
		return false
	}
	image := v.open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC)
	if image == nil {
		return false
	}
	imageStat, ok := processEntryStat(image.file)
	if !ok {
		return false
	}
	pathImage := v.openAbsolute(args.binary, false)
	if pathImage == nil {
		return false
	}
	pathStat, ok := processEntryStat(pathImage.file)
	if !ok || !processEntrySameFile(imageStat, pathStat) {
		return false
	}
	digest, ok := processEntryHashExecutable(image.file, imageStat)
	if !ok || digest != binding.ExecutableSHA256 {
		return false
	}
	pathAfter, ok := processEntryStat(pathImage.file)
	if !ok || !processEntryStableFile(pathStat, pathAfter) {
		return false
	}
	if args.input != "" {
		file := v.openAt(inputs, filepath.Base(args.input), processEntryRegularFlags)
		if file == nil {
			return false
		}
		before, ok := processEntryStat(file.file)
		limit := int64(32 * 1024)
		if args.operation == processmodel.CLIManagementGrantConfirm {
			limit = 8192
		}
		if !ok || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uint32(unix.Geteuid()) || before.Mode&07777 != 0600 || before.Size <= 0 || before.Size > limit {
			return false
		}
		// A second no-follow metadata open compares the exact ordinary input.
		check := v.openAt(inputs, filepath.Base(args.input), processEntryRegularFlags)
		if check == nil {
			return false
		}
		after, ok := processEntryStat(check.file)
		if !v.files.release(check, v.deadline) || !ok || !processEntryStableFile(before, after) {
			return false
		}
	}
	assetDigest, ok := processEntryAssetDigest()
	return ok && assetDigest == binding.AssetSHA256
}
