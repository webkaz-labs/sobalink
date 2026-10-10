package resourcecatalog

import (
	"bytes"
	"strings"
	"testing"
)

func TestCatalogStrictCodecRejectsAmbiguousShapes(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalSettings)
	b := fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s, fixtureRow(t, s, "unused"))); err != nil {
		t.Fatal(err)
	}
	data, err := EncodeSnapshot(finish(t, b), l)
	if err != nil {
		t.Fatal(err)
	}
	good := string(data)
	bad := []string{
		`null`, `[]`, good + `{}`, good + ` true`,
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"SchemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":null`, 1),
		strings.Replace(good, `"schemaVersion":1,`, ``, 1),
		strings.Replace(good, `"complete":true`, `"complete":null`, 1),
		strings.Replace(good, `"complete":true,`, ``, 1),
		strings.Replace(good, `"rows":[`, `"rows":null,"extra":[`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"default","Mode":"default"`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"default","mode":"default"`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"unlimited"`, 1),
		strings.Replace(good, `"value":2`, `"value":2.0`, 1),
		strings.Replace(good, `"value":2`, `"value":null`, 1),
		strings.Replace(good, `"value":2`, `"value":9007199254740992`, 1),
		strings.Replace(good, `"authority":"local"`, `"authority":"local","raw":{"path":"synthetic-only"}`, 1),
		strings.Replace(good, `"localSettings":`, `"unknownSettings":`, 1),
	}
	for i, input := range bad {
		if _, err := DecodeSnapshot([]byte(input), l); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
	invalidUTF8 := append([]byte{}, data...)
	invalidUTF8[bytes.Index(invalidUTF8, []byte("synthetic"))] = 0xff
	if _, err := DecodeSnapshot(invalidUTF8, l); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	small := l
	small.MaxBytes, small.MaxPageBytes, small.MaxStringBytes = len(data)-1, len(data)-1, 128
	if _, err := DecodeSnapshot(data, small); err == nil {
		t.Fatal("input byte bound ignored")
	}
}

func TestCatalogDecoderRejectsForgedCompletionAndRevision(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	b := fixtureBuilder(t, l, s)
	pending := finish(t, b)
	pending.Complete = true
	if _, err := DecodeSnapshot(mustJSON(t, pending), l); err == nil {
		t.Fatal("forged complete snapshot accepted")
	}
	if err := b.AddPage(fixturePage(s, fixtureRow(t, s, "saved-id"))); err != nil {
		t.Fatal(err)
	}
	complete := finish(t, b)
	complete.Sources[0].Rows[0].LocalService.Name = "Changed synthetic label"
	if _, err := DecodeSnapshot(mustJSON(t, complete), l); err == nil {
		t.Fatal("stale content digest accepted")
	}
	complete = finish(t, b)
	complete.Sources[0].Rows[0].RemoteService = &SharedService{}
	if _, err := DecodeSnapshot(mustJSON(t, complete), l); err == nil {
		t.Fatal("mixed union accepted")
	}
}
