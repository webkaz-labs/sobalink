package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEndpointCLIExactReviewAndLocalizedPreview(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		calls := 0
		query := func(name string, payload any, result any) error {
			calls++
			if name != "direct-lan.endpoint.move.preview" {
				t.Fatal(name)
			}
			data, _ := json.Marshal(payload)
			if strings.Contains(string(data), "expectedRevision") || strings.Contains(string(data), "probe") {
				t.Fatal(string(data))
			}
			return json.Unmarshal([]byte(`{"revision":"review-1","endpoint":"127.0.0.2:48444","previousEndpoint":"127.0.0.1:48444","deliveries":[],"destinations":{}}`), result)
		}
		request := func(string, any) error { t.Fatal("unexpected write"); return nil }
		if err := directLANEndpointCLI(context.Background(), []string{"move", "--endpoint", "127.0.0.2:48444"}, ja, &out, strings.NewReader(""), false, query, request); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(out.String(), "review-1") || !strings.Contains(out.String(), "--apply --review") {
			t.Fatal(out.String())
		}
		if ja && !strings.Contains(out.String(), "確認リビジョン") {
			t.Fatal(out.String())
		}
	}
}
func TestEndpointCLINoImplicitAuthorityOrDelivery(t *testing.T) {
	cases := [][]string{{"move", "--endpoint", "127.0.0.2:48444", "--apply"}, {"move", "--endpoint", "127.0.0.2:48444", "--review", "x"}, {"delivery", "--peer", "p", "--expires", "2030-01-01T00:00:00Z"}, {"move", "--endpoint", "127.0.0.2:48444", "--lifetime", "until-revoked"}, {"export", "--peer", "p"}, {"import", "--peer", "p", "--update", "private-proof"}, {"follow", "--peer", "p", "--granted", "2026-01-01T00:00:00Z", "--lifetime", "until-revoked"}}
	for _, args := range cases {
		var out bytes.Buffer
		called := false
		query := func(string, any, any) error { called = true; return nil }
		request := func(string, any) error { called = true; return nil }
		err := directLANEndpointCLI(context.Background(), args, false, &out, strings.NewReader(""), true, query, request)
		if err == nil || called {
			t.Fatalf("args=%v err=%v called=%v", args, err, called)
		}
	}
}
func TestEndpointCLIDeliveryUsesOnlyReviewedPeer(t *testing.T) {
	var baseline string
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		request := func(name string, payload any) error {
			if name != "direct-lan.endpoint.delivery.apply" {
				t.Fatal(name)
			}
			raw, _ := json.Marshal(payload)
			if string(raw) != `{"expectedRevision":"exact-review","peerId":"synthetic-peer"}` {
				t.Fatal(string(raw))
			}
			baseline = string(raw)
			return nil
		}
		query := func(string, any, any) error { t.Fatal("unexpected read"); return nil }
		if err := directLANEndpointCLI(context.Background(), []string{"delivery", "--peer", "synthetic-peer", "--apply", "--review", "exact-review", "--json"}, ja, &out, strings.NewReader(""), false, query, request); err != nil {
			t.Fatal(err)
		}
	}
	if baseline == "" {
		t.Fatal("no request")
	}
}
func TestEndpointCLIStatusSeparatesSavedActiveAndUnknown(t *testing.T) {
	var out bytes.Buffer
	query := func(name string, payload any, result any) error {
		if name != "direct-lan.endpoint.status" {
			t.Fatal(name)
		}
		return json.Unmarshal([]byte(`{"state":"saved_unavailable","saved":true,"active":false,"deliveries":[{"peerId":"synthetic-peer","outcome":"unconfirmed"}]}`), result)
	}
	if err := directLANEndpointCLI(context.Background(), []string{"status"}, true, &out, strings.NewReader(""), false, query, func(string, any) error { t.Fatal("unexpected request"); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"保存済み・利用不可", "配送未確認", "false"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}
