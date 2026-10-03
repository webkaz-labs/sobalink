package servicepresets

import "testing"

func TestCatalogIsEditableExamplesWithoutSharedMutation(t *testing.T) {
	all := All()
	if len(all) != 4 {
		t.Fatalf("examples = %d", len(all))
	}
	seen := map[string]bool{}
	for _, item := range all {
		if seen[item.ID] || item.Port < 1 || item.Port > 65535 || item.LocalPort < 1024 || item.LocalPort > 65535 || item.Network != "tcp" || item.Label.EN == "" || item.Label.JA == "" {
			t.Fatalf("invalid example: %+v", item)
		}
		seen[item.ID] = true
		if got, ok := Find(item.ID); !ok || got != item {
			t.Fatal("catalog lookup diverged")
		}
	}
	all[0].Port = 9999
	if got, _ := Find("web"); got.Port != 8080 {
		t.Fatal("caller mutated shared catalog")
	}
	if _, ok := Find("unknown"); ok {
		t.Fatal("unknown example accepted")
	}
}
