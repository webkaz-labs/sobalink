package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"reflect"
	"sort"
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
		n, initial := v.Field(i).Int(), defaults.Field(i).Int()
		if n == 0 {
			v.Field(i).SetInt(initial)
		} else if n < 0 {
			return Limits{}, fmt.Errorf("%w: %s", ErrLimit, v.Type().Field(i).Name)
		}
	}
	// Streaming readers reserve one extra byte to detect a changed size.
	if in.MaxFileBytes == int64(^uint64(0)>>1) || in.MaxBatchBytes == int64(^uint64(0)>>1) {
		return Limits{}, fmt.Errorf("%w: byte limit leaves no framing allowance", ErrLimit)
	}
	// Paths are part of the finite manifest budget. A depth of n needs at
	// least 2*n-1 path bytes, regardless of a caller's logical path choices.
	in.MaxPathBytes = int(min(int64(in.MaxPathBytes), in.MaxManifestBytes))
	in.MaxDepth = min(in.MaxDepth, in.MaxPathBytes/2+in.MaxPathBytes%2)
	return in, nil
}

// ValidateLimits validates a proposed update without changing a Manager.
func ValidateLimits(in Limits) error {
	_, err := limitsOrDefault(in)
	return err
}

// ManifestMetadataBytes returns the conservative retained-memory accounting
// used by manifest validation and both sending and receiving admission.
func ManifestMetadataBytes(m Manifest) int64 { return metadataSize(m) }

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
		return 0, ErrMetadataLimit
	}
	ids := map[string]bool{}
	type manifestPath struct{ original, key string }
	paths := make([]manifestPath, 0, len(manifest.Entries))
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
		// Order by components, with the separator before every portable name
		// character. A parent then directly precedes its first descendant.
		// Keeping one key per entry avoids retaining every full parent prefix,
		// which otherwise grows quadratically when path depth is raised.
		paths = append(paths, manifestPath{e.Path, strings.ReplaceAll(canonical(e.Path), "/", "\x00")})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].key < paths[j].key })
	for i := 1; i < len(paths); i++ {
		previous, current := paths[i-1], paths[i]
		for {
			left, leftRest, leftMore := strings.Cut(previous.key, "\x00")
			right, rightRest, rightMore := strings.Cut(current.key, "\x00")
			if left != right {
				break
			}
			leftName, leftOriginal, _ := strings.Cut(previous.original, "/")
			rightName, rightOriginal, _ := strings.Cut(current.original, "/")
			if leftName != rightName {
				return 0, fmt.Errorf("%w: case or Unicode path collision", ErrInvalidManifest)
			}
			if !leftMore && !rightMore {
				return 0, fmt.Errorf("%w: duplicate path", ErrInvalidManifest)
			}
			if !leftMore || !rightMore {
				return 0, fmt.Errorf("%w: file or empty directory has children", ErrInvalidManifest)
			}
			previous = manifestPath{leftOriginal, leftRest}
			current = manifestPath{rightOriginal, rightRest}
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
