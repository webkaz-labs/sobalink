package main

import "testing"

func TestFirstAppleLanguage(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"(\n    \"ja-JP\",\n    en-US\n)\n", "ja-JP"},
		{"(\n    en,\n    ja\n)", "en"},
		{"(ja)", "ja"},
		{"(\n  \"ja\"\n)", "ja"},
		{"", ""},
		{"()", ""},
	} {
		if got := firstAppleLanguage(test.input); got != test.want {
			t.Fatalf("%q: got %q, want %q", test.input, got, test.want)
		}
	}
}
