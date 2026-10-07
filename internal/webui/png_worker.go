package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
)

const pngWorkerCSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'none'; worker-src 'none'; base-uri 'none'; frame-ancestors 'none'"

var pngWorkerPath = regexp.MustCompile(`^assets/device-card-worker-[A-Za-z0-9_-]+\.js$`)

// The manifest and worker are trusted embedded build contents. No path prefix,
// caller header, missing file, redirect or error response gets a CSP exception.
func (s *Server) servePNGWorker(w http.ResponseWriter, r *http.Request) bool {
	data, err := fs.ReadFile(s.assets, "png-worker-manifest.json")
	if err != nil || len(data) > 1024 {
		return false
	}
	var manifest struct {
		Version int    `json:"version"`
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 || !pngWorkerPath.MatchString(manifest.Path) || r.URL.Path != "/"+manifest.Path {
		return false
	}
	source, err := fs.ReadFile(s.assets, manifest.Path)
	if err != nil || len(source) == 0 {
		return false
	}
	hash := sha256.Sum256(source)
	if manifest.SHA256 != hex.EncodeToString(hash[:]) {
		return false
	}
	w.Header().Set("Content-Security-Policy", pngWorkerCSP)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// Serve only the verified source. Do not use path-cleaning FileServer here.
	w.Header().Set("Content-Length", strconv.Itoa(len(source)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(source)
	}
	return true
}
