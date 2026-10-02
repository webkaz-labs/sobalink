package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func limitsOrDefault(in Limits) (Limits, error) {
	d := DefaultLimits()
	v, defaults := reflect.ValueOf(&in).Elem(), reflect.ValueOf(d)
	for i := 0; i < v.NumField(); i++ {
		n, max := v.Field(i).Int(), defaults.Field(i).Int()
		if n == 0 {
			v.Field(i).SetInt(max)
		} else if n < 0 || n > max {
			return Limits{}, fmt.Errorf("%w: %s", ErrLimit, v.Type().Field(i).Name)
		}
	}
	return in, nil
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validPeer(p Peer) bool {
	if p.Generation == 0 || len(p.ID) < 1 || len(p.ID) > 256 || !utf8.ValidString(p.ID) {
		return false
	}
	for _, c := range p.ID {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

func canonical(s string) string {
	// Uppercasing also covers Windows-style case equivalence such as I/ı;
	// Unicode folding then covers expansions such as ß/SS.
	return norm.NFC.String(cases.Fold().String(strings.ToUpper(norm.NFC.String(s))))
}

func validatePath(p string, lim Limits) error {
	if p == "" || len(p) > lim.MaxPathBytes || !utf8.ValidString(p) || path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "\\") {
		return ErrUnsafePath
	}
	parts := strings.Split(p, "/")
	if len(parts) > lim.MaxDepth {
		return ErrLimit
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return ErrUnsafePath
		}
		for _, c := range part {
			if unicode.IsControl(c) || strings.ContainsRune("<>:\"|?*", c) {
				return ErrUnsafePath
			}
		}
		stem := strings.ToUpper(strings.TrimRight(strings.SplitN(part, ".", 2)[0], " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
			return ErrUnsafePath
		}
		if len([]rune(stem)) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.ContainsRune("123456789¹²³", []rune(stem)[3]) {
			return ErrUnsafePath
		}
	}
	return nil
}

// ValidateManifest checks portable names, collisions, hierarchy and resource
// limits before an offer can reserve memory or destination storage.
func ValidateManifest(manifest Manifest, limits Limits) error {
	lim, err := limitsOrDefault(limits)
	if err != nil {
		return err
	}
	_, err = validateManifest(manifest, lim)
	return err
}

func validateManifest(manifest Manifest, lim Limits) (int64, error) {
	if !validID(manifest.ID) || len(manifest.Entries) == 0 {
		return 0, ErrInvalidManifest
	}
	if len(manifest.Entries) > lim.MaxEntries {
		return 0, ErrLimit
	}
	if metadataSize(manifest) > lim.MaxManifestBytes {
		return 0, ErrLimit
	}
	ids, names, full := map[string]bool{}, map[string]string{}, map[string]Kind{}
	var total int64
	for _, e := range manifest.Entries {
		if !validID(e.ID) || ids[e.ID] {
			return 0, fmt.Errorf("%w: duplicate or invalid file ID", ErrInvalidManifest)
		}
		ids[e.ID] = true
		if err := validatePath(e.Path, lim); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
		}
		if e.Kind != File && e.Kind != Directory || e.Size < 0 {
			return 0, ErrInvalidManifest
		}
		if e.Kind == Directory {
			if e.Size != 0 || e.SHA256 != "" {
				return 0, ErrInvalidManifest
			}
		} else {
			digest, err := hex.DecodeString(e.SHA256)
			if err != nil || len(digest) != sha256.Size || strings.ToLower(e.SHA256) != e.SHA256 {
				return 0, ErrInvalidManifest
			}
			if e.Size > lim.MaxFileBytes || e.Size > lim.MaxBatchBytes-total {
				return 0, ErrLimit
			}
			total += e.Size
		}
		parts := strings.Split(e.Path, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			key := canonical(prefix)
			if previous, ok := names[key]; ok && previous != prefix {
				return 0, fmt.Errorf("%w: case or Unicode path collision", ErrInvalidManifest)
			}
			names[key] = prefix
		}
		key := canonical(e.Path)
		if _, exists := full[key]; exists {
			return 0, fmt.Errorf("%w: duplicate path", ErrInvalidManifest)
		}
		full[key] = e.Kind
	}
	for p := range full {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if _, exists := full[parent]; exists {
				return 0, fmt.Errorf("%w: file or empty directory has children", ErrInvalidManifest)
			}
		}
	}
	return total, nil
}

func metadataSize(m Manifest) int64 {
	n := int64(512 + len(m.ID))
	for _, e := range m.Entries {
		n += int64(512 + len(e.ID) + len(e.Path) + len(e.SHA256))
	}
	return n
}
