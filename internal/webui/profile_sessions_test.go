package webui

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Exercise browser cookie storage rules without opening any listener. Cookies
// for 127.0.0.1 are sent to both ports, so server-side ownership remains vital.
func TestProfileSessionsCoexistInOneBrowser(t *testing.T) {
	first, _ := testServer(t)
	second, _ := testServer(t)
	second.host, second.url = "127.0.0.1:45679", "http://127.0.0.1:45679"
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(s *Server, method, path, body, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		u, err := url.Parse(s.url)
		if err != nil {
			t.Fatal(err)
		}
		r := request(s, method, path, body, nil, csrf)
		for _, cookie := range jar.Cookies(u) {
			r.AddCookie(cookie)
		}
		w := serve(s, r)
		jar.SetCookies(u, w.Result().Cookies())
		return w
	}
	login := func(s *Server) string {
		t.Helper()
		code, err := s.IssueCode()
		if err != nil {
			t.Fatal(err)
		}
		w := call(s, "POST", "/api/session", `{"code":"`+code+`"}`, "")
		var result struct {
			CSRF string `json:"csrfToken"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.CSRF == "" {
			t.Fatal("profile login failed")
		}
		return result.CSRF
	}
	firstCSRF := login(first)
	secondCSRF := login(second)
	for _, s := range []*Server{first, second} {
		if call(s, "GET", "/api/state", "", "").Code != http.StatusOK {
			t.Fatal("another profile login displaced an existing session")
		}
	}
	if login(first) != firstCSRF || len(first.sessions) != 1 || len(second.sessions) != 1 {
		t.Fatal("repeated launch replaced or multiplied profile sessions")
	}
	// An interrupted/invalid login must not alter either profile's session.
	if call(second, "POST", "/api/session", `{"code":"invalid"}`, "").Code != http.StatusUnauthorized {
		t.Fatal("invalid code accepted")
	}
	if call(first, "POST", "/api/command", `{"requestId":"test","name":"settings.update","payload":{}}`, secondCSRF).Code != http.StatusForbidden {
		t.Fatal("another profile's CSRF authorized a mutation")
	}
	if call(second, "DELETE", "/api/session", "", secondCSRF).Code != http.StatusOK {
		t.Fatal("profile logout failed")
	}
	if call(first, "GET", "/api/state", "", "").Code != http.StatusOK || call(second, "GET", "/api/state", "", "").Code != http.StatusUnauthorized {
		t.Fatal("logout affected the wrong profile")
	}
	if login(second) == secondCSRF || login(first) != firstCSRF {
		t.Fatal("re-login did not remain profile-scoped")
	}
}

func TestProfileSessionsRejectForeignLegacyAndStaleCookies(t *testing.T) {
	first, _ := testServer(t)
	cookie, _ := signIn(t, first)
	second, _ := testServer(t)
	second.host, second.url = "127.0.0.1:45679", "http://127.0.0.1:45679"
	for _, name := range []string{cookie.Name, second.sessionCookieName(), "soba_session"} {
		w := serve(second, request(second, "GET", "/api/state", "", &http.Cookie{Name: name, Value: cookie.Value}, ""))
		if w.Code != http.StatusUnauthorized {
			t.Fatal("foreign or legacy session authorized another profile")
		}
	}
	// Port reuse after restart must not restore the previous owner's session.
	restarted, _ := testServer(t)
	if serve(restarted, request(restarted, "GET", "/api/state", "", cookie, "")).Code != http.StatusUnauthorized {
		t.Fatal("a restarted owner accepted a previous session")
	}
	// Do not recognize the old shared cookie even with a current token.
	legacy := &http.Cookie{Name: "soba_session", Value: cookie.Value}
	if serve(first, request(first, "GET", "/api/state", "", legacy, "")).Code != http.StatusUnauthorized {
		t.Fatal("legacy shared cookie accepted")
	}
}
