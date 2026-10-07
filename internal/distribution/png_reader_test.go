package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func readerFixture(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"share.js", "reader/index.js", "reader/zxing_reader.wasm", "LICENSE", "NOTICE_INVENTORY.json", "README.md", "THIRD_PARTY_NOTICES.txt", "UPSTREAM.json"} {
		data, err := readSourceFile(source, pngReaderRoot+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, filepath.FromSlash(pngReaderRoot), filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	wasm, _ := readSourceFile(source, pngReaderRoot+"/reader/zxing_reader.wasm")
	notice, _ := readSourceFile(source, pngReaderRoot+"/THIRD_PARTY_NOTICES.txt")
	writeFixture(t, filepath.Join(root, "web/dist/assets/zxing_reader-fixture.wasm"), string(wasm))
	writeFixture(t, filepath.Join(root, "web/dist/assets/png-reader-notices.txt"), string(notice))
	worker := "/* synthetic build worker */"
	writeFixture(t, filepath.Join(root, "web/dist/assets/device-card-worker-fixture.js"), worker)
	writeFixture(t, filepath.Join(root, "web/dist/png-worker-manifest.json"), `{"version":1,"path":"assets/device-card-worker-fixture.js","sha256":"`+bytesHash([]byte(worker))+`"}`)
	return root
}
func TestPNGReaderInventoryCarriesOfflineNoticesAndPins(t *testing.T) {
	root, share := readerFixture(t), t.TempDir()
	reader, err := pngReaderInventory(root, share)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Name != "zxing-wasm" || reader.Version != "3.1.5" || reader.Integrity != pngReaderIntegrity || len(reader.Notices) != 5 {
		t.Fatal("reader inventory missing provenance/notices")
	}
	for _, notice := range reader.Notices {
		data, err := readSourceFile(share, notice.Path)
		if err != nil || bytesHash(data) != notice.SHA256 {
			t.Fatal("retained reader notice differs")
		}
	}
}
func TestPNGReaderInventoryRejectsChangedAssetsOrMissingNotices(t *testing.T) {
	for _, path := range []string{pngReaderRoot + "/share.js", pngReaderRoot + "/THIRD_PARTY_NOTICES.txt", "web/dist/assets/zxing_reader-fixture.wasm", "web/dist/assets/png-reader-notices.txt", "web/dist/assets/device-card-worker-fixture.js", "web/dist/png-worker-manifest.json"} {
		t.Run(path, func(t *testing.T) {
			root := readerFixture(t)
			writeFixture(t, filepath.Join(root, filepath.FromSlash(path)), "changed")
			if _, err := pngReaderInventory(root, t.TempDir()); err == nil {
				t.Fatal("accepted altered runtime/notice input")
			}
		})
	}
}
