package resource

import (
	"strings"
	"testing"
)

func TestResourceStrictJSON(t *testing.T) {
	good := `{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","settings":{"transferConcurrentFiles":{"mode":"default"},"transferConcurrentPerPeer":{"mode":"limited","value":2}}}`
	var in PreviewRequest
	if err := Decode([]byte(good), 4096, &in); err != nil || in.Target.Validate() != nil || in.Settings.Validate() != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, bad := range []string{
		`null`, `[]`, good + `{}`,
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"SchemaVersion":1`, 1),
		strings.Replace(good, `"settings":`, `"Settings":`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"default","Mode":"limited"`, 1), strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":null`, 1),
		strings.Replace(good, `"settings":{`, `"settings":{"unknown":1,`, 1),
		strings.Replace(good, `"mode":"default"`, `"mode":"default","mode":"default"`, 1),
		strings.Replace(good, `"value":2`, `"value":null`, 1),
		strings.Replace(good, `"value":2`, `"value":18446744073709551616`, 1),
		strings.Replace(good, `"value":2`, `"value":2.0`, 1),
	} {
		var out PreviewRequest
		if Decode([]byte(bad), 4096, &out) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, bad := range []string{strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1), strings.Replace(good, `"mode":"default"`, `"mode":"unlimited"`, 1), strings.Replace(good, `0123456789abcdef0123456789abcdef`, `0123456789ABCDEF0123456789ABCDEF`, 1), strings.Replace(good, `"transferConcurrentFiles":{"mode":"default"},`, ``, 1)} {
		var out PreviewRequest
		err := Decode([]byte(bad), 4096, &out)
		if err == nil && out.Target.Validate() == nil && out.Settings.Validate() == nil {
			t.Errorf("accepted invalid contract %s", bad)
		}
	}
	if Decode([]byte(good), len(good)-1, &in) == nil {
		t.Fatal("size limit ignored")
	}
}
