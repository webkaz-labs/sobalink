package webui

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPNGWorkerCSPIsBoundToVerifiedBuildEntry(t *testing.T) {
	path := "assets/device-card-worker-Fixture123.js"
	source := []byte("/* synthetic worker */")
	hash := sha256.Sum256(source)
	files := fstest.MapFS{
		"index.html": {Data: []byte("local UI")}, path: {Data: source},
		"assets/other.js":          {Data: source},
		"assets/reader.wasm":       {Data: []byte{0, 97, 115, 109}},
		"png-worker-manifest.json": {Data: []byte(fmt.Sprintf(`{"version":1,"path":%q,"sha256":"%x"}`, path, hash))},
	}
	s, _ := testServer(t)
	s.assets = files
	for _, method := range []string{"GET", "HEAD"} {
		for _, target := range []string{"/", "/" + path, "/assets/other.js", "/assets/missing.js", "/assets/reader.wasm", "/assets/../" + path} {
			response := serve(s, request(s, method, target, "", nil, ""))
			csp := response.Header().Get("Content-Security-Policy")
			if got, want := strings.Contains(csp, "wasm-unsafe-eval"), target == "/"+path; got != want {
				t.Fatalf("%s %s unexpected policy %s", method, target, csp)
			}
			if target == "/"+path && (csp != pngWorkerCSP || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/javascript")) {
				t.Fatal("wrong exact worker headers")
			}
			if target == "/assets/reader.wasm" && response.Header().Get("Content-Type") != "application/wasm" {
				t.Fatal("wrong WASM MIME")
			}
			if response.Code == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("lost no-store: %s", target)
			}
		}
	}
	for _, data := range []string{`{}`, `{"version":1,"path":"assets/other.js","sha256":"bad"}`, fmt.Sprintf(`{"version":1,"path":%q,"sha256":"bad"}`, path)} {
		files["png-worker-manifest.json"].Data = []byte(data)
		response := serve(s, request(s, "GET", "/"+path, "", nil, ""))
		if strings.Contains(response.Header().Get("Content-Security-Policy"), "wasm-unsafe-eval") {
			t.Fatal("invalid manifest broadened policy")
		}
	}
	response := serve(s, request(s, "POST", "/"+path, "", nil, ""))
	if response.Code != http.StatusMethodNotAllowed || strings.Contains(response.Header().Get("Content-Security-Policy"), "wasm-unsafe-eval") {
		t.Fatal("method error broadened policy")
	}
}
