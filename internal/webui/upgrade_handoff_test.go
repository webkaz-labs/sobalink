package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Inert authentication-boundary tests, without a listener or real sign-in.
func TestWebUpgradeHandoffRequiresCurrentSessionAndCSRF(t *testing.T) {
	for _, variant := range []string{"valid", "missing-session", "expired-session", "wrong-csrf", "wrong-origin", "cross-site"} {
		t.Run(variant, func(t *testing.T) {
			calls := 0
			payload := `{"intent":{"deadline":"` + time.Now().UTC().Add(time.Minute).Format(time.RFC3339) + `"}}`
			s := &Server{host: "127.0.0.1:12345", url: "http://127.0.0.1:12345", slots: make(chan struct{}, 1), sessions: map[string]session{"synthetic-session": {csrf: "synthetic-csrf", expires: time.Now().Add(time.Minute)}}}
			s.handoff = func(_ context.Context, origin, authorization string, raw json.RawMessage) (any, error) {
				calls++
				if authorization == "" {
					t.Fatal("missing session delegation")
				}
				if origin != s.url || string(raw) != payload {
					t.Fatal(origin, string(raw))
				}
				return map[string]string{"token": "synthetic-transfer"}, nil
			}
			r := httptest.NewRequest("POST", s.url+"/api/upgrade-handoff", strings.NewReader(payload))
			r.RemoteAddr = "127.0.0.1:34567"
			r.Header.Set("Origin", s.url)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", "synthetic-csrf")
			if variant != "missing-session" {
				r.AddCookie(&http.Cookie{Name: "soba_session", Value: "synthetic-session"})
			}
			switch variant {
			case "expired-session":
				s.sessions["synthetic-session"] = session{csrf: "synthetic-csrf", expires: time.Now().Add(-time.Second)}
			case "wrong-csrf":
				r.Header.Set("X-CSRF-Token", "wrong")
			case "wrong-origin":
				r.Header.Set("Origin", "https://example.invalid")
			case "cross-site":
				r.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if variant == "valid" {
				if w.Code != 200 || calls != 1 {
					t.Fatal(w.Code, calls, w.Body.String())
				}
			} else if w.Code == 200 || calls != 0 || strings.Contains(w.Body.String(), "synthetic-transfer") {
				t.Fatal("unauthenticated handoff", w.Code, calls)
			}
		})
	}
}

func TestWebUpgradeAuthorizationDiesWithOriginalSession(t *testing.T) {
	for _, variant := range []string{"logout", "session-expiry", "csrf-reset", "deadline", "changed-request"} {
		t.Run(variant, func(t *testing.T) {
			current := session{csrf: "synthetic-csrf", expires: time.Now().Add(time.Hour)}
			s := &Server{sessions: map[string]session{"synthetic-session": current}}
			raw := json.RawMessage(`{"intent":{"peerId":"synthetic-peer","deadline":"` + time.Now().UTC().Add(time.Minute).Format(time.RFC3339) + `","expectedRevision":"synthetic-review"},"locale":"en"}`)
			token, err := s.issueUpgradeAuthorization("synthetic-session", current, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CheckUpgradeAuthorization(token, raw, false); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "logout":
				delete(s.sessions, "synthetic-session")
			case "session-expiry":
				current.expires = time.Now().Add(-time.Second)
				s.sessions["synthetic-session"] = current
			case "csrf-reset":
				current.csrf = "another-csrf"
				s.sessions["synthetic-session"] = current
			case "deadline":
				grant := s.handoffs[token]
				grant.expires = time.Now().Add(-time.Second)
				s.handoffs[token] = grant
			case "changed-request":
				raw = json.RawMessage(strings.Replace(string(raw), "synthetic-peer", "other-peer", 1))
			}
			if err := s.CheckUpgradeAuthorization(token, raw, true); err == nil {
				t.Fatal("stale session authorized stop")
			}
		})
	}
}

func TestWebUpgradeAuthorizationConsumesOnceAtStopAdmission(t *testing.T) {
	current := session{csrf: "synthetic-csrf", expires: time.Now().Add(time.Hour)}
	s := &Server{sessions: map[string]session{"synthetic-session": current}}
	raw := json.RawMessage(`{"locale":"ja","intent":{"deadline":"` + time.Now().UTC().Add(time.Minute).Format(time.RFC3339) + `","peerId":"synthetic-peer"}}`)
	token, err := s.issueUpgradeAuthorization("synthetic-session", current, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckUpgradeAuthorization(token, raw, false); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckUpgradeAuthorization(token, raw, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckUpgradeAuthorization(token, raw, true); err == nil {
		t.Fatal("stop admission replayed")
	}
	if len(s.handoffs) != 0 {
		t.Fatal("consumed authorization retained")
	}
}
