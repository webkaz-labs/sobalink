//go:build resource_process_native

package resourceacceptance

import (
	"path/filepath"
	"strings"
)

func processEntryEnvironment(values []string, root string) bool {
	if len(values) != 14 || !processEntryPath(root) {
		return false
	}
	keys := [...]string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ", "RUNEWIDTH_EASTASIAN", "GOTRACEBACK"}
	want := [...]string{filepath.Join(root, "home"), filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "cache"), filepath.Join(root, "appdata"), filepath.Join(root, "localappdata"), filepath.Join(root, "tmp"), filepath.Join(root, "tmp"), filepath.Join(root, "tmp"), "C.UTF-8", "C.UTF-8", "UTC", "0", "single"}
	var seen [14]bool
	total := 0
	for _, value := range values {
		total += len(value)
		if total > 8192 || strings.ContainsRune(value, 0) {
			return false
		}
		key, contents, ok := strings.Cut(value, "=")
		if !ok {
			return false
		}
		found := false
		for i, expected := range keys {
			if key == expected {
				if seen[i] || contents != want[i] {
					return false
				}
				seen[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
