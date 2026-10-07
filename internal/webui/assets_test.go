package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/webkaz-labs/sobalink/web"
)

func TestEmbeddedFrontendAssetsAreReachable(t *testing.T) {
	s, _ := testServer(t)
	var err error
	s.assets, err = web.Assets()
	if err != nil {
		t.Fatal(err)
	}
	documentCSP := serve(s, request(s, "GET", "/", "", nil, "")).Header().Get("Content-Security-Policy")
	if err := fs.WalkDir(s.assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		source, err := fs.ReadFile(s.assets, path)
		if err != nil {
			return err
		}
		route := "/" + path
		if path == "index.html" {
			route = "/"
		}
		wantCSP := documentCSP
		if pngWorkerPath.MatchString(path) {
			wantCSP = pngWorkerCSP
		}
		for _, method := range []string{"GET", "HEAD"} {
			response := serve(s, request(s, method, route, "", nil, ""))
			if response.Code != http.StatusOK {
				t.Errorf("%s %s: status %d", method, route, response.Code)
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(len(source)) {
				t.Errorf("%s %s: wrong content length", method, route)
			}
			if response.Header().Get("Content-Security-Policy") != wantCSP {
				t.Errorf("%s %s: wrong content security policy", method, route)
			}
			if method == "GET" && !bytes.Equal(response.Body.Bytes(), source) {
				t.Errorf("%s: served bytes differ from the embedded asset", route)
			}
			if method == "HEAD" && response.Body.Len() != 0 {
				t.Errorf("HEAD %s: unexpected body", route)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPNGWorkerManifestUsesReadOnlyAssetPolicy(t *testing.T) {
	s, _ := testServer(t)
	s.assets = fstest.MapFS{
		"index.html":               {Data: []byte("local UI")},
		"png-worker-manifest.json": {Data: []byte(`{"version":1}`)},
		"other.json":               {Data: []byte(`{}`)},
	}
	documentCSP := serve(s, request(s, "GET", "/", "", nil, "")).Header().Get("Content-Security-Policy")
	for _, method := range []string{"GET", "HEAD"} {
		response := serve(s, request(s, method, "/png-worker-manifest.json", "", nil, ""))
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s manifest: status %d, content type %q", method, response.Code, response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Content-Security-Policy") != documentCSP || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s manifest: lost default asset policy", method)
		}
		for _, path := range []string{"/other.json", "/png-worker-manifest.json/", "/png-worker-manifest.json.extra"} {
			if response := serve(s, request(s, method, path, "", nil, "")); response.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", method, path, response.Code)
			}
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		if response := serve(s, request(s, method, "/png-worker-manifest.json", "", nil, "")); response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s manifest: status %d, want 405", method, response.Code)
		}
	}
}
