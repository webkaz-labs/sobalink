package transfer

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestManifestRejectsNonportablePaths(t *testing.T) {
	paths := []string{
		"", ".", "..", "../escape", "dir/../escape", "/absolute", "//server/share",
		"dir//file", "./file", "dir/", `dir\file`, `C:\file`, "C:file",
		"dir/CON", "nul.txt", "prn", "AUX.log", "CLOCK$", "CONIN$", "CONOUT$",
		"COM1.txt", "lpt9", "COM¹.log", "LPT²", "com³", "CON .txt", "NUL  .log",
		"trailing.", "trailing ", "dir /file", "file:stream", "question?", "star*",
		"less<", "greater>", "quote\"", "pipe|", "line\nfeed", "tab\tname", "zero\x00byte",
		"control\u0085", "invalid\xff", strings.Repeat("a", 256),
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			m := testManifest("batch", testEntry("file", path, "payload"))
			if err := ValidateManifest(m, Limits{}); !errors.Is(err, ErrInvalidManifest) || !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("ValidateManifest(%q) = %v, want invalid unsafe path", path, err)
			}
		})
	}
}

func TestManifestRejectsPortableCollisionsAndHierarchy(t *testing.T) {
	tests := map[string][]Entry{
		"same path":                 {testEntry("a", "same", ""), testEntry("b", "same", "")},
		"case":                      {testEntry("a", "Readme", ""), testEntry("b", "README", "")},
		"case parent":               {testEntry("a", "Folder/a", ""), testEntry("b", "folder/b", "")},
		"Unicode normalization":     {testEntry("a", "caf\u00e9", ""), testEntry("b", "cafe\u0301", "")},
		"Unicode parent":            {testEntry("a", "caf\u00e9/a", ""), testEntry("b", "cafe\u0301/b", "")},
		"Unicode full case fold":    {testEntry("a", "Straße", ""), testEntry("b", "STRASSE", "")},
		"Unicode sigma":             {testEntry("a", "σ", ""), testEntry("b", "ς", "")},
		"Unicode Windows case":      {testEntry("a", "I.txt", ""), testEntry("b", "ı.txt", "")},
		"file has child":            {testEntry("a", "parent", ""), testEntry("b", "parent/child", "")},
		"child before file":         {testEntry("b", "parent/child", ""), testEntry("a", "parent", "")},
		"empty directory has child": {{ID: "a", Path: "parent", Kind: Directory}, testEntry("b", "parent/child", "")},
		"duplicate entry ID":        {testEntry("a", "one", ""), testEntry("a", "two", "")},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateManifest(testManifest("batch", entries...), Limits{}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ValidateManifest = %v, want invalid manifest", err)
			}
		})
	}
}

func TestManifestAllowsPortableUnicodeAndImplicitDirectories(t *testing.T) {
	m := testManifest("batch_1", testEntry("a", "資料/写真.jpg", "a"), testEntry("b", "資料/説明.txt", "b"),
		testEntry("c", "cafe\u0301.txt", ""), Entry{ID: "empty", Path: "空のフォルダー", Kind: Directory})
	if err := ValidateManifest(m, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsInvalidMetadataAndContentClaims(t *testing.T) {
	base := testEntry("file", "file.txt", "payload")
	tests := map[string]Manifest{
		"empty batch":      {ID: "batch"},
		"invalid batch ID": testManifest("../batch", base),
		"long batch ID":    testManifest(strings.Repeat("a", 129), base),
	}
	for name, change := range map[string]func(*Entry){
		"invalid entry ID":      func(e *Entry) { e.ID = "../file" },
		"negative size":         func(e *Entry) { e.Size = -1 },
		"invalid kind":          func(e *Entry) { e.Kind = "symlink" },
		"missing digest":        func(e *Entry) { e.SHA256 = "" },
		"short digest":          func(e *Entry) { e.SHA256 = "00" },
		"nonhex digest":         func(e *Entry) { e.SHA256 = strings.Repeat("z", 64) },
		"uppercase digest":      func(e *Entry) { e.SHA256 = strings.ToUpper(e.SHA256) },
		"directory with bytes":  func(e *Entry) { e.Kind, e.SHA256 = Directory, "" },
		"directory with digest": func(e *Entry) { e.Kind, e.Size = Directory, 0 },
	} {
		e := base
		change(&e)
		tests[name] = testManifest("batch", e)
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateManifest(manifest, Limits{}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ValidateManifest = %v, want invalid manifest", err)
			}
		})
	}
}

func TestManifestEnforcesMetadataAndByteLimits(t *testing.T) {
	m := testManifest("batch", testEntry("a", "dir/a", "abcd"), testEntry("b", "dir/b", "efgh"))
	for name, limits := range map[string]Limits{
		"entries": {MaxEntries: 1}, "depth": {MaxDepth: 1}, "path bytes": {MaxPathBytes: 4},
		"metadata": {MaxManifestBytes: metadataSize(m) - 1}, "file bytes": {MaxFileBytes: 3},
		"batch bytes": {MaxBatchBytes: 7},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateManifest(m, limits)
			if errors.Is(err, ErrMetadataLimit) != (name == "metadata") {
				t.Fatalf("incorrect metadata error classification: %v", err)
			}
			if name == "path bytes" {
				if !errors.Is(err, ErrUnsafePath) {
					t.Fatalf("error = %v, want unsafe path", err)
				}
			} else if !errors.Is(err, ErrLimit) {
				t.Fatalf("error = %v, want limit", err)
			}
		})
	}
	if err := ValidateManifest(m, Limits{MaxEntries: 2, MaxDepth: 2, MaxPathBytes: 5, MaxManifestBytes: metadataSize(m), MaxFileBytes: 4, MaxBatchBytes: 8}); err != nil {
		t.Fatalf("exact bounds rejected: %v", err)
	}
}

func TestLimitsCannotRaiseHardCeilings(t *testing.T) {
	defaults := reflect.ValueOf(DefaultLimits())
	for i := 0; i < defaults.NumField(); i++ {
		for _, n := range []int64{-1, defaults.Field(i).Int() + 1} {
			limits := Limits{}
			reflect.ValueOf(&limits).Elem().Field(i).SetInt(n)
			if _, err := NewManager(Options{Limits: limits}); !errors.Is(err, ErrLimit) {
				t.Errorf("%s = %d: error = %v, want limit", defaults.Type().Field(i).Name, n, err)
			}
		}
	}
}
